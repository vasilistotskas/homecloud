package platform

import (
	"fmt"
	"path/filepath"
	"strings"
)

// MarkHidden checks that path is hidden: on Linux a name starting with "." is
// what hides a file or folder in file managers and `ls`, so there is no
// attribute to set.
func MarkHidden(path string) error {
	if !strings.HasPrefix(filepath.Base(path), ".") {
		return fmt.Errorf("%q is not hidden: its name must start with a dot", path)
	}
	return nil
}
