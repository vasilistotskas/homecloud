package platform

import (
	"fmt"
	"os"
	"syscall"
)

// FileID returns what identifies an open file on its system: hard links to
// one file share it.
func FileID(f *os.File) (FileIdentity, error) {
	info, err := f.Stat()
	if err != nil {
		return FileIdentity{}, fmt.Errorf("stat %s: %w", f.Name(), err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return FileIdentity{}, fmt.Errorf("stat %s: no device and inode", f.Name())
	}
	return FileIdentity{Volume: st.Dev, Index: st.Ino}, nil
}
