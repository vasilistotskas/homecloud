//go:build windows

// Package instance guarantees that only one homecloud agent runs per user session.
package instance

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// ErrAlreadyRunning is returned when another process holds the lock.
var ErrAlreadyRunning = errors.New("another instance is already running")

// Lock is a held single-instance lock.
type Lock struct {
	h windows.Handle
}

// Acquire takes the named mutex (scoped to the current session via the Local\ namespace).
func Acquire(name string) (*Lock, error) {
	n, err := windows.UTF16PtrFromString(`Local\` + name)
	if err != nil {
		return nil, fmt.Errorf("encode mutex name: %w", err)
	}
	h, err := windows.CreateMutex(nil, false, n)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		// CreateMutexW returns a valid handle to the existing mutex; release it.
		return nil, errors.Join(ErrAlreadyRunning, windows.CloseHandle(h))
	}
	if err != nil {
		return nil, fmt.Errorf("create mutex %s: %w", name, err)
	}
	return &Lock{h: h}, nil
}

// Release frees the lock.
func (l *Lock) Release() error {
	if err := windows.CloseHandle(l.h); err != nil {
		return fmt.Errorf("release instance lock: %w", err)
	}
	return nil
}
