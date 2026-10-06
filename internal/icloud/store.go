package icloud

import (
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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
	data, err := readRaw(path)
	if err != nil {
		return Session{}, err
	}
	return decodeSession(path, data)
}

// readRaw returns the saved session's bytes, nil before the first sign-in.
func readRaw(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the per-user state file
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read iCloud session: %w", err)
	}
	return data, nil
}

func decodeSession(path string, data []byte) (Session, error) {
	if data == nil {
		return Session{}, nil
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return Session{}, fmt.Errorf("decode iCloud session %s: %w", path, err)
	}
	return s, nil
}

// LoginSession is the session a new interactive sign-in starts from: nothing
// from the saved one but its trust token, and that only for the same Apple
// Account. A saved session that cannot be read never blocks signing in again.
func LoginSession(path, appleID string) Session {
	saved, err := LoadSession(path)
	if err != nil || !strings.EqualFold(saved.AppleID, appleID) {
		return Session{AppleID: appleID}
	}
	return Session{AppleID: appleID, TrustToken: saved.TrustToken}
}

// SessionFile is a saved session held by a long-running user (the agent),
// which must not overwrite a newer sign-in saved meanwhile.
type SessionFile struct {
	path   string
	digest [sha256.Size]byte
}

// OpenSessionFile reads the saved session and remembers its contents.
func OpenSessionFile(path string) (*SessionFile, Session, error) {
	data, err := readRaw(path)
	if err != nil {
		return nil, Session{}, err
	}
	s, err := decodeSession(path, data)
	if err != nil {
		return nil, Session{}, err
	}
	return &SessionFile{path: path, digest: sha256.Sum256(data)}, s, nil
}

// Save writes s unless the file changed since it was read (the owner signed
// in again meanwhile); saved reports which. The check and the write hold the
// session lock that SaveSession takes too, so the newer sign-in always wins.
func (f *SessionFile) Save(s Session) (saved bool, err error) {
	lock, err := lockSession(f.path)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	current, err := readRaw(f.path)
	if err != nil {
		return false, err
	}
	if sha256.Sum256(current) != f.digest {
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

// SaveSession writes the session owner-only, replacing the file atomically:
// the result of an interactive sign-in, which replaces whatever is saved.
func SaveSession(path string, s Session) (err error) {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode iCloud session: %w", err)
	}
	lock, err := lockSession(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
	return writeSession(path, data)
}

// lockSession takes the lock every writer of the session file holds.
func lockSession(path string) (*platform.FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	lock, err := platform.LockFile(path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("lock iCloud session: %w", err)
	}
	return lock, nil
}

func writeSession(path string, data []byte) error {
	if err := platform.WriteSecretFile(path, data); err != nil {
		return fmt.Errorf("save iCloud session: %w", err)
	}
	return nil
}
