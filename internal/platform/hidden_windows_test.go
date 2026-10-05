package platform

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestMarkHidden(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".Hidden")
	if err := windows.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for range 2 { // idempotent
		if err := MarkHidden(dir); err != nil {
			t.Fatal(err)
		}
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil || attrs&windows.FILE_ATTRIBUTE_HIDDEN == 0 || attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		t.Fatalf("attributes = %#x, %v", attrs, err)
	}
}
