package drive

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vasilistotskas/homecloud/internal/platform"
)

// plantLink makes link a junction to target: unlike a symbolic link, any
// user can create one.
func plantLink(t *testing.T, link, target string) {
	t.Helper()
	cmd := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	if out, err := platform.Command(t.Context(), cmd, "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v: %s", err, out)
	}
}
