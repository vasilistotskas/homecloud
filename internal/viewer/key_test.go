package viewer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseKey(t *testing.T) {
	if k, err := ParseKey("  " + string(testKey) + "\n"); err != nil || k != testKey {
		t.Fatalf("ParseKey = %q, %v", k, err)
	}
	for _, bad := range []string{"", "has space inside", "with/slash", string(testKey) + "X"} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) accepted", bad)
		}
	}
}

func TestSaveAndLoadKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", KeyFile)
	if _, err := LoadKey(path); !errors.Is(err, ErrNoKey) {
		t.Fatalf("missing key: err = %v, want ErrNoKey", err)
	}
	if err := SaveKey(path, testKey); err != nil {
		t.Fatal(err)
	}
	if k, err := LoadKey(path); err != nil || k != testKey {
		t.Fatalf("LoadKey = %q, %v", k, err)
	}
	if err := os.WriteFile(path, []byte("not a key!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(path); err == nil {
		t.Fatal("a damaged key file was accepted")
	}
}
