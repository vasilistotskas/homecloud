package platform

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileReplacesAtomicallyAndIsShared(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.csv")
	for _, data := range []string{"first", "second"} {
		if err := WriteFile(path, []byte(data)); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path) //nolint:gosec // test file
		if err != nil || string(got) != data {
			t.Fatalf("read = %q, %v; want %q", got, err, data)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("dir holds %v (%v); want only the file, no temporary files", entries, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != sharedFileMode {
			t.Fatalf("mode = %o, want %o", mode, sharedFileMode)
		}
	}
}

func TestWriteFileIfChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "README.txt")
	writes := 0
	write := func(p string, data []byte) error {
		writes++
		return WriteFile(p, data)
	}
	for i, tc := range []struct {
		data    string
		changed bool
	}{{"a", true}, {"a", false}, {"b", true}} {
		changed, err := WriteFileIfChanged(path, []byte(tc.data), write)
		if err != nil || changed != tc.changed {
			t.Fatalf("step %d: changed = %v, %v; want %v", i, changed, err, tc.changed)
		}
	}
	if writes != 2 {
		t.Fatalf("writes = %d, want 2", writes)
	}

	failed := errors.New("disk full")
	if _, err := WriteFileIfChanged(path, []byte("c"), func(string, []byte) error { return failed }); !errors.Is(err, failed) {
		t.Fatalf("err = %v, want the write's error", err)
	}
}
