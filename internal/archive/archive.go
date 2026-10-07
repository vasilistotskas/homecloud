// Package archive identifies a homecloud archive by a marker file at its
// root. The agent works only on the archive its configuration names: an
// unmounted drive, another disk or an empty folder has no marker or another
// one, so nothing is downloaded, checked or deleted there. The marker travels
// with the files, so an archive moved to another disk or machine keeps its
// identity, and it needs nothing from the operating system, so it works the
// same on Windows, Linux, in containers and on network shares.
package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
	"uuid"

	"github.com/vasilistotskas/homecloud/internal/platform"
)

// MarkerFile is the file at the archive root that names the archive.
const MarkerFile = "homecloud-archive.json"

// ErrNotMarked is returned for a folder that holds no homecloud archive.
var ErrNotMarked = errors.New("no homecloud archive marker")

// marker is the content of MarkerFile.
type marker struct {
	ID        string    `json:"archive_id"`
	CreatedAt time.Time `json:"created_at"`
}

// ValidateID accepts an archive id as Init creates it: a canonical,
// lowercase random (version 4) UUID. The version is the high four bits of
// octet 6 (RFC 9562, section 4.2).
func ValidateID(id string) error {
	u, err := uuid.Parse(id)
	if err != nil || u[6]>>4 != 4 || u.String() != id {
		return fmt.Errorf("%q is not an archive id (a lowercase UUID as homecloud archive init prints it)", id)
	}
	return nil
}

// Init marks root, an existing folder, as a new archive and returns its id.
// It never replaces a marker: an archive keeps its identity for life. The
// folder is made the current user's alone first (platform.MakePrivate): it
// will hold the only copy of the library, Hidden photos included.
func Init(root string, now time.Time) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("archive root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("archive root %s is not a folder", root)
	}
	if _, err = platform.MakePrivate(root); err != nil {
		return "", err
	}
	id := uuid.NewV4().String()
	data, err := json.MarshalIndent(marker{ID: id, CreatedAt: now.UTC()}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode archive marker: %w", err)
	}
	path := filepath.Join(root, MarkerFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // the marker is not secret
	if errors.Is(err, fs.ErrExist) {
		existing, readErr := ID(root)
		if readErr != nil {
			return "", fmt.Errorf("%s already exists and cannot be read: %w", path, readErr)
		}
		return "", fmt.Errorf("%s is already homecloud archive %s", root, existing)
	}
	if err != nil {
		return "", fmt.Errorf("create archive marker: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return "", errors.Join(fmt.Errorf("write archive marker: %w", err), f.Close(), os.Remove(path))
	}
	if err := f.Sync(); err != nil {
		return "", errors.Join(fmt.Errorf("sync archive marker: %w", err), f.Close(), os.Remove(path))
	}
	if err := f.Close(); err != nil {
		return "", errors.Join(fmt.Errorf("close archive marker: %w", err), os.Remove(path))
	}
	return id, nil
}

// ID returns the id of the archive at root, or ErrNotMarked.
func ID(root string) (string, error) {
	path := filepath.Join(root, MarkerFile)
	data, err := os.ReadFile(path) //nolint:gosec // the configured archive root
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w at %s", ErrNotMarked, root)
	}
	if err != nil {
		return "", fmt.Errorf("read archive marker: %w", err)
	}
	var m marker
	if err := json.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("archive marker %s: %w", path, err)
	}
	if err := ValidateID(m.ID); err != nil {
		return "", fmt.Errorf("archive marker %s: %w", path, err)
	}
	return m.ID, nil
}

// Check returns an error unless root holds the archive want: the archive
// drive is connected and mounted, and it is the configured one.
func Check(root, want string) error {
	got, err := ID(root)
	if errors.Is(err, ErrNotMarked) {
		return fmt.Errorf("%w: is the archive drive connected and mounted at %s?", err, root)
	}
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s holds homecloud archive %s, not the configured %s", root, got, want)
	}
	return nil
}
