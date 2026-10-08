package thumbs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/tool"
)

// TestRendersRealSamples renders every file in a folder of real library
// files (one per format, e.g. HEIC, JPEG, PNG, DNG, WebP, GIF, MOV) as a
// thumbnail and a preview, checking a pinned FFmpeg build such as the
// container image's. Opt-in: HOMECLOUD_SAMPLE_DIR, with ffmpeg and ffprobe
// on PATH.
func TestRendersRealSamples(t *testing.T) {
	dir := os.Getenv("HOMECLOUD_SAMPLE_DIR")
	if dir == "" {
		t.Skip("HOMECLOUD_SAMPLE_DIR not set")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := New(config.FFmpeg{FFmpeg: ffmpeg, FFprobe: ffprobe, Renderers: 2, RenderTimeout: config.Duration(time.Minute), ProbeTimeout: config.Duration(time.Minute)}, t.TempDir(), quiet)
	for i, e := range entries {
		t.Run(e.Name(), func(t *testing.T) {
			src := Source{Path: filepath.Join(dir, e.Name()), SHA256: fmt.Sprintf("%064x", i)}
			for _, size := range []Size{Thumb, Preview} {
				out, err := r.Ensure(t.Context(), src, size)
				if err != nil {
					t.Fatalf("%s: %v", size, err)
				}
				probe := platform.Command(t.Context(), ffprobe, //nolint:gosec // test tool from PATH
					"-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", out)
				if got, err := probe.Output(); err != nil || strings.TrimSpace(string(got)) != "webp" {
					t.Fatalf("%s rendered %q, %v", size, got, err)
				}
			}
		})
	}
}

func TestRenderArgs(t *testing.T) {
	got := renderArgs(tool.FFmpegFile("in.HEIC"), "out.webp", "scale=1", true)
	want := []string{
		"-nostdin", "-v", "error", "-y", "-protocol_whitelist", "file", "-i", "file:in.HEIC",
		"-filter_complex", "[0:g:0]scale=1[o]", "-map", "[o]",
		"-frames:v", "1", "-c:v", "libwebp", "-quality", "75", "-f", "webp", "out.webp",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("tiled args (-want +got):\n%s", diff)
	}
	if got := renderArgs(tool.FFmpegFile("in.MOV"), "out.webp", "scale=1", false); !strings.Contains(strings.Join(got, " "), "-protocol_whitelist file -i file:in.MOV -vf scale=1") {
		t.Fatalf("plain args = %v", got)
	}
	if got := renderArgs(tool.FFmpegStdin("jpeg_pipe"), "out.webp", "scale=1", false); !strings.Contains(strings.Join(got, " "), "-protocol_whitelist pipe -f jpeg_pipe -i pipe:0 -vf scale=1") {
		t.Fatalf("raw preview args = %v", got)
	}
	if got := renderArgs(tool.FFmpegStdin("jpegxl_pipe"), "out.webp", "scale=1", false); !strings.Contains(strings.Join(got, " "), "-protocol_whitelist pipe -f jpegxl_pipe -i pipe:0 -vf scale=1") {
		t.Fatalf("raw jxl preview args = %v", got)
	}
}

func TestParseSize(t *testing.T) {
	if s, err := ParseSize("thumb"); err != nil || s != Thumb {
		t.Fatalf("ParseSize(thumb) = %v, %v", s, err)
	}
	if _, err := ParseSize("huge"); err == nil {
		t.Fatal("unknown size must be rejected")
	}
}

// TestEnsureWithFFmpeg renders a real image when ffmpeg and ffprobe are on PATH;
// it is skipped elsewhere.
func TestEnsureWithFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not on PATH")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "red.png")
	// ffmpeg and ffprobe come from PATH, found by exec.LookPath above.
	makeSource := platform.Command(t.Context(), ffmpeg, //nolint:gosec // test tool from PATH
		"-v", "error", "-f", "lavfi", "-i", "color=red:s=640x480", "-frames:v", "1", src)
	if out, runErr := makeSource.CombinedOutput(); runErr != nil {
		t.Fatalf("make source: %v %s", runErr, out)
	}
	r := New(config.FFmpeg{FFmpeg: ffmpeg, FFprobe: ffprobe, Renderers: 2, RenderTimeout: config.Duration(time.Minute), ProbeTimeout: config.Duration(time.Minute)}, filepath.Join(dir, "cache"), quiet)
	sha := strings.Repeat("ab", 32)
	path, err := r.Ensure(t.Context(), Source{Path: src, SHA256: sha}, Thumb)
	if err != nil {
		t.Fatal(err)
	}
	if path != r.CachePath(sha, Thumb) {
		t.Fatalf("path = %s", path)
	}
	probe := platform.Command(t.Context(), ffprobe, //nolint:gosec // test tool from PATH
		"-v", "error", "-show_entries", "stream=codec_name,width,height", "-of", "csv=p=0", path)
	out, err := probe.Output()
	if err != nil || strings.TrimSpace(string(out)) != "webp,320,320" {
		t.Fatalf("rendered = %q, %v", out, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.Ensure(t.Context(), Source{Path: src, SHA256: sha}, Thumb)
	if err != nil || again != path {
		t.Fatalf("cached Ensure = %s, %v", again, err)
	}
	if info2, _ := os.Stat(path); !info2.ModTime().Equal(info.ModTime()) {
		t.Fatal("a cached rendering must not be rendered again")
	}

	// A render outlives the request that started it: it fills the cache for others.
	gone, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = r.Ensure(gone, Source{Path: src, SHA256: sha}, Preview); err != nil {
		t.Fatalf("render started by a cancelled request: %v", err)
	}
	if _, err = os.Stat(r.CachePath(sha, Preview)); err != nil {
		t.Fatalf("cancelled request left no cached preview: %v", err)
	}

	// A DNG renders from its embedded preview: a 64×48 JPEG fits the 1600 box unscaled.
	dng := writeTIFF(t, dir, testJPEG(t, 64, 48), 64, 48, true)
	dngSHA := strings.Repeat("cd", 32)
	dngPath, err := r.Ensure(t.Context(), Source{Path: dng, SHA256: dngSHA}, Preview)
	if err != nil {
		t.Fatal(err)
	}
	probe = platform.Command(t.Context(), ffprobe, //nolint:gosec // test tool from PATH
		"-v", "error", "-show_entries", "stream=codec_name,width,height", "-of", "csv=p=0", dngPath)
	if out, err := probe.Output(); err != nil || strings.TrimSpace(string(out)) != "webp,64,48" {
		t.Fatalf("rendered DNG = %q, %v", out, err)
	}
}

func TestPruneKeepsOnlyCurrentSpecs(t *testing.T) {
	dir := t.TempDir()
	r := New(config.FFmpeg{Renderers: 2, RenderTimeout: config.Duration(time.Minute), ProbeTimeout: config.Duration(time.Minute)}, dir, quiet)
	sum := strings.Repeat("ef", 32)
	current := r.CachePath(sum, Thumb)
	stale := filepath.Join(dir, "preview-00000000", sum[:2], sum+".webp")
	legacy := filepath.Join(dir, "thumb", sum[:2], sum+".webp")
	for _, p := range []string{current, stale, legacy} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("webp"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasPrefix(filepath.Base(filepath.Dir(filepath.Dir(current))), "thumb-") {
		t.Fatalf("cache path %s is not under a spec directory", current)
	}

	removed, err := r.Prune()
	if err != nil || removed != 2 {
		t.Fatalf("Prune = %d, %v; want the stale spec and the unversioned directory removed", removed, err)
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("current rendering removed: %v", err)
	}
	for _, p := range []string{stale, legacy} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s survived Prune: %v", p, err)
		}
	}

	if removed, err := New(config.FFmpeg{Renderers: 2, RenderTimeout: config.Duration(time.Minute), ProbeTimeout: config.Duration(time.Minute)}, filepath.Join(dir, "missing"), quiet).Prune(); err != nil || removed != 0 {
		t.Fatalf("Prune of a missing cache = %d, %v", removed, err)
	}
}

func TestSpecDirChangesWithRendering(t *testing.T) {
	if specDir(Thumb) == specDir(Preview) {
		t.Fatal("sizes must not share a cache directory")
	}
	if specName(Preview, sizeFilters[Preview]+",hflip") == specDir(Preview) {
		t.Fatal("changing a size's filter must change its cache directory")
	}
}

// quiet discards the renderer's log.
var quiet = slog.New(slog.DiscardHandler)

// counter counts the records logged through it.
type counter struct {
	slog.Handler
	n *int
}

func (counter) Enabled(context.Context, slog.Level) bool { return true }

func (c counter) Handle(context.Context, slog.Record) error { *c.n++; return nil }

// TestFailureLoggedOnce: a file ffmpeg cannot render is tried at every
// request, but logged only the first time.
func TestFailureLoggedOnce(t *testing.T) {
	logged := 0
	missing := filepath.Join(t.TempDir(), "no-such-tool")
	r := New(config.FFmpeg{FFmpeg: missing, FFprobe: missing, Renderers: 1, RenderTimeout: config.Duration(time.Minute), ProbeTimeout: config.Duration(time.Minute)},
		t.TempDir(), slog.New(counter{slog.DiscardHandler, &logged}))
	src := Source{Path: filepath.Join(t.TempDir(), "photo.jpg"), SHA256: strings.Repeat("a", 64)}
	if err := os.WriteFile(src.Path, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := r.Ensure(context.Background(), src, Thumb); err == nil {
			t.Fatal("rendered with no ffmpeg")
		}
	}
	if logged != 1 {
		t.Fatalf("logged %d times, want once", logged)
	}
}
