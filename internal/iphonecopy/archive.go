// Package iphonecopy keeps verified copies of finished iPhone backups in the
// archive, and carries them there from the computer Apple's tool backs up
// to.
//
// A copy is a snapshot: <UDID>/<stamp>/ under the archive's iPhone folder
// holds the backup folder exactly as Apple's tool wrote it (copy it back into
// MobileSync\Backup\<UDID> to restore it there), and <UDID>/<stamp>.sha256
// lists every file's SHA-256 (Manifest). <stamp> is when the backup finished,
// in UTC. A snapshot is complete once its manifest exists. Files unchanged
// since the newest snapshot are hard links to it, so every further snapshot
// costs only what changed; removing an old snapshot leaves the files newer
// ones share.
//
// Every file operation goes through an os.Root on the iPhone folder (and on
// the source and incoming folders), so nothing outside them is ever read,
// written or removed, whatever links they hold.
package iphonecopy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/vasilistotskas/homecloud/internal/platform"
)

// stampLayout names a snapshot after when its backup finished, in UTC.
const stampLayout = "20060102T150405Z"

// Stamp returns the snapshot name of a backup that finished at t.
func Stamp(t time.Time) string { return t.UTC().Format(stampLayout) }

const (
	manifestExt   = ".sha256"
	partialPrefix = ".partial-"
	// dirMode and fileMode: copies of a backup are the owner's only.
	dirMode  = 0o700
	fileMode = 0o600
)

var (
	stampPattern = regexp.MustCompile(`^\d{8}T\d{6}Z$`)
	// udidPattern matches the device identifiers Apple's tools name backup
	// folders by: 40 hex digits, or 8 + "-" + 16 since 2018.
	udidPattern = regexp.MustCompile(`^[0-9A-Fa-f]{8}(-?[0-9A-Fa-f]{16}|[0-9A-Fa-f]{32})$`)
)

// ValidUDID reports whether udid can name a device folder.
func ValidUDID(udid string) error {
	if !udidPattern.MatchString(udid) {
		return fmt.Errorf("%q is not a device identifier", udid)
	}
	return nil
}

// ErrMismatch means a file did not have the SHA-256 its manifest says.
var ErrMismatch = errors.New("a file does not match its manifest")

// Archive is the copies of iPhone backups under one folder of the archive.
type Archive struct {
	root *os.Root
}

// Open opens the copies folder dir, creating it.
func Open(dir string) (*Archive, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	return &Archive{root: root}, nil
}

// Close releases the folder.
func (a *Archive) Close() error {
	if err := a.root.Close(); err != nil {
		return fmt.Errorf("close %s: %w", a.root.Name(), err)
	}
	return nil
}

// Path returns the absolute path of rel, a path inside the copies folder.
func (a *Archive) Path(rel string) string {
	return a.root.Name() + string(os.PathSeparator) + strings.ReplaceAll(rel, "/", string(os.PathSeparator))
}

// Snapshot is one complete copy.
type Snapshot struct {
	UDID string
	// Stamp is when the backup finished (Stamp); CompletedAt the same time.
	Stamp       string
	CompletedAt time.Time
}

// Dir is the snapshot's folder inside the copies folder.
func (s Snapshot) Dir() string { return s.UDID + "/" + s.Stamp }

func (s Snapshot) manifest() string { return s.Dir() + manifestExt }

// Devices lists the devices that have a folder of copies.
func (a *Archive) Devices() ([]string, error) {
	entries, err := fs.ReadDir(a.root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", a.root.Name(), err)
	}
	var udids []string
	for _, e := range entries {
		if e.IsDir() && ValidUDID(e.Name()) == nil {
			udids = append(udids, e.Name())
		}
	}
	return udids, nil
}

// Snapshots lists a device's complete snapshots, newest first.
func (a *Archive) Snapshots(udid string) ([]Snapshot, error) {
	entries, err := fs.ReadDir(a.root.FS(), udid)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list copies of %s: %w", udid, err)
	}
	var out []Snapshot
	for _, e := range entries {
		stamp, ok := strings.CutSuffix(e.Name(), manifestExt)
		if !ok || e.IsDir() || !stampPattern.MatchString(stamp) {
			continue
		}
		info, err := a.root.Stat(udid + "/" + stamp)
		if err != nil || !info.IsDir() {
			continue // a manifest without its folder is not a snapshot
		}
		at, err := time.Parse(stampLayout, stamp)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s of %s: %w", stamp, udid, err)
		}
		out = append(out, Snapshot{UDID: udid, Stamp: stamp, CompletedAt: at})
	}
	slices.SortFunc(out, func(x, y Snapshot) int { return y.CompletedAt.Compare(x.CompletedAt) })
	return out, nil
}

// Has reports whether the device's backup that finished at t is copied.
func (a *Archive) Has(udid string, t time.Time) (bool, error) {
	snaps, err := a.Snapshots(udid)
	if err != nil {
		return false, err
	}
	stamp := Stamp(t)
	return slices.ContainsFunc(snaps, func(s Snapshot) bool { return s.Stamp == stamp }), nil
}

// Manifest reads a snapshot's manifest.
func (a *Archive) Manifest(s Snapshot) (Manifest, error) {
	f, err := a.root.Open(s.manifest())
	if err != nil {
		return nil, fmt.Errorf("open manifest of %s: %w", s.Dir(), err)
	}
	defer f.Close() //nolint:errcheck // read-only
	m, err := ParseManifest(f)
	if err != nil {
		return nil, fmt.Errorf("manifest of %s: %w", s.Dir(), err)
	}
	return m, nil
}

// Recover finishes or discards what an interrupted copy left of a device's
// snapshots: a snapshot whose folder was renamed into place gets its
// manifest; partial folders and folders without a manifest are removed.
func (a *Archive) Recover(udid string) error {
	entries, err := fs.ReadDir(a.root.FS(), udid)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list copies of %s: %w", udid, err)
	}
	have := map[string]bool{}
	for _, e := range entries {
		have[e.Name()] = true
	}
	for _, e := range entries {
		name, rel := e.Name(), udid+"/"+e.Name()
		switch {
		case strings.HasPrefix(name, partialPrefix) && strings.HasSuffix(name, manifestExt):
			stamp := strings.TrimSuffix(strings.TrimPrefix(name, partialPrefix), manifestExt)
			if have[stamp] && stampPattern.MatchString(stamp) {
				if err := a.root.Rename(rel, udid+"/"+stamp+manifestExt); err != nil {
					return fmt.Errorf("complete snapshot %s of %s: %w", stamp, udid, err)
				}
				have[stamp+manifestExt] = true
				continue
			}
			if err := a.root.Remove(rel); err != nil {
				return fmt.Errorf("remove %s: %w", rel, err)
			}
		case strings.HasPrefix(name, partialPrefix) && e.IsDir():
			if err := a.root.RemoveAll(rel); err != nil {
				return fmt.Errorf("remove %s: %w", rel, err)
			}
		}
	}
	for _, e := range entries {
		if e.IsDir() && stampPattern.MatchString(e.Name()) && !have[e.Name()+manifestExt] &&
			!have[partialPrefix+e.Name()+manifestExt] {
			if err := a.root.RemoveAll(udid + "/" + e.Name()); err != nil {
				return fmt.Errorf("remove incomplete snapshot %s of %s: %w", e.Name(), udid, err)
			}
		}
	}
	return nil
}

// Stats says how a snapshot was made.
type Stats struct {
	// Files and Bytes are the whole backup's.
	Files int
	Bytes int64
	// Copied and CopiedBytes count the files written; Linked those shared
	// with the previous snapshot.
	Copied      int
	CopiedBytes int64
	Linked      int
}

// source opens a backup's files by their path inside the backup.
type source func(rel string) (io.ReadCloser, error)

// add makes the snapshot of the device's backup that finished at t from
// manifest m. A file the base snapshot holds with the same SHA-256 is linked
// to it; every other is read from src and must hash to m's value
// (ErrMismatch otherwise). base may be nil. Before the snapshot is made
// complete, commit is asked whether to keep it.
func (a *Archive) add(ctx context.Context, udid string, t time.Time, m Manifest, base *Snapshot, src source,
	commit func() error,
) (Snapshot, Stats, error) {
	if err := ValidUDID(udid); err != nil {
		return Snapshot{}, Stats{}, err
	}
	snap := Snapshot{UDID: udid, Stamp: Stamp(t), CompletedAt: t.UTC().Truncate(time.Second)}
	partial := udid + "/" + partialPrefix + snap.Stamp
	if err := a.root.RemoveAll(partial); err != nil {
		return Snapshot{}, Stats{}, fmt.Errorf("clear %s: %w", partial, err)
	}
	if err := a.root.MkdirAll(partial, dirMode); err != nil {
		return Snapshot{}, Stats{}, fmt.Errorf("create %s: %w", partial, err)
	}
	var baseManifest Manifest
	if base != nil {
		var err error
		if baseManifest, err = a.Manifest(*base); err != nil {
			return Snapshot{}, Stats{}, err
		}
	}

	var st Stats
	for _, rel := range slices.Sorted(maps.Keys(m)) {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, Stats{}, fmt.Errorf("copy cancelled: %w", err)
		}
		dst := partial + "/" + rel
		if dir := path.Dir(dst); dir != partial {
			if err := a.root.MkdirAll(dir, dirMode); err != nil {
				return Snapshot{}, Stats{}, fmt.Errorf("create %s: %w", dir, err)
			}
		}
		st.Files++
		if d, ok := baseManifest[rel]; ok && d == m[rel] {
			old := base.Dir() + "/" + rel
			if err := a.root.Link(old, dst); err != nil {
				return Snapshot{}, Stats{}, fmt.Errorf("link %s: %w", rel, err)
			}
			info, err := a.root.Stat(dst)
			if err != nil {
				return Snapshot{}, Stats{}, fmt.Errorf("stat %s: %w", dst, err)
			}
			st.Linked++
			st.Bytes += info.Size()
			continue
		}
		n, err := a.copyFile(src, rel, dst, m[rel])
		if err != nil {
			return Snapshot{}, Stats{}, err
		}
		st.Copied++
		st.CopiedBytes += n
		st.Bytes += n
	}
	if err := commit(); err != nil {
		return Snapshot{}, Stats{}, err
	}
	// The manifest is written beside the folder before the folder is renamed
	// into place; Recover completes the rename if this is interrupted.
	pending := udid + "/" + partialPrefix + snap.Stamp + manifestExt
	if err := writeFile(a.root, pending, m.Bytes()); err != nil {
		return Snapshot{}, Stats{}, err
	}
	if err := a.root.Rename(partial, snap.Dir()); err != nil {
		return Snapshot{}, Stats{}, fmt.Errorf("rename %s: %w", partial, err)
	}
	if err := a.root.Rename(pending, snap.manifest()); err != nil {
		return Snapshot{}, Stats{}, fmt.Errorf("rename %s: %w", pending, err)
	}
	return snap, st, nil
}

// copyFile copies rel from src to dst, flushing it to disk, and checks the
// data read against want.
func (a *Archive) copyFile(src source, rel, dst string, want Digest) (int64, error) {
	in, err := src(rel)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", rel, err)
	}
	defer in.Close() //nolint:errcheck // read-only
	out, err := a.root.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", dst, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, fmt.Errorf("copy %s: %w", rel, err)
	}
	if got := Digest(h.Sum(nil)); got != want {
		return 0, fmt.Errorf("%w: %s", ErrMismatch, rel)
	}
	return n, nil
}

// Prune removes a device's oldest snapshots beyond the newest keep, manifest
// first so that an interruption never leaves a snapshot that looks complete.
func (a *Archive) Prune(udid string, keep int) ([]Snapshot, error) {
	snaps, err := a.Snapshots(udid)
	if err != nil || len(snaps) <= keep {
		return nil, err
	}
	removed := snaps[keep:]
	for _, s := range removed {
		if err := a.root.Remove(s.manifest()); err != nil {
			return nil, fmt.Errorf("remove %s: %w", s.manifest(), err)
		}
		if err := a.root.RemoveAll(s.Dir()); err != nil {
			return nil, fmt.Errorf("remove %s: %w", s.Dir(), err)
		}
	}
	return removed, nil
}

// Verify re-hashes every file of the given snapshots of one device (newest
// first) against their manifests and returns, per snapshot stamp, the files
// that no longer match or cannot be read. A file several snapshots share as
// a hard link is read once.
func (a *Archive) Verify(ctx context.Context, snaps []Snapshot) (map[string][]string, error) {
	damaged := map[string][]string{}
	checked := map[platform.FileIdentity]bool{}
	buf := make([]byte, readBuffer)
	for _, s := range snaps {
		m, err := a.Manifest(s)
		if err != nil {
			return nil, err
		}
		bad := []string{}
		for _, rel := range slices.Sorted(maps.Keys(m)) {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("verify cancelled: %w", err)
			}
			ok, err := a.verifyFile(s.Dir()+"/"+rel, m[rel], checked, buf)
			if err != nil || !ok {
				bad = append(bad, rel)
			}
		}
		damaged[s.Stamp] = bad
	}
	return damaged, nil
}

// readBuffer is the chunk files are hashed in.
const readBuffer = 1 << 20

func (a *Archive) verifyFile(rel string, want Digest, checked map[platform.FileIdentity]bool, buf []byte) (bool, error) {
	f, err := a.root.Open(rel)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", rel, err)
	}
	defer f.Close() //nolint:errcheck // read-only
	id, err := platform.FileID(f)
	if err != nil {
		return false, err
	}
	if checked[id] {
		return true, nil
	}
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return false, fmt.Errorf("read %s: %w", rel, err)
	}
	if Digest(h.Sum(nil)) != want {
		return false, nil
	}
	checked[id] = true
	return true, nil
}

// writeFile writes data to name in root and flushes it to disk; name must
// not exist.
func writeFile(root *os.Root, name string, data []byte) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// Size counts a snapshot's files and bytes on disk.
func (a *Archive) Size(s Snapshot) (files int, size int64, _ error) {
	err := fs.WalkDir(a.root.FS(), s.Dir(), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err //nolint:wrapcheck // wrapped below
		}
		files++
		size += info.Size()
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("size of %s: %w", s.Dir(), err)
	}
	return files, size, nil
}
