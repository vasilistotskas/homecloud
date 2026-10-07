package platform

import (
	"errors"
	"syscall"
)

// IsDiskFull reports whether err means the volume (or the user's quota) had
// no room left.
func IsDiskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}
