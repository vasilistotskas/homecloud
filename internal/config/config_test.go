package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// Platform-specific absolute paths and volume ids come from config_<os>_test.go.
var (
	testKeiBinary  = filepath.Join(testTools, "kei", "kei")
	testKeiConfig  = filepath.Join(testRoot, "kei", "config.toml")
	testFFprobeBin = filepath.Join(testTools, "ffmpeg", "ffprobe")
	testFFmpegBin  = filepath.Join(testTools, "ffmpeg", "ffmpeg")
	testBackupDir  = filepath.Join(testTools, "ledger-backups")
	testIPhoneDir  = filepath.Join(testTools, "MobileSync", "Backup")
	testArchiveID  = "0a1b2c3d-1111-4222-8333-444455556666"
)

// validTOML is a complete valid configuration.
var validTOML = strings.NewReplacer(
	"@ROOT@", testRoot, "@ARCHIVE_ID@", testArchiveID,
	"@KEI_BINARY@", testKeiBinary, "@KEI_CONFIG@", testKeiConfig, "@FFPROBE@", testFFprobeBin, "@FFMPEG@", testFFmpegBin, "@BACKUP_DIR@", testBackupDir, "@IPHONE_DIR@", testIPhoneDir,
	"@DRIVE_HEALTH@", testDriveHealthTOML,
).Replace(`
[storage]
root = '@ROOT@'
archive_id = '@ARCHIVE_ID@'
min_free_percent = 15

[icloud]
apple_id = "someone@example.com"
include_hidden = true
request_timeout = "1m"

[kei]
binary = '@KEI_BINARY@'
version = "0.24.1"
config = '@KEI_CONFIG@'

[ffmpeg]
ffprobe = '@FFPROBE@'
ffmpeg = '@FFMPEG@'
version = "9.0.2"
probe_timeout = "5m"
renderers = 4
render_timeout = "2m"

[verify]
workers = 4

[web]
listen = "127.0.0.1:8790"
public_origin = ""
session_ttl = "168h"

[notify]
server = "https://ntfy.sh"
topic = "hc_0123456789abcdefABCDEF"
repeat_every = "24h"
timeout = "30s"

[schedule]
sync_interval = "1h"
scrub_interval = "168h"
scrub_fraction = 0.05

[backup]
dir = '@BACKUP_DIR@'
interval = "24h"
keep = 14

[iphone_backup]
dirs = ['@IPHONE_DIR@']
keep = 3
max_age = "168h"
check_interval = "24h"
max_shrink_percent = 50
read_interval = "168h"
stalled_after = "6h"

[drive]
sync_interval = "6h"
workers = 4
stall_timeout = "2m"

[logs]
max_size_mb = 20
max_backups = 10
@DRIVE_HEALTH@`)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(writeConfig(t, validTOML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := &Config{
		Storage: Storage{
			Root:           testRoot,
			ArchiveID:      testArchiveID,
			MinFreePercent: 15,
		},
		ICloud: ICloud{AppleID: "someone@example.com", IncludeHidden: new(true), RequestTimeout: Duration(time.Minute)},
		Kei:    Kei{Binary: testKeiBinary, Version: "0.24.1", Config: testKeiConfig},
		FFmpeg: FFmpeg{
			FFprobe: testFFprobeBin, FFmpeg: testFFmpegBin, Version: "9.0.2",
			ProbeTimeout: Duration(5 * time.Minute), Renderers: 4, RenderTimeout: Duration(2 * time.Minute),
		},
		Verify: Verify{Workers: 4},
		Web:    Web{Listen: "127.0.0.1:8790", PublicOrigin: new(""), SessionTTL: Duration(168 * time.Hour)},
		Notify: Notify{
			Server: "https://ntfy.sh", Topic: "hc_0123456789abcdefABCDEF",
			RepeatEvery: Duration(24 * time.Hour), Timeout: Duration(30 * time.Second),
		},
		Schedule: Schedule{SyncInterval: Duration(time.Hour), ScrubInterval: Duration(168 * time.Hour), ScrubFraction: 0.05},
		Backup:   Backup{Dir: testBackupDir, Interval: Duration(24 * time.Hour), Keep: 14},
		Drive:    Drive{SyncInterval: Duration(6 * time.Hour), Workers: 4, StallTimeout: Duration(2 * time.Minute)},
		IPhoneBackup: IPhoneBackup{
			Dirs: []string{testIPhoneDir}, Keep: 3, MaxAge: Duration(168 * time.Hour),
			CheckInterval: Duration(24 * time.Hour), MaxShrinkPercent: 50,
			ReadInterval: Duration(168 * time.Hour), StalledAfter: Duration(6 * time.Hour),
		},
		Logs:        Logs{MaxSizeMB: 20, MaxBackups: 10},
		DriveHealth: testDriveHealth,
		Smartctl:    testSmartctl,
	}
	if diff := cmp.Diff(want, cfg); diff != "" {
		t.Errorf("config mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	_, err := Load(writeConfig(t, validTOML+"\n[storage.extra]\ntypo = 1\n"))
	if err == nil || !strings.Contains(err.Error(), "unknown keys") {
		t.Fatalf("want unknown-keys error, got %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.toml")); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestValidate(t *testing.T) {
	base := func() Config {
		cfg, err := Load(writeConfig(t, validTOML))
		if err != nil {
			t.Fatal(err)
		}
		return *cfg
	}
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"relative root", func(c *Config) { c.Storage.Root = `HomeCloud` }, "storage.root"},
		{"bad archive id", func(c *Config) { c.Storage.ArchiveID = "not-an-archive" }, "storage.archive_id"},
		{"free percent zero", func(c *Config) { c.Storage.MinFreePercent = 0 }, "storage.min_free_percent"},
		{"free percent 100", func(c *Config) { c.Storage.MinFreePercent = 100 }, "storage.min_free_percent"},
		{"no apple id", func(c *Config) { c.ICloud.AppleID = "" }, "icloud.apple_id"},
		{"display-name apple id", func(c *Config) { c.ICloud.AppleID = "Me <me@example.com>" }, "icloud.apple_id"},
		{"hidden unset", func(c *Config) { c.ICloud.IncludeHidden = nil }, "icloud.include_hidden"},
		{"relative kei binary", func(c *Config) { c.Kei.Binary = `kei.exe` }, "kei.binary"},
		{"loose kei version", func(c *Config) { c.Kei.Version = "0.24" }, "kei.version"},
		{"loose ffmpeg version", func(c *Config) { c.FFmpeg.Version = "latest" }, "ffmpeg.version"},
		{"relative ffmpeg", func(c *Config) { c.FFmpeg.FFmpeg = "ffmpeg.exe" }, "ffmpeg.ffmpeg"},
		{"non-loopback listen", func(c *Config) { c.Web.Listen = "0.0.0.0:8790" }, "web.listen"},
		{"ipv6 loopback listen", func(c *Config) { c.Web.Listen = "[::1]:8790" }, "127.0.0.1"},
		{"listen without port", func(c *Config) { c.Web.Listen = "127.0.0.1" }, "web.listen"},
		{"http ntfy", func(c *Config) { c.Notify.Server = "http://ntfy.sh" }, "notify.server"},
		{"ntfy with path", func(c *Config) { c.Notify.Server = "https://ntfy.sh/topic" }, "notify.server"},
		{"short topic", func(c *Config) { c.Notify.Topic = "short" }, "notify.topic"},
		{"sync too frequent", func(c *Config) { c.Schedule.SyncInterval = Duration(time.Second) }, "schedule.sync_interval"},
		{"scrub too frequent", func(c *Config) { c.Schedule.ScrubInterval = Duration(time.Minute) }, "schedule.scrub_interval"},
		{"scrub fraction zero", func(c *Config) { c.Schedule.ScrubFraction = 0 }, "schedule.scrub_fraction"},
		{"scrub fraction above one", func(c *Config) { c.Schedule.ScrubFraction = 1.5 }, "schedule.scrub_fraction"},
		{"relative backup dir", func(c *Config) { c.Backup.Dir = "backups" }, "backup.dir"},
		{"backup too frequent", func(c *Config) { c.Backup.Interval = Duration(time.Minute) }, "backup.interval"},
		{"keep no backups", func(c *Config) { c.Backup.Keep = 0 }, "backup.keep"},
		{"drive syncs too often", func(c *Config) { c.Drive.SyncInterval = Duration(time.Minute) }, "drive.sync_interval"},
		{"no drive workers", func(c *Config) { c.Drive.Workers = 0 }, "drive.workers"},
		{"drive stall timeout too short", func(c *Config) { c.Drive.StallTimeout = Duration(time.Second) }, "drive.stall_timeout"},
		{"iphone dir twice", func(c *Config) {
			c.IPhoneBackup.Dirs = append(c.IPhoneBackup.Dirs, strings.ToUpper(c.IPhoneBackup.Dirs[0]))
		}, "iphone_backup.dirs"},
		{"iphone check slower than max age", func(c *Config) { c.IPhoneBackup.CheckInterval = Duration(200 * time.Hour) }, "iphone_backup.check_interval"},
		{"iphone read more often than checks", func(c *Config) { c.IPhoneBackup.ReadInterval = Duration(time.Hour) }, "iphone_backup.read_interval"},
		{"iphone stalled too soon", func(c *Config) { c.IPhoneBackup.StalledAfter = Duration(time.Minute) }, "iphone_backup.stalled_after"},
		{"alerts repeat too often", func(c *Config) { c.Notify.RepeatEvery = Duration(time.Minute) }, "notify.repeat_every"},
		{"no iphone backup dirs", func(c *Config) { c.IPhoneBackup.Dirs = nil }, "iphone_backup.dirs"},
		{"icloud requests time out too soon", func(c *Config) { c.ICloud.RequestTimeout = Duration(time.Second) }, "icloud.request_timeout"},
		{"probe timeout too long", func(c *Config) { c.FFmpeg.ProbeTimeout = Duration(2 * time.Hour) }, "ffmpeg.probe_timeout"},
		{"no renderers", func(c *Config) { c.FFmpeg.Renderers = 0 }, "ffmpeg.renderers"},
		{"render timeout too short", func(c *Config) { c.FFmpeg.RenderTimeout = Duration(time.Second) }, "ffmpeg.render_timeout"},
		{"no verify workers", func(c *Config) { c.Verify.Workers = 0 }, "verify.workers"},
		{"public origin not set", func(c *Config) { c.Web.PublicOrigin = nil }, "web.public_origin"},
		{"public origin over http", func(c *Config) { c.Web.PublicOrigin = new("http://nas.example.ts.net") }, "web.public_origin"},
		{"public origin with a path", func(c *Config) { c.Web.PublicOrigin = new("https://nas.example.ts.net/app") }, "web.public_origin"},
		{"public origin in upper case", func(c *Config) { c.Web.PublicOrigin = new("https://NAS.example.ts.net") }, "web.public_origin"},
		{"browser sessions too long", func(c *Config) { c.Web.SessionTTL = Duration(1000 * time.Hour) }, "web.session_ttl"},
		{"notify timeout too short", func(c *Config) { c.Notify.Timeout = Duration(time.Second) }, "notify.timeout"},
		{"log files too large", func(c *Config) { c.Logs.MaxSizeMB = 4096 }, "logs.max_size_mb"},
		{"no rotated logs kept", func(c *Config) { c.Logs.MaxBackups = 0 }, "logs.max_backups"},
		{"relative iphone backup dir", func(c *Config) { c.IPhoneBackup.Dirs = []string{"Backup"} }, "iphone_backup.dirs"},
		{"iphone max age too short", func(c *Config) { c.IPhoneBackup.MaxAge = Duration(time.Minute) }, "iphone_backup.max_age"},
		{"iphone checks too frequent", func(c *Config) { c.IPhoneBackup.CheckInterval = Duration(time.Minute) }, "iphone_backup.check_interval"},
		{"iphone shrink 100", func(c *Config) { c.IPhoneBackup.MaxShrinkPercent = 100 }, "iphone_backup.max_shrink_percent"},
		{"iphone keep negative", func(c *Config) { c.IPhoneBackup.Keep = -1 }, "iphone_backup.keep"},
		{"iphone keep too many", func(c *Config) { c.IPhoneBackup.Keep = 101 }, "iphone_backup.keep"},
		{"relative incoming", func(c *Config) { c.IPhoneBackup.Incoming = "incoming" }, "iphone_backup.incoming"},
		{"incoming without copies", func(c *Config) {
			c.IPhoneBackup.Incoming, c.IPhoneBackup.Keep = filepath.Join(testTools, "incoming"), 0
		}, "iphone_backup.keep must be at least 1"},
		{"incoming is the archive", func(c *Config) { c.IPhoneBackup.Incoming = testRoot }, "apart from storage.root"},
		{"incoming inside the archive", func(c *Config) {
			c.IPhoneBackup.Incoming = filepath.Join(testRoot, "iPhone backups")
		}, "apart from storage.root"},
		{"incoming holds the ledger backups", func(c *Config) { c.IPhoneBackup.Incoming = filepath.Dir(testBackupDir) }, "apart from backup.dir"},
		{"incoming is Apple's backup folder", func(c *Config) { c.IPhoneBackup.Incoming = testIPhoneDir }, "apart from iphone_backup.dirs entry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	var cfg Config
	err := cfg.Validate()
	if err == nil {
		t.Fatal("want errors for zero config")
	}
	for _, key := range []string{"storage.root", "kei.binary", "web.listen", "notify.topic", "schedule.scrub_fraction", "backup.dir"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("joined error missing %q", key)
		}
	}
}

func TestDurationRejectsGarbage(t *testing.T) {
	var d Duration
	if err := d.UnmarshalText([]byte("soon")); err == nil {
		t.Fatal("want parse error")
	}
}

func TestResolvePrecedence(t *testing.T) {
	t.Setenv(EnvPath, "")
	def, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Resolve(""); err != nil || got != def {
		t.Fatalf("Resolve(\"\") = %q, %v; want the default %q", got, err, def)
	}
	t.Setenv(EnvPath, "from-env.toml")
	if got, _ := Resolve(""); got != "from-env.toml" {
		t.Fatalf("Resolve with %s = %q", EnvPath, got)
	}
	if got, _ := Resolve("from-flag.toml"); got != "from-flag.toml" {
		t.Fatalf("Resolve with a flag = %q; the flag must win", got)
	}
}
