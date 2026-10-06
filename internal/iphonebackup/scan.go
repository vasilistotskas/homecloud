// Package iphonebackup inspects the local iPhone backups that Apple's Devices
// app (or iTunes, or libimobiledevice's idevicebackup2) writes, without the
// backup password and without ever writing to them.
//
// A backup directory holds one folder per device, named by the device's UDID.
// Each holds Info.plist (XML), Manifest.plist and Status.plist (binary), the
// encrypted Manifest.db and the backed-up files. Folders named
// <UDID>-yyyymmdd-hhmmss are archived copies the user made; they are reported
// but not judged.
package iphonebackup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"howett.net/plist"
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
	// InProgress is set while a backup is being written: Apple's tool works in
	// a Snapshot subfolder.
	InProgress bool
	// Files and Bytes count every file in the folder.
	Files, Bytes int64
	// Unreadable lists files that could not be read in full.
	Unreadable []string
	// Err is set when the folder is not a valid backup (missing or malformed
	// plists, or a UDID that does not match the folder name).
	Err error
}

// Archived is a backup the user archived in Apple's tool (a frozen copy).
type Archived struct {
	Dir, UDID string
}

// Plist keys, as written by Apple's backup tools (libimobiledevice
// tools/idevicebackup2.c; The Apple Wiki "ITunes Backup").
type infoPlist struct {
	DeviceName       string `plist:"Device Name"`
	ProductVersion   string `plist:"Product Version"`
	UniqueIdentifier string `plist:"Unique Identifier"`
}

type manifestPlist struct {
	IsEncrypted *bool `plist:"IsEncrypted"`
}

type statusPlist struct {
	SnapshotState string    `plist:"SnapshotState"`
	Date          time.Time `plist:"Date"`
}

// snapshotDir is where a backup in progress is written.
const snapshotDir = "Snapshot"

// Scan inspects every device folder in dir. A missing dir means no backup was
// ever made there, which is not an error. Reading every file in full catches
// disk errors under the backup.
func Scan(ctx context.Context, dir string) (current []Backup, archived []Archived, err error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("list %s: %w", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		folder := filepath.Join(dir, e.Name())
		b := inspect(ctx, folder)
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("scan %s: %w", dir, err)
		}
		switch {
		case b.Err == nil && strings.EqualFold(e.Name(), b.UDID):
			current = append(current, b)
		case b.Err == nil && strings.HasPrefix(strings.ToUpper(e.Name()), strings.ToUpper(b.UDID)+"-"):
			archived = append(archived, Archived{Dir: folder, UDID: b.UDID})
		case b.Err == nil:
			b.Err = fmt.Errorf("folder name %q does not match the device's UDID %s", e.Name(), b.UDID)
			current = append(current, b)
		default:
			current = append(current, b)
		}
	}
	return current, archived, nil
}

// inspect reads one backup folder. Problems are reported in Backup.Err or
// Backup.Unreadable rather than returned, so one bad folder never hides the
// others.
func inspect(ctx context.Context, dir string) Backup {
	b := Backup{Dir: dir}
	var info infoPlist
	if err := readPlist(filepath.Join(dir, "Info.plist"), &info); err != nil {
		b.Err = err
		return b
	}
	if info.UniqueIdentifier == "" {
		b.Err = fmt.Errorf("%s: Info.plist has no Unique Identifier", dir)
		return b
	}
	b.UDID, b.DeviceName, b.ProductVersion = info.UniqueIdentifier, info.DeviceName, info.ProductVersion

	if _, err := os.Stat(filepath.Join(dir, snapshotDir)); err == nil {
		b.InProgress = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		b.Err = fmt.Errorf("check %s: %w", snapshotDir, err)
		return b
	}

	// A device's first backup writes Manifest.plist and Status.plist only as
	// it goes, so while one is in progress their absence is expected.
	var manifest manifestPlist
	switch err := readPlist(filepath.Join(dir, "Manifest.plist"), &manifest); {
	case errors.Is(err, fs.ErrNotExist) && b.InProgress:
	case err != nil:
		b.Err = err
		return b
	case manifest.IsEncrypted == nil:
		b.Err = fmt.Errorf("%s: Manifest.plist has no IsEncrypted", dir)
		return b
	default:
		b.Encrypted = *manifest.IsEncrypted
	}

	var status statusPlist
	switch err := readPlist(filepath.Join(dir, "Status.plist"), &status); {
	case errors.Is(err, fs.ErrNotExist) && b.InProgress:
	case err != nil:
		b.Err = err
		return b
	default:
		b.SnapshotState, b.CompletedAt = status.SnapshotState, status.Date
	}

	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("scan cancelled: %w", err)
		}
		if walkErr != nil {
			b.Unreadable = append(b.Unreadable, path)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		n, err := readAll(path)
		b.Files++
		b.Bytes += n
		if err != nil {
			b.Unreadable = append(b.Unreadable, path)
		}
		return nil
	}); err != nil {
		b.Err = fmt.Errorf("walk %s: %w", dir, err)
	}
	return b
}

// readPlist decodes the XML or binary plist at path into v.
func readPlist(path string, v any) error {
	data, err := os.ReadFile(path) //nolint:gosec // a file of a configured backup folder
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if _, err := plist.Unmarshal(data, v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// readAll reads the file at path to its end and returns how many bytes it holds.
func readAll(path string) (int64, error) {
	f, err := os.Open(path) //nolint:gosec // a file of a configured backup folder
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle
	n, err := io.Copy(io.Discard, f)
	if err != nil {
		return n, fmt.Errorf("read %s: %w", path, err)
	}
	return n, nil
}
