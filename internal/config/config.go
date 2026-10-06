// Package config loads and validates the homecloud configuration file.
//
// The file is TOML, decoded strictly: unknown keys are rejected so typos
// cannot silently fall back to zero values.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/mail"
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
	ICloud   ICloud   `toml:"icloud"`
	Kei      Kei      `toml:"kei"`
	FFmpeg   FFmpeg   `toml:"ffmpeg"`
	Web      Web      `toml:"web"`
	Notify   Notify   `toml:"notify"`
	Schedule Schedule `toml:"schedule"`
	Backup   Backup   `toml:"backup"`
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

// ICloud describes the account and what of it is archived.
type ICloud struct {
	// AppleID is the account kei signs in to.
	AppleID string `toml:"apple_id"`
	// IncludeHidden archives photos hidden in iCloud Photos, kept apart under
	// Photos/Hidden and hidden on disk. It must be set explicitly.
	IncludeHidden *bool `toml:"include_hidden"`
}

// Kei pins the kei downloader. homecloud generates kei's configuration at
// Config from its own settings; edits to that file are overwritten.
type Kei struct {
	Binary  string `toml:"binary"`
	Version string `toml:"version"`
	Config  string `toml:"config"`
}

// FFmpeg pins one FFmpeg build: ffprobe checks that videos play, ffmpeg
// renders thumbnails and previews. Both must report Version.
type FFmpeg struct {
	FFprobe string `toml:"ffprobe"`
	FFmpeg  string `toml:"ffmpeg"`
	Version string `toml:"version"`
}

// Web configures the local API and web app endpoint.
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

// Backup configures snapshots of the ledger. They belong on a different
// volume than storage.root, so losing the archive drive does not also lose the
// record of what was archived and verified.
type Backup struct {
	Dir      string   `toml:"dir"`
	Interval Duration `toml:"interval"`
	// Keep is how many of the newest snapshots are kept.
	Keep int `toml:"keep"`
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
	if _, err := mail.ParseAddress(c.ICloud.AppleID); err != nil || strings.ContainsAny(c.ICloud.AppleID, "<> ") {
		add("icloud.apple_id must be the Apple Account email address, got %q", c.ICloud.AppleID)
	}
	if c.ICloud.IncludeHidden == nil {
		add("icloud.include_hidden must be set to true or false")
	}

	type field struct{ key, value string }
	for _, f := range []field{
		{"kei.binary", c.Kei.Binary},
		{"kei.config", c.Kei.Config},
		{"ffmpeg.ffprobe", c.FFmpeg.FFprobe},
		{"ffmpeg.ffmpeg", c.FFmpeg.FFmpeg},
	} {
		if !filepath.IsAbs(f.value) {
			add("%s must be an absolute path, got %q", f.key, f.value)
		}
	}
	for _, f := range []field{{"kei.version", c.Kei.Version}, {"ffmpeg.version", c.FFmpeg.Version}} {
		if !versionPattern.MatchString(f.value) {
			add("%s must be an exact MAJOR.MINOR.PATCH version, got %q", f.key, f.value)
		}
	}

	if ap, err := netip.ParseAddrPort(c.Web.Listen); err != nil {
		add("web.listen must be ip:port, got %q: %w", c.Web.Listen, err)
	} else if !ap.Addr().IsLoopback() {
		add("web.listen must be a loopback address, got %q", c.Web.Listen)
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

	if !filepath.IsAbs(c.Backup.Dir) {
		add("backup.dir must be an absolute path, got %q", c.Backup.Dir)
	}
	if c.Backup.Interval.Std() < time.Hour {
		add("backup.interval must be at least 1h, got %s", c.Backup.Interval.Std())
	}
	if c.Backup.Keep < 1 {
		add("backup.keep must be at least 1, got %d", c.Backup.Keep)
	}

	return errors.Join(errs...)
}
