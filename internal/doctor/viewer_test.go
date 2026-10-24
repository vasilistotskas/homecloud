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
	permissions := `["asset.read","stack.read","stack.create"]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/api-keys/me" || r.Header.Get("x-api-key") != "testkey0123456789" {
			http.Error(w, `{"message":"Invalid API key"}`, http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":"k","name":"homecloud","permissions":%s}`, permissions)
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
	permissions = `["asset.read"]`
	if r := checkViewer(ctx, cfg); r.OK() || !strings.Contains(r.Err.Error(), "stack.read, stack.create") {
		t.Fatalf("a key that lacks permissions: %+v", r)
	}
	if err := viewer.SaveKey(filepath.Join(state, viewer.KeyFile), "anotherkey"); err != nil {
		t.Fatal(err)
	}
	if r := checkViewer(ctx, cfg); r.OK() || !strings.Contains(r.Err.Error(), "HTTP 401") {
		t.Fatalf("a key Immich refuses: %+v", r)
	}
}
