// Package platformtest helps tests isolate themselves from the computer they
// run on.
package platformtest

import (
	"path/filepath"
	"testing"

	"github.com/vasilistotskas/homecloud/internal/platform"
)

// StateDir points platform.StateDir at a fresh folder for the rest of the
// test, on Windows (%LocalAppData%) and Linux ($XDG_STATE_HOME) alike, so a
// test never reads or writes this computer's real state, and returns it.
func StateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("XDG_STATE_HOME", dir)
	state, err := platform.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(dir, state); err != nil || !filepath.IsLocal(rel) {
		t.Fatalf("the state directory %s is outside %s", state, dir)
	}
	return state
}
