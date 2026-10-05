// Package verify implements the archive verification tiers.
package verify

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/tool"
)

// ffprobeVersionPattern matches the first line of `ffprobe -version`, e.g.
// "ffprobe version 9.0.2-full_build-www.gyan.dev Copyright ...".
var ffprobeVersionPattern = regexp.MustCompile(`^ffprobe version (\d+\.\d+\.\d+)\b`)

// FFprobePin returns the version pin for the configured ffprobe binary.
func FFprobePin(cfg config.FFprobe) tool.Pin {
	return tool.Pin{
		Name:    "ffprobe",
		Binary:  cfg.Binary,
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

// Probe runs ffprobe on a video (tier 3). It requires a clean exit with no
// diagnostics, a positive container duration, and at least one video stream
// with a positive duration.
func Probe(ctx context.Context, cfg config.FFprobe, path string) error {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, cfg.Binary, //nolint:gosec // binary from validated config, path from ledger
		"-v", "error",
		"-show_entries", "format=duration:stream=codec_type,duration",
		"-of", "json",
		path)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("ffprobe %s: %w", path, ctx.Err())
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
