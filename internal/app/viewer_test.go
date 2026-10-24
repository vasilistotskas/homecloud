package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/viewer"
)

// editedFixture writes an edited photo, its original and a manifest of both,
// returning the manifest path.
func editedFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	month := filepath.Join(kei.DownloadDir(dir), "2022", "06")
	if err := os.MkdirAll(month, 0o700); err != nil {
		t.Fatal(err)
	}
	var entries []string
	for _, f := range []struct{ version, name string }{
		{"original", "IMG_0001_QUFBQTA.HEIC"},
		{"adjusted", "IMG_0001_edited_QUFBQTA.HEIC"},
	} {
		data := []byte("the " + f.version + " of an edited photo")
		path := filepath.Join(month, f.name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		checksum := hex.EncodeToString(sum[:])
		entries = append(entries, fmt.Sprintf(`{"library":"PrimarySync","asset_id":"A1","version":%q,"filename":%q,
			"local_path":%q,"checksum":"AQ","local_checksum":%q,"download_checksum":%q,"size_bytes":%d,
			"created_at":"2022-06-03T10:00:00Z","added_at":"2022-06-03T10:00:01Z","downloaded_at":"2026-10-05T10:00:00Z",
			"last_seen_at":"2026-10-05T10:00:00Z","media_type":"photo","status":"downloaded","albums":[]}`,
			f.version, f.name, path, checksum, checksum, len(data)))
	}
	manifest := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifest, []byte("["+strings.Join(entries, ",")+"]"), 0o600); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// withViewer points the agent at an Immich served by h and saves its key.
func withViewer(t *testing.T, agent *Agent, h http.Handler) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	agent.cfg.Viewer = config.Viewer{URL: srv.URL, PhotosPath: "/mnt/archive/Photos", Timeout: config.Duration(10 * time.Second)}
	path, err := agent.viewerKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := viewer.SaveKey(path, "testkey0123456789"); err != nil {
		t.Fatal(err)
	}
}

func TestCycleStacksEditsInTheViewer(t *testing.T) {
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	agent, _ := newTestAgent(t, editedFixture(t), "0", &clock)
	var (
		mu      sync.Mutex
		created [][]string
		scans   int
	)
	withViewer(t, agent, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("x-api-key") != "testkey0123456789" {
			t.Errorf("%s %s without the API key", r.Method, r.URL.Path)
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /api/search/metadata":
			_, _ = fmt.Fprint(w, `{"assets":{"items":[
				{"id":"o","originalPath":"/mnt/archive/Photos/2022/06/IMG_0001_QUFBQTA.HEIC"},
				{"id":"e","originalPath":"/mnt/archive/Photos/2022/06/IMG_0001_edited_QUFBQTA.HEIC"}],"nextCursor":null}}`)
		case "GET /api/libraries":
			_, _ = fmt.Fprint(w, `[{"id":"lib","name":"Photos","importPaths":["/mnt/archive/Photos"]}]`)
		case "POST /api/libraries/lib/scan":
			scans++
			w.WriteHeader(http.StatusNoContent)
		case "GET /api/stacks":
			if len(created) > 0 {
				_, _ = fmt.Fprint(w, `[{"id":"s1","primaryAssetId":"e","assets":[{"id":"e"},{"id":"o"}]}]`)
				return
			}
			_, _ = fmt.Fprint(w, `[]`)
		case "POST /api/stacks":
			var req struct {
				AssetIDs []string `json:"assetIds"`
			}
			if err := json.UnmarshalRead(r.Body, &req); err != nil {
				t.Error(err)
			}
			created = append(created, req.AssetIDs)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"id":"s1"}`)
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))

	if err := agent.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(created) != 1 || !slices.Equal(created[0], []string{"e", "o"}) || scans != 1 {
		t.Fatalf("stacks created = %v, scans = %d; want the edit over its original, after a scan for the new files", created, scans)
	}
	mu.Unlock()

	// Nothing new on disk: no scan, and the stack is already there.
	if err := agent.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(created) != 1 || scans != 1 {
		t.Fatalf("a cycle with nothing new: stacks created = %v, scans = %d; want neither again", created, scans)
	}
	if s, _ := agent.Hub().Snapshot(); s.Phase != PhaseIdle {
		t.Fatalf("phase after the cycle = %v, want idle", s.Phase)
	}
}

func TestCycleSucceedsWhenTheViewerFails(t *testing.T) {
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	agent, _ := newTestAgent(t, editedFixture(t), "0", &clock)
	withViewer(t, agent, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"down"}`, http.StatusServiceUnavailable)
	}))
	if err := agent.Cycle(context.Background()); err != nil {
		t.Fatalf("a viewer failure failed the cycle: %v", err)
	}
}

func TestCycleSucceedsWithoutAViewerKey(t *testing.T) {
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	agent, _ := newTestAgent(t, editedFixture(t), "0", &clock)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("called the viewer without a key: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	agent.cfg.Viewer = config.Viewer{URL: srv.URL, PhotosPath: "/mnt/archive/Photos", Timeout: config.Duration(10 * time.Second)}
	if err := agent.Cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
}
