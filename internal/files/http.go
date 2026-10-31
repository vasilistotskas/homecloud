package files

import (
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/tus/tusd/v2/pkg/filestore"
	tusd "github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/memorylocker"
	expslog "golang.org/x/exp/slog"

	"github.com/vasilistotskas/homecloud/internal/auth"
	"github.com/vasilistotskas/homecloud/internal/media"
)

const (
	// DownloadPrefix is where signed download links point.
	DownloadPrefix = "/files/download/"
	// UploadPrefix is where each location's tus endpoint lives:
	// UploadPrefix + url.PathEscape(location) + "/".
	UploadPrefix = "/files/uploads/"
	// SavedAsHeader carries the path a finished upload was saved at,
	// URL-encoded, on the response that finishes it.
	SavedAsHeader = "Homecloud-Saved-As"

	// Upload metadata keys the app sends.
	metaName = "filename"
	metaDir  = "dir"

	// networkTimeout is how long an upload's connection may send nothing
	// before the agent gives up on that request (the client resumes).
	networkTimeout = time.Minute
	// lockTimeout bounds the wait for an upload another request is writing.
	lockTimeout = 20 * time.Second
)

// What a cross-origin tus 1.0 client (tus-js-client) uses beyond Connect's
// own CORS lists: the methods and request headers of creating, resuming and
// cancelling an upload, and the response headers it reads.
var (
	TusMethods         = []string{http.MethodHead, http.MethodPatch, http.MethodDelete}
	TusRequestHeaders  = []string{"Tus-Resumable", "Upload-Length", "Upload-Offset", "Upload-Metadata"}
	TusResponseHeaders = []string{"Location", "Tus-Resumable", "Upload-Offset", "Upload-Length", SavedAsHeader}
)

// UploadPath is the tus endpoint for uploads into location loc.
func UploadPath(loc string) string { return UploadPrefix + url.PathEscape(loc) + "/" }

// DownloadPath is the escaped, unsigned path that serves p in location loc.
func DownloadPath(loc, p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return DownloadPrefix + url.PathEscape(loc) + "/" + strings.Join(parts, "/")
}

// Downloads serves GET DownloadPrefix{location}/{path...} to signed links
// (media.Signer) whose access has not ended: attachment by default, inline
// with ?inline=1. Ranges are served, so videos stream.
func Downloads(s *Service, signer *media.Signer, a *auth.Authenticator, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+DownloadPrefix+"{location}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		if !signer.Admit(w, r, a, log) {
			return
		}
		f, info, err := s.Open(r.PathValue("location"), r.PathValue("path"))
		switch {
		case errors.Is(err, ErrNoLocation), errors.Is(err, ErrNotFound), errors.Is(err, ErrInvalid):
			http.NotFound(w, r)
			return
		case errors.Is(err, ErrUnavailable):
			http.Error(w, "this location is not available", http.StatusServiceUnavailable)
			return
		case err != nil:
			log.ErrorContext(r.Context(), "file download failed", "err", err)
			http.Error(w, "the file could not be opened", http.StatusInternalServerError)
			return
		}
		defer f.Close() //nolint:errcheck // read-only
		disposition := "attachment"
		if r.URL.Query().Get("inline") == "1" {
			disposition = "inline"
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": info.Name()}))
		// The link may be cached as long as it is valid; the file may change.
		w.Header().Set("Cache-Control", "private, no-cache")
		// A served document must not run scripts at the agent's origin.
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'")
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	})
	return mux
}

// Uploads serves each location's tus endpoint (UploadPath). The caller
// authenticates requests first, and states the scheme a proxy served them
// over in X-Forwarded-Proto (dropping every other forwarding header): tusd
// builds each new upload's absolute URL from it.
func Uploads(s *Service, log *slog.Logger) (http.Handler, error) {
	mux := http.NewServeMux()
	// tusd logs every request at info; only its problems are worth keeping.
	tusLog := expslog.New(tusdLog{h: log.Handler(), level: slog.LevelWarn})
	for _, loc := range s.locations {
		h, err := s.uploadHandler(loc.Name, filepath.Join(loc.Path, filepath.FromSlash(uploadsDir)), tusLog)
		if err != nil {
			return nil, err
		}
		base := UploadPath(loc.Name)
		mux.Handle(base, http.StripPrefix(strings.TrimSuffix(base, "/"), h))
	}
	return mux, nil
}

func (s *Service) uploadHandler(loc, dir string, log *expslog.Logger) (http.Handler, error) {
	store := filestore.New(dir)
	composer := tusd.NewStoreComposer()
	store.UseIn(composer)
	// One agent owns every upload, so locks need not outlive the process.
	memorylocker.New().UseIn(composer)
	h, err := tusd.NewHandler(tusd.Config{
		StoreComposer:           composer,
		BasePath:                UploadPath(loc),
		DisableDownload:         true,
		DisableConcatenation:    true,
		Cors:                    &tusd.CorsConfig{Disable: true},
		Logger:                  log,
		RespectForwardedHeaders: true,
		NetworkTimeout:          networkTimeout,
		AcquireLockTimeout:      lockTimeout,
		PreUploadCreateCallback: func(ev tusd.HookEvent) (tusd.HTTPResponse, tusd.FileInfoChanges, error) {
			return tusd.HTTPResponse{}, tusd.FileInfoChanges{}, s.beforeUpload(loc, ev.Upload)
		},
		PreFinishResponseCallback: func(ev tusd.HookEvent) (tusd.HTTPResponse, error) {
			saved, err := s.FinishUpload(loc, ev.Upload.ID, ev.Upload.MetaData[metaDir], ev.Upload.MetaData[metaName])
			if err != nil {
				return tusd.HTTPResponse{}, uploadError(err)
			}
			s.log.InfoContext(ev.Context, "file uploaded", "location", loc, "size", ev.Upload.Size,
				"principal", principalOf(ev))
			return tusd.HTTPResponse{Header: tusd.HTTPHeader{SavedAsHeader: url.PathEscape(saved)}}, nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("upload handler for %q: %w", loc, err)
	}
	return h, nil
}

// beforeUpload admits a new upload: its length is known and fits the
// volume, its folder exists and its name is valid.
func (s *Service) beforeUpload(loc string, info tusd.FileInfo) error {
	if info.SizeIsDeferred {
		return tusd.NewError("ERR_LENGTH_REQUIRED", "the upload's length must be known when it starts", http.StatusBadRequest)
	}
	if err := s.CheckUpload(loc, info.MetaData[metaDir], info.MetaData[metaName]); err != nil {
		return uploadError(err)
	}
	dir, err := s.UploadDir(loc)
	if err != nil {
		return uploadError(err)
	}
	free, err := s.diskFree(dir)
	if err != nil {
		return uploadError(err)
	}
	if info.Size < 0 || uint64(info.Size) > free {
		return tusd.NewError("ERR_NO_SPACE", "not enough free space for this file", http.StatusInsufficientStorage)
	}
	return nil
}

// uploadError gives an error from this package the HTTP status a tus client
// shows; tus clients retry only 5xx and 423.
func uploadError(err error) error {
	switch {
	case errors.Is(err, ErrInvalid):
		return tusd.NewError("ERR_INVALID", err.Error(), http.StatusBadRequest)
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrNoLocation):
		return tusd.NewError("ERR_NOT_FOUND", err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrExists):
		return tusd.NewError("ERR_EXISTS", err.Error(), http.StatusConflict)
	case errors.Is(err, ErrUnavailable):
		return tusd.NewError("ERR_UNAVAILABLE", "this location is not available", http.StatusServiceUnavailable)
	default:
		return tusd.NewError("ERR_INTERNAL", "the upload could not be saved", http.StatusInternalServerError)
	}
}

func principalOf(ev tusd.HookEvent) auth.Principal {
	p, _ := auth.PrincipalFrom(ev.Context)
	return p
}
