package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

func TestIsDiskFull(t *testing.T) {
	if IsDiskFull(fmt.Errorf("copy: %w", fs.ErrPermission)) || IsDiskFull(errors.New("disk full")) {
		t.Fatal("an unrelated error counts as a full disk")
	}
	if !IsDiskFull(fmt.Errorf("copy: %w", &fs.PathError{Op: "write", Path: "x", Err: errDiskFull})) {
		t.Fatal("the operating system's disk-full error does not count")
	}
}
