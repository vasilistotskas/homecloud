package icloud

import (
	"crypto/sha256"
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
	s, _, err := readSession(path)
	return s, err
}

func readSession(path string) (Session, [sha256.Size]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the per-user state file
	if errors.Is(err, fs.ErrNotExist) {
		return Session{}, sha256.Sum256(nil), nil
	}
	if err != nil {
		return Session{}, [sha256.Size]byte{}, fmt.Errorf("read iCloud session: %w", err)
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return Session{}, [sha256.Size]byte{}, fmt.Errorf("decode iCloud session %s: %w", path, err)
	}
	return s, sha256.Sum256(data), nil
}

// SessionFile is a saved session held by a long-running user (the agent),
// which must not overwrite a newer sign-in saved meanwhile.
type SessionFile struct {
	path   string
	digest [sha256.Size]byte
}

// OpenSessionFile reads the saved session and remembers its contents.
func OpenSessionFile(path string) (*SessionFile, Session, error) {
	s, digest, err := readSession(path)
	if err != nil {
		return nil, Session{}, err
	}
	return &SessionFile{path: path, digest: digest}, s, nil
}

// Save writes s unless the file changed since it was read (the owner signed
// in again meanwhile); saved reports which. The newer sign-in always wins.
func (f *SessionFile) Save(s Session) (saved bool, err error) {
	_, current, err := readSession(f.path)
	if err != nil {
		return false, err
	}
	if current != f.digest {
		return false, nil
	}
	data, err := json.Marshal(s)
	if err != nil {
		return false, fmt.Errorf("encode iCloud session: %w", err)
	}
	if err := writeSession(f.path, data); err != nil {
		return false, err
	}
	f.digest = sha256.Sum256(data)
	return true, nil
}

// SaveSession writes the session owner-only, replacing the file atomically.
func SaveSession(path string, s Session) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode iCloud session: %w", err)
	}
	return writeSession(path, data)
}

func writeSession(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := platform.WriteSecretFile(path, data); err != nil {
		return fmt.Errorf("save iCloud session: %w", err)
	}
	return nil
}
