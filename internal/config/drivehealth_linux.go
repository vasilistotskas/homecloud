package config

import (
	"path/filepath"
	"regexp"
	"time"
)

// releasePattern is a MAJOR.MINOR release such as smartctl's "7.5".
var releasePattern = regexp.MustCompile(`^\d+\.\d+$`)

// validateDriveHealth requires an explicit source, and [smartctl] exactly
// when the source is smartctl.
func (c *Config) validateDriveHealth(add func(format string, args ...any)) {
	switch c.DriveHealth.Source {
	case DriveHealthUdisks:
		if c.Smartctl != (Smartctl{}) {
			add("[smartctl] is used only when drive_health.source is %q; remove it", DriveHealthSmartctl)
		}
	case DriveHealthSmartctl:
		s := c.Smartctl
		if !filepath.IsAbs(s.Binary) {
			add("smartctl.binary must be an absolute path, got %q", s.Binary)
		}
		if !releasePattern.MatchString(s.Version) {
			add("smartctl.version must be smartctl's MAJOR.MINOR release, got %q", s.Version)
		}
		if t := s.Timeout.Std(); t < 10*time.Second || t > 10*time.Minute {
			add("smartctl.timeout must be between 10s and 10m, got %s", t)
		}
		if s.Socket != "" && !filepath.IsAbs(s.Socket) {
			add("smartctl.socket must be an absolute path or empty, got %q", s.Socket)
		}
	default:
		add("drive_health.source must be %q (a desktop with udisks2) or %q (a server or container), got %q",
			DriveHealthUdisks, DriveHealthSmartctl, c.DriveHealth.Source)
	}
}
