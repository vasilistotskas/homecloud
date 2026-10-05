// Package thumbs renders and caches WebP thumbnails and previews of archived
// photos and videos with the pinned FFmpeg build. The cache is derived data:
// it can be deleted at any time and is never consulted for verification.
//
// Images are named by the SHA-256 of their source file, so a file that kei
// moves keeps its thumbnail, and identical files share one.
package thumbs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/tool"
)

// Size is a rendering of a source file.
type Size string

// Sizes rendered. Thumbnails are square crops for the grid; previews fit a
// box for the detail view.
const (
	Thumb   Size = "thumb"
	Preview Size = "preview"
)

const (
	thumbEdge   = 320
	previewEdge = 1600
	webpQuality = "75"
	// renderers bounds concurrent ffmpeg processes.
	renderers = 4
)

var sizeFilters = map[Size]string{
	Thumb:   fmt.Sprintf("scale=%[1]d:%[1]d:force_original_aspect_ratio=increase,crop=%[1]d:%[1]d", thumbEdge),
	Preview: fmt.Sprintf("scale=%[1]d:%[1]d:force_original_aspect_ratio=decrease", previewEdge),
}

// ParseSize validates a size name.
func ParseSize(s string) (Size, error) {
	if _, ok := sizeFilters[Size(s)]; !ok {
		return "", fmt.Errorf("unknown size %q", s)
	}
	return Size(s), nil
}

// ffmpegVersionPattern matches the first line of `ffmpeg -version`, e.g.
// "ffmpeg version 9.0.2-full_build-www.gyan.dev Copyright ...".
var ffmpegVersionPattern = regexp.MustCompile(`^ffmpeg version (\d+\.\d+\.\d+)\b`)

// FFmpegPin returns the version pin for the configured ffmpeg binary.
func FFmpegPin(cfg config.FFmpeg) tool.Pin {
	return tool.Pin{
		Name:    "ffmpeg",
		Binary:  cfg.FFmpeg,
		Args:    []string{"-version"},
		Pattern: ffmpegVersionPattern,
		Want:    cfg.Version,
	}
}

// Source is a file to render.
type Source struct {
	Path   string
	SHA256 string
}

// Renderer renders and caches images under Dir.
type Renderer struct {
	cfg config.FFmpeg
	dir string
	sem chan struct{}

	mu       sync.Mutex
	inflight map[string]*call
}

type call struct {
	done chan struct{}
	err  error
}

// New returns a Renderer caching into dir.
func New(cfg config.FFmpeg, dir string) *Renderer {
	return &Renderer{cfg: cfg, dir: dir, sem: make(chan struct{}, renderers), inflight: map[string]*call{}}
}

// CachePath is where the rendering of a file with sha256 at size is kept.
func (r *Renderer) CachePath(sha256 string, size Size) string {
	return filepath.Join(r.dir, string(size), sha256[:2], sha256+".webp")
}

// Ensure returns the cached rendering of src at size, rendering it first if
// needed. Concurrent requests for the same image share one render.
func (r *Renderer) Ensure(ctx context.Context, src Source, size Size) (string, error) {
	if len(src.SHA256) != 64 {
		return "", fmt.Errorf("source %s: malformed sha256", src.Path)
	}
	path := r.CachePath(src.SHA256, size)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	r.mu.Lock()
	if c, ok := r.inflight[path]; ok {
		r.mu.Unlock()
		select {
		case <-c.done:
			return path, c.err
		case <-ctx.Done():
			return "", fmt.Errorf("render %s: %w", src.Path, ctx.Err())
		}
	}
	c := &call{done: make(chan struct{})}
	r.inflight[path] = c
	r.mu.Unlock()

	c.err = r.render(ctx, src, size, path)
	r.mu.Lock()
	delete(r.inflight, path)
	r.mu.Unlock()
	close(c.done)
	return path, c.err
}

func (r *Renderer) render(ctx context.Context, src Source, size Size, dest string) (err error) {
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("render %s: %w", src.Path, ctx.Err())
	}
	defer func() { <-r.sem }()

	grid, err := r.isTiled(ctx, src.Path)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	tmp := dest + ".tmp.webp"
	defer func() {
		if err != nil {
			if rmErr := os.Remove(tmp); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				err = errors.Join(err, rmErr)
			}
		}
	}()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.cfg.FFmpeg, renderArgs(src.Path, tmp, sizeFilters[size], grid)...) //nolint:gosec // binary from validated config, paths from the ledger
	cmd.Stderr = &stderr
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg %s: %w: %s", src.Path, err, strings.TrimSpace(stderr.String()))
	}
	if err = os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("store rendering of %s: %w", src.Path, err)
	}
	return nil
}

// renderArgs builds the ffmpeg command line. Tiled HEIF images (iPhone HEIC)
// expose their picture as stream group 0, which the filter graph must stitch;
// everything else is a plain video stream, of which the first frame is used.
// ffmpeg applies EXIF and container rotation itself (autorotate).
func renderArgs(src, dest, filter string, tiled bool) []string {
	args := []string{"-nostdin", "-v", "error", "-y", "-i", src}
	if tiled {
		args = append(args, "-filter_complex", "[0:g:0]"+filter+"[o]", "-map", "[o]")
	} else {
		args = append(args, "-vf", filter)
	}
	return append(args, "-frames:v", "1", "-c:v", "libwebp", "-quality", webpQuality, "-f", "webp", dest)
}

// isTiled reports whether src is a tiled (grid) HEIF image.
func (r *Renderer) isTiled(ctx context.Context, src string) (bool, error) {
	out, err := exec.CommandContext(ctx, r.cfg.FFprobe, //nolint:gosec // binary from validated config, path from the ledger
		"-v", "error", "-show_entries", "stream_group=index", "-of", "csv=p=0", src).Output()
	if err != nil {
		return false, fmt.Errorf("ffprobe %s: %w", src, err)
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}
