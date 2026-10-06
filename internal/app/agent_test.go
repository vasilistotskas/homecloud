package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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

// The test binary doubles as a fake kei when HOMECLOUD_FAKE_KEI=1:
//
//	sync     → exits with HOMECLOUD_FAKE_KEI_SYNC_EXIT
//	manifest → prints the file named by HOMECLOUD_FAKE_KEI_MANIFEST
func TestMain(m *testing.M) {
	if os.Getenv("HOMECLOUD_FAKE_KEI") == "1" {
		os.Exit(fakeKei(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeKei(args []string) int {
	if len(args) == 0 {
		return 1
	}
	switch args[0] {
	case "sync":
		var code int
		if _, err := fmt.Sscan(os.Getenv("HOMECLOUD_FAKE_KEI_SYNC_EXIT"), &code); err != nil {
			return 1
		}
		return code
	case "manifest":
		data, err := os.ReadFile(os.Getenv("HOMECLOUD_FAKE_KEI_MANIFEST")) //nolint:gosec // test fixture path set by the test itself
		if err != nil {
			return 1
		}
		_, _ = os.Stdout.Write(data)
		return 0
	default:
		return 1
	}
}

type recordingNotifier struct {
	mu   sync.Mutex
	msgs []notify.Msg
}

func (r *recordingNotifier) Send(_ context.Context, m notify.Msg) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
	return nil
}

func (r *recordingNotifier) titles() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, m := range r.msgs {
		out = append(out, m.Title)
	}
	return out
}

// fixture writes a photo file and a manifest describing it, returning the
// manifest path; corrupt makes the manifest's checksum disagree with the file.
func fixture(t *testing.T, corrupt bool) string {
	t.Helper()
	dir := t.TempDir()
	photo := filepath.Join(dir, "IMG_0001_QUFBQTA.HEIC")
	data := []byte("not really a heic, but content is what tier 1 checks")
	if err := os.WriteFile(photo, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:])
	if corrupt {
		checksum = strings.Repeat("0", 64)
	}
	doc := fmt.Sprintf(`[{"library":"PrimarySync","asset_id":"A1","version":"original","filename":"IMG_0001_QUFBQTA.HEIC",
		"local_path":%q,"checksum":"AQ","local_checksum":%q,"download_checksum":%q,"size_bytes":%d,
		"created_at":"2022-06-03T10:00:00Z","added_at":"2022-06-03T10:00:01Z","downloaded_at":"2026-10-05T10:00:00Z",
		"last_seen_at":"2026-10-05T10:00:00Z","media_type":"photo","status":"downloaded","albums":[]}]`,
		photo, checksum, checksum, len(data))
	manifest := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifest, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func newTestAgent(t *testing.T, manifest, syncExit string, clock *time.Time) (*Agent, *recordingNotifier) {
	t.Helper()
	includeHidden := true
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOMECLOUD_FAKE_KEI", "1")
	t.Setenv("HOMECLOUD_FAKE_KEI_SYNC_EXIT", syncExit)
	t.Setenv("HOMECLOUD_FAKE_KEI_MANIFEST", manifest)

	db, err := ledger.Open(context.Background(), filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	n := &recordingNotifier{}
	cfg := &config.Config{
		Storage: config.Storage{Root: filepath.Dir(manifest)},
		ICloud:  config.ICloud{AppleID: "someone@example.com", IncludeHidden: &includeHidden},
		Kei:     config.Kei{Binary: exe, Config: "unused.toml"},
		Schedule: config.Schedule{
			SyncInterval:  config.Duration(time.Hour),
			ScrubInterval: config.Duration(168 * time.Hour),
			ScrubFraction: 1,
		},
		Backup: config.Backup{Dir: filepath.Join(t.TempDir(), "ledger-backups"), Interval: config.Duration(24 * time.Hour), Keep: 2},
		IPhoneBackup: config.IPhoneBackup{
			Dirs: []string{filepath.Join(t.TempDir(), "MobileSync", "Backup")}, MaxAge: config.Duration(7 * 24 * time.Hour),
			CheckInterval: config.Duration(24 * time.Hour), MaxShrinkPercent: 50,
		},
	}
	// No ffmpeg in tests: previews fail and are logged, as they would without one.
	renderer := thumbs.New(config.FFmpeg{FFmpeg: filepath.Join(t.TempDir(), "no-ffmpeg"), FFprobe: filepath.Join(t.TempDir(), "no-ffprobe")}, ThumbnailDir(cfg))
	agent := NewAgent(cfg, db, slog.New(slog.NewTextHandler(io.Discard, nil)), n, io.Discard, renderer,
		func() time.Time { return *clock })
	// Tests have no configured volume; the archive counts as present.
	agent.checkArchive = func() error { return nil }
	// Tests judge a healthy drive unless they say otherwise.
	agent.readDrive = func(context.Context) (platform.DriveHealth, error) {
		return platform.DriveHealth{Protocol: "NVMe"}, nil
	}
	return agent, n
}

func TestCycleVerifiesEvenWhenAuthRequired(t *testing.T) {
	manifest := fixture(t, false)
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	agent, notes := newTestAgent(t, manifest, "3", &clock)
	ctx := context.Background()

	if err := agent.Cycle(ctx); err != nil {
		t.Fatalf("Cycle: %v", err)
	}
	counts, err := agent.db.StateCounts(ctx)
	if err != nil || counts[ledger.StateVerified] != 1 {
		t.Fatalf("states = %v, %v; want 1 verified despite auth-required sync", counts, err)
	}
	if got := notes.titles(); len(got) != 1 || !strings.Contains(got[0], "sign-in") {
		t.Fatalf("alerts = %v, want one sign-in alert", got)
	}

	clock = clock.Add(time.Hour)
	if err := agent.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if got := notes.titles(); len(got) != 1 {
		t.Fatalf("alerts within 24h = %v, want throttled to 1", got)
	}

	clock = clock.Add(24 * time.Hour)
	if err := agent.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if got := notes.titles(); len(got) != 2 {
		t.Fatalf("alerts after 24h = %v, want 2", got)
	}
}

func TestCycleQuarantinesAndAlertsOnMismatch(t *testing.T) {
	manifest := fixture(t, true)
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	agent, notes := newTestAgent(t, manifest, "0", &clock)
	ctx := context.Background()

	if err := agent.Cycle(ctx); err != nil {
		t.Fatalf("Cycle: %v", err)
	}
	counts, err := agent.db.StateCounts(ctx)
	if err != nil || counts[ledger.StateQuarantined] != 1 {
		t.Fatalf("states = %v, %v; want 1 quarantined", counts, err)
	}
	got := notes.titles()
	if len(got) != 1 || !strings.Contains(got[0], "failed verification") {
		t.Fatalf("alerts = %v, want one verification alert", got)
	}
}

func TestCycleSurfacesUnexpectedKeiFailure(t *testing.T) {
	manifest := fixture(t, false)
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	agent, notes := newTestAgent(t, manifest, "1", &clock)

	err := agent.Cycle(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exit code 1") {
		t.Fatalf("want kei exit-code error, got %v", err)
	}
	if got := notes.titles(); len(got) != 1 || !strings.Contains(got[0], "download failed") {
		t.Fatalf("alerts = %v, want download-failed alert", got)
	}
	counts, _ := agent.db.StateCounts(context.Background())
	if counts[ledger.StateVerified] != 1 {
		t.Fatalf("ingest/verify must still run after kei failure; states = %v", counts)
	}
}

func TestRequestCycleCoalesces(t *testing.T) {
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	agent, _ := newTestAgent(t, fixture(t, false), "0", &clock)
	_, changed := agent.Hub().Snapshot()
	if !agent.RequestCycle() {
		t.Fatal("first request on an idle agent must be accepted")
	}
	if agent.RequestCycle() {
		t.Fatal("a second request while one is pending must be refused")
	}
	<-agent.trigger
	agent.setPhase(PhaseVerifying)
	select {
	case <-changed:
	default:
		t.Fatal("a phase change must wake watchers")
	}
	if st, _ := agent.Hub().Snapshot(); st.Phase != PhaseVerifying || !st.PhaseStartedAt.Equal(clock) {
		t.Fatalf("status = %+v", st)
	}
	if agent.RequestCycle() {
		t.Fatal("a request while a cycle runs must be refused")
	}
	agent.setPhase(PhaseCheckingArchive)
	if !agent.RequestCycle() {
		t.Fatal("a request during maintenance must be queued for right after it")
	}
}

func TestCycleRecordsParsableReportAndEndsIdle(t *testing.T) {
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	agent, _ := newTestAgent(t, fixture(t, false), "2", &clock)
	ctx := context.Background()
	if err := agent.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := agent.Hub().Snapshot(); st.Phase != PhaseIdle || !st.PhaseStartedAt.IsZero() {
		t.Fatalf("status after cycle = %+v, want idle", st)
	}
	run, err := agent.db.LastFinishedRun(ctx, CycleRunKind)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := ParseCycleReport(run.Detail)
	if err != nil || rep.KeiOutcome != "partial" || rep.Ingested.Assets != 1 || rep.Verified.Passed != 1 {
		t.Fatalf("report = %+v, %v", rep, err)
	}
}

func TestCycleAlertsOnceWhenDriveFails(t *testing.T) {
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	agent, notes := newTestAgent(t, fixture(t, false), "0", &clock)
	warnings := []string{"spare"}
	agent.readDrive = func(context.Context) (platform.DriveHealth, error) {
		return platform.DriveHealth{Protocol: "NVMe", Warnings: warnings}, nil
	}
	ctx := context.Background()
	for range 2 {
		if err := agent.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := notes.titles(); len(got) != 1 || !strings.Contains(got[0], "archive drive") {
		t.Fatalf("alerts = %v, want one drive alert", got)
	}
	if st, _ := agent.Hub().Snapshot(); st.Drive.State != health.StateFailing || st.Drive.Drive == nil {
		t.Fatalf("status drive = %+v", st.Drive)
	}

	warnings = nil
	if err := agent.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := agent.db.Events(ctx, []string{ledger.EventDriveHealthChanged}, 0, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("drive events = %+v, %v; want failing then ok", events, err)
	}
}

func TestCycleExportsManifest(t *testing.T) {
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	manifest := fixture(t, false)
	agent, _ := newTestAgent(t, manifest, "0", &clock)
	if err := agent.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(manifest), export.ManifestFile)) //nolint:gosec // test temp path
	if err != nil || !strings.Contains(string(data), "IMG_0001_QUFBQTA.HEIC,photo") {
		t.Fatalf("manifest = %q, %v", data, err)
	}
}

func TestCycleKeepsHiddenFolderHidden(t *testing.T) {
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	manifest := fixture(t, false)
	agent, _ := newTestAgent(t, manifest, "0", &clock)
	dir := filepath.Join(filepath.Dir(manifest), kei.PhotosDir, kei.HiddenDir)
	if err := agent.Cycle(context.Background()); err != nil {
		t.Fatalf("cycle without a hidden folder: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := agent.Cycle(context.Background()); err != nil {
		t.Fatalf("cycle with a hidden folder: %v", err)
	}
}
