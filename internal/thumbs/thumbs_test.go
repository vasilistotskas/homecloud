package thumbs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vasilistotskas/homecloud/internal/config"
)

func TestRenderArgs(t *testing.T) {
	got := renderArgs("in.HEIC", "out.webp", "scale=1", true)
	want := []string{
		"-nostdin", "-v", "error", "-y", "-i", "in.HEIC",
		"-filter_complex", "[0:g:0]scale=1[o]", "-map", "[o]",
		"-frames:v", "1", "-c:v", "libwebp", "-quality", "75", "-f", "webp", "out.webp",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("tiled args (-want +got):\n%s", diff)
	}
	if got := renderArgs("in.MOV", "out.webp", "scale=1", false); !strings.Contains(strings.Join(got, " "), "-vf scale=1") {
		t.Fatalf("plain args = %v", got)
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
}
