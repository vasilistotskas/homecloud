package platform

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// FileLock is a held exclusive lock on a lock file.
type FileLock struct {
	f *os.File
}

// LockFile waits for an exclusive lock on path (created if missing), shared
// with every process that locks the same path. The file is never deleted,
// which would race with a concurrent locker.
func LockFile(path string) (*FileLock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // a lock file in the per-user state dir
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	// Without LOCKFILE_FAIL_IMMEDIATELY the call waits for the lock.
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &windows.Overlapped{}); err != nil {
		return nil, errors.Join(fmt.Errorf("lock %s: %w", path, err), f.Close())
	}
	return &FileLock{f: f}, nil
}

// Unlock releases the lock.
func (l *FileLock) Unlock() error {
	unlockErr := windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, &windows.Overlapped{})
	if err := errors.Join(unlockErr, l.f.Close()); err != nil {
		return fmt.Errorf("unlock %s: %w", l.f.Name(), err)
	}
	return nil
}
