package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/ledger"
)

// verifiedAgent returns an agent whose single fixture photo is verified, and
// that photo's path.
func verifiedAgent(t *testing.T, clock *time.Time) (*Agent, *recordingNotifier, string) {
	t.Helper()
	manifest := fixture(t, false)
	agent, n := newTestAgent(t, manifest, "0", clock)
	if err := agent.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	return agent, n, filepath.Join(filepath.Dir(manifest), "IMG_0001_QUFBQTA.HEIC")
}

func lastRun(t *testing.T, a *Agent, kind string) ledger.Run {
	t.Helper()
	run, err := a.db.LastFinishedRun(context.Background(), kind)
	if err != nil {
		t.Fatalf("last %s run: %v", kind, err)
	}
	return run
}

func TestScrubPassesAndIsNotDueAgain(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, _, _ := verifiedAgent(t, &clock)
	ctx := context.Background()
	if err := agent.maintain(ctx); err != nil {
		t.Fatal(err)
	}
	run := lastRun(t, agent, ScrubRunKind)
	rep, err := ParseScrubReport(run.Detail)
	if err != nil || !*run.OK || rep.Eligible != 1 || rep.Passed != 1 {
		t.Fatalf("scrub run ok=%v report=%+v, %v", *run.OK, rep, err)
	}

	clock = clock.Add(time.Hour)
	if err := agent.maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if again := lastRun(t, agent, ScrubRunKind); again.ID != run.ID {
		t.Fatalf("scrub ran again after an hour (run %d, was %d)", again.ID, run.ID)
	}
	if st, _ := agent.Hub().Snapshot(); st.Phase != PhaseIdle {
		t.Fatalf("phase after maintenance = %v, want idle", st.Phase)
	}
}

func TestScrubQuarantinesChangedAndMissingFiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(path string) error
	}{
		{"changed", func(p string) error { return os.WriteFile(p, []byte("bit rot"), 0o600) }},
		{"missing", os.Remove},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
			agent, n, photo := verifiedAgent(t, &clock)
			ctx := context.Background()
			if err := tc.damage(photo); err != nil {
				t.Fatal(err)
			}
			if err := agent.maintain(ctx); err != nil {
				t.Fatal(err)
			}
			run := lastRun(t, agent, ScrubRunKind)
			rep, err := ParseScrubReport(run.Detail)
			if err != nil || *run.OK || rep.Mismatched != 1 {
				t.Fatalf("scrub run ok=%v report=%+v, %v", *run.OK, rep, err)
			}
			counts, err := agent.db.StateCounts(ctx)
			if err != nil || counts[ledger.StateQuarantined] != 1 {
				t.Fatalf("state counts = %v, %v", counts, err)
			}
			if !slices.Contains(n.titles(), "homecloud: archived files changed on disk") {
				t.Fatalf("alerts = %v, want the scrub alert", n.titles())
			}
		})
	}
}

func TestScrubDoesNothingWithoutTheArchiveVolume(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, _, photo := verifiedAgent(t, &clock)
	ctx := context.Background()
	agent.checkArchive = func() error { return errors.New("volume not mounted") }
	if err := os.Remove(photo); err != nil {
		t.Fatal(err)
	}
	if err := agent.maintain(ctx); err != nil {
		t.Fatal(err)
	}
	run := lastRun(t, agent, ScrubRunKind)
	rep, err := ParseScrubReport(run.Detail)
	if err != nil || *run.OK || rep.Checked != 0 || rep.Error == "" {
		t.Fatalf("scrub run ok=%v report=%+v, %v", *run.OK, rep, err)
	}
	if counts, err := agent.db.StateCounts(ctx); err != nil || counts[ledger.StateQuarantined] != 0 {
		t.Fatalf("state counts = %v, %v; nothing may be quarantined", counts, err)
	}
}

func TestLedgerBackupKeepsNewestSnapshots(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, _, _ := verifiedAgent(t, &clock)
	ctx := context.Background()
	dir := agent.cfg.Backup.Dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(unrelated, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	for range 3 {
		if err := agent.maintain(ctx); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(25 * time.Hour)
	}
	run := lastRun(t, agent, LedgerBackupRunKind)
	rep, err := ParseLedgerBackupReport(run.Detail)
	if err != nil || !*run.OK || rep.Pruned != 1 || rep.SizeBytes == 0 {
		t.Fatalf("backup run ok=%v report=%+v, %v", *run.OK, rep, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"homecloud-20261007T040000Z.db", "homecloud-20261008T050000Z.db", "notes.txt"}
	if !slices.Equal(names, want) {
		t.Fatalf("backup dir = %v, want %v", names, want)
	}

	snap, err := ledger.Open(ctx, rep.File)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Close() }()
	if counts, err := snap.StateCounts(ctx); err != nil || counts[ledger.StateVerified] != 1 {
		t.Fatalf("snapshot state counts = %v, %v", counts, err)
	}
}
