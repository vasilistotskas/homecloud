// Package kei drives the kei iCloud Photos downloader through its public CLI.
package kei

import (
	"regexp"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/tool"
)

// versionPattern matches `kei --version` output, e.g. "kei 0.24.1".
var versionPattern = regexp.MustCompile(`(?m)^kei (\d+\.\d+\.\d+)\s*$`)

// Pin returns the version pin for the configured kei binary.
func Pin(cfg config.Kei) tool.Pin {
	return tool.Pin{
		Name:    "kei",
		Binary:  cfg.Binary,
		Args:    []string{"--version"},
		Pattern: versionPattern,
		Want:    cfg.Version,
	}
}
