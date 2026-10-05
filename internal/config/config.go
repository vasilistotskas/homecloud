// Package config loads and validates the homecloud configuration file.
//
// The file is TOML, decoded strictly: unknown keys are rejected so typos
// cannot silently fall back to zero values.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/vasilistotskas/homecloud/internal/platform"
)

// Config is the complete homecloud configuration.
type Config struct {
	Storage  Storage  `toml:"storage"`
	Kei      Kei      `toml:"kei"`
	FFprobe  FFprobe  `toml:"ffprobe"`
	Web      Web      `toml:"web"`
	Notify   Notify   `toml:"notify"`
	Schedule Schedule `toml:"schedule"`
}

// Storage describes the archive volume.
type Storage struct {
	// Root is the archive root directory (for example E:\HomeCloud).
	Root string `toml:"root"`
	// VolumeID is the expected identity of the volume holding Root: a volume
	// GUID name (\\?\Volume{...}\) on Windows, a filesystem UUID on Linux. It
	// guards against a different disk being mounted at the same location.
	VolumeID string `toml:"volume_id"`
	// MinFreePercent is the minimum free space on the archive volume.
	MinFreePercent float64 `toml:"min_free_percent"`
}

// Kei pins the kei downloader.
type Kei struct {
	Binary  string `toml:"binary"`
	Version string `toml:"version"`
	Config  string `toml:"config"`
}

// FFprobe pins the ffprobe binary used for video verification.
type FFprobe struct {
	Binary  string `toml:"binary"`
	Version string `toml:"version"`
}

// Web configures the local status page.
type Web struct {
	Listen string `toml:"listen"`
}

// Notify configures ntfy push notifications.
type Notify struct {
	Server string `toml:"server"`
	Topic  string `toml:"topic"`
}

// Schedule configures background job cadence.
type Schedule struct {
	SyncInterval  Duration `toml:"sync_interval"`
	ScrubInterval Duration `toml:"scrub_interval"`
	// ScrubFraction is the share of the archive re-hashed per scrub run.
	ScrubFraction float64 `toml:"scrub_fraction"`
}

// Duration is a time.Duration decoded from a Go duration string such as "1h30m".
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", text, err)
	}
	*d = Duration(v)
	return nil
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Load reads, strictly decodes and validates the configuration at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is operator-supplied by design
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return nil, fmt.Errorf("config %s: unknown keys:\n%s", path, strict.String())
		}
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &cfg, nil
}

var (
	versionPattern   = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	ntfyTopicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{24,64}$`)
)

// Validate reports every configuration problem at once.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if !filepath.IsAbs(c.Storage.Root) {
		add("storage.root must be an absolute path, got %q", c.Storage.Root)
	}
	if err := platform.ValidateVolumeID(c.Storage.VolumeID); err != nil {
		add("storage.volume_id: %w", err)
	}
	if c.Storage.MinFreePercent <= 0 || c.Storage.MinFreePercent >= 100 {
		add("storage.min_free_percent must be in (0, 100), got %v", c.Storage.MinFreePercent)
	}

	type field struct{ key, value string }
	for _, f := range []field{
		{"kei.binary", c.Kei.Binary}, {"kei.config", c.Kei.Config}, {"ffprobe.binary", c.FFprobe.Binary},
	} {
		if !filepath.IsAbs(f.value) {
			add("%s must be an absolute path, got %q", f.key, f.value)
		}
	}
	for _, f := range []field{{"kei.version", c.Kei.Version}, {"ffprobe.version", c.FFprobe.Version}} {
		if !versionPattern.MatchString(f.value) {
			add("%s must be an exact MAJOR.MINOR.PATCH version, got %q", f.key, f.value)
		}
	}

	if ap, err := netip.ParseAddrPort(c.Web.Listen); err != nil {
		add("web.listen must be ip:port, got %q: %w", c.Web.Listen, err)
	} else if !ap.Addr().IsLoopback() {
		add("web.listen must be a loopback address (the status page is unauthenticated), got %q", c.Web.Listen)
	}

	if u, err := url.Parse(c.Notify.Server); err != nil || u.Scheme != "https" || u.Host == "" || strings.TrimSuffix(u.Path, "/") != "" {
		add("notify.server must be an https origin such as https://ntfy.sh, got %q", c.Notify.Server)
	}
	if !ntfyTopicPattern.MatchString(c.Notify.Topic) {
		add("notify.topic must be 24-64 characters of [A-Za-z0-9_-] (topics are public; length is the secret)")
	}

	if c.Schedule.SyncInterval.Std() < time.Minute {
		add("schedule.sync_interval must be at least 1m, got %s", c.Schedule.SyncInterval.Std())
	}
	if c.Schedule.ScrubInterval.Std() < time.Hour {
		add("schedule.scrub_interval must be at least 1h, got %s", c.Schedule.ScrubInterval.Std())
	}
	if c.Schedule.ScrubFraction <= 0 || c.Schedule.ScrubFraction > 1 {
		add("schedule.scrub_fraction must be in (0, 1], got %v", c.Schedule.ScrubFraction)
	}

	return errors.Join(errs...)
}
