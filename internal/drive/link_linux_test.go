package drive

import (
	"os"
	"testing"
)

// plantLink makes link a symbolic link to target.
func plantLink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
