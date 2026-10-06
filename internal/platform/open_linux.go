package platform

import (
	"context"
	"fmt"
)

// OpenURL opens url in the user's default browser through xdg-open
// (freedesktop.org xdg-utils).
func OpenURL(url string) error {
	// The URL comes from the agent's own discovery file and is passed as one argument, never through a shell.
	if err := Command(context.Background(), "xdg-open", url).Run(); err != nil {
		return fmt.Errorf("xdg-open %s: %w", url, err)
	}
	return nil
}
