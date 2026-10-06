package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestCheckBackupVolume(t *testing.T) {
	dir := t.TempDir()
	here, err := platform.VolumeID(dir)
	if errors.Is(err, platform.ErrNoVolumeIdentity) {
		t.Skipf("the temp dir has no volume identity here (e.g. a container without /dev/disk/by-uuid): %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Backup: config.Backup{Dir: filepath.Join(dir, "not", "yet", "created")}}

	cfg.Storage.VolumeID = here
	if r := checkBackupVolume(cfg); r.OK() {
		t.Error("backups on the archive volume accepted")
	}
	cfg.Storage.VolumeID = "another-volume"
	if r := checkBackupVolume(cfg); !r.OK() {
		t.Errorf("backups off the archive volume rejected: %v", r.Err)
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
