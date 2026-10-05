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
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
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
		Kei:      config.Kei{Binary: exe, Config: "unused.toml"},
		Schedule: config.Schedule{SyncInterval: config.Duration(time.Hour)},
	}
	return &Agent{
		Cfg: cfg, DB: db, Notify: n, KeiLog: io.Discard,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return *clock },
	}, n
}

func TestCycleVerifiesEvenWhenAuthRequired(t *testing.T) {
	manifest := fixture(t, false)
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	agent, notes := newTestAgent(t, manifest, "3", &clock)
	ctx := context.Background()

	if err := agent.Cycle(ctx); err != nil {
		t.Fatalf("Cycle: %v", err)
	}
	counts, err := agent.DB.StateCounts(ctx)
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
	counts, err := agent.DB.StateCounts(ctx)
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
	counts, _ := agent.DB.StateCounts(context.Background())
	if counts[ledger.StateVerified] != 1 {
		t.Fatalf("ingest/verify must still run after kei failure; states = %v", counts)
	}
}
