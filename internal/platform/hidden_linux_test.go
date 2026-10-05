package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMarkHidden(t *testing.T) {
	dir := t.TempDir()
	list := filepath.Join(dir, ".hidden")
	if err := os.WriteFile(list, []byte("Other"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 { // idempotent
		if err := MarkHidden(filepath.Join(dir, "Hidden")); err != nil {
			t.Fatal(err)
		}
	}
	if err := MarkHidden(filepath.Join(dir, ".cache")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(list) //nolint:gosec // test temp path
	if err != nil || string(got) != "Other\nHidden\n" {
		t.Fatalf(".hidden = %q, %v", got, err)
	}
}
