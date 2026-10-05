// Package media serves thumbnails and previews of archived files over
// signed, expiring URLs. Image elements cannot send the API's bearer token or
// rely on its session checks, so each URL carries an HMAC of its path and
// expiry instead; only the agent can mint them, and only for ledger entries.
package media

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/thumbs"
)

// Prefix is the URL path under which media is served.
const Prefix = "/media/"

const (
	// window is the granularity of expiry times: URLs minted within one window
	// are identical, so browsers can cache the images.
	window = 6 * time.Hour
	// validity is how long a URL works at least.
	validity = 12 * time.Hour
	keyBytes = 32
	// cacheFor matches the shortest remaining validity of a URL.
	cacheFor = "private, max-age=43200"
)

// Signer mints and checks media URLs with a key that changes every time the
// agent starts.
type Signer struct {
	key []byte
	now func() time.Time
}

// NewSigner returns a Signer with a fresh random key.
func NewSigner(now func() time.Time) (*Signer, error) {
	key := make([]byte, keyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("media signing key: %w", err)
	}
	return &Signer{key: key, now: now}, nil
}

func (s *Signer) mac(path string, exp int64) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(path + "\n" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// URL returns a signed URL for one rendition at size.
func (s *Signer) URL(size thumbs.Size, library, assetID, version string) string {
	path := Prefix + url.PathEscape(string(size)) + "/" + url.PathEscape(library) + "/" +
		url.PathEscape(assetID) + "/" + url.PathEscape(version)
	exp := s.now().Add(validity).Truncate(window).Add(window).Unix()
	return path + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + s.mac(path, exp)
}

// verify checks a request's signature and expiry.
func (s *Signer) verify(r *http.Request) error {
	exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	if err != nil {
		return errors.New("missing or malformed expiry")
	}
	if s.now().Unix() > exp {
		return errors.New("link expired")
	}
	if !hmac.Equal([]byte(r.URL.Query().Get("sig")), []byte(s.mac(r.URL.EscapedPath(), exp))) {
		return errors.New("bad signature")
	}
	return nil
}

// Ledger finds a rendition's file.
type Ledger interface {
	Rendition(ctx context.Context, library, assetID, version string) (ledger.RenditionFile, error)
}

// Handler serves GET /media/{size}/{library}/{asset}/{version}.
func Handler(s *Signer, db Ledger, renderer *thumbs.Renderer, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Prefix+"{size}/{library}/{asset}/{version}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.verify(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		size, err := thumbs.ParseSize(r.PathValue("size"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		f, err := db.Rendition(r.Context(), r.PathValue("library"), r.PathValue("asset"), r.PathValue("version"))
		if errors.Is(err, ledger.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			log.Error("media: look up rendition", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		path, err := renderer.Ensure(r.Context(), thumbs.Source{Path: f.LocalPath, SHA256: f.SHA256}, size)
		if err != nil {
			log.Error("media: render", "err", err)
			http.Error(w, "could not render this file", http.StatusUnprocessableEntity)
			return
		}
		serveFile(w, r, path)
	})
	return mux
}

func serveFile(w http.ResponseWriter, r *http.Request, path string) {
	file, err := os.Open(path) //nolint:gosec // path is inside the renderer's cache
	if err != nil {
		http.Error(w, "rendering vanished", http.StatusInternalServerError)
		return
	}
	defer file.Close() //nolint:errcheck // read-only
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "rendering vanished", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("Cache-Control", cacheFor)
	http.ServeContent(w, r, "", info.ModTime(), file)
}
