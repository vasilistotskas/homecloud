// Package verify implements the archive verification tiers.
package verify

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/tool"
)

// ffprobeVersionPattern matches the first line of `ffprobe -version`, e.g.
// "ffprobe version 9.0.2-full_build-www.gyan.dev Copyright ...".
var ffprobeVersionPattern = regexp.MustCompile(`^ffprobe version (\d+\.\d+\.\d+)\b`)

// FFprobePin returns the version pin for the configured ffprobe binary.
func FFprobePin(cfg config.FFmpeg) tool.Pin {
	return tool.Pin{
		Name:    "ffprobe",
		Binary:  cfg.FFprobe,
		Args:    []string{"-version"},
		Pattern: ffprobeVersionPattern,
		Want:    cfg.Version,
	}
}

// probeReport is the subset of `ffprobe -of json` output homecloud inspects.
// ffprobe encodes durations as decimal strings.
type probeReport struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		Duration  string `json:"duration"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// probeTimeout bounds one ffprobe run. Probing reads container metadata, which
// takes seconds even for multi-gigabyte videos; a run this long is hung, and
// its file is retried next cycle instead of stalling the cycle.
const probeTimeout = 5 * time.Minute

// Probe runs ffprobe on a video (tier 3). It requires a clean exit with no
// diagnostics, a positive container duration, and at least one video stream
// with a positive duration.
func Probe(ctx context.Context, cfg config.FFmpeg, path string) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := platform.Command(ctx, cfg.FFprobe,
		"-v", "error",
		"-show_entries", "format=duration:stream=codec_type,duration",
		"-of", "json",
		path)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("ffprobe %s: %w", path, ctx.Err())
		}
		// Only ffprobe itself rejecting the file is evidence against it. Failing
		// to start ffprobe (moved, locked, missing) says nothing about the file
		// and is retried, never quarantined.
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return fmt.Errorf("run ffprobe on %s: %w", path, err)
		}
		return fmt.Errorf("%w: ffprobe rejected %s: %s", ErrMismatch, path, bytes.TrimSpace(stderr.Bytes()))
	}
	if msg := bytes.TrimSpace(stderr.Bytes()); len(msg) > 0 {
		return fmt.Errorf("%w: ffprobe reported problems in %s: %s", ErrMismatch, path, msg)
	}
	var rep probeReport
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		return fmt.Errorf("decode ffprobe output for %s: %w", path, err)
	}
	return checkProbe(path, &rep)
}

func checkProbe(path string, rep *probeReport) error {
	if !positive(rep.Format.Duration) {
		return fmt.Errorf("%w: %s has no positive container duration (%q)", ErrMismatch, path, rep.Format.Duration)
	}
	for _, s := range rep.Streams {
		if s.CodecType == "video" && positive(s.Duration) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s has no video stream with a positive duration", ErrMismatch, path)
}

func positive(decimal string) bool {
	v, err := strconv.ParseFloat(decimal, 64)
	return err == nil && v > 0
}
