package platform

import (
	"os"
	"strings"
	"testing"
)

func TestMakePrivate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // opened to others on purpose, to be made private
		t.Fatal(err)
	}
	if err := CheckPrivate(dir); err == nil || !strings.Contains(err.Error(), "0755") {
		t.Fatalf("CheckPrivate on a 0755 folder = %v", err)
	}
	changed, err := MakePrivate(dir)
	if err != nil || !changed {
		t.Fatalf("MakePrivate = %v, %v", changed, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != privateMode {
		t.Fatalf("mode = %04o, want %04o", perm, privateMode)
	}
	if changed, err := MakePrivate(dir); err != nil || changed {
		t.Fatalf("MakePrivate on a private folder = %v, %v; want no change", changed, err)
	}
}
