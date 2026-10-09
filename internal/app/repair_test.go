package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// quarantinedAgent runs a cycle over a photo whose bytes do not match kei's
// checksum, so the photo is quarantined, and requests its repair at the
// returned time.
func quarantinedAgent(t *testing.T) (*Agent, *recordingNotifier, *time.Time, string) {
	t.Helper()
	manifest := fixture(t, true)
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	agent, notes := newTestAgent(t, manifest, "0", &clock)
	ctx := context.Background()
	if err := agent.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if counts, err := agent.db.StateCounts(ctx); err != nil || counts[ledger.StateQuarantined] != 1 {
		t.Fatalf("states = %v, %v; want the photo quarantined", counts, err)
	}
	clock = clock.Add(time.Hour)
	if err := agent.db.RequestRepair(ctx, "PrimarySync", "A1", clock); err != nil {
		t.Fatal(err)
	}
	return agent, notes, &clock, filepath.Dir(manifest)
}

// damagedCopy is where the repair requested at requested keeps the fixture photo.
func damagedCopy(root string, requested time.Time) string {
	return filepath.Join(root, DamagedDir, requested.UTC().Format(platform.FileTimeLayout), "2022", "06", "IMG_0001_QUFBQTA.HEIC")
}

func TestRepairDownloadsTheFileAgain(t *testing.T) {
	agent, _, clock, root := quarantinedAgent(t)
	ctx := context.Background()
	requested := *clock
	photo := fixturePhoto(root)
	damaged, readErr := os.ReadFile(photo) //nolint:gosec // the test's own fixture
	if readErr != nil {
		t.Fatal(readErr)
	}

	*clock = clock.Add(time.Hour)
	t.Setenv("HOMECLOUD_FAKE_KEI_DOWNLOAD", "the bytes iCloud has")
	t.Setenv("HOMECLOUD_FAKE_KEI_NOW", clock.Format(time.RFC3339))
	if err := agent.Cycle(ctx); err != nil {
		t.Fatal(err)
	}

	if counts, err := agent.db.StateCounts(ctx); err != nil || counts[ledger.StateVerified] != 1 {
		t.Fatalf("states = %v, %v; want the repaired photo verified", counts, err)
	}
	if got, err := os.ReadFile(photo); err != nil || string(got) != "the bytes iCloud has" { //nolint:gosec // the test's own fixture
		t.Fatalf("photo = %q, %v; want the new download", got, err)
	}
	if got, err := os.ReadFile(damagedCopy(root, requested)); err != nil || string(got) != string(damaged) {
		t.Fatalf("damaged copy = %q, %v; want the quarantined bytes kept", got, err)
	}
	events, err := agent.db.Events(ctx, ledger.EventQuery{Kinds: []string{
		ledger.EventRepairRequested, ledger.EventRenditionSetAside, ledger.EventRepaired,
	}}, 0, 10)
	if err != nil || len(events) != 3 || events[0].Kind != ledger.EventRepaired {
		t.Fatalf("events = %+v, %v", events, err)
	}
	if repairs, err := agent.db.Repairs(ctx); err != nil || len(repairs) != 0 {
		t.Fatalf("repairs = %+v, %v", repairs, err)
	}
}

func TestRepairOfAMissingFile(t *testing.T) {
	agent, _, clock, root := quarantinedAgent(t)
	ctx := context.Background()
	requested := *clock
	if err := os.Remove(fixturePhoto(root)); err != nil {
		t.Fatal(err)
	}

	*clock = clock.Add(time.Hour)
	t.Setenv("HOMECLOUD_FAKE_KEI_DOWNLOAD", "the bytes iCloud has")
	t.Setenv("HOMECLOUD_FAKE_KEI_NOW", clock.Format(time.RFC3339))
	if err := agent.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if counts, err := agent.db.StateCounts(ctx); err != nil || counts[ledger.StateVerified] != 1 {
		t.Fatalf("states = %v, %v; want the repaired photo verified", counts, err)
	}
	if _, err := os.Stat(damagedCopy(root, requested)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a damaged copy of a missing file: %v", err)
	}
}

func TestRepairAbandonedWhenKeiKeepsItsRecord(t *testing.T) {
	agent, notes, clock, root := quarantinedAgent(t)
	ctx := context.Background()
	photo := fixturePhoto(root)
	damaged, readErr := os.ReadFile(photo) //nolint:gosec // the test's own fixture
	if readErr != nil {
		t.Fatal(readErr)
	}

	*clock = clock.Add(time.Hour)
	t.Setenv("HOMECLOUD_FAKE_KEI_RECONCILE", "keep")
	if err := agent.Cycle(ctx); err != nil {
		t.Fatal(err)
	}

	if counts, err := agent.db.StateCounts(ctx); err != nil || counts[ledger.StateQuarantined] != 1 {
		t.Fatalf("states = %v, %v; want the photo still quarantined", counts, err)
	}
	if got, err := os.ReadFile(photo); err != nil || string(got) != string(damaged) { //nolint:gosec // the test's own fixture
		t.Fatalf("photo = %q, %v; want it back where it was", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, DamagedDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the emptied %s folder is left: %v", DamagedDir, err)
	}
	if repairs, err := agent.db.Repairs(ctx); err != nil || len(repairs) != 0 {
		t.Fatalf("repairs = %+v, %v; want the repair abandoned", repairs, err)
	}
	if !slices.ContainsFunc(notes.titles(), func(s string) bool { return strings.Contains(s, "repair not possible") }) {
		t.Fatalf("alerts = %v; want the abandoned repair reported", notes.titles())
	}
}

func TestSetAsideFindsAFileMovedBeforeAStop(t *testing.T) {
	agent, _, clock, root := quarantinedAgent(t)
	ctx := context.Background()
	repairs, listErr := agent.db.Repairs(ctx)
	if listErr != nil || len(repairs) != 1 {
		t.Fatalf("repairs = %+v, %v", repairs, listErr)
	}
	// An earlier attempt moved the file and stopped before recording it.
	kept := damagedCopy(root, *clock)
	if err := os.MkdirAll(filepath.Dir(kept), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixturePhoto(root), kept); err != nil {
		t.Fatal(err)
	}
	archive, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close() //nolint:errcheck // read-only use in a test
	keptAs, err := agent.setAside(archive, repairs[0])
	if err != nil || keptAs != filepath.ToSlash(strings.TrimPrefix(kept, root+string(filepath.Separator))) {
		t.Fatalf("setAside = %q, %v; want the earlier move found", keptAs, err)
	}

	// A file outside the Photos folder is never moved.
	outside := repairs[0]
	outside.LocalPath = filepath.Join(root, kei.PhotosDir+"-other", "x.heic")
	if _, err := agent.setAside(archive, outside); err == nil {
		t.Fatal("set aside a file outside the Photos folder")
	}
}
