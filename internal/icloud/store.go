package icloud

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/vasilistotskas/homecloud/internal/platform"
)

// sessionFile is the saved session's name in the per-user state directory.
const sessionFile = "icloud-session.json"

// SessionPath is where the session is saved: the per-user state directory,
// never the archive.
func SessionPath() (string, error) {
	dir, err := platform.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionFile), nil
}

// LoadSession reads the saved session; before the first sign-in it returns an
// empty one.
func LoadSession(path string) (Session, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the per-user state file
	if errors.Is(err, fs.ErrNotExist) {
		return Session{}, nil
	}
	if err != nil {
		return Session{}, fmt.Errorf("read iCloud session: %w", err)
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return Session{}, fmt.Errorf("decode iCloud session %s: %w", path, err)
	}
	return s, nil
}

// SaveSession writes the session owner-only, replacing the file atomically.
func SaveSession(path string, s Session) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode iCloud session: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := platform.WriteSecretFile(path, data); err != nil {
		return fmt.Errorf("save iCloud session: %w", err)
	}
	return nil
}
