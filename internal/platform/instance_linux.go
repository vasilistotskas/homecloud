package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Lock is a held single-instance lock.
type Lock struct {
	f *os.File
}

// AcquireInstance takes an exclusive flock on $XDG_RUNTIME_DIR/homecloud/<name>.lock.
// The kernel releases it when the process exits; the file is never deleted, which
// would race with a concurrent acquirer.
func AcquireInstance(name string) (*Lock, error) {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" || !filepath.IsAbs(runtimeDir) {
		return nil, errors.New("XDG_RUNTIME_DIR is not set to an absolute path (start homecloud from a systemd user session)")
	}
	dir := filepath.Join(runtimeDir, "homecloud")
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // dir is under the user's own XDG_RUNTIME_DIR
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // lock file under the user's own XDG_RUNTIME_DIR
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil { //nolint:gosec // fd fits in int
		closeErr := f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.Join(ErrAlreadyRunning, closeErr)
		}
		return nil, errors.Join(fmt.Errorf("flock %s: %w", f.Name(), err), closeErr)
	}
	return &Lock{f: f}, nil
}

// Release frees the lock.
func (l *Lock) Release() error {
	unlockErr := unix.Flock(int(l.f.Fd()), unix.LOCK_UN) //nolint:gosec // fd fits in int
	if err := errors.Join(unlockErr, l.f.Close()); err != nil {
		return fmt.Errorf("release instance lock: %w", err)
	}
	return nil
}
