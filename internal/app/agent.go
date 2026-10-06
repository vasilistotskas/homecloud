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
	"sync"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/export"
	"github.com/vasilistotskas/homecloud/internal/health"
	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/thumbs"
)

// Notifier delivers operator alerts.
type Notifier interface {
	Send(ctx context.Context, m notify.Msg) error
}

// repeatAlertEvery throttles alerts about a condition that persists across
// cycles (sign-in needed, archive drive missing, low space).
const repeatAlertEvery = 24 * time.Hour

// Alert keys of throttled alerts.
const (
	alertSignIn  = "sign-in"
	alertVolume  = "archive-volume"
	alertLowDisk = "low-space"
	alertBackup  = "ledger-backup"
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
	// readDrive reads the archive drive's SMART data.
	readDrive func(context.Context) (platform.DriveHealth, error)
	// checkArchive confirms the archive volume is the configured one.
	checkArchive func() error
	// checkSpace reports the archive volume's space, wrapping
	// platform.ErrLowSpace below storage.min_free_percent.
	checkSpace func() (platform.Space, error)
	// renderer makes the library browser's thumbnails.
	renderer *thumbs.Renderer

	hub     *Hub
	trigger chan struct{}

	mu         sync.Mutex
	lastAlerts map[string]time.Time
}

// NewAgent returns an idle agent. keiLog receives kei's own output.
func NewAgent(cfg *config.Config, db *ledger.DB, log *slog.Logger, n Notifier, keiLog io.Writer,
	renderer *thumbs.Renderer, now func() time.Time,
) *Agent {
	return &Agent{
		cfg: cfg, db: db, log: log, notify: n, keiLog: keiLog, renderer: renderer, now: now,
		readDrive: func(ctx context.Context) (platform.DriveHealth, error) {
			return platform.DriveHealthOf(ctx, cfg.Storage.Root)
		},
		checkArchive: func() error { return platform.CheckVolume(cfg.Storage.Root, cfg.Storage.VolumeID) },
		checkSpace: func() (platform.Space, error) {
			return platform.CheckFreeSpace(cfg.Storage.Root, cfg.Storage.MinFreePercent)
		},
		hub:        NewHub(),
		trigger:    make(chan struct{}, 1),
		lastAlerts: map[string]time.Time{},
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
		default:
			return fmt.Errorf("run %d has unknown kind %q", r.ID, r.Kind)
		}
		if err := a.db.FinishRun(ctx, r.ID, false, detail, a.now()); err != nil {
			return err
		}
		a.log.Warn("closed interrupted run", "run", r.ID, "kind", r.Kind, "started", r.StartedAt)
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
		if err := a.Cycle(ctx); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("agent stopped: %w", ctx.Err())
			}
			a.log.Error("cycle failed", "err", err)
		}
		if err := a.maintain(ctx); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("agent stopped: %w", ctx.Err())
			}
			a.log.Error("maintenance failed", "err", err)
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
	KeiOutcome string             `json:"kei_outcome"`
	Ingested   ledger.IngestStats `json:"ingested"`
	Verified   VerifyStats        `json:"verified"`
	Error      string             `json:"error,omitempty"`
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
	runID, err := a.db.StartRun(ctx, CycleRunKind, a.now())
	if err != nil {
		return err
	}
	rep, cycleErr := a.cycle(ctx)
	if cycleErr != nil {
		rep.Error = cycleErr.Error()
	}
	// Files that could not be checked (rep.Verified.Errored) are retried next
	// cycle, but this cycle did not verify everything it should have.
	ok := cycleErr == nil && rep.Verified.Mismatched == 0 && rep.Verified.Errored == 0
	// Record the outcome even when ctx was cancelled mid-cycle.
	finishErr := a.db.FinishRun(context.WithoutCancel(ctx), runID, ok, rep, a.now())
	return errors.Join(cycleErr, finishErr)
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
	outcome, err := a.download(ctx)
	switch {
	case errors.Is(err, platform.ErrLowSpace):
		// Below storage.min_free_percent: archive what is on disk, download nothing new.
		a.alertRepeated(ctx, alertLowDisk, notify.Msg{
			Title:    "homecloud: archive drive almost full",
			Body:     "Free space is below the configured minimum, so new photos are not downloaded. Existing files are still checked.",
			Priority: notify.PriorityHigh,
			Tags:     []string{"warning"},
		})
		a.log.Warn("download skipped", "err", err)
	case err != nil:
		errs = append(errs, fmt.Errorf("kei sync: %w", err))
		a.alert(ctx, notify.Msg{Title: "homecloud: download failed", Body: "kei sync exited unexpectedly; see logs.", Priority: notify.PriorityHigh, Tags: []string{"warning"}})
	case outcome == kei.OutcomeAuthRequired:
		a.alertAuth(ctx)
	case outcome == kei.OutcomeAuthTerminal:
		a.alert(ctx, notify.Msg{Title: "homecloud: Apple account needs attention", Body: "Apple reported an account state that needs you (kei exit 4). Run kei login and check appleid.apple.com.", Priority: notify.PriorityUrgent, Tags: []string{"rotating_light"}})
	case outcome == kei.OutcomePartial:
		a.log.Warn("kei sync partially failed; failed assets retry next cycle")
	case outcome == kei.OutcomeOK:
	}
	switch {
	case err == nil:
		rep.KeiOutcome = outcome.String()
	case errors.Is(err, platform.ErrLowSpace):
		rep.KeiOutcome = KeiOutcomeLowSpace
	default:
		rep.KeiOutcome = KeiOutcomeError
	}
	a.log.Info("kei sync finished", "outcome", rep.KeiOutcome)

	a.setPhase(PhaseIngesting)
	ingested, err := Ingest(ctx, a.cfg, a.db, a.now())
	rep.Ingested = ingested
	if err != nil {
		return rep, errors.Join(append(errs, err)...)
	}
	a.log.Info("ingested", "assets", ingested.Assets, "new", ingested.NewRenditions, "changed", ingested.ChangedRenditions)

	a.setPhase(PhaseVerifying)
	verified, err := Verify(ctx, a.cfg, a.db, a.log, a.now, a.hub.Changed)
	rep.Verified = verified
	if err != nil {
		errs = append(errs, err)
	}
	a.log.Info("verified", "checked", verified.Checked, "passed", verified.Passed, "mismatched", verified.Mismatched, "errored", verified.Errored)
	if verified.Mismatched > 0 {
		a.alert(ctx, notify.Msg{
			Title:    "homecloud: files failed verification",
			Body:     fmt.Sprintf("%d file(s) did not match their download checksum or structure and were quarantined. Nothing will be deleted for those assets.", verified.Mismatched),
			Priority: notify.PriorityUrgent,
			Tags:     []string{"rotating_light"},
		})
	}
	if err := a.checkDrive(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := a.hideHiddenFolder(); err != nil {
		errs = append(errs, err)
	}
	if files, err := export.Write(ctx, a.db, a.cfg.Storage.Root); err != nil {
		errs = append(errs, err)
	} else {
		a.log.Info("manifest exported", "files", files)
	}
	a.preparePreviews(ctx)
	return rep, errors.Join(errs...)
}

func (a *Agent) alertAuth(ctx context.Context) {
	a.log.Warn("kei needs Apple sign-in (2FA)")
	a.alertRepeated(ctx, alertSignIn, notify.Msg{
		Title:    "homecloud: Apple sign-in needed",
		Body:     "New photos are not being downloaded. Run: kei login --config <kei config> and approve the code on your iPhone.",
		Priority: notify.PriorityHigh,
		Tags:     []string{"key"},
	})
}

// alertRepeated sends m at most once per repeatAlertEvery for key, so a
// condition that persists across hourly cycles alerts once a day.
func (a *Agent) alertRepeated(ctx context.Context, key string, m notify.Msg) {
	a.mu.Lock()
	last, seen := a.lastAlerts[key]
	due := !seen || a.now().Sub(last) >= repeatAlertEvery
	if due {
		a.lastAlerts[key] = a.now()
	}
	a.mu.Unlock()
	if due {
		a.alert(ctx, m)
	}
}

// download runs kei unless the archive volume is below its free-space
// minimum, in which case it returns an ErrLowSpace-wrapping error.
func (a *Agent) download(ctx context.Context) (kei.Outcome, error) {
	if _, err := a.checkSpace(); err != nil {
		return 0, err
	}
	return kei.Sync(ctx, a.cfg.Kei, a.keiLog)
}

func (a *Agent) alert(ctx context.Context, m notify.Msg) {
	if err := a.notify.Send(ctx, m); err != nil {
		a.log.Error("notification failed", "title", m.Title, "err", err)
	}
}

// ParseCycleReport decodes the detail recorded for a finished cycle run.
func ParseCycleReport(detail string) (CycleReport, error) {
	var rep CycleReport
	if err := json.Unmarshal([]byte(detail), &rep); err != nil {
		return CycleReport{}, fmt.Errorf("decode cycle report: %w", err)
	}
	return rep, nil
}

// checkDrive reads and judges the archive drive, publishes the result, and
// records and alerts on changes. The archive is the only copy of the library,
// so a drive that predicts its own failure is urgent.
func (a *Agent) checkDrive(ctx context.Context) error {
	report := health.Check(func() (platform.DriveHealth, error) { return a.readDrive(ctx) }, a.now())
	a.hub.update(func(s *Status) { s.Drive = report })

	previous, changed, err := a.db.RecordDriveHealth(ctx, string(report.State), report.Reasons, report.CheckedAt)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	a.log.Info("archive drive health changed", "from", previous, "to", report.State, "reasons", report.Reasons)
	if report.State == health.StateWarning || report.State == health.StateFailing {
		a.alert(ctx, notify.Msg{
			Title: "homecloud: archive drive needs attention",
			Body: "The drive holding your archive reports a problem (" + strings.Join(report.Reasons, "; ") +
				"). It holds your only copy: replace it soon, and keep iCloud cleanup off until then.",
			Priority: notify.PriorityUrgent,
			Tags:     []string{"rotating_light"},
		})
	}
	return nil
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
			page, err := a.db.LibraryPage(ctx, ledger.LibraryQuery{HiddenDir: hiddenDir, Hidden: hidden, After: after, Limit: previewPage})
			if err != nil {
				a.log.Error("prepare previews: read library", "err", err)
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
				if _, err := a.renderer.Ensure(ctx, thumbs.Source{Path: asset.Display.LocalPath, SHA256: asset.Display.SHA256}, thumbs.Thumb); err != nil {
					if ctx.Err() != nil {
						return
					}
					failed++
					a.log.Warn("prepare previews: render", "asset", asset.AssetID, "err", err)
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
		a.log.Warn("prepare previews: hide cache folder", "err", err)
	}
	if rendered > 0 || failed > 0 {
		a.log.Info("previews prepared", "rendered", rendered, "failed", failed)
	}
}
