package app

import (
	"context"
	"crypto/sha1" //nolint:gosec // Apple's signature format
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/drive"
	"github.com/vasilistotskas/homecloud/internal/icloud"
)

const driveFileBody = "tax return"

// fakeICloudDrive serves one file at the root of iCloud Drive, or refuses the
// session when expired is set.
func fakeICloudDrive(t *testing.T, expired *atomic.Bool) *httptest.Server {
	t.Helper()
	h := sha1.New() //nolint:gosec // Apple's signature format
	h.Write([]byte("com.apple.XattrObjectSalt\x00com.apple.DataObjectSalt\x00" + driveFileBody))
	sig := base64.StdEncoding.EncodeToString(append([]byte{1}, h.Sum(nil)...))
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /drive/retrieveAppLibraries", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	mux.HandleFunc("POST /drive/retrieveItemDetailsInFolders", func(w http.ResponseWriter, _ *http.Request) {
		if expired.Load() {
			w.WriteHeader(http.StatusMisdirectedRequest)
			return
		}
		_, _ = w.Write([]byte(`[{"drivewsid":"` + drive.RootID + `","type":"FOLDER","items":[
			{"drivewsid":"FILE::com.apple.CloudDocs::DOC1","type":"FILE","name":"tax","extension":"pdf","size":` +
			strconv.Itoa(len(driveFileBody)) + `,"etag":"e1","dateModified":"2026-09-01T10:00:00Z"}]}]`))
	})
	mux.HandleFunc("GET /docs/ws/{zone}/download/by_id", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data_token":{"url":"` + srv.URL + `/content","signature":"` + sig + `"}}`))
	})
	mux.HandleFunc("GET /content", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(driveFileBody))
	})
	// An expired session fails its refresh too.
	mux.HandleFunc("POST /setup/validate", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	mux.HandleFunc("POST /setup/accountLogin", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// withICloudDrive points the agent at the fake service with a saved session.
func withICloudDrive(t *testing.T, agent *Agent, srv *httptest.Server) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "icloud-session.json")
	if err := icloud.SaveSession(path, icloud.Session{SessionToken: "token", Services: map[string]icloud.Service{
		icloud.ServiceDrive: {URL: srv.URL + "/drive"},
		icloud.ServiceDocs:  {URL: srv.URL + "/docs"},
	}}); err != nil {
		t.Fatal(err)
	}
	agent.icloudSession = func() (string, error) { return path, nil }
	agent.icloudEndpoints = icloud.Endpoints{Auth: srv.URL + "/auth", Setup: srv.URL + "/setup", Home: srv.URL}
}

func TestDriveSyncArchivesAndExports(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, _ := newTestAgent(t, fixture(t, false), "0", &clock)
	var expired atomic.Bool
	withICloudDrive(t, agent, fakeICloudDrive(t, &expired))

	rep, ok := agent.syncDrive(context.Background())
	r := rep.(DriveSyncReport)
	if !ok || r.Outcome != DriveOutcomeOK || r.Archived != 1 || r.ManifestRows != 1 {
		t.Fatalf("sync = %+v, ok=%v", r, ok)
	}
	root := filepath.Join(agent.cfg.Storage.Root, drive.Dir)
	if data, err := os.ReadFile(filepath.Join(root, drive.TreeDrive, "tax.pdf")); err != nil || string(data) != driveFileBody { //nolint:gosec // test temp path
		t.Fatalf("archived file = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "manifest.csv")); err != nil {
		t.Fatal(err)
	}

	// The scrub catches a copy that rotted on disk, and the next sync repairs it.
	if err := os.WriteFile(filepath.Join(root, drive.TreeDrive, "tax.pdf"), []byte("tax retvrn"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent.cfg.Schedule.ScrubFraction = 1
	scrub, _ := agent.scrub(context.Background())
	if s := scrub.(ScrubReport); s.Drive.Checked != 1 || s.Drive.Damaged != 1 {
		t.Fatalf("scrub = %+v", s.Drive)
	}
	rep, _ = agent.syncDrive(context.Background())
	if r := rep.(DriveSyncReport); r.Repaired != 1 {
		t.Fatalf("repair sync = %+v", r)
	}
}

func TestDriveSyncNeedsSignIn(t *testing.T) {
	clock := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	agent, n := newTestAgent(t, fixture(t, false), "0", &clock)
	var expired atomic.Bool
	expired.Store(true)
	withICloudDrive(t, agent, fakeICloudDrive(t, &expired))
	for range 2 {
		rep, ok := agent.syncDrive(context.Background())
		if r := rep.(DriveSyncReport); ok || r.Outcome != DriveOutcomeSignInRequired {
			t.Fatalf("sync = %+v, ok=%v", r, ok)
		}
	}
	if got := countTitle(n, "homecloud: iCloud Drive sign-in needed"); got != 1 {
		t.Fatalf("sign-in alerts = %d; want 1 (once a day)", got)
	}
}
