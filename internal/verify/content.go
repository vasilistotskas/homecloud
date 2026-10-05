package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrMismatch marks a file whose content or structure disagrees with expectations.
// Mismatches quarantine an asset; other errors (I/O, timeouts) are retried.
var ErrMismatch = errors.New("verification mismatch")

// Content checks that the file at path has exactly wantSize bytes and the
// lowercase hex SHA-256 wantSHA256 (tier 1).
func Content(path string, wantSize uint64, wantSHA256 string) error {
	f, err := os.Open(path) //nolint:gosec // path comes from the ledger
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle; close errors cannot affect the result

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if size := info.Size(); size < 0 || uint64(size) != wantSize {
		return fmt.Errorf("%w: %s is %d bytes, expected %d", ErrMismatch, path, size, wantSize)
	}

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash %s: %w", path, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA256 {
		return fmt.Errorf("%w: %s has SHA-256 %s, expected %s", ErrMismatch, path, got, wantSHA256)
	}
	return nil
}
