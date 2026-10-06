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
)

// validTOML is a complete valid configuration.
var validTOML = strings.NewReplacer(
	"@ROOT@", testRoot, "@VOLUME_ID@", testVolumeID,
	"@KEI_BINARY@", testKeiBinary, "@KEI_CONFIG@", testKeiConfig, "@FFPROBE@", testFFprobeBin, "@FFMPEG@", testFFmpegBin, "@BACKUP_DIR@", testBackupDir, "@IPHONE_DIR@", testIPhoneDir,
).Replace(`
[storage]
root = '@ROOT@'
volume_id = '@VOLUME_ID@'
min_free_percent = 15

[icloud]
apple_id = "someone@example.com"
include_hidden = true

[kei]
binary = '@KEI_BINARY@'
version = "0.24.1"
config = '@KEI_CONFIG@'

[ffmpeg]
ffprobe = '@FFPROBE@'
ffmpeg = '@FFMPEG@'
version = "9.0.2"

[web]
listen = "127.0.0.1:8790"

[notify]
server = "https://ntfy.sh"
topic = "hc_0123456789abcdefABCDEF"

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
max_age = "168h"
check_interval = "24h"
max_shrink_percent = 50

[drive]
sync_interval = "6h"
workers = 4
stall_timeout = "2m"
`)

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
			VolumeID:       testVolumeID,
			MinFreePercent: 15,
		},
		ICloud:   ICloud{AppleID: "someone@example.com", IncludeHidden: ptr(true)},
		Kei:      Kei{Binary: testKeiBinary, Version: "0.24.1", Config: testKeiConfig},
		FFmpeg:   FFmpeg{FFprobe: testFFprobeBin, FFmpeg: testFFmpegBin, Version: "9.0.2"},
		Web:      Web{Listen: "127.0.0.1:8790"},
		Notify:   Notify{Server: "https://ntfy.sh", Topic: "hc_0123456789abcdefABCDEF"},
		Schedule: Schedule{SyncInterval: Duration(time.Hour), ScrubInterval: Duration(168 * time.Hour), ScrubFraction: 0.05},
		Backup:   Backup{Dir: testBackupDir, Interval: Duration(24 * time.Hour), Keep: 14},
		Drive:    Drive{SyncInterval: Duration(6 * time.Hour), Workers: 4, StallTimeout: Duration(2 * time.Minute)},
		IPhoneBackup: IPhoneBackup{
			Dirs: []string{testIPhoneDir}, MaxAge: Duration(168 * time.Hour),
			CheckInterval: Duration(24 * time.Hour), MaxShrinkPercent: 50,
		},
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
		{"bad volume id", func(c *Config) { c.Storage.VolumeID = "not-a-volume" }, "storage.volume_id"},
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
		{"no iphone backup dirs", func(c *Config) { c.IPhoneBackup.Dirs = nil }, "iphone_backup.dirs"},
		{"relative iphone backup dir", func(c *Config) { c.IPhoneBackup.Dirs = []string{"Backup"} }, "iphone_backup.dirs"},
		{"iphone max age too short", func(c *Config) { c.IPhoneBackup.MaxAge = Duration(time.Minute) }, "iphone_backup.max_age"},
		{"iphone checks too frequent", func(c *Config) { c.IPhoneBackup.CheckInterval = Duration(time.Minute) }, "iphone_backup.check_interval"},
		{"iphone shrink 100", func(c *Config) { c.IPhoneBackup.MaxShrinkPercent = 100 }, "iphone_backup.max_shrink_percent"},
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

func ptr[T any](v T) *T { return &v }

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
