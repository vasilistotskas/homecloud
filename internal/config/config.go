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

	"github.com/vasilistotskas/homecloud/internal/archive"
)

// Config is the complete homecloud configuration.
type Config struct {
	Storage      Storage      `toml:"storage"`
	ICloud       ICloud       `toml:"icloud"`
	Kei          Kei          `toml:"kei"`
	FFmpeg       FFmpeg       `toml:"ffmpeg"`
	Verify       Verify       `toml:"verify"`
	Web          Web          `toml:"web"`
	Notify       Notify       `toml:"notify"`
	Schedule     Schedule     `toml:"schedule"`
	Backup       Backup       `toml:"backup"`
	IPhoneBackup IPhoneBackup `toml:"iphone_backup"`
	Drive        Drive        `toml:"drive"`
	Logs         Logs         `toml:"logs"`
	DriveHealth  DriveHealth  `toml:"drive_health"`
	Smartctl     Smartctl     `toml:"smartctl"`
	SafeToDelete SafeToDelete `toml:"safe_to_delete"`
}

// Storage describes the archive.
type Storage struct {
	// Root is the archive root directory, for example D:\Archive (Windows) or
	// /srv/homecloud (Linux).
	Root string `toml:"root"`
	// ArchiveID names the archive at Root (archive.MarkerFile); the agent works
	// only on that archive, so a missing drive or another disk never counts.
	ArchiveID string `toml:"archive_id"`
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
	// RequestTimeout bounds one call of homecloud's own iCloud client (sign-in
	// and iCloud Drive listing); downloads are bounded by drive.stall_timeout.
	RequestTimeout Duration `toml:"request_timeout"`
}

// Kei pins the kei downloader. homecloud generates kei's configuration at
// Config from its own settings; edits to that file are overwritten.
type Kei struct {
	Binary  string `toml:"binary"`
	Version string `toml:"version"`
	Config  string `toml:"config"`
	// CheckpointAlertCycles is how many syncs in a row may end with kei
	// keeping its previous iCloud checkpoint before the agent alerts. kei
	// keeps it for one sync by design, e.g. after its settings changed; a
	// checkpoint kept sync after sync means every sync lists the whole
	// library again and kei is stuck.
	CheckpointAlertCycles int `toml:"checkpoint_alert_cycles"`
}

// FFmpeg pins one FFmpeg build: ffprobe checks that videos play, ffmpeg
// renders thumbnails and previews. Both must report Version.
type FFmpeg struct {
	FFprobe string `toml:"ffprobe"`
	FFmpeg  string `toml:"ffmpeg"`
	Version string `toml:"version"`
	// ProbeTimeout bounds one ffprobe run on a video. Probing reads container
	// metadata, which takes seconds even for large videos; a run this long is
	// hung, and its file is retried at the next sync.
	ProbeTimeout Duration `toml:"probe_timeout"`
	// Renderers is how many thumbnails and previews render at once.
	Renderers int `toml:"renderers"`
	// RenderTimeout bounds rendering one thumbnail or preview; a hung render
	// is retried at the next request.
	RenderTimeout Duration `toml:"render_timeout"`
}

// Verify configures checking archived files.
type Verify struct {
	// Workers is how many files are checked at once, by sync cycles and by
	// integrity checks.
	Workers int `toml:"workers"`
}

// Web configures the local API and web app endpoint.
type Web struct {
	Listen string `toml:"listen"`
	// PublicOrigin is the https origin a proxy on this host serves the agent
	// at (Tailscale Serve, e.g. https://nas.example.ts.net), so the app can be
	// used from other devices; "" when it is used on this computer only. It
	// must be set, so the choice is always explicit.
	PublicOrigin *string `toml:"public_origin"`
	// SessionTTL is how long a browser stays signed in after exchanging a
	// login code.
	SessionTTL Duration `toml:"session_ttl"`
}

// Notify configures ntfy push notifications.
type Notify struct {
	Server string `toml:"server"`
	Topic  string `toml:"topic"`
	// RepeatEvery is how often an alert about a condition that persists
	// (sign-in needed, drive missing, low space, a backup problem) repeats.
	RepeatEvery Duration `toml:"repeat_every"`
	// Timeout bounds sending one notification.
	Timeout Duration `toml:"timeout"`
}

// Schedule configures background job cadence.
type Schedule struct {
	SyncInterval  Duration `toml:"sync_interval"`
	ScrubInterval Duration `toml:"scrub_interval"`
	// ScrubFraction is the share of the archive re-hashed per scrub run.
	ScrubFraction float64 `toml:"scrub_fraction"`
}

// Drive configures the iCloud Drive archive, kept under <storage.root>/Drive.
type Drive struct {
	// SyncInterval is how often iCloud Drive is listed and new or changed
	// files are archived.
	SyncInterval Duration `toml:"sync_interval"`
	// Workers is how many files download at once.
	Workers int `toml:"workers"`
	// StallTimeout abandons a download that receives no data for this long;
	// it is retried at the next sync.
	StallTimeout Duration `toml:"stall_timeout"`
}

// DriveHealth chooses, on Linux, where the SMART data of the drives holding
// the archive comes from. Windows reads NVMe health itself and has no
// [drive_health] section.
type DriveHealth struct {
	// Source is DriveHealthUdisks or DriveHealthSmartctl.
	Source string `toml:"source"`
}

// Drive health sources on Linux.
const (
	// DriveHealthUdisks reads what the udisks2 service of a desktop keeps;
	// it needs no rights.
	DriveHealthUdisks = "udisks"
	// DriveHealthSmartctl runs smartctl ([smartctl]), for servers and the
	// container, which have no udisks. smartctl needs the drives' device
	// nodes and the capabilities to query them (README, NAS mode).
	DriveHealthSmartctl = "smartctl"
)

// Smartctl pins smartmontools' smartctl, used when drive_health.source is
// "smartctl" and absent otherwise.
type Smartctl struct {
	Binary string `toml:"binary"`
	// Version is smartctl's MAJOR.MINOR release, e.g. "7.5".
	Version string `toml:"version"`
	// Timeout bounds one smartctl run on one drive.
	Timeout Duration `toml:"timeout"`
	// Socket is where the container's smartctl sidecar (`homecloud smartctl
	// serve`) answers: the agent asks it instead of running smartctl itself.
	// Empty runs smartctl in the agent (a server without containers).
	Socket string `toml:"socket"`
}

// Logs configures the agent's rotating log files in %LOCALAPPDATA%\homecloud\logs
// on Windows, and how many crash reports are kept there. On Linux the agent
// logs to journald, which rotates by itself.
type Logs struct {
	// MaxSizeMB is the size at which a log file is rotated, in MiB.
	MaxSizeMB int `toml:"max_size_mb"`
	// MaxBackups is how many rotated files are kept per log.
	MaxBackups int `toml:"max_backups"`
}

// MaxBytes is MaxSizeMB in bytes.
func (l Logs) MaxBytes() int64 { return int64(l.MaxSizeMB) << 20 }

// IPhoneBackup configures the monitor of iPhone backups, which Apple's
// Devices app (or iTunes, or idevicebackup2) writes (homecloud only reads
// them), and the copies of them kept in the archive.
type IPhoneBackup struct {
	// Dirs are the backup directories on this computer to watch, as absolute
	// paths, e.g. C:\Users\<you>\Apple\MobileSync\Backup for the Apple
	// Devices app. Empty when backups only arrive through Incoming.
	Dirs []string `toml:"dirs"`
	// Incoming is a folder another computer pushes finished backups to with
	// "homecloud iphone push" (on a NAS, a shared folder); "" when backups
	// are made on this computer only.
	Incoming string `toml:"incoming"`
	// Keep is how many copies of each device's backups the archive keeps
	// under <storage.root>/iPhone backups, newest first; 0 keeps none.
	Keep int `toml:"keep"`
	// MaxAge is the oldest acceptable finished backup.
	MaxAge Duration `toml:"max_age"`
	// CheckInterval is how often the backups' metadata and sizes are checked.
	CheckInterval Duration `toml:"check_interval"`
	// ReadInterval is how often every backed-up file is read in full, which
	// proves the disk still delivers the backup but takes as long as reading
	// the whole backup.
	ReadInterval Duration `toml:"read_interval"`
	// StalledAfter is how long a backup in progress may go without writing
	// before it counts as stopped.
	StalledAfter Duration `toml:"stalled_after"`
	// MaxShrinkPercent is the largest acceptable size drop against the last
	// normal backup, in percent.
	MaxShrinkPercent float64 `toml:"max_shrink_percent"`
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

// SafeToDelete configures the report of iCloud Photos items whose archived
// copy is proven, which the owner may delete in Apple's Photos app. homecloud
// itself deletes nothing.
type SafeToDelete struct {
	// MinAge is how long an item must have been in iCloud Photos before the
	// report lists it, so recent photos stay on the owner's devices.
	MinAge Duration `toml:"min_age"`
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

// EnvPath names the environment variable that overrides the default path.
const EnvPath = "HOMECLOUD_CONFIG"

// DefaultPath is where the configuration lives unless --config or EnvPath
// says otherwise: %APPDATA%\homecloud\config.toml on Windows,
// $XDG_CONFIG_HOME/homecloud/config.toml (default ~/.config) on Linux.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config dir: %w", err)
	}
	return filepath.Join(dir, "homecloud", "config.toml"), nil
}

// Resolve returns the configuration path in precedence order: flag, then
// EnvPath, then DefaultPath.
func Resolve(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if p := os.Getenv(EnvPath); p != "" {
		return p, nil
	}
	return DefaultPath()
}

// Load reads, strictly decodes and validates the configuration at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is operator-supplied by design
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		if strict, ok := errors.AsType[*toml.StrictMissingError](err); ok {
			return nil, fmt.Errorf("config %s: unknown keys:\n%s", path, strict.String())
		}
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &cfg, nil
}

// maxIPhoneCopies bounds iphone_backup.keep, and with it the hard links one
// backed-up file gets (NTFS allows 1024 per file).
const maxIPhoneCopies = 100

var (
	versionPattern   = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	ntfyTopicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{24,64}$`)
)

// ValidatePublicOrigin accepts an https origin written exactly as browsers
// send it in the Origin header: lowercase scheme and host, an optional port,
// nothing else.
func ValidatePublicOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || "https://"+u.Host != origin || origin != strings.ToLower(origin) {
		return fmt.Errorf("must be a lowercase https origin such as https://nas.example.ts.net, got %q", origin)
	}
	return nil
}

// Validate reports every configuration problem at once.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if !filepath.IsAbs(c.Storage.Root) {
		add("storage.root must be an absolute path, got %q", c.Storage.Root)
	}
	if err := archive.ValidateID(c.Storage.ArchiveID); err != nil {
		add("storage.archive_id: %w", err)
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
	if t := c.ICloud.RequestTimeout.Std(); t < 10*time.Second || t > 10*time.Minute {
		add("icloud.request_timeout must be between 10s and 10m, got %s", t)
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

	// One kept checkpoint is kei working as designed, so a single sync never alerts.
	if n := c.Kei.CheckpointAlertCycles; n < 2 || n > 100 {
		add("kei.checkpoint_alert_cycles must be between 2 and 100, got %d", n)
	}

	if t := c.FFmpeg.ProbeTimeout.Std(); t < 30*time.Second || t > time.Hour {
		add("ffmpeg.probe_timeout must be between 30s and 1h, got %s", t)
	}
	if c.FFmpeg.Renderers < 1 || c.FFmpeg.Renderers > 16 {
		add("ffmpeg.renderers must be between 1 and 16, got %d", c.FFmpeg.Renderers)
	}
	if t := c.FFmpeg.RenderTimeout.Std(); t < 10*time.Second || t > 30*time.Minute {
		add("ffmpeg.render_timeout must be between 10s and 30m, got %s", t)
	}
	if c.Verify.Workers < 1 || c.Verify.Workers > 32 {
		add("verify.workers must be between 1 and 32, got %d", c.Verify.Workers)
	}

	if ap, err := netip.ParseAddrPort(c.Web.Listen); err != nil {
		add("web.listen must be ip:port, got %q: %w", c.Web.Listen, err)
	} else if ap.Addr() != netip.AddrFrom4([4]byte{127, 0, 0, 1}) {
		// The desktop app's content security policy allows exactly this address.
		add("web.listen must be 127.0.0.1:<port> (local only, and the address the desktop app trusts), got %q", c.Web.Listen)
	}
	switch {
	case c.Web.PublicOrigin == nil:
		add(`web.public_origin must be set: "" for this computer only, or the https origin a proxy serves the agent at`)
	case *c.Web.PublicOrigin != "":
		if err := ValidatePublicOrigin(*c.Web.PublicOrigin); err != nil {
			add("web.public_origin: %w", err)
		}
	}
	if t := c.Web.SessionTTL.Std(); t < time.Hour || t > 30*24*time.Hour {
		add("web.session_ttl must be between 1h and 720h, got %s", t)
	}

	if u, err := url.Parse(c.Notify.Server); err != nil || u.Scheme != "https" || u.Host == "" || strings.TrimSuffix(u.Path, "/") != "" {
		add("notify.server must be an https origin such as https://ntfy.sh, got %q", c.Notify.Server)
	}
	if !ntfyTopicPattern.MatchString(c.Notify.Topic) {
		add("notify.topic must be 24-64 characters of [A-Za-z0-9_-] (topics are public; length is the secret)")
	}
	if c.Notify.RepeatEvery.Std() < time.Hour {
		add("notify.repeat_every must be at least 1h, got %s", c.Notify.RepeatEvery.Std())
	}
	if t := c.Notify.Timeout.Std(); t < 5*time.Second || t > 2*time.Minute {
		add("notify.timeout must be between 5s and 2m, got %s", t)
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

	if c.Drive.SyncInterval.Std() < 15*time.Minute {
		add("drive.sync_interval must be at least 15m, got %s", c.Drive.SyncInterval.Std())
	}
	if c.Drive.Workers < 1 || c.Drive.Workers > 16 {
		add("drive.workers must be between 1 and 16, got %d", c.Drive.Workers)
	}
	if st := c.Drive.StallTimeout.Std(); st < 30*time.Second || st > 10*time.Minute {
		add("drive.stall_timeout must be between 30s and 10m, got %s", st)
	}

	if a := c.SafeToDelete.MinAge.Std(); a < 24*time.Hour || a > 10*365*24*time.Hour {
		add("safe_to_delete.min_age must be between 24h and 87600h (10 years), got %s", a)
	}

	if c.Logs.MaxSizeMB < 1 || c.Logs.MaxSizeMB > 1024 {
		add("logs.max_size_mb must be between 1 and 1024, got %d", c.Logs.MaxSizeMB)
	}
	if c.Logs.MaxBackups < 1 || c.Logs.MaxBackups > 100 {
		add("logs.max_backups must be between 1 and 100, got %d", c.Logs.MaxBackups)
	}

	ib := c.IPhoneBackup
	if len(ib.Dirs) == 0 && ib.Incoming == "" {
		add("iphone_backup.dirs or iphone_backup.incoming must name where backups are")
	}
	if ib.Incoming != "" && !filepath.IsAbs(ib.Incoming) {
		add("iphone_backup.incoming must be an absolute path, got %q", ib.Incoming)
	}
	// The agent removes each push it takes in and writes receipts beside it, so
	// incoming must hold nothing else: not the archive (its copies would look
	// like pushes), not the ledger backups, not Apple's own backup folders.
	if ib.Incoming != "" {
		apart := map[string]string{"storage.root": c.Storage.Root, "backup.dir": c.Backup.Dir}
		for _, d := range ib.Dirs {
			apart["iphone_backup.dirs entry "+d] = d
		}
		for key, dir := range apart {
			if dir != "" && nested(ib.Incoming, dir) {
				add("iphone_backup.incoming %q must be apart from %s (neither the same folder nor inside the other)", ib.Incoming, key)
			}
		}
	}
	switch {
	case ib.Keep < 0 || ib.Keep > maxIPhoneCopies:
		add("iphone_backup.keep must be between 0 and %d, got %d", maxIPhoneCopies, ib.Keep)
	case ib.Incoming != "" && ib.Keep == 0:
		add("iphone_backup.keep must be at least 1 when iphone_backup.incoming is set: pushed backups are kept only as copies")
	}
	seenDirs := map[string]bool{}
	for _, d := range ib.Dirs {
		if !filepath.IsAbs(d) {
			add("iphone_backup.dirs entries must be absolute paths, got %q", d)
		}
		key := strings.ToLower(filepath.Clean(d))
		if seenDirs[key] {
			add("iphone_backup.dirs lists %q twice", d)
		}
		seenDirs[key] = true
	}
	if ib.MaxAge.Std() < time.Hour {
		add("iphone_backup.max_age must be at least 1h, got %s", ib.MaxAge.Std())
	}
	if ib.CheckInterval.Std() < time.Hour || ib.CheckInterval.Std() > ib.MaxAge.Std() {
		add("iphone_backup.check_interval must be between 1h and iphone_backup.max_age, got %s", ib.CheckInterval.Std())
	}
	if ib.ReadInterval.Std() < ib.CheckInterval.Std() {
		add("iphone_backup.read_interval must be at least iphone_backup.check_interval, got %s", ib.ReadInterval.Std())
	}
	if ib.StalledAfter.Std() < time.Hour {
		add("iphone_backup.stalled_after must be at least 1h, got %s", ib.StalledAfter.Std())
	}
	if ib.MaxShrinkPercent <= 0 || ib.MaxShrinkPercent >= 100 {
		add("iphone_backup.max_shrink_percent must be in (0, 100), got %v", ib.MaxShrinkPercent)
	}

	c.validateDriveHealth(add)
	return errors.Join(errs...)
}

// nested reports whether a and b are the same folder or one lies inside the
// other.
func nested(a, b string) bool {
	inside := func(dir, path string) bool {
		rel, err := filepath.Rel(dir, path)
		return err == nil && filepath.IsLocal(rel)
	}
	return inside(a, b) || inside(b, a)
}
