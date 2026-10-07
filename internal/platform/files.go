package platform

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// sharedFileMode is the mode of files other programs and people read, such as
// the archive's manifest: like a file the user saves, writable only by them.
const sharedFileMode = 0o644

// WriteFile atomically replaces path with data in a file anyone with access
// to the folder can read, so readers never see a partial file.
func WriteFile(path string, data []byte) error {
	// CreateTemp creates the file with mode 0600.
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp for %q: %w", path, err)
	}
	if err := f.Chmod(sharedFileMode); err != nil {
		return errors.Join(fmt.Errorf("chmod %q: %w", f.Name(), err), f.Close(), os.Remove(f.Name()))
	}
	return commit(f, path, data)
}

// WriteFileIfChanged writes data to path with write unless path already holds
// exactly data, and reports whether it wrote.
func WriteFileIfChanged(path string, data []byte, write func(path string, data []byte) error) (bool, error) {
	current, err := os.ReadFile(path) //nolint:gosec // callers pass their own configured files
	switch {
	case err == nil && bytes.Equal(current, data):
		return false, nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return false, fmt.Errorf("read %q: %w", path, err)
	}
	if err := write(path, data); err != nil {
		return false, err
	}
	return true, nil
}

// commit writes data to f, a new temporary file beside path, makes it durable
// and renames it over path. On failure the temporary file is removed.
func commit(f *os.File, path string, data []byte) (err error) {
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
	// Renaming within a volume keeps the file's own mode and, on Windows,
	// its own security descriptor.
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	return nil
}

// FileTimeLayout is the UTC time layout for file and folder names: name order
// is time order, and it has no character any supported file system forbids.
const FileTimeLayout = "20060102T150405Z"

// FileIdentity identifies a file on its system (see FileID): equal for every
// hard link to it.
type FileIdentity struct {
	Volume, Index uint64
}
