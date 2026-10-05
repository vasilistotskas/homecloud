package platform

import (
	"errors"
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
func WriteSecretFile(path string, data []byte) (err error) {
	// CreateTemp creates the file with mode 0600.
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp for %q: %w", path, err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(tmp))
		}
	}()
	if _, err = f.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write %q: %w", tmp, err), f.Close())
	}
	if err = f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync %q: %w", tmp, err), f.Close())
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close %q: %w", tmp, err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	return nil
}
