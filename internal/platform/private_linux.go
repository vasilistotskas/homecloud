package platform

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// privateMode lets only the owner list, enter and change the folder; whatever
// lies inside is out of reach of everyone else, whatever its own mode.
const privateMode os.FileMode = 0o700

// CheckPrivate reports, as an error, a dir that the current user does not own
// or that others can list or enter.
func CheckPrivate(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("read the permissions of %s: %w", dir, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("read the owner of %s: no stat data", dir)
	}
	if uid := os.Geteuid(); int(st.Uid) != uid {
		return fmt.Errorf("%s belongs to uid %d, not to uid %d that homecloud runs as", dir, st.Uid, uid)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("other accounts can access %s (mode %04o)", dir, perm)
	}
	return nil
}

// MakePrivate sets dir to mode 0700 when CheckPrivate finds others can reach
// it, and reports whether it changed anything. A folder owned by another
// account cannot be changed here: its owner must hand it over.
func MakePrivate(dir string) (bool, error) {
	err := CheckPrivate(dir)
	if err == nil {
		return false, nil
	}
	info, statErr := os.Stat(dir)
	if statErr != nil {
		return false, err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		return false, err
	}
	if err := os.Chmod(dir, privateMode); err != nil {
		return false, fmt.Errorf("make %s private: %w", dir, err)
	}
	if err := CheckPrivate(dir); err != nil {
		return true, errors.Join(errors.New("permissions were set but are still not private"), err)
	}
	return true, nil
}
