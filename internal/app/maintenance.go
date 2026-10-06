package app

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/vasilistotskas/homecloud/internal/drive"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/parallel"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/verify"
)

// Run kinds of the maintenance jobs.
const (
	ScrubRunKind        = "scrub"
	LedgerBackupRunKind = "ledger_backup"
)

// ledgerBackupName matches the snapshots the agent writes and may prune, and
// ledgerBackupPartial a snapshot that was interrupted while being written.
var (
	ledgerBackupName    = regexp.MustCompile(`^homecloud-\d{8}T\d{6}Z\.db$`)
	ledgerBackupPartial = regexp.MustCompile(`^homecloud-\d{8}T\d{6}Z\.db\.partial$`)
)

// ScrubReport is recorded as the detail of each scrub run.
type ScrubReport struct {
	// Eligible is how many verified renditions the archive holds.
	Eligible   int `json:"eligible"`
	Checked    int `json:"checked"`
	Passed     int `json:"passed"`
	Mismatched int `json:"mismatched"`
	Errored    int `json:"errored"`
	// Drive counts the same for archived iCloud Drive files. A damaged Drive
	// file is downloaded again by the next Drive sync.
	Drive DriveScrub `json:"drive"`
	Error string     `json:"error,omitempty"`
}

// DriveScrub is a scrub's re-check of archived iCloud Drive files.
type DriveScrub struct {
	Eligible int `json:"eligible"`
	Checked  int `json:"checked"`
	Passed   int `json:"passed"`
	Damaged  int `json:"damaged"`
	Errored  int `json:"errored"`
}

// LedgerBackupReport is recorded as the detail of each ledger backup run.
type LedgerBackupReport struct {
	File      string `json:"file,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
	// Pruned is how many older snapshots were removed beyond backup.keep.
	Pruned int    `json:"pruned"`
	Error  string `json:"error,omitempty"`
}

// ParseScrubReport decodes the detail recorded for a finished scrub run.
func ParseScrubReport(detail string) (ScrubReport, error) {
	var rep ScrubReport
	if err := json.Unmarshal([]byte(detail), &rep); err != nil {
		return ScrubReport{}, fmt.Errorf("decode scrub report: %w", err)
	}
	return rep, nil
}

// ParseLedgerBackupReport decodes the detail recorded for a finished ledger backup run.
func ParseLedgerBackupReport(detail string) (LedgerBackupReport, error) {
	var rep LedgerBackupReport
	if err := json.Unmarshal([]byte(detail), &rep); err != nil {
		return LedgerBackupReport{}, fmt.Errorf("decode ledger backup report: %w", err)
	}
	return rep, nil
}

// maintain runs the iCloud Drive sync, the scrub, the ledger backup and the
// iPhone backup check when they are due. Each is a run of its own kind, so the schedules survive agent
// restarts.
func (a *Agent) maintain(ctx context.Context) error {
	return errors.Join(
		a.runIfDue(ctx, DriveSyncRunKind, a.cfg.Drive.SyncInterval.Std(), PhaseSyncingDrive, a.syncDrive),
		a.runIfDue(ctx, ScrubRunKind, a.cfg.Schedule.ScrubInterval.Std(), PhaseCheckingArchive, a.scrub),
		a.runIfDue(ctx, LedgerBackupRunKind, a.cfg.Backup.Interval.Std(), PhaseBackingUpLedger, a.backupLedger),
		a.runIfDue(ctx, IPhoneBackupRunKind, a.cfg.IPhoneBackup.CheckInterval.Std(), PhaseCheckingIPhoneBackups, a.checkIPhoneBackups),
	)
}

// job does one maintenance task and returns its report and whether it succeeded.
type job func(context.Context) (report any, ok bool)

// runIfDue runs do when no run of kind has completed within every. A run that
// could not do its work is retried at the next cycle.
func (a *Agent) runIfDue(ctx context.Context, kind string, every time.Duration, p Phase, do job) error {
	last, err := a.db.LastCompletedRun(ctx, kind)
	switch {
	case errors.Is(err, ledger.ErrNotFound):
	case err != nil:
		return err
	case a.now().Before(last.FinishedAt.Add(every)):
		return nil
	}
	a.setPhase(p)
	defer a.setPhase(PhaseIdle)
	id, err := a.db.StartRun(ctx, kind, a.now())
	if err != nil {
		return err
	}
	rep, ok := do(ctx)
	// Record the outcome even when ctx was cancelled mid-run.
	return a.db.FinishRun(context.WithoutCancel(ctx), id, ok, rep, a.now())
}

// scrub re-hashes the share schedule.scrub_fraction of verified files whose
// content was confirmed longest ago. A file that changed or vanished is
// quarantined, so nothing will be deleted from iCloud for its asset.
func (a *Agent) scrub(ctx context.Context) (any, bool) {
	var rep ScrubReport
	fail := func(err error) (any, bool) {
		a.log.Error("scrub failed", "err", err)
		rep.Error = err.Error()
		return rep, false
	}
	// A missing file only means loss when the archive volume itself is there.
	if err := a.checkArchive(); err != nil {
		return fail(fmt.Errorf("archive volume unavailable: %w", err))
	}
	batch, eligible, err := a.db.ScrubBatch(ctx, a.cfg.Schedule.ScrubFraction)
	if err != nil {
		return fail(err)
	}
	rep.Eligible = eligible

	var mu sync.Mutex
	parallel.ForEach(ctx, batch, a.cfg.Verify.Workers, func(t ledger.ScrubTarget) {
		defer a.hub.Changed() // live views follow quarantines as they happen
		outcome := a.scrubOne(ctx, t)
		mu.Lock()
		defer mu.Unlock()
		rep.Checked++
		switch {
		case outcome == nil:
			rep.Passed++
		case errors.Is(outcome, verify.ErrMismatch):
			rep.Mismatched++
			a.log.Warn("scrub mismatch; asset quarantined", "asset", t.AssetID, "version", t.Version, "err", outcome)
		default:
			rep.Errored++
			a.log.Error("scrub error; checked again next scrub", "asset", t.AssetID, "version", t.Version, "err", outcome)
		}
	})
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := a.scrubDrive(ctx, &rep.Drive); err != nil {
		return fail(err)
	}
	a.log.Info("scrubbed", "eligible", rep.Eligible, "checked", rep.Checked, "passed", rep.Passed,
		"mismatched", rep.Mismatched, "errored", rep.Errored)
	if rep.Mismatched > 0 {
		a.alert(ctx, notify.Msg{
			Title:    "homecloud: archived files changed on disk",
			Body:     fmt.Sprintf("%d archived file(s) no longer match their download checksum and were quarantined. Nothing will be deleted for those assets. Check the archive drive's health.", rep.Mismatched),
			Priority: notify.PriorityUrgent,
			Tags:     []string{"rotating_light"},
		})
	}
	if rep.Drive.Damaged > 0 {
		a.alert(ctx, notify.Msg{
			Title:    "homecloud: archived iCloud Drive files changed on disk",
			Body:     fmt.Sprintf("%d archived iCloud Drive file(s) no longer match their checksum. The next iCloud Drive sync downloads them again and keeps the damaged copies aside. Check the archive drive's health.", rep.Drive.Damaged),
			Priority: notify.PriorityUrgent,
			Tags:     []string{"rotating_light"},
		})
	}
	return rep, rep.Mismatched == 0 && rep.Errored == 0 && rep.Drive.Damaged == 0 && rep.Drive.Errored == 0
}

// scrubDrive re-hashes the same share of archived iCloud Drive files. A copy
// that no longer matches is marked damaged: the next Drive sync downloads the
// file again and keeps the damaged copy under .versions.
func (a *Agent) scrubDrive(ctx context.Context, rep *DriveScrub) error {
	batch, eligible, err := a.db.DriveScrubBatch(ctx, a.cfg.Schedule.ScrubFraction)
	if err != nil {
		return err
	}
	rep.Eligible = eligible
	root := filepath.Join(a.cfg.Storage.Root, drive.Dir)
	var mu sync.Mutex
	parallel.ForEach(ctx, batch, a.cfg.Verify.Workers, func(f ledger.DriveFile) {
		var outcome error
		switch err := verify.Content(filepath.Join(root, filepath.FromSlash(f.LocalPath)), uint64(f.Size), f.SHA256); { //nolint:gosec // sizes are never negative
		case err == nil:
			outcome = a.db.RecordDriveScrub(ctx, f.DriveWSID, a.now())
		case errors.Is(err, verify.ErrMismatch):
			if markErr := a.db.MarkDriveFileDamaged(ctx, f.DriveWSID, err.Error(), a.now()); markErr != nil {
				outcome = markErr
			} else {
				outcome = err
			}
		default:
			outcome = err
		}
		mu.Lock()
		defer mu.Unlock()
		rep.Checked++
		switch {
		case outcome == nil:
			rep.Passed++
		case errors.Is(outcome, verify.ErrMismatch):
			rep.Damaged++
			a.log.Warn("scrub: iCloud Drive copy damaged; downloaded again next sync", "file", f.DriveWSID, "err", outcome)
		default:
			rep.Errored++
			a.log.Error("scrub error on an iCloud Drive file; checked again next scrub", "file", f.DriveWSID, "err", outcome)
		}
	})
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("drive scrub: %w", err)
	}
	return nil
}

// scrubOne returns nil when t still matches, an ErrMismatch-wrapping error
// after quarantining, or a transient error.
func (a *Agent) scrubOne(ctx context.Context, t ledger.ScrubTarget) error {
	err := verify.Content(t.LocalPath, t.SizeBytes, t.SHA256)
	switch {
	case err == nil:
		return a.db.RecordScrub(ctx, t.Key, a.now())
	case errors.Is(err, verify.ErrMismatch):
		if recErr := a.db.RecordMismatch(ctx, t.Key, ledger.TierContent, err.Error(), a.now()); recErr != nil {
			return recErr
		}
		return err
	default:
		return err
	}
}

// backupLedger writes a checked snapshot of the ledger to backup.dir and keeps
// the newest backup.keep snapshots.
func (a *Agent) backupLedger(ctx context.Context) (any, bool) {
	var rep LedgerBackupReport
	if err := a.writeLedgerBackup(ctx, &rep); err != nil {
		a.log.Error("ledger backup failed", "err", err)
		rep.Error = err.Error()
		a.alertRepeated(ctx, alertBackup, notify.Msg{
			Title:    "homecloud: ledger backup failed",
			Body:     "The daily copy of homecloud's ledger could not be written; it is retried every cycle. See logs.",
			Priority: notify.PriorityHigh,
			Tags:     []string{"warning"},
		})
		return rep, false
	}
	a.log.Info("ledger backed up", "file", rep.File, "bytes", rep.SizeBytes, "pruned", rep.Pruned)
	return rep, true
}

func (a *Agent) writeLedgerBackup(ctx context.Context, rep *LedgerBackupReport) error {
	dir := a.cfg.Backup.Dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	dest := filepath.Join(dir, "homecloud-"+a.now().UTC().Format(platform.FileTimeLayout)+".db")
	if err := a.db.Snapshot(ctx, dest); err != nil {
		return err
	}
	info, err := os.Stat(dest)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dest, err)
	}
	rep.File, rep.SizeBytes = dest, info.Size()
	rep.Pruned, err = pruneLedgerBackups(dir, a.cfg.Backup.Keep)
	return err
}

// pruneLedgerBackups removes all but the newest keep snapshots in dir, and any
// partial snapshot an interrupted backup left (backups run one at a time and
// pruning follows the current one's publication), and returns how many it
// removed. Other files in dir are never touched.
func pruneLedgerBackups(dir string, keep int) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("list %s: %w", dir, err)
	}
	var names, old []string
	for _, e := range entries {
		switch {
		case !e.Type().IsRegular():
		case ledgerBackupName.MatchString(e.Name()):
			names = append(names, e.Name())
		case ledgerBackupPartial.MatchString(e.Name()):
			old = append(old, e.Name())
		}
	}
	slices.Sort(names)
	if len(names) > keep {
		old = append(old, names[:len(names)-keep]...)
	}
	for _, name := range old {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return 0, fmt.Errorf("prune %s: %w", name, err)
		}
	}
	return len(old), nil
}
