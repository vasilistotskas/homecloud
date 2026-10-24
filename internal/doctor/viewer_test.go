package doctor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform/platformtest"
	"github.com/vasilistotskas/homecloud/internal/viewer"
)

func TestCheckViewer(t *testing.T) {
	state := platformtest.StateDir(t)
	permissions := `["asset.read","stack.read","stack.create","library.read","library.update"]`
	importPath := "/mnt/archive/Photos"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "testkey0123456789" {
			http.Error(w, `{"message":"Invalid API key"}`, http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/api-keys/me":
			_, _ = fmt.Fprintf(w, `{"id":"k","name":"homecloud","permissions":%s}`, permissions)
		case "/api/libraries":
			_, _ = fmt.Fprintf(w, `[{"id":"lib","name":"Photos","importPaths":[%q]}]`, importPath)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	cfg := config.Viewer{URL: srv.URL, PhotosPath: "/mnt/archive/Photos", Timeout: config.Duration(10 * time.Second)}
	ctx := t.Context()

	if r := checkViewer(ctx, cfg); r.OK() || !strings.Contains(r.Err.Error(), "homecloud viewer key --set") {
		t.Fatalf("no key: %+v", r)
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := viewer.SaveKey(filepath.Join(state, viewer.KeyFile), "testkey0123456789"); err != nil {
		t.Fatal(err)
	}
	if r := checkViewer(ctx, cfg); !r.OK() || r.Detail != srv.URL {
		t.Fatalf("a key that grants enough: %+v", r)
	}
	importPath = "/mnt/elsewhere"
	if r := checkViewer(ctx, cfg); r.OK() || !strings.Contains(r.Err.Error(), "no Immich library imports /mnt/archive/Photos") {
		t.Fatalf("no library over Photos: %+v", r)
	}
	permissions = `["asset.read"]`
	if r := checkViewer(ctx, cfg); r.OK() || !strings.Contains(r.Err.Error(), "stack.read, stack.create, library.read, library.update") {
		t.Fatalf("a key that lacks permissions: %+v", r)
	}
	if err := viewer.SaveKey(filepath.Join(state, viewer.KeyFile), "anotherkey"); err != nil {
		t.Fatal(err)
	}
	if r := checkViewer(ctx, cfg); r.OK() || !strings.Contains(r.Err.Error(), "HTTP 401") {
		t.Fatalf("a key Immich refuses: %+v", r)
	}
}
