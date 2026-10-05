package app

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/health"
	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// Notifier delivers operator alerts.
type Notifier interface {
	Send(ctx context.Context, m notify.Msg) error
}

// authAlertEvery throttles repeated "sign-in needed" alerts.
const authAlertEvery = 24 * time.Hour

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

	hub     *Hub
	trigger chan struct{}

	mu            sync.Mutex
	lastAuthAlert time.Time
}

// NewAgent returns an idle agent. keiLog receives kei's own output.
func NewAgent(cfg *config.Config, db *ledger.DB, log *slog.Logger, n Notifier, keiLog io.Writer, now func() time.Time) *Agent {
	return &Agent{
		cfg: cfg, db: db, log: log, notify: n, keiLog: keiLog, now: now,
		readDrive: func(ctx context.Context) (platform.DriveHealth, error) {
			return platform.DriveHealthOf(ctx, cfg.Storage.Root)
		},
		hub:     NewHub(),
		trigger: make(chan struct{}, 1),
	}
}

// Hub exposes the agent's live status.
func (a *Agent) Hub() *Hub { return a.hub }

// RequestCycle asks for a cycle to start now. It reports false, and does
// nothing, when a cycle is already running or requested.
func (a *Agent) RequestCycle() bool {
	if st, _ := a.hub.Snapshot(); st.Phase != PhaseIdle {
		return false
	}
	select {
	case a.trigger <- struct{}{}:
		return true
	default:
		return false
	}
}

// Run executes a cycle immediately, then again schedule.sync_interval after
// each cycle ends or as soon as one is requested, until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	interval := a.cfg.Schedule.SyncInterval.Std()
	for {
		if err := a.Cycle(ctx); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("agent stopped: %w", ctx.Err())
			}
			a.log.Error("cycle failed", "err", err)
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

// KeiOutcomeError is CycleReport.KeiOutcome when kei could not run or exited
// with an unexpected code.
const KeiOutcomeError = "error"

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
	ok := cycleErr == nil && rep.Verified.Mismatched == 0
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

	a.setPhase(PhaseDownloading)
	outcome, err := kei.Sync(ctx, a.cfg.Kei, a.keiLog)
	switch {
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
	if err == nil {
		rep.KeiOutcome = outcome.String()
	} else {
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
	verified, err := Verify(ctx, a.cfg, a.db, a.log, a.now)
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
	return rep, errors.Join(errs...)
}

func (a *Agent) alertAuth(ctx context.Context) {
	a.mu.Lock()
	due := a.lastAuthAlert.IsZero() || a.now().Sub(a.lastAuthAlert) >= authAlertEvery
	if due {
		a.lastAuthAlert = a.now()
	}
	a.mu.Unlock()
	a.log.Warn("kei needs Apple sign-in (2FA)")
	if due {
		a.alert(ctx, notify.Msg{
			Title:    "homecloud: Apple sign-in needed",
			Body:     "New photos are not being downloaded. Run: kei login --config <kei config> and approve the code on your iPhone.",
			Priority: notify.PriorityHigh,
			Tags:     []string{"key"},
		})
	}
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
