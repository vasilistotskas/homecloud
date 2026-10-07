package media

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/auth"
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

// noDevices is an auth.Devices with no paired device.
type noDevices struct{}

func (noDevices) AddPairedDevice(context.Context, string, string, [32]byte, time.Time) error {
	return nil
}

func (noDevices) PairedDeviceByToken(context.Context, [32]byte) (string, error) {
	return "", ledger.ErrNotFound
}
func (noDevices) PairedDeviceActive(context.Context, string) (bool, error)   { return false, nil }
func (noDevices) TouchPairedDevice(context.Context, string, time.Time) error { return nil }

func setup(t *testing.T, clock *time.Time) (*Signer, *auth.Authenticator, http.Handler) {
	t.Helper()
	now := func() time.Time { return *clock }
	s, err := NewSigner(now)
	if err != nil {
		t.Fatal(err)
	}
	a := auth.New("agent-token", time.Hour, false, noDevices{}, now)
	r := thumbs.New(config.FFmpeg{Renderers: 2, RenderTimeout: config.Duration(time.Minute), ProbeTimeout: config.Duration(time.Minute)}, t.TempDir())
	// A cached rendering lets the test run without ffmpeg.
	cached := r.CachePath(sha, thumbs.Thumb)
	if err := os.MkdirAll(filepath.Dir(cached), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("RIFF....WEBP"), 0o600); err != nil {
		t.Fatal(err)
	}
	return s, a, Handler(s, a, fakeLedger{}, r, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func get(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil))
	return rec
}

// agent is the agent token's principal, as package web passes it on.
func agent(t *testing.T, a *auth.Authenticator) auth.Principal {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer agent-token")
	p, err := a.Authenticate(r)
	if err != nil || p.Kind != auth.KindBearer {
		t.Fatalf("agent principal = %+v, %v", p, err)
	}
	return p
}

func TestSignedURL(t *testing.T) {
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s, a, h := setup(t, &clock)
	p := agent(t, a)
	u := s.URL(p, thumbs.Thumb, "PrimarySync", "A/1", "original")
	if !strings.HasPrefix(u, "/media/thumb/PrimarySync/A%2F1/original?p=") {
		t.Fatalf("url = %s", u)
	}
	if rec := get(h, u); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/webp" ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "max-age") {
		t.Fatalf("signed GET = %d %v", rec.Code, rec.Header())
	}
	if u2 := s.URL(p, thumbs.Thumb, "PrimarySync", "A/1", "original"); u2 != u {
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
	// Another principal's name on this link breaks its signature.
	other := strings.Replace(u, "p="+url.QueryEscape(p.String()), "p="+url.QueryEscape("d:someone"), 1)
	if rec := get(h, other); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "signature") {
		t.Fatalf("another principal = %d %s", rec.Code, rec.Body.String())
	}
	clock = clock.Add(validity + window + time.Minute)
	if rec := get(h, u); rec.Code != http.StatusForbidden {
		t.Fatalf("expired = %d", rec.Code)
	}
}

// TestLinkEndsWithItsSession: a link made for a browser session stops working
// when the session ends, however long it was valid for.
func TestLinkEndsWithItsSession(t *testing.T) {
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s, a, h := setup(t, &clock)
	code, _, err := a.NewLoginCode(auth.PurposeSignIn)
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := a.Exchange(code)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil)
	r.AddCookie(cookie)
	session, err := a.Authenticate(r)
	if err != nil || session.Kind != auth.KindSession {
		t.Fatalf("session = %+v, %v", session, err)
	}
	u := s.URL(session, thumbs.Thumb, "PrimarySync", "A/1", "original")
	if rec := get(h, u); rec.Code != http.StatusOK {
		t.Fatalf("link of an open session = %d", rec.Code)
	}
	a.SignOut(session.ID)
	if rec := get(h, u); rec.Code != http.StatusForbidden {
		t.Fatalf("link of an ended session = %d", rec.Code)
	}
}

func TestUnknownRendition(t *testing.T) {
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s, a, h := setup(t, &clock)
	if rec := get(h, s.URL(agent(t, a), thumbs.Thumb, "PrimarySync", "missing", "original")); rec.Code != http.StatusNotFound {
		t.Fatalf("missing rendition = %d", rec.Code)
	}
}
