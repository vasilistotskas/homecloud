package iphonecopy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vasilistotskas/homecloud/internal/platform"
)

// PushKeyFile is the push key's file name in each computer's per-user state
// directory (platform.StateDir): the archive's, and the one that pushes.
const PushKeyFile = "iphone-push.key"

// PushKeyPath is where this computer keeps its push key: the per-user state
// directory, never the archive or the incoming folder.
func PushKeyPath() (string, error) {
	dir, err := platform.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, PushKeyFile), nil
}

// PushKey authenticates pushed backups: the computer that pushes signs each
// push with it and the archive takes in only pushes signed with its own. The
// incoming folder is shared, so anything that can write to it could
// otherwise push a backup of its making. It is a secret, kept owner-only on
// both computers and never logged.
type PushKey [32]byte

// ErrNoPushKey means this computer has no push key yet.
var ErrNoPushKey = errors.New("no iPhone push key on this computer")

// NewPushKey returns a random key.
func NewPushKey() (PushKey, error) {
	var k PushKey
	if _, err := rand.Read(k[:]); err != nil {
		return PushKey{}, fmt.Errorf("generate push key: %w", err)
	}
	return k, nil
}

// String is the key as the owner copies it from one computer to the other.
func (k PushKey) String() string { return base64.RawURLEncoding.EncodeToString(k[:]) }

// ParsePushKey reads a key as String writes it.
func ParsePushKey(s string) (PushKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	var k PushKey
	if err != nil || len(raw) != len(k) {
		return PushKey{}, errors.New("not an iPhone push key (43 characters, as homecloud iphone push-key prints it)")
	}
	copy(k[:], raw)
	return k, nil
}

// LoadPushKey reads the key at path; a missing file is ErrNoPushKey.
func LoadPushKey(path string) (PushKey, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the per-user state directory
	if errors.Is(err, os.ErrNotExist) {
		return PushKey{}, ErrNoPushKey
	}
	if err != nil {
		return PushKey{}, fmt.Errorf("read push key: %w", err)
	}
	k, err := ParsePushKey(string(data))
	if err != nil {
		return PushKey{}, fmt.Errorf("%s: %w", path, err)
	}
	return k, nil
}

// SavePushKey writes k to path, readable by the current user only, creating
// its folder (on the computer that pushes, nothing else may have yet).
func SavePushKey(path string, k PushKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := platform.WriteSecretFile(path, []byte(k.String()+"\n")); err != nil {
		return fmt.Errorf("save push key: %w", err)
	}
	return nil
}

// signatureExt is the file beside a push's manifest that holds its signature.
const signatureExt = ".sig"

// sign is the signature of a push: HMAC-SHA256 over the device, the backup's
// time and the manifest, which names every file with its SHA-256.
func (k PushKey) sign(udid, stamp string, manifest []byte) []byte {
	mac := hmac.New(sha256.New, k[:])
	_, _ = fmt.Fprintf(mac, "homecloud iphone push v1\n%s\n%s\n", udid, stamp)
	_, _ = mac.Write(manifest)
	return mac.Sum(nil)
}

// signature is how sign's result is stored.
func (k PushKey) signature(udid, stamp string, manifest []byte) []byte {
	return []byte(hex.EncodeToString(k.sign(udid, stamp, manifest)) + "\n")
}

// verify reports whether stored is the signature of a push.
func (k PushKey) verify(udid, stamp string, manifest, stored []byte) bool {
	got, err := hex.DecodeString(strings.TrimSpace(string(stored)))
	return err == nil && hmac.Equal(got, k.sign(udid, stamp, manifest))
}
