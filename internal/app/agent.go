package app

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vasilistotskas/homecloud/internal/archive"
	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/export"
	"github.com/vasilistotskas/homecloud/internal/health"
	"github.com/vasilistotskas/homecloud/internal/icloud"
	"github.com/vasilistotskas/homecloud/internal/iphonecopy"
	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/logging"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/policy"
	"github.com/vasilistotskas/homecloud/internal/thumbs"
)

// Notifier delivers operator alerts.
type Notifier interface {
	Send(ctx context.Context, m notify.Msg) error
}

// Alert keys of throttled alerts.
const (
	alertSignIn  = "sign-in"
	alertVolume  = "archive-volume"
	alertLowDisk = "low-space"
	alertBackup  = "ledger-backup"
	// alertIPhoneNone: no local iPhone backup exists at all.
	alertIPhoneNone = "iphone-backup-none"
	// alertIPhonePrefix + folder + "/" + state: one backup folder's problem.
	alertIPhonePrefix = "iphone-backup/"
	// alertIPhoneCheck: a configured backup directory could not be checked.
	alertIPhoneCheck = "iphone-backup-check"
	// alertIPhoneCopy + UDID: a device's backup could not be copied.
	alertIPhoneCopy = "iphone-copy/"
	// alertIPhoneCopyDamaged + UDID: a copy in the archive is damaged.
	alertIPhoneCopyDamaged = "iphone-copy-damaged/"
	// alertIPhonePush: a backup pushed from another computer was refused,
	// or the incoming folder could not be read.
	alertIPhonePush = "iphone-push"
	// alertCheckpoint: kei kept its iCloud checkpoint for
	// kei.checkpoint_alert_cycles syncs in a row.
	alertCheckpoint = "kei-checkpoint"
)

// CycleRunKind is the ledger run kind of a sync cycle.
const CycleRunKind = "cycle"

// Agent runs the periodic sync → ingest → verify cycle.
type Agent struct {
	cfg    *config.Config
	db     *ledger.DB
	log    *slog.Logger
	notify Notifier
	keiLog io.Writer
	now    func() time.Time
	// readStorage finds the drives (and md arrays) holding the archive, and
	// readDrive reads one drive's SMART data.
	readStorage func() (platform.Storage, error)
	readDrive   health.DriveReader
	// checkArchive confirms the configured archive is present at storage.root.
	checkArchive func() error
	// checkSpace reports the archive volume's space, wrapping
	// platform.ErrLowSpace below storage.min_free_percent.
	checkSpace func() (platform.Space, error)
	// renderer makes the library browser's thumbnails.
	renderer *thumbs.Renderer
	// icloudSession locates homecloud's saved iCloud session, and
	// icloudEndpoints are Apple's services it talks to.
	icloudSession   func() (string, error)
	icloudEndpoints icloud.Endpoints
	// pushKey locates the key pushed iPhone backups must be signed with.
	pushKey func() (string, error)

	hub     *Hub
	trigger chan struct{}
}

// NewAgent returns an idle agent. keiLog receives kei's own output.
func NewAgent(cfg *config.Config, db *ledger.DB, log *slog.Logger, n Notifier, keiLog io.Writer,
	renderer *thumbs.Renderer, now func() time.Time,
) *Agent {
	return &Agent{
		cfg: cfg, db: db, log: log, notify: n, keiLog: keiLog, renderer: renderer, now: now,
		readStorage:  func() (platform.Storage, error) { return platform.StorageOf(cfg.Storage.Root) },
		readDrive:    health.ReaderFor(cfg),
		checkArchive: func() error { return archive.Check(cfg.Storage.Root, cfg.Storage.ArchiveID) },
		checkSpace: func() (platform.Space, error) {
			return platform.CheckFreeSpace(cfg.Storage.Root, cfg.Storage.MinFreePercent)
		},
		icloudSession:   icloud.SessionPath,
		icloudEndpoints: icloud.AppleEndpoints,
		pushKey:         iphonecopy.PushKeyPath,
		hub:             NewHub(),
		trigger:         make(chan struct{}, 1),
	}
}

// CheckArchive reports whether the configured archive volume is mounted at
// storage.root. Every cycle and scrub checks it first.
func (a *Agent) CheckArchive() error { return a.checkArchive() }

// Hub exposes the agent's live status.
func (a *Agent) Hub() *Hub { return a.hub }

// RequestCycle asks for a cycle as soon as possible: now when idle, or right
// after the maintenance job that is running. It reports false, and does
// nothing, when a cycle is already running or requested.
func (a *Agent) RequestCycle() bool {
	if st, _ := a.hub.Snapshot(); st.Phase.InCycle() {
		return false
	}
	select {
	case a.trigger <- struct{}{}:
		return true
	default:
		return false
	}
}

// closeInterrupted records every run left unfinished by a previous agent
// process as failed. Only one agent runs at a time (platform.AcquireInstance),
// so a run still open at startup was interrupted: on Windows the agent is
// ended without notice at logoff or shutdown.
func (a *Agent) closeInterrupted(ctx context.Context) error {
	runs, err := a.db.UnfinishedRuns(ctx)
	if err != nil {
		return err
	}
	for _, r := range runs {
		var detail any
		switch r.Kind {
		case CycleRunKind:
			detail = CycleReport{KeiOutcome: KeiOutcomeInterrupted, Error: interruptedError}
		case ScrubRunKind:
			detail = ScrubReport{Error: interruptedError}
		case LedgerBackupRunKind:
			detail = LedgerBackupReport{Error: interruptedError}
		case IPhoneBackupRunKind:
			detail = IPhoneBackupReport{Error: interruptedError}
		case DriveSyncRunKind:
			detail = DriveSyncReport{Outcome: DriveOutcomeError, Error: interruptedError}
		default:
			return fmt.Errorf("run %d has unknown kind %q", r.ID, r.Kind)
		}
		if err := a.db.FinishRun(ctx, r.ID, false, detail, a.now()); err != nil {
			return err
		}
		a.log.WarnContext(ctx, "closed a run the previous agent process left unfinished",
			logging.KeyRun, r.ID, logging.KeyRunKind, r.Kind, "started", r.StartedAt)
	}
	return nil
}

// Run executes a cycle immediately, followed by any maintenance that is due
// (scrub, ledger backup), then again schedule.sync_interval later or as soon
// as one is requested, until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.closeInterrupted(ctx); err != nil {
		return fmt.Errorf("close interrupted runs: %w", err)
	}
	interval := a.cfg.Schedule.SyncInterval.Std()
	for {
		// Runs log their own failures (recordRun); the next cycle tries again.
		_ = a.Cycle(ctx)
		_ = a.maintain(ctx)
		if ctx.Err() != nil {
			return fmt.Errorf("agent stopped: %w", ctx.Err())
		}
		next := a.now().Add(interval)
		a.hub.update(func(s *Status) { s.NextCycleAt = next })
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("agent stopped: %w", ctx.Err())
		case <-timer.C:
		case <-a.trigger:
			timer.Stop()
		}
	}
}

// CycleReport is recorded as the detail of each cycle run.
type CycleReport struct {
	KeiOutcome string `json:"kei_outcome"`
	// Checkpoint is nil when kei wrote no report for the cycle's sync.
	Checkpoint *Checkpoint        `json:"checkpoint,omitempty"`
	Ingested   ledger.IngestStats `json:"ingested"`
	Verified   VerifyStats        `json:"verified"`
	Error      string             `json:"error,omitempty"`
}

// CycleCheckpointField is CycleReport.Checkpoint's name in the ledger.
const CycleCheckpointField = "checkpoint"

// Checkpoint is whether kei's sync kept its previous iCloud checkpoint (sync
// token) instead of saving a new one, as kei's report says. The reasons are
// kei's own words, recorded as kei wrote them.
type Checkpoint struct {
	Blocked bool `json:"blocked"`
	// BlockedCycles counts the syncs in a row, this one included, that kept
	// the checkpoint; 0 when this one saved it. Cycles in which kei wrote no
	// report neither count nor reset it.
	BlockedCycles int    `json:"blocked_cycles"`
	Reason        string `json:"reason,omitempty"`
	// Source is who kei says caused it: "kei", "icloud" or "unknown".
	Source      string `json:"source,omitempty"`
	Explanation string `json:"explanation,omitempty"`
	Zone        string `json:"zone,omitempty"`
	// FullEnumerationReason is set when the sync listed the whole library
	// instead of the changes since the checkpoint, and says why.
	FullEnumerationReason string `json:"full_enumeration_reason,omitempty"`
}

// CycleReport.KeiOutcome values for cycles in which kei did not report one.
const (
	// KeiOutcomeError: kei could not run or exited with an unexpected code.
	KeiOutcomeError = "error"
	// KeiOutcomeLowSpace: the download was skipped because the archive volume
	// is below storage.min_free_percent.
	KeiOutcomeLowSpace = "skipped_low_space"
	// KeiOutcomeArchiveUnavailable: the cycle stopped before downloading
	// because the configured archive volume was missing or replaced.
	KeiOutcomeArchiveUnavailable = "archive_unavailable"
	// KeiOutcomeInterrupted: the agent stopped before the cycle finished.
	KeiOutcomeInterrupted = "interrupted"
)

// interruptedError is the error recorded on runs the agent never finished.
const interruptedError = "interrupted: the agent stopped before this run finished"

// Cycle performs one kei sync followed by ingest and verification and records
// the run in the ledger.
func (a *Agent) Cycle(ctx context.Context) error {
	defer a.setPhase(PhaseIdle)
	return a.recordRun(ctx, CycleRunKind, func(ctx context.Context) (any, bool, error) {
		rep, err := a.cycle(ctx)
		if err != nil {
			rep.Error = err.Error()
		}
		// Files that could not be checked (rep.Verified.Errored) are retried
		// next cycle, but this cycle did not verify everything it should have.
		return rep, err == nil && rep.Verified.Mismatched == 0 && rep.Verified.Errored == 0, err
	})
}

// recordRun records a run of kind in the ledger around do, and logs when it
// started and how it ended, with its error. do gets a context that names the
// run in every line logged with it (package logging).
func (a *Agent) recordRun(ctx context.Context, kind string, do func(context.Context) (report any, ok bool, err error)) error {
	started := a.now()
	id, err := a.db.StartRun(ctx, kind, started)
	if err != nil {
		a.log.ErrorContext(ctx, "run not started: the ledger did not record it", logging.KeyRunKind, kind, "err", err)
		return err
	}
	ctx = logging.WithRun(ctx, id, kind)
	a.log.InfoContext(ctx, "run started")
	rep, ok, err := do(ctx)
	stopping := ctx.Err() != nil
	// Record the outcome even when ctx was cancelled mid-run.
	ctx = context.WithoutCancel(ctx)
	err = errors.Join(err, a.db.FinishRun(ctx, id, ok, rep, a.now()))
	attrs := []any{"ok", ok, "duration_ms", a.now().Sub(started).Milliseconds()}
	switch {
	case stopping:
		a.log.WarnContext(ctx, "run finished: the agent is stopping", append(attrs, "err", err)...)
	case err != nil:
		a.log.ErrorContext(ctx, "run finished", append(attrs, "err", err)...)
	case !ok:
		a.log.WarnContext(ctx, "run finished", attrs...)
	default:
		a.log.InfoContext(ctx, "run finished", attrs...)
	}
	return err
}

func (a *Agent) setPhase(p Phase) {
	started := a.now()
	if p == PhaseIdle {
		started = time.Time{}
	}
	a.hub.update(func(s *Status) { s.Phase, s.PhaseStartedAt = p, started })
}

// cycle does the work. kei problems do not skip ingest/verify: the manifest
// stays authoritative for what is on disk.
func (a *Agent) cycle(ctx context.Context) (CycleReport, error) {
	var (
		errs []error
		rep  CycleReport
	)

	// Fail closed: nothing runs against a missing or different archive volume.
	if err := a.checkArchive(); err != nil {
		a.alertRepeated(ctx, alertVolume, notify.Msg{
			Title:    "homecloud: archive drive not available",
			Body:     "The configured archive drive is missing or a different drive is mounted there. Nothing is downloaded, checked or deleted until it is back.",
			Priority: notify.PriorityUrgent,
			Tags:     []string{"rotating_light"},
		})
		rep.KeiOutcome = KeiOutcomeArchiveUnavailable
		return rep, fmt.Errorf("archive volume: %w", err)
	}

	a.setPhase(PhaseDownloading)
	// A repair that cannot be prepared waits; the rest of the cycle runs.
	if err := a.prepareRepairs(ctx); err != nil {
		errs = append(errs, fmt.Errorf("repairs: %w", err))
	}
	outcome, report, err := a.download(ctx)
	if report != nil {
		cp, cpErr := a.checkpoint(ctx, report.Stats)
		rep.Checkpoint = cp
		if cpErr != nil {
			errs = append(errs, cpErr)
		}
	}
	switch {
	case errors.Is(err, platform.ErrLowSpace):
		// Below storage.min_free_percent: archive what is on disk, download nothing new.
		a.alertRepeated(ctx, alertLowDisk, notify.Msg{
			Title:    "homecloud: archive drive almost full",
			Body:     "Free space is below the configured minimum, so new photos are not downloaded. Existing files are still checked.",
			Priority: notify.PriorityHigh,
			Tags:     []string{"warning"},
		})
		a.log.WarnContext(ctx, "photos not downloaded: the archive drive is below its free-space minimum", "err", err)
	case err != nil:
		errs = append(errs, fmt.Errorf("kei sync: %w", err))
		a.alert(ctx, notify.Msg{Title: "homecloud: download failed", Body: "kei sync exited unexpectedly; see logs.", Priority: notify.PriorityHigh, Tags: []string{"warning"}})
	case outcome == kei.OutcomeAuthRequired:
		a.alertAuth(ctx)
	case outcome == kei.OutcomeAuthTerminal:
		a.alert(ctx, notify.Msg{Title: "homecloud: Apple account needs attention", Body: "Apple reported an account state that needs you (kei exit 4). Run kei login and check appleid.apple.com.", Priority: notify.PriorityUrgent, Tags: []string{"rotating_light"}})
	case outcome == kei.OutcomePartial, outcome == kei.OutcomeOK:
		// Failed assets are retried next cycle; download logged which.
	}
	switch {
	case err == nil:
		rep.KeiOutcome = outcome.String()
	case errors.Is(err, platform.ErrLowSpace):
		rep.KeiOutcome = KeiOutcomeLowSpace
	default:
		rep.KeiOutcome = KeiOutcomeError
	}

	a.setPhase(PhaseIngesting)
	ingested, err := Ingest(ctx, a.cfg, a.db, a.now())
	rep.Ingested = ingested
	if err != nil {
		return rep, errors.Join(append(errs, err)...)
	}
	a.log.InfoContext(ctx, "ingested", "assets", ingested.Assets, "new", ingested.NewRenditions, "changed", ingested.ChangedRenditions,
		"repaired", ingested.Repaired)

	a.setPhase(PhaseVerifying)
	verified, err := Verify(ctx, a.cfg, a.db, a.log, a.now, a.hub.Changed)
	rep.Verified = verified
	if err != nil {
		errs = append(errs, err)
	}
	a.log.InfoContext(ctx, "verified", "checked", verified.Checked, "passed", verified.Passed, "mismatched", verified.Mismatched, "errored", verified.Errored)
	if verified.Mismatched > 0 {
		a.alert(ctx, notify.Msg{
			Title:    "homecloud: files failed verification",
			Body:     fmt.Sprintf("%d file(s) did not match their download checksum or structure and were quarantined. Nothing will be deleted for those assets.", verified.Mismatched),
			Priority: notify.PriorityUrgent,
			Tags:     []string{"rotating_light"},
		})
	}
	if err := a.checkStorage(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := a.hideHiddenFolder(); err != nil {
		errs = append(errs, err)
	}
	if files, err := export.Write(ctx, a.db, a.cfg.Storage.Root); err != nil {
		errs = append(errs, err)
	} else {
		a.log.InfoContext(ctx, "manifest exported", "files", files)
	}
	if err := a.exportSafeToDelete(ctx); err != nil {
		errs = append(errs, err)
	}
	a.preparePreviews(ctx)
	return rep, errors.Join(errs...)
}

// exportSafeToDelete rewrites the safe-to-delete report at the archive root,
// judged against the storage check this cycle made.
func (a *Agent) exportSafeToDelete(ctx context.Context) error {
	st, _ := a.hub.Snapshot()
	r, err := BuildSafeToDelete(ctx, a.db, a.cfg.SafeToDelete.MinAge.Std(), st.Storage, a.now())
	if err != nil {
		return err
	}
	if err := export.WriteSafeToDelete(a.cfg.Storage.Root, r.Items); err != nil {
		return err
	}
	a.log.InfoContext(ctx, "safe-to-delete report exported", "assets", len(r.Items), "bytes", r.Bytes(),
		"withheld", r.Withheld, "not_verified", r.Counts[policy.NotVerified],
		"awaiting_scrub", r.Counts[policy.AwaitingScrub], "too_recent", r.Counts[policy.TooRecent])
	return nil
}

func (a *Agent) alertAuth(ctx context.Context) {
	a.log.WarnContext(ctx, "kei needs Apple sign-in (2FA)")
	a.alertRepeated(ctx, alertSignIn, notify.Msg{
		Title:    "homecloud: Apple sign-in needed",
		Body:     "New photos are not being downloaded. Run: kei login --config <kei config> and approve the code on your iPhone.",
		Priority: notify.PriorityHigh,
		Tags:     []string{"key"},
	})
}

// alertRepeated sends m at most once per notify.repeat_every for key, so a
// condition that persists across cycles does not alert every cycle. The ledger
// keeps the throttle, so restarts do not resend. When the throttle cannot be
// read the alert is sent: a duplicate is better than a lost alert.
func (a *Agent) alertRepeated(ctx context.Context, key string, m notify.Msg) {
	due, err := a.db.AlertDue(ctx, key, a.cfg.Notify.RepeatEvery.Std(), a.now())
	if err != nil {
		a.log.ErrorContext(ctx, "alert throttle unavailable; sending", "key", key, "err", err)
		due = true
	}
	if due {
		a.alert(ctx, m)
	}
}

// checkpoint records whether kei kept its iCloud checkpoint in the sync whose
// stats are s, and for how many syncs in a row, continuing the count of the
// last cycle that recorded one. It alerts when that reaches
// kei.checkpoint_alert_cycles: kei keeps the checkpoint for one sync by
// design, but a kept checkpoint means the next sync lists the library again,
// and one kept sync after sync means kei is stuck (rhoopr/kei#924).
func (a *Agent) checkpoint(ctx context.Context, s kei.Stats) (*Checkpoint, error) {
	cp := &Checkpoint{Blocked: s.SyncTokenBlocked, FullEnumerationReason: s.FullEnumerationReason}
	if !cp.Blocked {
		return cp, nil
	}
	cp.Reason, cp.Source, cp.Explanation, cp.Zone = s.SyncTokenBlockedReason, s.SyncTokenBlockedSource,
		s.SyncTokenBlockedExplanation, s.SyncTokenBlockedZone
	cp.BlockedCycles = 1
	switch last, err := a.db.LastFinishedRunWith(ctx, CycleRunKind, CycleCheckpointField); {
	case errors.Is(err, ledger.ErrNotFound):
	case err != nil:
		return cp, fmt.Errorf("kei checkpoint of the previous cycles: %w", err)
	default:
		previous, err := ParseCycleReport(last.Detail)
		if err != nil {
			return cp, fmt.Errorf("kei checkpoint of cycle %d: %w", last.ID, err)
		}
		cp.BlockedCycles += previous.Checkpoint.BlockedCycles
	}
	a.log.InfoContext(ctx, "kei kept its iCloud checkpoint", "syncs_in_a_row", cp.BlockedCycles,
		"reason", cp.Reason, "blocked_by", cp.Source, "explanation", cp.Explanation)
	if cp.BlockedCycles >= a.cfg.Kei.CheckpointAlertCycles {
		// kei's reason is from its fixed vocabulary: it names nothing in the library.
		a.alertRepeated(ctx, alertCheckpoint, notify.Msg{
			Title: "homecloud: photo syncs keep re-listing your library",
			Body: fmt.Sprintf("kei kept its place in iCloud Photos for %d syncs in a row (%s), so every sync lists the whole library again. "+
				"Nothing is missed; the app shows kei's explanation.", cp.BlockedCycles, cp.Reason),
			Priority: notify.PriorityDefault,
			Tags:     []string{"warning"},
		})
	}
	return cp, nil
}

// download runs kei unless the archive volume is below its free-space
// minimum, in which case it returns an ErrLowSpace-wrapping error. The report
// is what kei wrote for this sync, nil when it wrote none or it could not be
// read (logged).
func (a *Agent) download(ctx context.Context) (kei.Outcome, *kei.Report, error) {
	if _, err := a.checkSpace(); err != nil {
		return 0, nil, err
	}
	started := a.now()
	outcome, err := kei.Sync(ctx, a.cfg.Kei, a.keiLog)
	if ctx.Err() != nil { // a sync cut short by a stop is the run's to report
		return outcome, nil, err
	}
	return outcome, a.logKeiSync(ctx, outcome, err, started), err
}

// logKeiSync logs what a kei sync that started at started did, from the
// report kei wrote for it, and returns that report (nil if there is none): one
// line, at warning level or above when something needs a look.
func (a *Agent) logKeiSync(ctx context.Context, outcome kei.Outcome, syncErr error, started time.Time) *kei.Report {
	level, attrs := slog.LevelInfo, []any{"outcome", outcome.String()}
	switch {
	case syncErr != nil:
		level, attrs = slog.LevelError, []any{"outcome", KeiOutcomeError, "err", syncErr}
	case outcome != kei.OutcomeOK:
		level = slog.LevelWarn
	}
	rep, err := kei.ReadReport(kei.ReportPath(a.cfg.Kei), started)
	switch {
	case errors.Is(err, kei.ErrNoReport):
		attrs = append(attrs, "report", "none")
	case err != nil:
		level, attrs = max(level, slog.LevelWarn), append(attrs, "report_err", err)
	default:
		s := rep.Stats
		attrs = append(attrs, "status", rep.Status, "seen", s.Seen, "downloaded", s.Downloaded, "failed", s.Failed,
			"bytes", s.BytesDownloaded, "duration_ms", int64(s.ElapsedSeconds*1000),
			"enumeration_errors", s.EnumerationErrors, "enumeration_incomplete", s.EnumerationIncomplete,
			"inventory_drop_assets", s.InventoryDropAssets, "rate_limited", s.RateLimited,
			"state_write_failures", s.StateWriteFailures, "sync_token_blocked", s.SyncTokenBlocked)
		if s.FullEnumerationReason != "" {
			attrs = append(attrs, "full_enumeration_reason", s.FullEnumerationReason)
		}
		if len(rep.Failed) > 0 {
			attrs = append(attrs, "failed_assets", rep.Failed)
		}
		if rep.FailedOmitted > 0 {
			attrs = append(attrs, "failed_assets_omitted", rep.FailedOmitted)
		}
		if rep.Troubled() {
			level = max(level, slog.LevelWarn)
		}
	}
	a.log.Log(ctx, level, "kei sync finished", attrs...)
	if err != nil {
		return nil
	}
	return &rep
}

// alert sends m and logs that it did (alert titles and bodies name no file
// or device).
func (a *Agent) alert(ctx context.Context, m notify.Msg) {
	if err := a.notify.Send(ctx, m); err != nil {
		a.log.ErrorContext(ctx, "alert not sent", "title", m.Title, "err", err)
		return
	}
	a.log.InfoContext(ctx, "alert sent", "title", m.Title)
}

// ParseCycleReport decodes the detail recorded for a finished cycle run.
func ParseCycleReport(detail string) (CycleReport, error) {
	var rep CycleReport
	if err := json.Unmarshal([]byte(detail), &rep); err != nil {
		return CycleReport{}, fmt.Errorf("decode cycle report: %w", err)
	}
	return rep, nil
}

// checkStorage reads and judges the drives and arrays holding the archive,
// publishes the result, and records and alerts on changes. The archive is the
// only copy of the library, so a drive that predicts its own failure, or an
// array that lost its redundancy, is urgent.
func (a *Agent) checkStorage(ctx context.Context) error {
	report := health.Check(ctx, a.readStorage, a.readDrive, a.now())
	a.hub.SetStorage(report)

	previous, changed, err := a.db.RecordDriveHealth(ctx, string(report.State), report.Reasons, report.CheckedAt)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	level := map[health.State]slog.Level{
		health.StateOK:          slog.LevelInfo,
		health.StateUnavailable: slog.LevelWarn,
		health.StateWarning:     slog.LevelWarn,
		health.StateFailing:     slog.LevelError,
	}[report.State]
	a.log.Log(ctx, level, "archive storage health changed", "from", previous, "to", report.State, "reasons", report.Reasons)
	if report.State == health.StateWarning || report.State == health.StateFailing {
		a.alert(ctx, notify.Msg{
			Title: "homecloud: archive storage needs attention",
			Body: "The storage holding your archive reports a problem" + storageProblems(report) +
				". It holds your only copy: replace a failing drive soon, and keep iCloud cleanup off until then. The app shows which and why.",
			Priority: notify.PriorityUrgent,
			Tags:     []string{"rotating_light"},
		})
	}
	return nil
}

// storageProblems counts the drives and arrays with a problem, for an alert:
// ntfy topics are public, so the alert never names a drive (its model, serial
// number or device path) or quotes a reason. The app and the log do.
func storageProblems(r health.Report) string {
	var parts []string
	add := func(n int, what, verdict string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s %s", n, what, verdict))
		}
	}
	count := func(states []health.State, want health.State) int {
		n := 0
		for _, s := range states {
			if s == want {
				n++
			}
		}
		return n
	}
	drives := make([]health.State, 0, len(r.Drives))
	for _, d := range r.Drives {
		drives = append(drives, d.State)
	}
	arrays := make([]health.State, 0, len(r.Arrays))
	for _, a := range r.Arrays {
		arrays = append(arrays, a.State)
	}
	add(count(drives, health.StateFailing), "drive(s)", "failing")
	add(count(arrays, health.StateFailing), "RAID array(s)", "failing")
	add(count(drives, health.StateWarning), "drive(s)", "with a warning")
	add(count(arrays, health.StateWarning), "RAID array(s)", "with a warning")
	if len(parts) == 0 {
		return ""
	}
	return ": " + strings.Join(parts, ", ")
}

// hideHiddenFolder keeps the folder holding iCloud's Hidden photos hidden on
// disk as well, once kei has created it.
func (a *Agent) hideHiddenFolder() error {
	if !*a.cfg.ICloud.IncludeHidden {
		return nil
	}
	dir := kei.HiddenPath(a.cfg.Storage.Root)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("hidden photos folder: %w", err)
	}
	if err := platform.MarkHidden(dir); err != nil {
		return fmt.Errorf("hidden photos folder: %w", err)
	}
	return nil
}

// previewPage is how many assets preparePreviews reads from the ledger at once.
const previewPage = 200

// ThumbnailDir is where rendered thumbnails and previews are cached: derived
// data inside the archive root, hidden like the other dot-folders.
func ThumbnailDir(cfg *config.Config) string {
	return filepath.Join(cfg.Storage.Root, ".cache", "thumbnails")
}

// preparePreviews renders the grid thumbnail of every asset that lacks one,
// so the library browser opens fast. Thumbnails are derived data: failures are
// logged and retried next cycle, never failing the cycle.
func (a *Agent) preparePreviews(ctx context.Context) {
	a.setPhase(PhasePreparingPreviews)
	hiddenDir := kei.HiddenPath(a.cfg.Storage.Root) + string(filepath.Separator)
	rendered, failed := 0, 0
	for _, hidden := range []bool{false, true} {
		var after *ledger.LibraryCursor
		for {
			page, err := a.db.LibraryPage(ctx, ledger.LibraryQuery{LibraryFilter: ledger.LibraryFilter{HiddenDir: hiddenDir, Hidden: hidden}, After: after, Limit: previewPage})
			if err != nil {
				a.log.ErrorContext(ctx, "previews not prepared: the library could not be read", "err", err)
				return
			}
			for _, asset := range page {
				if asset.Display == nil {
					continue // nothing displayable on disk yet
				}
				cached := a.renderer.CachePath(asset.Display.SHA256, thumbs.Thumb)
				if _, statErr := os.Stat(cached); statErr == nil {
					continue
				}
				// The renderer logs a failure, once until the file renders.
				if _, err := a.renderer.Ensure(ctx, thumbs.Source{Path: asset.Display.LocalPath, SHA256: asset.Display.SHA256}, thumbs.Thumb); err != nil {
					if ctx.Err() != nil {
						return
					}
					failed++
					continue
				}
				rendered++
			}
			if len(page) < previewPage {
				break
			}
			after = &page[len(page)-1].Cursor
		}
	}
	if err := platform.MarkHidden(filepath.Dir(ThumbnailDir(a.cfg))); err != nil && !errors.Is(err, fs.ErrNotExist) {
		a.log.WarnContext(ctx, "preview cache folder not hidden", "err", err)
	}
	// Files that keep failing are retried every pass: worth a line only when
	// something new was rendered.
	if rendered > 0 {
		a.log.InfoContext(ctx, "previews prepared", "rendered", rendered, "failed", failed)
	}
}
