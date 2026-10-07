package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/archive"
	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

func TestErr(t *testing.T) {
	if err := Err([]Result{{Name: "a"}, {Name: "b"}}); err != nil {
		t.Fatalf("all passing: want nil, got %v", err)
	}
	err := Err([]Result{{Name: "a"}, {Name: "b", Err: errors.New("boom")}, {Name: "c", Err: errors.New("bang")}})
	if err == nil {
		t.Fatal("want joined error")
	}
	for _, want := range []string{"b: boom", "c: bang"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestCheckDir(t *testing.T) {
	dir := t.TempDir()
	if r := checkDir("d", dir); !r.OK() {
		t.Errorf("checkDir(existing dir): %v", r.Err)
	}
	if r := checkDir("d", filepath.Join(dir, "missing")); r.OK() {
		t.Error("checkDir(missing) should fail")
	}
}

// Ledger snapshots on the archive's own drive would be lost with it. A
// backup.dir that does not exist yet is judged by its nearest existing
// ancestor. (Tests have one drive, so only the refusal can be exercised.)
func TestCheckBackupVolumeRefusesTheArchiveDrive(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Storage: config.Storage{Root: dir},
		Backup:  config.Backup{Dir: filepath.Join(dir, "not", "yet", "created")},
	}
	r := checkBackupVolume(cfg)
	if r.OK() || !strings.Contains(r.Err.Error(), "archive's drive") {
		t.Fatalf("backups on the archive's drive = %+v; want refused", r)
	}
}

func TestCheckArchive(t *testing.T) {
	dir := t.TempDir()
	id, err := archive.Init(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r := checkArchive(config.Storage{Root: dir, ArchiveID: id}); !r.OK() || r.Detail != id {
		t.Errorf("own archive = %+v", r)
	}
	if r := checkArchive(config.Storage{Root: t.TempDir(), ArchiveID: id}); r.OK() {
		t.Error("an unmarked folder passed")
	}
}

func TestCheckKeiConfigOnAFreshInstall(t *testing.T) {
	include := true
	dir := t.TempDir()
	cfg := &config.Config{
		Storage: config.Storage{Root: dir},
		ICloud:  config.ICloud{AppleID: "someone@example.com", IncludeHidden: &include},
		Kei:     config.Kei{Config: filepath.Join(dir, "kei", "config.toml")},
	}
	if r := checkKeiConfig(cfg); !r.OK() || !strings.Contains(r.Detail, "not written yet") {
		t.Fatalf("fresh install = %+v; want OK, explaining the agent writes it", r)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Kei.Config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Kei.Config, []byte("edited by hand"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := checkKeiConfig(cfg); r.OK() {
		t.Fatal("a kei config that differs from homecloud's settings must fail")
	}
}

func TestCheckIPhoneBackupDirs(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{IPhoneBackup: config.IPhoneBackup{Dirs: []string{dir, filepath.Join(dir, "missing"), file}}}
	r := checkIPhoneBackupDirs(cfg)
	if len(r) != 3 || !r[0].OK() || !r[1].OK() || !strings.Contains(r[1].Detail, "no backup yet") || r[2].OK() {
		t.Fatalf("results = %+v", r)
	}
}

func TestCheckIncoming(t *testing.T) {
	dir := t.TempDir()
	if r := checkIncoming(dir); !r.OK() {
		t.Fatalf("incoming folder = %+v", r)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("the probe file was left: %v %v", entries, err)
	}
	if r := checkIncoming(filepath.Join(dir, "missing")); r.OK() {
		t.Fatal("a missing incoming folder passed")
	}
}

func TestCheckPrivate(t *testing.T) {
	dir := t.TempDir()
	if r := checkPrivate("archive access", filepath.Join(dir, "missing")); !r.OK() || !strings.Contains(r.Detail, "not created yet") {
		t.Fatalf("a folder not created yet: %+v", r)
	}
	if _, err := platform.MakePrivate(dir); err != nil {
		t.Fatal(err)
	}
	if r := checkPrivate("archive access", dir); !r.OK() {
		t.Fatalf("a private folder: %+v", r)
	}
}
