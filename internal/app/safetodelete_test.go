package app

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
	_ "time/tzdata" // the test loads Europe/Athens wherever it runs

	"github.com/google/go-cmp/cmp"

	"github.com/vasilistotskas/homecloud/internal/export"
	"github.com/vasilistotskas/homecloud/internal/health"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/policy"
)

type archivedAssets []ledger.ArchivedAsset

func (s archivedAssets) EachArchivedAsset(_ context.Context, fn func(ledger.ArchivedAsset) error) error {
	for _, a := range s {
		if err := fn(a); err != nil {
			return err
		}
	}
	return nil
}

// archivedPhoto is a photo verified at verified and scrubbed a week later,
// taken at taken and added to iCloud at added, its file size bytes.
func archivedPhoto(id string, taken, added, verified time.Time, bytes int64) ledger.ArchivedAsset {
	scrubbed := verified.Add(7 * 24 * time.Hour)
	return ledger.ArchivedAsset{
		LibraryAsset: ledger.LibraryAsset{
			Library: "PrimarySync", AssetID: id, Kind: ledger.KindPhoto, State: ledger.StateVerified, CreatedAt: &taken,
		},
		AddedAt: &added,
		Renditions: []ledger.RenditionEvidence{{
			Version: ledger.VersionOriginal, MediaType: "photo", SizeBytes: bytes,
			Passed: map[ledger.Tier]time.Time{ledger.TierContent: verified}, ConfirmedAt: &scrubbed,
		}},
	}
}

func TestBuildSafeToDelete(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	verified := now.Add(-30 * 24 * time.Hour)
	old := now.Add(-2 * 365 * 24 * time.Hour)
	// 22:30 UTC on 31 March is already April in Athens (UTC+3).
	lateMarch := time.Date(2024, 3, 31, 22, 30, 0, 0, time.UTC)
	quarantined := archivedPhoto("Q", old, old, verified, 1)
	quarantined.State = ledger.StateQuarantined
	unscrubbed := archivedPhoto("U", old, old, verified, 1)
	unscrubbed.Renditions[0].ConfirmedAt = &verified
	db := archivedAssets{
		archivedPhoto("A", time.Date(2024, 5, 2, 9, 0, 0, 0, time.UTC), old, verified, 300),
		archivedPhoto("B", lateMarch, old, verified, 200),
		archivedPhoto("C", time.Date(2024, 3, 3, 9, 0, 0, 0, time.UTC), old, verified, 100),
		archivedPhoto("R", old, now.Add(-time.Hour), verified, 1),
		quarantined, unscrubbed,
	}
	ok := health.Report{State: health.StateOK, CheckedAt: now}

	r, err := BuildSafeToDelete(context.Background(), db, 365*24*time.Hour, ok, now)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range r.Items {
		ids = append(ids, a.AssetID)
	}
	if r.Withheld != "" || !slices.Equal(ids, []string{"A", "B", "C"}) || r.Bytes() != 600 {
		t.Fatalf("report = withheld %q, items %v, %d bytes", r.Withheld, ids, r.Bytes())
	}
	wantCounts := map[policy.Verdict]int{policy.Eligible: 3, policy.TooRecent: 1, policy.NotVerified: 1, policy.AwaitingScrub: 1}
	if diff := cmp.Diff(wantCounts, r.Counts); diff != "" {
		t.Errorf("counts (-want +got):\n%s", diff)
	}

	athens, err := time.LoadLocation("Europe/Athens")
	if err != nil {
		t.Fatal(err)
	}
	want := []MonthTotal{
		{Year: 2024, Month: time.May, Assets: 1, Bytes: 300},
		{Year: 2024, Month: time.April, Assets: 1, Bytes: 200},
		{Year: 2024, Month: time.March, Assets: 1, Bytes: 100},
	}
	if diff := cmp.Diff(want, r.Months(athens)); diff != "" {
		t.Errorf("months in Athens (-want +got):\n%s", diff)
	}
	want = []MonthTotal{{Year: 2024, Month: time.May, Assets: 1, Bytes: 300}, {Year: 2024, Month: time.March, Assets: 2, Bytes: 300}}
	if diff := cmp.Diff(want, r.Months(time.UTC)); diff != "" {
		t.Errorf("months in UTC (-want +got):\n%s", diff)
	}

	undated := archivedPhoto("D", old, old, verified, 5)
	undated.CreatedAt = nil
	r, err = BuildSafeToDelete(context.Background(), archivedAssets{db[0], undated}, 365*24*time.Hour, ok, now)
	if err != nil {
		t.Fatal(err)
	}
	want = []MonthTotal{{Year: 2024, Month: time.May, Assets: 1, Bytes: 300}, {Assets: 1, Bytes: 5}}
	if diff := cmp.Diff(want, r.Months(time.UTC)); diff != "" {
		t.Errorf("months with an undated item (-want +got):\n%s", diff)
	}
}

func TestSafeToDeleteWithheldUnlessStorageHealthy(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * 365 * 24 * time.Hour)
	db := archivedAssets{archivedPhoto("A", old, old, now.Add(-30*24*time.Hour), 1)}
	for _, tc := range []struct {
		name     string
		storage  health.Report
		withheld string
	}{
		{"not checked yet", health.Report{}, "the archive drive's health has not been checked since the agent started"},
		{
			"warning",
			health.Report{State: health.StateWarning, Reasons: []string{"nvme0: 2 media errors"}},
			"the archive drive's health is warning: nvme0: 2 media errors",
		},
		{
			"unreadable",
			health.Report{State: health.StateUnavailable, Reasons: []string{"no drive found under the archive"}},
			"the archive drive's health is unavailable: no drive found under the archive",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := BuildSafeToDelete(context.Background(), db, 365*24*time.Hour, tc.storage, now)
			if err != nil {
				t.Fatal(err)
			}
			if r.Withheld != tc.withheld || len(r.Items) != 0 || r.Counts[policy.Eligible] != 1 {
				t.Fatalf("report = withheld %q, %d items, counts %v", r.Withheld, len(r.Items), r.Counts)
			}
		})
	}
}

// readSafeToDelete returns the asset ids safe-to-delete.csv lists.
func readSafeToDelete(t *testing.T, a *Agent) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(a.cfg.Storage.Root, export.SafeToDeleteFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, row := range rows[1:] {
		ids = append(ids, row[len(row)-1])
	}
	return ids
}

func TestCycleExportsSafeToDelete(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, _, _ := verifiedAgent(t, &clock)
	ctx := context.Background()
	if got := readSafeToDelete(t, agent); len(got) != 0 {
		t.Fatalf("listed before any scrub: %v", got)
	}

	clock = clock.Add(time.Hour)
	if err := agent.maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := agent.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if got := readSafeToDelete(t, agent); !slices.Equal(got, []string{"A1"}) {
		t.Fatalf("listed after the scrub: %v, want A1", got)
	}

	agent.readDrive = func(context.Context, platform.Drive) (platform.DriveHealth, error) {
		return platform.DriveHealth{Protocol: "NVMe", Warnings: []string{"spare"}}, nil
	}
	if err := agent.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if got := readSafeToDelete(t, agent); len(got) != 0 {
		t.Fatalf("listed while a drive is failing: %v", got)
	}
}
