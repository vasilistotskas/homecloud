package iphonecopy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/vasilistotskas/homecloud/internal/iphonebackup"
)

// ErrChanged means the backup changed while it was copied: a new backup
// started or finished meanwhile. The copy is dropped and made again later.
var ErrChanged = errors.New("the backup changed while it was copied")

// snapshotDir is the folder Apple's tool writes a backup in progress to; it
// is never part of a copy.
const snapshotDir = "Snapshot"

// HashTree hashes every file of the backup folder root (Apple's work folder
// Snapshot aside). Anything but regular files in the layout Apple's tools
// write is refused.
func HashTree(ctx context.Context, root *os.Root) (Manifest, error) {
	m := Manifest{}
	buf := make([]byte, readBuffer)
	err := fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("list %s: %w", rel, walkErr)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("hashing cancelled: %w", ctxErr)
		}
		switch {
		case rel == ".":
			return nil
		case d.IsDir():
			if rel == snapshotDir {
				return fs.SkipDir
			}
			return validPath(rel)
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is not a regular file", rel)
		}
		if pathErr := validPath(rel); pathErr != nil {
			return pathErr
		}
		sum, hashErr := hashFile(root, rel, buf)
		if hashErr != nil {
			return hashErr
		}
		m[rel] = sum
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("hash %s: %w", root.Name(), err)
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("%s holds no files", root.Name())
	}
	return m, nil
}

func hashFile(root *os.Root, rel string, buf []byte) (Digest, error) {
	f, err := root.Open(rel)
	if err != nil {
		return Digest{}, fmt.Errorf("open %s: %w", rel, err)
	}
	defer f.Close() //nolint:errcheck // read-only
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return Digest{}, fmt.Errorf("read %s: %w", rel, err)
	}
	return Digest(h.Sum(nil)), nil
}

// CopyLocal copies the finished backup b (a device folder Apple's tool
// wrote, on this computer) into the archive as a snapshot, based on base
// (nil for a full copy). It hashes the folder, links what base already holds,
// copies the rest checking each file against its hash, and keeps the
// snapshot only if the folder still holds the same finished backup
// afterwards (ErrChanged otherwise).
func (a *Archive) CopyLocal(ctx context.Context, b iphonebackup.Backup, base *Snapshot) (Snapshot, Stats, error) {
	if b.InProgress || b.Err != nil || b.CompletedAt.IsZero() {
		return Snapshot{}, Stats{}, fmt.Errorf("%s holds no finished backup to copy", b.Dir)
	}
	src, err := os.OpenRoot(b.Dir)
	if err != nil {
		return Snapshot{}, Stats{}, fmt.Errorf("open %s: %w", b.Dir, err)
	}
	defer src.Close() //nolint:errcheck // read-only
	m, err := HashTree(ctx, src)
	if err != nil {
		return Snapshot{}, Stats{}, err
	}
	open := func(rel string) (io.ReadCloser, error) { return src.Open(rel) }
	snap, st, err := a.add(ctx, b.UDID, b.CompletedAt, m, base, open, func() error {
		return unchanged(ctx, b)
	})
	if errors.Is(err, ErrMismatch) {
		// A file changed between hashing and copying: a backup ran meanwhile.
		return Snapshot{}, Stats{}, fmt.Errorf("%w: %w", ErrChanged, err)
	}
	return snap, st, err
}

// unchanged checks that b's folder still holds the same finished backup.
func unchanged(ctx context.Context, b iphonebackup.Backup) error {
	now := iphonebackup.Inspect(ctx, b.Dir, b.UDID)
	if now.Err != nil || now.InProgress || !now.CompletedAt.Equal(b.CompletedAt) {
		return ErrChanged
	}
	return nil
}

// ParseStamp returns when the backup of a snapshot stamp finished.
func ParseStamp(stamp string) (time.Time, error) {
	t, err := time.Parse(stampLayout, stamp)
	if err != nil {
		return time.Time{}, fmt.Errorf("snapshot name %q: %w", stamp, err)
	}
	return t, nil
}
