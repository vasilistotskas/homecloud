package iphonecopy

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Digest is a file's SHA-256.
type Digest [sha256.Size]byte

func (d Digest) String() string { return hex.EncodeToString(d[:]) }

// Manifest maps every file of a backup, by its slash-separated path inside
// the backup folder, to its SHA-256. It is written in sha256sum's binary
// format ("<hex> *<path>", one line per file, sorted by path), so a copy can
// be checked with `sha256sum -c` from inside the snapshot folder.
type Manifest map[string]Digest

// pathPart is one component of a path inside a backup: Apple's tools write
// Info.plist, Manifest.plist, Status.plist, Manifest.db and files named by a
// hex hash in two-hex-digit folders. Anything else is refused, so a manifest
// can never name a path outside its folder; nor a name ending in a dot, which
// NTFS stores but Windows programs cannot open.
var pathPart = regexp.MustCompile(`^[A-Za-z0-9_-]([A-Za-z0-9._-]*[A-Za-z0-9_-])?$`)

// maxDepth bounds the folders a path may go through.
const maxDepth = 4

// validPath reports whether rel is a path homecloud accepts inside a backup.
func validPath(rel string) error {
	parts := strings.Split(rel, "/")
	if len(parts) > maxDepth {
		return fmt.Errorf("path %q is deeper than %d levels", rel, maxDepth)
	}
	for _, p := range parts {
		if !pathPart.MatchString(p) {
			return fmt.Errorf("unexpected path %q in a backup", rel)
		}
	}
	return nil
}

// ParseManifest reads a manifest in sha256sum's format; both the binary
// ("*") and text (" ") marker are accepted.
func ParseManifest(r io.Reader) (Manifest, error) {
	m := Manifest{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		hexDigest, rest, ok := strings.Cut(line, " ")
		if !ok || len(rest) < 2 || (rest[0] != '*' && rest[0] != ' ') {
			return nil, fmt.Errorf("manifest line %d is not \"<sha256> *<path>\"", n)
		}
		var d Digest
		raw, err := hex.DecodeString(hexDigest)
		if err != nil || len(raw) != len(d) {
			return nil, fmt.Errorf("manifest line %d: malformed SHA-256", n)
		}
		copy(d[:], raw)
		rel := rest[1:]
		if pathErr := validPath(rel); pathErr != nil {
			return nil, fmt.Errorf("manifest line %d: %w", n, pathErr)
		}
		if _, dup := m[rel]; dup {
			return nil, fmt.Errorf("manifest line %d: %s is listed twice", n, rel)
		}
		m[rel] = d
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	if len(m) == 0 {
		return nil, errors.New("the manifest lists no files")
	}
	return m, nil
}

// Bytes encodes the manifest in sha256sum's binary format.
func (m Manifest) Bytes() []byte {
	var b bytes.Buffer
	for _, rel := range slices.Sorted(maps.Keys(m)) {
		d := m[rel]
		b.WriteString(d.String())
		b.WriteString(" *")
		b.WriteString(rel)
		b.WriteByte('\n')
	}
	return b.Bytes()
}
