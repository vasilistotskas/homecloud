package platform

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// OpenURL opens url in the user's default browser.
func OpenURL(url string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return fmt.Errorf("encode verb: %w", err)
	}
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return fmt.Errorf("encode url: %w", err)
	}
	if err := windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("open %s: %w", url, err)
	}
	return nil
}
