package platform

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// hiddenListFile lists names a file manager should treat as hidden in its
// directory; GNOME Files (Nautilus), the GTK file chooser, Dolphin and
// Konqueror honour it (Thunar does not).
const hiddenListFile = ".hidden"

// MarkHidden hides path in file managers. A leading dot is the universal way
// on Linux, so dot-names need nothing; other names are added to the parent
// directory's .hidden list, keeping its existing entries.
func MarkHidden(path string) error {
	name := filepath.Base(path)
	if strings.HasPrefix(name, ".") {
		return nil
	}
	list := filepath.Join(filepath.Dir(path), hiddenListFile)
	current, err := os.ReadFile(list) //nolint:gosec // fixed name next to a path homecloud manages
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", list, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(current))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == name {
			return nil
		}
	}
	if len(current) > 0 && !bytes.HasSuffix(current, []byte("\n")) {
		current = append(current, '\n')
	}
	if err := os.WriteFile(list, append(current, []byte(name+"\n")...), 0o644); err != nil { //nolint:gosec // read by this user's file managers
		return fmt.Errorf("update %s: %w", list, err)
	}
	return nil
}
