package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// StateDir is the per-user directory for homecloud's runtime state:
// $XDG_STATE_HOME/homecloud, where the XDG Base Directory specification
// defines an unset or relative XDG_STATE_HOME as $HOME/.local/state.
func StateDir() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "homecloud"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "homecloud"), nil
}

// WriteSecretFile atomically replaces path with data in a file of mode 0600.
func WriteSecretFile(path string, data []byte) error {
	// CreateTemp creates the file with mode 0600.
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp for %q: %w", path, err)
	}
	return commit(f, path, data)
}
