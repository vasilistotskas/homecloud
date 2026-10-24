package viewer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vasilistotskas/homecloud/internal/platform"
)

// KeyFile is the API key's file name in the per-user state directory
// (platform.StateDir).
const KeyFile = "immich-api.key"

// KeyPath is where the agent's computer keeps the viewer's API key: the
// per-user state directory, never the archive or the configuration file.
func KeyPath() (string, error) {
	dir, err := platform.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, KeyFile), nil
}

// Key is an Immich API key, a secret: kept owner-only and never logged.
type Key string

// ErrNoKey means the viewer's API key has not been saved on this computer.
var ErrNoKey = errors.New("no viewer API key on this computer")

// keyPattern is an Immich API key: 32 random bytes in base64 with its
// non-word characters removed (Immich v3.3.5, CryptoRepository.randomBytesAsText).
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,43}$`)

// ParseKey reads a key as Immich shows it once, when it is created.
func ParseKey(s string) (Key, error) {
	s = strings.TrimSpace(s)
	if !keyPattern.MatchString(s) {
		return "", errors.New("not an Immich API key (up to 43 letters and digits, as Immich shows it once when it is created)")
	}
	return Key(s), nil
}

// LoadKey reads the key at path; a missing file is ErrNoKey.
func LoadKey(path string) (Key, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the per-user state directory
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoKey
	}
	if err != nil {
		return "", fmt.Errorf("read viewer API key: %w", err)
	}
	k, err := ParseKey(string(data))
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return k, nil
}

// SaveKey writes k to path, readable by the current user only, creating its
// folder.
func SaveKey(path string, k Key) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := platform.WriteSecretFile(path, []byte(string(k)+"\n")); err != nil {
		return fmt.Errorf("save viewer API key: %w", err)
	}
	return nil
}
