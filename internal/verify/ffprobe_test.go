package verify

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/vasilistotskas/homecloud/internal/config"
)

func TestFFprobeVersionPattern(t *testing.T) {
	tests := []struct {
		output, want string
	}{
		{"ffprobe version 9.0.2-full_build-www.gyan.dev Copyright (c) 2007-2026 the FFmpeg developers", "9.0.2"},
		{"ffprobe version 9.0.2 Copyright", "9.0.2"},
		{"ffprobe version 9.0.21-x", "9.0.21"},
		{"ffprobe version N-12345-gabcdef", ""},
		{"ffmpeg version 9.0.2", ""},
	}
	for _, tt := range tests {
		m := ffprobeVersionPattern.FindStringSubmatch(tt.output)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != tt.want {
			t.Errorf("ffprobeVersionPattern(%q) = %q, want %q", tt.output, got, tt.want)
		}
	}
}

func TestProbeUnstartableFFprobeIsTransient(t *testing.T) {
	cfg := config.FFmpeg{FFprobe: filepath.Join(t.TempDir(), "no-ffprobe")}
	err := Probe(context.Background(), cfg, filepath.Join(t.TempDir(), "clip.mov"))
	if err == nil || errors.Is(err, ErrMismatch) {
		t.Fatalf("Probe with a missing ffprobe = %v; want a transient error, never a mismatch", err)
	}
}
