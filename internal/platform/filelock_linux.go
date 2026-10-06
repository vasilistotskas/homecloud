package platform

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// FileLock is a held exclusive lock on a lock file.
type FileLock struct {
	f *os.File
}

// LockFile waits for an exclusive flock on path (created if missing), shared
// with every process that locks the same path. The file is never deleted,
// which would race with a concurrent locker.
func LockFile(path string) (*FileLock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // a lock file in the per-user state dir
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil { //nolint:gosec // fd fits in int
		return nil, errors.Join(fmt.Errorf("flock %s: %w", path, err), f.Close())
	}
	return &FileLock{f: f}, nil
}

// Unlock releases the lock.
func (l *FileLock) Unlock() error {
	unlockErr := unix.Flock(int(l.f.Fd()), unix.LOCK_UN) //nolint:gosec // fd fits in int
	if err := errors.Join(unlockErr, l.f.Close()); err != nil {
		return fmt.Errorf("unlock %s: %w", l.f.Name(), err)
	}
	return nil
}
