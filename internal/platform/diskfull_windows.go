package platform

import (
	"errors"

	"golang.org/x/sys/windows"
)

// IsDiskFull reports whether err means the volume had no room left.
func IsDiskFull(err error) bool {
	return errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL)
}
