package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
)

// Notifier delivers operator alerts.
type Notifier interface {
	Send(ctx context.Context, m notify.Msg) error
}

// authAlertEvery throttles repeated "sign-in needed" alerts.
const authAlertEvery = 24 * time.Hour

// Agent runs the periodic sync → ingest → verify cycle.
type Agent struct {
	Cfg    *config.Config
	DB     *ledger.DB
	Log    *slog.Logger
	Notify Notifier
	// KeiLog receives kei's own output.
	KeiLog io.Writer
	Now    func() time.Time

	mu            sync.Mutex
	lastAuthAlert time.Time
}

// Run executes a cycle immediately and then every schedule.sync_interval until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	ticker := time.NewTicker(a.Cfg.Schedule.SyncInterval.Std())
	defer ticker.Stop()
	for {
		if err := a.Cycle(ctx); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("agent stopped: %w", ctx.Err())
			}
			a.Log.Error("cycle failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("agent stopped: %w", ctx.Err())
		case <-ticker.C:
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

// Cycle performs one kei sync followed by ingest and verification and records
// the run in the ledger.
func (a *Agent) Cycle(ctx context.Context) error {
	runID, err := a.DB.StartRun(ctx, "cycle", a.Now())
	if err != nil {
		return err
	}
	rep, cycleErr := a.cycle(ctx)
	if cycleErr != nil {
		rep.Error = cycleErr.Error()
	}
	ok := cycleErr == nil && rep.Verified.Mismatched == 0
	// Record the outcome even when ctx was cancelled mid-cycle.
	finishErr := a.DB.FinishRun(context.WithoutCancel(ctx), runID, ok, rep, a.Now())
	return errors.Join(cycleErr, finishErr)
}

// cycle does the work. kei problems do not skip ingest/verify: the manifest
// stays authoritative for what is on disk.
func (a *Agent) cycle(ctx context.Context) (CycleReport, error) {
	var (
		errs []error
		rep  CycleReport
	)

	outcome, err := kei.Sync(ctx, a.Cfg.Kei, a.KeiLog)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("kei sync: %w", err))
		a.alert(ctx, notify.Msg{Title: "homecloud: download failed", Body: "kei sync exited unexpectedly; see logs.", Priority: notify.PriorityHigh, Tags: []string{"warning"}})
	case outcome == kei.OutcomeAuthRequired:
		a.alertAuth(ctx)
	case outcome == kei.OutcomeAuthTerminal:
		a.alert(ctx, notify.Msg{Title: "homecloud: Apple account needs attention", Body: "Apple reported an account state that needs you (kei exit 4). Run kei login and check appleid.apple.com.", Priority: notify.PriorityUrgent, Tags: []string{"rotating_light"}})
	case outcome == kei.OutcomePartial:
		a.Log.Warn("kei sync partially failed; failed assets retry next cycle")
	case outcome == kei.OutcomeOK:
	}
	if err == nil {
		rep.KeiOutcome = outcome.String()
	} else {
		rep.KeiOutcome = "error"
	}
	a.Log.Info("kei sync finished", "outcome", rep.KeiOutcome)

	ingested, err := Ingest(ctx, a.Cfg, a.DB, a.Now())
	rep.Ingested = ingested
	if err != nil {
		return rep, errors.Join(append(errs, err)...)
	}
	a.Log.Info("ingested", "assets", ingested.Assets, "new", ingested.NewRenditions, "changed", ingested.ChangedRenditions)

	verified, err := Verify(ctx, a.Cfg, a.DB, a.Log, a.Now)
	rep.Verified = verified
	if err != nil {
		errs = append(errs, err)
	}
	a.Log.Info("verified", "checked", verified.Checked, "passed", verified.Passed, "mismatched", verified.Mismatched, "errored", verified.Errored)
	if verified.Mismatched > 0 {
		a.alert(ctx, notify.Msg{
			Title:    "homecloud: files failed verification",
			Body:     fmt.Sprintf("%d file(s) did not match their download checksum or structure and were quarantined. Nothing will be deleted for those assets.", verified.Mismatched),
			Priority: notify.PriorityUrgent,
			Tags:     []string{"rotating_light"},
		})
	}
	return rep, errors.Join(errs...)
}

func (a *Agent) alertAuth(ctx context.Context) {
	a.mu.Lock()
	due := a.lastAuthAlert.IsZero() || a.Now().Sub(a.lastAuthAlert) >= authAlertEvery
	if due {
		a.lastAuthAlert = a.Now()
	}
	a.mu.Unlock()
	a.Log.Warn("kei needs Apple sign-in (2FA)")
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
	if err := a.Notify.Send(ctx, m); err != nil {
		a.Log.Error("notification failed", "title", m.Title, "err", err)
	}
}
