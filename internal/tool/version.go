// Package tool verifies pinned external executables.
package tool

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"time"
)

// Pin describes an external executable whose exact version is required.
type Pin struct {
	// Name is a human-readable label used in errors.
	Name string
	// Binary is the absolute path to the executable.
	Binary string
	// Args prints the version (for example "--version").
	Args []string
	// Pattern extracts the version from the output; its first submatch is the version.
	Pattern *regexp.Regexp
	// Want is the exact required version.
	Want string
}

const versionTimeout = 15 * time.Second

// Version runs the binary and returns the version it reports.
func (p Pin) Version(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, p.Binary, p.Args...).CombinedOutput() //nolint:gosec // binary path comes from validated config
	if err != nil {
		return "", fmt.Errorf("%s: run %s: %w", p.Name, p.Binary, err)
	}
	m := p.Pattern.FindSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("%s: unrecognised version output %q", p.Name, firstLine(out))
	}
	return string(m[1]), nil
}

// Check returns an error unless the binary reports exactly the pinned version.
func (p Pin) Check(ctx context.Context) error {
	got, err := p.Version(ctx)
	if err != nil {
		return err
	}
	if got != p.Want {
		return fmt.Errorf("%s: version %s found, %s required", p.Name, got, p.Want)
	}
	return nil
}

func firstLine(b []byte) string {
	for i, c := range b {
		if c == '\n' || c == '\r' {
			return string(b[:i])
		}
	}
	return string(b)
}
