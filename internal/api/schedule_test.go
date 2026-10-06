package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/ledger"
)

// A maintenance job that finished with findings (no "error") counts as
// completed, exactly as the agent's scheduler counts it; one that could not
// do its work is retried at the next cycle.
func TestNextRunFollowsTheScheduler(t *testing.T) {
	ctx := context.Background()
	db, err := ledger.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	t0 := time.Date(2026, 10, 6, 13, 5, 0, 0, time.UTC)
	nextCycle := t0.Add(55 * time.Minute)
	s := &Service{db: db, cfg: &config.Config{Schedule: config.Schedule{SyncInterval: config.Duration(time.Hour)}}}

	finish := func(kind string, ok bool, rep any) {
		t.Helper()
		id, err := db.StartRun(ctx, kind, t0)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.FinishRun(ctx, id, ok, rep, t0); err != nil {
			t.Fatal(err)
		}
	}
	finish(app.IPhoneBackupRunKind, false, app.IPhoneBackupReport{})
	finish(app.LedgerBackupRunKind, false, app.LedgerBackupReport{Error: "disk full"})

	for _, tt := range []struct {
		kind string
		want time.Time
	}{
		{app.IPhoneBackupRunKind, nextCycle.Add(24 * time.Hour)}, // due 13:05 tomorrow; cycles run at :00
		{app.LedgerBackupRunKind, nextCycle},                     // could not run: retried
		{app.ScrubRunKind, nextCycle},                            // never ran
	} {
		got, err := s.nextRun(ctx, tt.kind, 24*time.Hour, nextCycle)
		if err != nil {
			t.Fatal(err)
		}
		if !got.AsTime().Equal(tt.want) {
			t.Errorf("%s: next = %v, want %v", tt.kind, got.AsTime(), tt.want)
		}
	}
}
