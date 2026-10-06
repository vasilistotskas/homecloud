package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

func TestLowSpaceSkipsDownloadButStillVerifies(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	// kei would exit 1 (unexpected) if it ran: the cycle must not run it.
	agent, n := newTestAgent(t, fixture(t, false), "1", &clock)
	agent.checkSpace = func() (platform.Space, error) {
		return platform.Space{}, fmt.Errorf("%w: test volume", platform.ErrLowSpace)
	}
	ctx := context.Background()
	for range 2 {
		if err := agent.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(time.Hour)
	}
	rep, err := ParseCycleReport(lastRun(t, agent, CycleRunKind).Detail)
	if err != nil || rep.KeiOutcome != KeiOutcomeLowSpace {
		t.Fatalf("report = %+v, %v; want a skipped download", rep, err)
	}
	if counts, err := agent.db.StateCounts(ctx); err != nil || counts[ledger.StateVerified] != 1 {
		t.Fatalf("state counts = %v, %v; files on disk must still be verified", counts, err)
	}
	alerts := 0
	for _, title := range n.titles() {
		if title == "homecloud: archive drive almost full" {
			alerts++
		}
	}
	if alerts != 1 {
		t.Fatalf("low-space alerts = %d over two hourly cycles, want 1 (alerts: %v)", alerts, n.titles())
	}
}

func TestMissingArchiveVolumeStopsTheCycle(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, n := newTestAgent(t, fixture(t, false), "0", &clock)
	agent.checkArchive = func() error { return errors.New("volume not mounted") }
	ctx := context.Background()
	if err := agent.Cycle(ctx); err == nil {
		t.Fatal("cycle against a missing archive volume succeeded")
	}
	run := lastRun(t, agent, CycleRunKind)
	rep, err := ParseCycleReport(run.Detail)
	if err != nil || *run.OK || rep.KeiOutcome != KeiOutcomeArchiveUnavailable || rep.Ingested.Assets != 0 {
		t.Fatalf("run ok=%v report=%+v, %v; want a failed cycle that touched nothing", *run.OK, rep, err)
	}
	if !slices.Contains(n.titles(), "homecloud: archive drive not available") {
		t.Fatalf("alerts = %v, want the archive-drive alert", n.titles())
	}
}

func TestCloseInterruptedRuns(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, _ := newTestAgent(t, fixture(t, false), "0", &clock)
	ctx := context.Background()
	for _, kind := range []string{CycleRunKind, ScrubRunKind, LedgerBackupRunKind} {
		if _, err := agent.db.StartRun(ctx, kind, clock); err != nil {
			t.Fatal(err)
		}
	}
	if err := agent.closeInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	if open, err := agent.db.UnfinishedRuns(ctx); err != nil || len(open) != 0 {
		t.Fatalf("unfinished after close = %v, %v", open, err)
	}
	cycle, err := ParseCycleReport(lastRun(t, agent, CycleRunKind).Detail)
	if err != nil || cycle.KeiOutcome != KeiOutcomeInterrupted || cycle.Error == "" {
		t.Fatalf("cycle report = %+v, %v", cycle, err)
	}
	scrub, err := ParseScrubReport(lastRun(t, agent, ScrubRunKind).Detail)
	if err != nil || scrub.Error == "" {
		t.Fatalf("scrub report = %+v, %v", scrub, err)
	}
	backup, err := ParseLedgerBackupReport(lastRun(t, agent, LedgerBackupRunKind).Detail)
	if err != nil || backup.Error == "" {
		t.Fatalf("backup report = %+v, %v", backup, err)
	}
}
