package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/export"
	"github.com/vasilistotskas/homecloud/internal/health"
	"github.com/vasilistotskas/homecloud/internal/icloud"
	"github.com/vasilistotskas/homecloud/internal/iphonecopy"
	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/logging"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/thumbs"
	"github.com/vasilistotskas/homecloud/internal/viewer"
)

// The test binary doubles as a fake kei when HOMECLOUD_FAKE_KEI=1:
//
//	sync      → writes HOMECLOUD_FAKE_KEI_REPORT (if set) as its report next to
//	            --config; when HOMECLOUD_FAKE_KEI_DOWNLOAD is set, downloads
//	            every failed row of the manifest again with that content at
//	            HOMECLOUD_FAKE_KEI_NOW; then exits with HOMECLOUD_FAKE_KEI_SYNC_EXIT
//	manifest  → prints the file named by HOMECLOUD_FAKE_KEI_MANIFEST
//	reconcile → marks the manifest's downloaded rows whose file is missing as
//	            failed, unless HOMECLOUD_FAKE_KEI_RECONCILE=keep (kei leaves
//	            the rows it protects unchanged)
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
		if report := os.Getenv("HOMECLOUD_FAKE_KEI_REPORT"); report != "" {
			i := slices.Index(args, "--config")
			if i < 0 || i+1 >= len(args) {
				return 1
			}
			if err := os.WriteFile(kei.ReportPath(config.Kei{Config: args[i+1]}), []byte(report), 0o600); err != nil { //nolint:gosec // the test passes --config, a path in its own temporary folder
				return 1
			}
		}
		if content := os.Getenv("HOMECLOUD_FAKE_KEI_DOWNLOAD"); content != "" && fakeKeiDownload(content) != nil {
			return 1
		}
		var code int
		if _, err := fmt.Sscan(os.Getenv("HOMECLOUD_FAKE_KEI_SYNC_EXIT"), &code); err != nil {
			return 1
		}
		return code
	case "reconcile":
		if os.Getenv("HOMECLOUD_FAKE_KEI_RECONCILE") == "keep" {
			return 0
		}
		if editFakeManifest(func(row map[string]any) error {
			if row["status"] == "downloaded" {
				if _, err := os.Stat(row["local_path"].(string)); err != nil { //nolint:forcetypeassert // fixture rows
					row["status"] = "failed"
				}
			}
			return nil
		}) != nil {
			return 1
		}
		return 0
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

// editFakeManifest rewrites each row of the fake kei's manifest with edit.
func editFakeManifest(edit func(map[string]any) error) error {
	file := os.Getenv("HOMECLOUD_FAKE_KEI_MANIFEST")
	data, err := os.ReadFile(file) //nolint:gosec // test fixture path set by the test itself
	if err != nil {
		return err
	}
	var rows []map[string]any
	if err = json.Unmarshal(data, &rows); err != nil {
		return err
	}
	for _, row := range rows {
		if err = edit(row); err != nil {
			return err
		}
	}
	if data, err = json.Marshal(rows); err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o600) //nolint:gosec // test fixture path set by the test itself
}

// fakeKeiDownload downloads every failed row again with content.
func fakeKeiDownload(content string) error {
	sum := sha256.Sum256([]byte(content))
	return editFakeManifest(func(row map[string]any) error {
		if row["status"] != "failed" {
			return nil
		}
		if err := os.WriteFile(row["local_path"].(string), []byte(content), 0o600); err != nil { //nolint:forcetypeassert,gosec // fixture rows
			return err
		}
		row["status"] = "downloaded"
		row["downloaded_at"] = os.Getenv("HOMECLOUD_FAKE_KEI_NOW")
		row["download_checksum"] = hex.EncodeToString(sum[:])
		row["local_checksum"] = row["download_checksum"]
		row["size_bytes"] = len(content)
		return nil
	})
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

// fixturePhoto is where fixture puts its photo for the archive at root: in
// kei's download folder, by capture month.
func fixturePhoto(root string) string {
	return filepath.Join(kei.DownloadDir(root), "2022", "06", "IMG_0001_QUFBQTA.HEIC")
}

// fixture writes a photo file and a manifest describing it, returning the
// manifest path; corrupt makes the manifest's checksum disagree with the file.
func fixture(t *testing.T, corrupt bool) string {
	t.Helper()
	dir := t.TempDir()
	photo := fixturePhoto(dir)
	if err := os.MkdirAll(filepath.Dir(photo), 0o700); err != nil {
		t.Fatal(err)
	}
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
		ICloud: config.ICloud{
			AppleID: "someone@example.com", IncludeHidden: &includeHidden, RequestTimeout: config.Duration(time.Minute),
			QuietAfter: config.Duration(14 * 24 * time.Hour),
		},
		Verify: config.Verify{Workers: 2},
		Kei:    config.Kei{Binary: exe, Config: filepath.Join(t.TempDir(), "kei.toml"), CheckpointAlertCycles: 3},
		Schedule: config.Schedule{
			SyncInterval:  config.Duration(time.Hour),
			ScrubInterval: config.Duration(168 * time.Hour),
			ScrubFraction: 1,
		},
		Backup:       config.Backup{Dir: filepath.Join(t.TempDir(), "ledger-backups"), Interval: config.Duration(24 * time.Hour), Keep: 2},
		Notify:       config.Notify{RepeatEvery: config.Duration(24 * time.Hour), Timeout: config.Duration(time.Minute)},
		SafeToDelete: config.SafeToDelete{MinAge: config.Duration(365 * 24 * time.Hour)},
		Drive:        config.Drive{SyncInterval: config.Duration(6 * time.Hour), Workers: 2, StallTimeout: config.Duration(time.Minute)},
		IPhoneBackup: config.IPhoneBackup{
			Dirs: []string{filepath.Join(t.TempDir(), "MobileSync", "Backup")}, MaxAge: config.Duration(7 * 24 * time.Hour),
			CheckInterval: config.Duration(24 * time.Hour), MaxShrinkPercent: 50,
			ReadInterval: config.Duration(7 * 24 * time.Hour), StalledAfter: config.Duration(6 * time.Hour),
		},
	}
	log := slog.New(slog.DiscardHandler)
	// No ffmpeg in tests: previews fail and are logged, as they would without one.
	renderer := thumbs.New(config.FFmpeg{FFmpeg: filepath.Join(t.TempDir(), "no-ffmpeg"), FFprobe: filepath.Join(t.TempDir(), "no-ffprobe"), Renderers: 2, RenderTimeout: config.Duration(time.Minute), ProbeTimeout: config.Duration(time.Minute)}, ThumbnailDir(cfg), log)
	agent := NewAgent(cfg, db, log, n, io.Discard, renderer,
		func() time.Time { return *clock })
	// Tests have no configured volume; the archive counts as present.
	agent.checkArchive = func() error { return nil }
	// Tests never touch a real iCloud account: no saved session, and Apple's
	// endpoints fail the test if anything calls them. Drive tests install a
	// fake service with withICloudDrive.
	session := filepath.Join(t.TempDir(), "icloud-session.json")
	agent.icloudSession = func() (string, error) { return session, nil }
	apple := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a test called Apple: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(apple.Close)
	agent.icloudEndpoints = icloud.Endpoints{Auth: apple.URL, Setup: apple.URL, Home: apple.URL}
	// Nor this computer's push key: tests that push save their own.
	pushKey := filepath.Join(t.TempDir(), iphonecopy.PushKeyFile)
	agent.pushKey = func() (string, error) { return pushKey, nil }
	// Nor its viewer key: tests with a viewer save their own.
	viewerKey := filepath.Join(t.TempDir(), viewer.KeyFile)
	agent.viewerKey = func() (string, error) { return viewerKey, nil }
	// Tests judge one healthy drive unless they say otherwise.
	agent.readStorage = func() (platform.Storage, error) {
		return platform.Storage{Drives: []platform.Drive{{Name: "nvme0n1", Device: "/dev/nvme0n1"}}}, nil
	}
	agent.readDrive = func(context.Context, platform.Drive) (platform.DriveHealth, error) {
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
	agent.readDrive = func(context.Context, platform.Drive) (platform.DriveHealth, error) {
		return platform.DriveHealth{Protocol: "NVMe", Warnings: warnings}, nil
	}
	ctx := context.Background()
	for range 2 {
		if err := agent.Cycle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := notes.titles(); len(got) != 1 || !strings.Contains(got[0], "archive storage") {
		t.Fatalf("alerts = %v, want one storage alert", got)
	}
	// ntfy topics are public: the alert counts, it never names the drive.
	if body := notes.msgs[0].Body; strings.Contains(body, "nvme0n1") || !strings.Contains(body, "1 drive(s) failing") {
		t.Fatalf("storage alert body = %q", body)
	}
	if st, _ := agent.Hub().Snapshot(); st.Storage.State != health.StateFailing || len(st.Storage.Drives) != 1 || st.Storage.Drives[0].Health == nil {
		t.Fatalf("status storage = %+v", st.Storage)
	}

	warnings = nil
	if err := agent.Cycle(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := agent.db.Events(ctx, ledger.EventQuery{Kinds: []string{ledger.EventDriveHealthChanged}}, 0, 10)
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

// logLines captures what an agent logs, as decoded JSON lines.
type logLines struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *logLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p) //nolint:wrapcheck // strings.Builder never fails
}

// with returns the lines whose message is msg.
func (l *logLines) with(t *testing.T, msg string) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var found []map[string]any
	for line := range strings.Lines(l.buf.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if m["msg"] == msg {
			found = append(found, m)
		}
	}
	return found
}

// captureLogs sends the agent's log to the returned lines, through the
// handler the agent uses.
func captureLogs(agent *Agent) *logLines {
	lines := &logLines{}
	agent.log = slog.New(logging.Handler(slog.NewJSONHandler(lines, nil)))
	return lines
}

// TestCycleLogsWhatKeiDid: one line says what kei's sync did, from its
// report, with the failed assets and no signed URL; every line of the run
// names it.
func TestCycleLogsWhatKeiDid(t *testing.T) {
	clock := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	agent, _ := newTestAgent(t, fixture(t, false), "2", &clock)
	t.Setenv("HOMECLOUD_FAKE_KEI_REPORT", `{"version":"3","kei_version":"0.24.1","timestamp":"2026-10-07T12:00:30+00:00",
		"status":"partial_failure","options":{"username":"someone@example.com"},
		"stats":{"assets_seen":12,"downloaded":3,"failed":1,"bytes_downloaded":4096,"elapsed_secs":2.5},
		"failed_assets":[{"id":"A1","version_size":"original","error_message":"HTTP 403 for https://cvws.icloud-content.com/B/x?sig=SECRET&e=1"}]}`)
	lines := captureLogs(agent)
	if err := agent.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := lines.with(t, "kei sync finished")
	if len(got) != 1 {
		t.Fatalf("kei lines = %v", got)
	}
	k := got[0]
	if k["level"] != "WARN" || k["outcome"] != "partial" || k["downloaded"] != float64(3) || k["failed"] != float64(1) ||
		k["bytes"] != float64(4096) || k["duration_ms"] != float64(2500) || k[logging.KeyRunKind] != CycleRunKind {
		t.Fatalf("kei line = %v", k)
	}
	failed, _ := json.Marshal(k["failed_assets"])
	if !strings.Contains(string(failed), "https://cvws.icloud-content.com/…") || strings.Contains(string(failed), "SECRET") ||
		strings.Contains(fmt.Sprint(k), "someone@example.com") {
		t.Fatalf("failed assets = %s; want the host only, and never the account", failed)
	}

	started, finished := lines.with(t, "run started"), lines.with(t, "run finished")
	if len(started) != 1 || len(finished) != 1 || started[0][logging.KeyRun] != k[logging.KeyRun] ||
		finished[0]["ok"] != true || finished[0]["duration_ms"] == nil {
		t.Fatalf("run lines = %v %v", started, finished)
	}
}

// TestCycleAlertsWhenKeiKeepsItsCheckpoint: each cycle records whether kei
// kept its iCloud checkpoint and for how many syncs in a row; a cycle in which
// kei wrote no report neither counts nor resets that. kei.checkpoint_alert_cycles
// in a row alert once (then every notify.repeat_every), and a sync that saves
// the checkpoint resets the count.
func TestCycleAlertsWhenICloudPhotosGoesQuiet(t *testing.T) {
	// The fixture's only photo was added to iCloud on 2022-06-03.
	added := time.Date(2022, 6, 3, 10, 0, 1, 0, time.UTC)
	clock := added.Add(10 * 24 * time.Hour)
	agent, notes := newTestAgent(t, fixture(t, false), "0", &clock)
	ctx := context.Background()
	quietAlerts := func() int {
		n := 0
		for _, m := range notes.msgs {
			if strings.Contains(m.Title, "no new photos") {
				n++
			}
		}
		return n
	}
	// The report's timestamp is after every cycle below starts, so each one reads it as its own.
	report := func(listed bool) string {
		return fmt.Sprintf(`{"version":"3","timestamp":"2023-01-01T00:00:00Z","status":"success","stats":{"assets_seen":1,"enumeration_incomplete":%t}}`, !listed)
	}
	cycle := func(syncExit, keiReport string) {
		t.Helper()
		t.Setenv("HOMECLOUD_FAKE_KEI_SYNC_EXIT", syncExit)
		t.Setenv("HOMECLOUD_FAKE_KEI_REPORT", keiReport)
		_ = agent.Cycle(ctx) // a kei failure fails the cycle; what was judged is what is checked
	}
	last := func() ledger.PhotosActivity {
		t.Helper()
		a, err := agent.db.LastPhotosActivity(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	cycle("0", report(true))
	if last() != ledger.PhotosActive || quietAlerts() != 0 {
		t.Fatalf("10 days after the last addition: %q, %d alerts; want active and none", last(), quietAlerts())
	}

	clock = added.Add(20 * 24 * time.Hour)
	cycle("0", report(false))
	if last() != ledger.PhotosActive || quietAlerts() != 0 {
		t.Fatalf("a sync that did not list everything judged the library: %q, %d alerts", last(), quietAlerts())
	}
	cycle("1", "")
	if last() != ledger.PhotosActive || quietAlerts() != 0 {
		t.Fatalf("a failed sync judged the library: %q, %d alerts", last(), quietAlerts())
	}

	cycle("0", report(true))
	if last() != ledger.PhotosQuiet || quietAlerts() != 1 {
		t.Fatalf("20 days after the last addition: %q, %d alerts; want quiet and one alert", last(), quietAlerts())
	}
	for _, m := range notes.msgs {
		if strings.Contains(m.Title, "no new photos") && (!strings.Contains(m.Body, "20 days") || strings.Contains(m.Body, "IMG_0001")) {
			t.Fatalf("alert = %q; want the days, and no file name", m.Body)
		}
	}
	clock = clock.Add(time.Hour)
	cycle("0", report(true))
	if quietAlerts() != 1 {
		t.Fatalf("an hour later: %d alerts, want the repeat to wait for notify.repeat_every", quietAlerts())
	}
	clock = clock.Add(25 * time.Hour)
	cycle("0", report(true))
	if quietAlerts() != 2 {
		t.Fatalf("a day later: %d alerts, want it repeated", quietAlerts())
	}
	events, err := agent.db.Events(ctx, ledger.EventQuery{Kinds: []string{ledger.EventPhotosActivityChanged}}, 0, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %v, %v; want active, then quiet", events, err)
	}
}

func TestCycleAlertsWhenKeiKeepsItsCheckpoint(t *testing.T) {
	clock := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	agent, notes := newTestAgent(t, fixture(t, false), "0", &clock)
	ctx := context.Background()
	// Written after every cycle below starts, so each one reads it as its own.
	report := func(blocked bool) string {
		stats := `"sync_token_blocked":false`
		if blocked {
			stats = `"sync_token_blocked":true,"sync_token_blocked_reason":"inventory_delta_bridge_failed",
				"sync_token_blocked_source":"kei","sync_token_blocked_zone":"PrimarySync",
				"sync_token_blocked_explanation":"the prior provider checkpoint could not be replayed incrementally after inventory reconciliation",
				"full_enumeration_reason":"enum_config_hash_drift"`
		}
		return `{"version":"3","timestamp":"2026-10-09T23:00:00Z","status":"success","stats":{"assets_seen":1,` + stats + `}}`
	}
	checkpointAlerts := func() []notify.Msg {
		var out []notify.Msg
		for _, m := range notes.msgs {
			if strings.Contains(m.Title, "re-listing") {
				out = append(out, m)
			}
		}
		return out
	}
	cycle := func(syncExit, keiReport string) *Checkpoint {
		t.Helper()
		t.Setenv("HOMECLOUD_FAKE_KEI_SYNC_EXIT", syncExit)
		t.Setenv("HOMECLOUD_FAKE_KEI_REPORT", keiReport)
		if keiReport == "" {
			if err := os.Remove(kei.ReportPath(agent.cfg.Kei)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
		}
		_ = agent.Cycle(ctx) // a kei failure fails the cycle; the record is what is checked
		clock = clock.Add(time.Hour)
		run, err := agent.db.LastFinishedRun(ctx, CycleRunKind)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := ParseCycleReport(run.Detail)
		if err != nil {
			t.Fatal(err)
		}
		return rep.Checkpoint
	}
	held := func(syncs int) *Checkpoint {
		return &Checkpoint{
			Blocked: true, BlockedCycles: syncs, Reason: "inventory_delta_bridge_failed", Source: "kei", Zone: "PrimarySync",
			Explanation:           "the prior provider checkpoint could not be replayed incrementally after inventory reconciliation",
			FullEnumerationReason: "enum_config_hash_drift",
		}
	}

	for _, s := range []struct {
		name, syncExit, report string
		want                   *Checkpoint
		alerts                 int
	}{
		{"saved", "0", report(false), &Checkpoint{}, 0},
		{"kept once, by design", "0", report(true), held(1), 0},
		{"kept twice", "0", report(true), held(2), 0},
		{"kei failed without a report", "1", "", nil, 0},
		{"kept a third time", "0", report(true), held(3), 1},
		{"kept a fourth time, within notify.repeat_every", "0", report(true), held(4), 1},
		{"saved again", "0", report(false), &Checkpoint{}, 1},
		{"kept once more", "0", report(true), held(1), 1},
	} {
		got := cycle(s.syncExit, s.report)
		if diff := cmp.Diff(s.want, got); diff != "" {
			t.Fatalf("%s: checkpoint (-want +got):\n%s", s.name, diff)
		}
		if n := len(checkpointAlerts()); n != s.alerts {
			t.Fatalf("%s: %d checkpoint alerts, want %d (all: %v)", s.name, n, s.alerts, notes.titles())
		}
	}
	// ntfy topics are public: the alert gives kei's reason, nothing of the library.
	if body := checkpointAlerts()[0].Body; !strings.Contains(body, "3 syncs in a row (inventory_delta_bridge_failed)") {
		t.Fatalf("alert body = %q", body)
	}
}

// TestCycleWithoutKeiReport: a sync kei ended before writing its report says
// so instead of repeating the previous sync's counts.
func TestCycleWithoutKeiReport(t *testing.T) {
	clock := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	agent, _ := newTestAgent(t, fixture(t, false), "3", &clock)
	stale := `{"version":"3","timestamp":"2026-10-07T11:00:00+00:00","status":"success","stats":{"downloaded":99}}`
	if err := os.WriteFile(kei.ReportPath(agent.cfg.Kei), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	lines := captureLogs(agent)
	if err := agent.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := lines.with(t, "kei sync finished")
	if len(got) != 1 || got[0]["report"] != "none" || got[0]["downloaded"] != nil || got[0]["outcome"] != "auth_required" {
		t.Fatalf("kei line = %v", got)
	}
}
