// Package iphonebackup inspects the local iPhone backups that Apple's Devices
// app (or iTunes, or libimobiledevice's idevicebackup2) writes, without the
// backup password and without ever writing to them.
//
// A backup directory holds one folder per device, named by the device's UDID.
// Each holds Info.plist (XML), Manifest.plist and Status.plist (binary), the
// encrypted Manifest.db and the backed-up files; while a backup is written,
// Apple's tool works in a Snapshot subfolder. Folders named
// <UDID>-yyyymmdd-hhmmss are copies the user archived; they are counted, not
// read.
package iphonebackup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Backup is what a device's current backup folder says about itself.
type Backup struct {
	// Dir is the backup folder.
	Dir string
	// UDID is the device identifier (Info.plist "Unique Identifier").
	UDID string
	// DeviceName and ProductVersion (iOS) come from Info.plist.
	DeviceName, ProductVersion string
	// Encrypted is Manifest.plist "IsEncrypted".
	Encrypted bool
	// SnapshotState is Status.plist "SnapshotState": "finished" once a backup
	// completed.
	SnapshotState string
	// CompletedAt is Status.plist "Date": when the backup last finished.
	CompletedAt time.Time
	// InProgress is set while a backup is being written (a Snapshot
	// subfolder exists); SnapshotActivity is the newest write in it.
	InProgress       bool
	SnapshotActivity time.Time
	// Files and Bytes count the folder's files, from their metadata.
	Files, Bytes int64
	// Unlisted names files or folders whose metadata could not be read.
	Unlisted []string
	// Err is set when the folder is not a valid backup (missing or malformed
	// plists, or a UDID that does not match the folder name). It is never set
	// while a backup is in progress: the files are then still being written.
	Err error
}

// archivedName matches a copy the user archived in Apple's tool.
var archivedName = regexp.MustCompile(`^.+-\d{8}-\d{6}$`)

// What homecloud reads from each plist, under the keys Apple's backup tools
// write (libimobiledevice tools/idevicebackup2.c; The Apple Wiki "ITunes
// Backup"). An absent key reads as the zero value; a key of another type is
// an error.
type infoPlist struct {
	DeviceName, ProductVersion, UniqueIdentifier string
}

type manifestPlist struct {
	// IsEncrypted is nil when the key is absent.
	IsEncrypted *bool
}

type statusPlist struct {
	SnapshotState string
	Date          time.Time
}

func readInfo(path string) (infoPlist, error) {
	var info infoPlist
	err := readKeys(path, func(d dict) error {
		return errors.Join(
			d.str("Device Name", &info.DeviceName),
			d.str("Product Version", &info.ProductVersion),
			d.str("Unique Identifier", &info.UniqueIdentifier),
		)
	})
	return info, err
}

func readManifest(path string) (manifestPlist, error) {
	var manifest manifestPlist
	err := readKeys(path, func(d dict) error {
		return d.boolean("IsEncrypted", &manifest.IsEncrypted)
	})
	return manifest, err
}

func readStatus(path string) (statusPlist, error) {
	var status statusPlist
	err := readKeys(path, func(d dict) error {
		return errors.Join(
			d.str("SnapshotState", &status.SnapshotState),
			d.date("Date", &status.Date),
		)
	})
	return status, err
}

// readKeys reads the plist at path and its keys with read.
func readKeys(path string, read func(dict) error) error {
	d, err := readPlist(path)
	if err != nil {
		return err
	}
	if err := read(d); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// snapshotDir is where a backup in progress is written.
const snapshotDir = "Snapshot"

// Scan inspects every device folder in dir and counts the archived copies. A
// missing dir means no backup was ever made there, which is not an error.
// Only metadata is read here; ReadAll reads a folder's bytes.
func Scan(ctx context.Context, dir string) (current []Backup, archived int, _ error) {
	// Only a missing directory means "no backup yet": on Windows listing a
	// file also reports not-found, so the path is checked first.
	switch info, err := os.Stat(dir); {
	case errors.Is(err, fs.ErrNotExist):
		return nil, 0, nil
	case err != nil:
		return nil, 0, fmt.Errorf("check %s: %w", dir, err)
	case !info.IsDir():
		return nil, 0, fmt.Errorf("%s is not a folder", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, fmt.Errorf("list %s: %w", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if archivedName.MatchString(e.Name()) {
			archived++
			continue
		}
		b := inspect(ctx, filepath.Join(dir, e.Name()))
		if err := ctx.Err(); err != nil {
			return nil, 0, fmt.Errorf("scan %s: %w", dir, err)
		}
		current = append(current, b)
	}
	return current, archived, nil
}

// inspect reads one backup folder's plists and file sizes. Problems are
// reported in the Backup rather than returned, so one bad folder never hides
// the others.
func inspect(ctx context.Context, dir string) Backup {
	b := Backup{Dir: dir}
	activity, inProgress, err := snapshotActivity(ctx, filepath.Join(dir, snapshotDir))
	if err != nil {
		b.Err = err
		return b
	}
	b.InProgress, b.SnapshotActivity = inProgress, activity

	// While a backup is written its plists may be missing or half written:
	// read what is there and judge nothing else.
	info, err := readInfo(filepath.Join(dir, "Info.plist"))
	if err != nil {
		if !b.InProgress {
			b.Err = err
		}
		return b
	}
	b.UDID, b.DeviceName, b.ProductVersion = info.UniqueIdentifier, info.DeviceName, info.ProductVersion
	if b.InProgress {
		return b
	}
	switch {
	case info.UniqueIdentifier == "":
		b.Err = fmt.Errorf("%s: Info.plist has no Unique Identifier", dir)
		return b
	case !strings.EqualFold(filepath.Base(dir), info.UniqueIdentifier):
		b.Err = fmt.Errorf("folder name %q does not match the device's UDID %s", filepath.Base(dir), info.UniqueIdentifier)
		return b
	}

	manifest, err := readManifest(filepath.Join(dir, "Manifest.plist"))
	if err != nil {
		b.Err = err
		return b
	}
	if manifest.IsEncrypted == nil {
		b.Err = fmt.Errorf("%s: Manifest.plist has no IsEncrypted", dir)
		return b
	}
	b.Encrypted = *manifest.IsEncrypted

	status, err := readStatus(filepath.Join(dir, "Status.plist"))
	if err != nil {
		b.Err = err
		return b
	}
	b.SnapshotState, b.CompletedAt = status.SnapshotState, status.Date

	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("scan cancelled: %w", err)
		}
		if walkErr != nil {
			b.Unlisted = append(b.Unlisted, path)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			b.Unlisted = append(b.Unlisted, path)
			return nil //nolint:nilerr // recorded in Unlisted
		}
		b.Files++
		b.Bytes += info.Size()
		return nil
	}); err != nil {
		b.Err = fmt.Errorf("walk %s: %w", dir, err)
	}
	return b
}

// snapshotActivity reports whether dir (a backup's Snapshot folder) exists
// and the newest modification time in it.
func snapshotActivity(ctx context.Context, dir string) (time.Time, bool, error) {
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("check %s: %w", dir, err)
	}
	newest := info.ModTime()
	// Files may vanish while the backup runs; what remains is enough.
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, walkErr error) error { //nolint:errcheck // best view of a folder being written
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // stops the walk; checked by the caller
		}
		if walkErr != nil {
			return nil //nolint:nilerr // entries vanish while the backup is written
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		return nil
	})
	return newest, true, nil
}

// readBuffer is the chunk ReadAll reads files in; between chunks it checks for
// cancellation.
const readBuffer = 1 << 20

// ReadAll reads every file of a backup folder to its end and returns those
// that could not be read in full: the check that the disk still delivers the
// backup. It stops when ctx is cancelled.
func ReadAll(ctx context.Context, dir string) (unreadable []string, _ error) {
	buf := make([]byte, readBuffer)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("read cancelled: %w", ctxErr)
		}
		if walkErr != nil {
			unreadable = append(unreadable, path)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if readErr := readFile(ctx, path, buf); readErr != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("read cancelled: %w", ctx.Err())
			}
			unreadable = append(unreadable, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	return unreadable, nil
}

func readFile(ctx context.Context, path string, buf []byte) error {
	f, err := os.Open(path) //nolint:gosec // a file of a configured backup folder
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		_, err := f.Read(buf)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
	}
}
