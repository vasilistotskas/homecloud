package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/vasilistotskas/homecloud/internal/platform/platformtest"
	"github.com/vasilistotskas/homecloud/internal/viewer"
)

func TestViewerKey(t *testing.T) {
	state := platformtest.StateDir(t)
	if out, err := cli(t, "", "viewer", "key"); err != nil || !strings.Contains(out, "No viewer API key") {
		t.Fatalf("viewer key before --set: %q, %v", out, err)
	}
	const key = "abcDEF0123456789abcDEF0123456789abcDEF01234"
	if out, err := cli(t, key+"\n", "viewer", "key", "--set"); err != nil {
		t.Fatalf("viewer key --set: %v\n%s", err, out)
	}
	if saved, err := viewer.LoadKey(filepath.Join(state, viewer.KeyFile)); err != nil || saved != key {
		t.Fatalf("saved key %q, %v", saved, err)
	}
	out, err := cli(t, "", "viewer", "key")
	if err != nil || !strings.Contains(out, "is saved in") || strings.Contains(out, key) {
		t.Fatalf("viewer key printed %q, %v; want where it is saved, never the key", out, err)
	}
	if out, err := cli(t, "not a key!\n", "viewer", "key", "--set"); err == nil {
		t.Fatalf("a malformed key was saved: %s", out)
	}
}
