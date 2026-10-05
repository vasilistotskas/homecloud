package platform

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// MarkHidden sets the hidden attribute on path, so Explorer and the file
// dialogs hide it unless "Show hidden items" is on.
func MarkHidden(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode path %q: %w", path, err)
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return fmt.Errorf("attributes of %q: %w", path, err)
	}
	if attrs&windows.FILE_ATTRIBUTE_HIDDEN != 0 {
		return nil
	}
	if err := windows.SetFileAttributes(p, attrs|windows.FILE_ATTRIBUTE_HIDDEN); err != nil {
		return fmt.Errorf("hide %q: %w", path, err)
	}
	return nil
}
