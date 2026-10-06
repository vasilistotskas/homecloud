package doctor

import (
	"errors"
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
