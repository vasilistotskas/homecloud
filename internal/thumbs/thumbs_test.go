package thumbs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vasilistotskas/homecloud/internal/config"
)

func TestRenderArgs(t *testing.T) {
	got := renderArgs(fileInput("in.HEIC"), "out.webp", "scale=1", true)
	want := []string{
		"-nostdin", "-v", "error", "-y", "-i", "in.HEIC",
		"-filter_complex", "[0:g:0]scale=1[o]", "-map", "[o]",
		"-frames:v", "1", "-c:v", "libwebp", "-quality", "75", "-f", "webp", "out.webp",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("tiled args (-want +got):\n%s", diff)
	}
	if got := renderArgs(fileInput("in.MOV"), "out.webp", "scale=1", false); !strings.Contains(strings.Join(got, " "), "-i in.MOV -vf scale=1") {
		t.Fatalf("plain args = %v", got)
	}
	if got := renderArgs(stdinJPEG, "out.webp", "scale=1", false); !strings.Contains(strings.Join(got, " "), "-f jpeg_pipe -i pipe:0 -vf scale=1") {
		t.Fatalf("raw preview args = %v", got)
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
	makeSource := exec.CommandContext(context.Background(), ffmpeg, //nolint:gosec // test tool from PATH
		"-v", "error", "-f", "lavfi", "-i", "color=red:s=640x480", "-frames:v", "1", src)
	if out, runErr := makeSource.CombinedOutput(); runErr != nil {
		t.Fatalf("make source: %v %s", runErr, out)
	}
	r := New(config.FFmpeg{FFmpeg: ffmpeg, FFprobe: ffprobe}, filepath.Join(dir, "cache"))
	sha := strings.Repeat("ab", 32)
	path, err := r.Ensure(context.Background(), Source{Path: src, SHA256: sha}, Thumb)
	if err != nil {
		t.Fatal(err)
	}
	if path != r.CachePath(sha, Thumb) {
		t.Fatalf("path = %s", path)
	}
	probe := exec.CommandContext(context.Background(), ffprobe, //nolint:gosec // test tool from PATH
		"-v", "error", "-show_entries", "stream=codec_name,width,height", "-of", "csv=p=0", path)
	out, err := probe.Output()
	if err != nil || strings.TrimSpace(string(out)) != "webp,320,320" {
		t.Fatalf("rendered = %q, %v", out, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.Ensure(context.Background(), Source{Path: src, SHA256: sha}, Thumb)
	if err != nil || again != path {
		t.Fatalf("cached Ensure = %s, %v", again, err)
	}
	if info2, _ := os.Stat(path); !info2.ModTime().Equal(info.ModTime()) {
		t.Fatal("a cached rendering must not be rendered again")
	}

	// A DNG renders from its embedded preview: a 64×48 JPEG fits the 1600 box unscaled.
	dng := writeTIFF(t, dir, testJPEG(t, 64, 48), 64, 48, true)
	dngSHA := strings.Repeat("cd", 32)
	dngPath, err := r.Ensure(context.Background(), Source{Path: dng, SHA256: dngSHA}, Preview)
	if err != nil {
		t.Fatal(err)
	}
	probe = exec.CommandContext(context.Background(), ffprobe, //nolint:gosec // test tool from PATH
		"-v", "error", "-show_entries", "stream=codec_name,width,height", "-of", "csv=p=0", dngPath)
	if out, err := probe.Output(); err != nil || strings.TrimSpace(string(out)) != "webp,64,48" {
		t.Fatalf("rendered DNG = %q, %v", out, err)
	}
}

func TestPruneKeepsOnlyCurrentSpecs(t *testing.T) {
	dir := t.TempDir()
	r := New(config.FFmpeg{}, dir)
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

	if removed, err := New(config.FFmpeg{}, filepath.Join(dir, "missing")).Prune(); err != nil || removed != 0 {
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
