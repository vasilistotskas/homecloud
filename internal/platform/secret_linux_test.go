package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSecretFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	for _, content := range []string{"first", "second"} {
		if err := WriteSecretFile(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600", mode)
	}
}

func TestStateDirFollowsXDG(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/x/state")
	if dir, err := StateDir(); err != nil || dir != "/x/state/homecloud" {
		t.Fatalf("StateDir = %q, %v", dir, err)
	}
	t.Setenv("XDG_STATE_HOME", "relative")
	t.Setenv("HOME", "/home/u")
	if dir, err := StateDir(); err != nil || dir != "/home/u/.local/state/homecloud" {
		t.Fatalf("StateDir with relative XDG_STATE_HOME = %q, %v", dir, err)
	}
}
