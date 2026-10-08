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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform"
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
)

// sizeFilters shape each size. Thumbnails fill and crop a square (small
// sources are enlarged to fill the grid cell); previews fit the box and are
// never enlarged, so a small image keeps its own size.
var sizeFilters = map[Size]string{
	Thumb:   fmt.Sprintf("scale=%[1]d:%[1]d:force_original_aspect_ratio=increase,crop=%[1]d:%[1]d", thumbEdge),
	Preview: fmt.Sprintf("scale=w='min(%[1]d,iw)':h='min(%[1]d,ih)':force_original_aspect_ratio=decrease", previewEdge),
}

// specIDBytes is how much of the spec hash names a cache directory.
const specIDBytes = 4

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
	log *slog.Logger

	mu       sync.Mutex
	inflight map[string]*call
	// failed holds the renderings that failed since their last success, so a
	// file ffmpeg cannot read is logged once, not at every attempt.
	failed map[string]bool
}

type call struct {
	done chan struct{}
	err  error
}

// New returns a Renderer caching into dir, which it owns: Prune deletes
// anything in dir that is not a current rendering. A rendering that fails is
// logged to log once until it succeeds.
func New(cfg config.FFmpeg, dir string, log *slog.Logger) *Renderer {
	return &Renderer{
		cfg: cfg, dir: dir, sem: make(chan struct{}, cfg.Renderers), log: log,
		inflight: map[string]*call{}, failed: map[string]bool{},
	}
}

// specDir names the cache directory of size after the ffmpeg arguments that
// shape its output (filter, codec, quality). Changing how a size is rendered
// therefore starts a new directory instead of serving stale renderings.
func specDir(size Size) string { return specName(size, sizeFilters[size]) }

// specName fingerprints the ffmpeg arguments that render size with filter.
func specName(size Size, filter string) string {
	sum := sha256.Sum256([]byte(strings.Join(renderArgs(nil, "", filter, false), "\x00")))
	return string(size) + "-" + hex.EncodeToString(sum[:specIDBytes])
}

// CachePath is where the rendering at size of the file with SHA-256 sum is kept.
func (r *Renderer) CachePath(sum string, size Size) string {
	return filepath.Join(r.dir, specDir(size), sum[:2], sum+".webp")
}

// Prune deletes renderings made by a superseded spec, and anything else in
// the cache directory that is not a current size directory. It returns the
// number of top-level entries removed.
func (r *Renderer) Prune() (int, error) {
	entries, err := os.ReadDir(r.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("list thumbnail cache: %w", err)
	}
	current := make(map[string]bool, len(sizeFilters))
	for size := range sizeFilters {
		current[specDir(size)] = true
	}
	removed := 0
	for _, e := range entries {
		if current[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(r.dir, e.Name())); err != nil {
			return removed, fmt.Errorf("prune thumbnail cache: %w", err)
		}
		removed++
	}
	return removed, nil
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

	// The render serves every waiter and fills the cache, so it does not end
	// when the request that started it is cancelled; ffmpeg.render_timeout bounds it.
	renderCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.cfg.RenderTimeout.Std())
	c.err = r.render(renderCtx, src, size, path)
	cancel()
	r.mu.Lock()
	delete(r.inflight, path)
	report := c.err != nil && !r.failed[path]
	if c.err != nil {
		r.failed[path] = true
	} else {
		delete(r.failed, path)
	}
	r.mu.Unlock()
	close(c.done)
	if report {
		// The error names the source file. A failed rendering is tried again
		// at the next request or preview pass, without logging it again.
		r.log.WarnContext(ctx, "rendering failed; tried again later, logged again only after a success",
			"size", size, "sha256", src.SHA256, "err", c.err)
	}
	return path, c.err
}

func (r *Renderer) render(ctx context.Context, src Source, size Size, dest string) (err error) {
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("render %s: %w", src.Path, ctx.Err())
	}
	defer func() { <-r.sem }()

	preview, format, isRaw, err := rawPreview(src.Path)
	if err != nil {
		return err
	}
	input, tiled := tool.FFmpegFile(src.Path), false
	if isRaw {
		input = tool.FFmpegStdin(format)
	} else if tiled, err = r.isTiled(ctx, src.Path); err != nil {
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
	stderr := &tool.Tail{}
	cmd := platform.Command(ctx, r.cfg.FFmpeg, renderArgs(input, tmp, sizeFilters[size], tiled)...)
	if isRaw {
		cmd.Stdin = bytes.NewReader(preview)
	}
	cmd.Stderr = stderr
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg %s: %w: %s", src.Path, err, stderr)
	}
	if err = os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("store rendering of %s: %w", src.Path, err)
	}
	return nil
}

// renderArgs builds the ffmpeg command line for input: the source file
// (tool.FFmpegFile) or a raw's embedded JPEG or JPEG XL preview on standard input
// (tool.FFmpegStdin; -nostdin only disables interactive keyboard control). Tiled HEIF images (iPhone HEIC) expose their picture as stream
// group 0, which the filter graph must stitch; everything else is a plain
// video stream, of which the first frame is used. ffmpeg applies EXIF and
// container rotation itself (autorotate).
func renderArgs(input []string, dest, filter string, tiled bool) []string {
	args := append([]string{"-nostdin", "-v", "error", "-y"}, input...)
	if tiled {
		args = append(args, "-filter_complex", "[0:g:0]"+filter+"[o]", "-map", "[o]")
	} else {
		args = append(args, "-vf", filter)
	}
	return append(args, "-frames:v", "1", "-c:v", "libwebp", "-quality", webpQuality, "-f", "webp", dest)
}

// isTiled reports whether src is a tiled (grid) HEIF image.
func (r *Renderer) isTiled(ctx context.Context, src string) (bool, error) {
	args := append([]string{"-v", "error", "-show_entries", "stream_group=index", "-of", "csv=p=0"}, tool.FFmpegFile(src)...)
	// Any stream group at all means tiled: the start of the list is enough.
	out, stderr := &tool.Head{Max: 1 << 10}, &tool.Tail{}
	cmd := platform.Command(ctx, r.cfg.FFprobe, args...)
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("ffprobe %s: %w: %s", src, err, stderr)
	}
	return len(bytes.TrimSpace(out.Bytes())) > 0, nil
}
