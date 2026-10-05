package media

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/thumbs"
)

var sha = strings.Repeat("c", 64)

type fakeLedger struct{}

func (fakeLedger) Rendition(_ context.Context, library, assetID, version string) (ledger.RenditionFile, error) {
	if library != "PrimarySync" || assetID != "A/1" || version != "original" {
		return ledger.RenditionFile{}, ledger.ErrNotFound
	}
	return ledger.RenditionFile{Version: version, LocalPath: "unused", SHA256: sha, MediaType: "photo"}, nil
}

func setup(t *testing.T, clock *time.Time) (*Signer, http.Handler) {
	t.Helper()
	s, err := NewSigner(func() time.Time { return *clock })
	if err != nil {
		t.Fatal(err)
	}
	r := thumbs.New(config.FFmpeg{}, t.TempDir())
	// A cached rendering lets the test run without ffmpeg.
	cached := r.CachePath(sha, thumbs.Thumb)
	if err := os.MkdirAll(filepath.Dir(cached), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("RIFF....WEBP"), 0o600); err != nil {
		t.Fatal(err)
	}
	return s, Handler(s, fakeLedger{}, r, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func get(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil))
	return rec
}

func TestSignedURL(t *testing.T) {
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s, h := setup(t, &clock)
	u := s.URL(thumbs.Thumb, "PrimarySync", "A/1", "original")
	if !strings.HasPrefix(u, "/media/thumb/PrimarySync/A%2F1/original?exp=") {
		t.Fatalf("url = %s", u)
	}
	if rec := get(h, u); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/webp" ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "max-age") {
		t.Fatalf("signed GET = %d %v", rec.Code, rec.Header())
	}
	if u2 := s.URL(thumbs.Thumb, "PrimarySync", "A/1", "original"); u2 != u {
		t.Fatal("URLs minted moments apart must be identical so browsers can cache them")
	}

	tampered := strings.Replace(u, "/original?", "/adjusted?", 1)
	if rec := get(h, tampered); rec.Code != http.StatusForbidden {
		t.Fatalf("tampered path = %d", rec.Code)
	}
	unsigned, _, _ := strings.Cut(u, "&sig=")
	if rec := get(h, unsigned); rec.Code != http.StatusForbidden {
		t.Fatalf("unsigned = %d", rec.Code)
	}
	clock = clock.Add(validity + window + time.Minute)
	if rec := get(h, u); rec.Code != http.StatusForbidden {
		t.Fatalf("expired = %d", rec.Code)
	}
}

func TestUnknownRendition(t *testing.T) {
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s, h := setup(t, &clock)
	if rec := get(h, s.URL(thumbs.Thumb, "PrimarySync", "missing", "original")); rec.Code != http.StatusNotFound {
		t.Fatalf("missing rendition = %d", rec.Code)
	}
}
