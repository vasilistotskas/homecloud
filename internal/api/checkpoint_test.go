package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv3 "github.com/vasilistotskas/homecloud/gen/homecloud/agent/v3"
	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/ledger"
)

func openLedger(t *testing.T) *ledger.DB {
	t.Helper()
	db, err := ledger.Open(context.Background(), filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

// The overview's checkpoint comes from the latest cycle in which kei wrote a
// report: a later cycle in which kei failed without one does not hide it.
func TestDownloaderCheckpointSurvivesCyclesWithoutReport(t *testing.T) {
	ctx := context.Background()
	db := openLedger(t)
	t0 := time.Date(2026, 10, 9, 16, 0, 0, 0, time.UTC)
	s := &Service{db: db}
	finish := func(rep app.CycleReport) {
		t.Helper()
		id, err := db.StartRun(ctx, app.CycleRunKind, t0)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.FinishRun(ctx, id, rep.Error == "", rep, t0); err != nil {
			t.Fatal(err)
		}
	}

	if got, err := s.downloaderCheckpoint(ctx); err != nil || got != nil {
		t.Fatalf("before any cycle = %v, %v; want none", got, err)
	}
	finish(app.CycleReport{KeiOutcome: "ok", Checkpoint: &app.Checkpoint{
		Blocked: true, BlockedCycles: 3, Reason: "inventory_delta_bridge_failed", Source: "kei", Zone: "PrimarySync",
		Explanation:           "the prior provider checkpoint could not be replayed incrementally after inventory reconciliation",
		FullEnumerationReason: "enum_config_hash_drift",
	}})
	finish(app.CycleReport{KeiOutcome: app.KeiOutcomeError, Error: "kei sync failed with exit code 1"})

	want := &agentv3.DownloaderCheckpoint{
		Blocked: true, BlockedCycles: 3, Reason: "inventory_delta_bridge_failed", Source: "kei", Zone: "PrimarySync",
		Explanation:           "the prior provider checkpoint could not be replayed incrementally after inventory reconciliation",
		FullEnumerationReason: "enum_config_hash_drift",
	}
	got, err := s.downloaderCheckpoint(ctx)
	if err != nil || !proto.Equal(got, want) {
		t.Fatalf("checkpoint = %v, %v; want %v", got, err, want)
	}

	// The cycle without a report has no checkpoint of its own.
	runs, err := db.Runs(ctx, app.CycleRunKind, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	run, err := runToProto(runs[0])
	if err != nil || run.GetCycle().GetCheckpoint() != nil {
		t.Fatalf("cycle without a kei report = %v, %v; want no checkpoint", run, err)
	}
}
