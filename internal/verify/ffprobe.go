// Package verify implements the archive verification tiers.
package verify

import (
	"regexp"

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
