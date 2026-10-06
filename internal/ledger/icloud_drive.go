package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DriveLibrary is the events' library for iCloud Drive files; their asset id
// is the file's drivewsid.
const DriveLibrary = "icloud_drive"

// iCloud Drive event kinds.
const (
	// EventDriveFileArchived: a file was archived for the first time.
	EventDriveFileArchived = "drive_file_archived"
	// EventDriveFileChanged: a file's bytes changed in iCloud; the previous
	// copy was kept (detail "kept_as").
	EventDriveFileChanged = "drive_file_changed"
	// EventDriveFileMoved: a file was moved or renamed in iCloud; the archived
	// copy moved with it.
	EventDriveFileMoved = "drive_file_moved"
	// EventDriveFileDeleted: a file is gone from iCloud; its copy is kept.
	EventDriveFileDeleted = "drive_file_deleted"
	// EventDriveFileRestored: a file deleted in iCloud reappeared.
	EventDriveFileRestored = "drive_file_restored"
	// EventDriveFileDamaged: an archived copy no longer matched its checksum;
	// the next sync downloads it again.
	EventDriveFileDamaged = "drive_file_damaged"
)

// DriveFile is one archived iCloud Drive file.
type DriveFile struct {
	DriveWSID string
	// ICloudPath is the file's '/'-separated path in iCloud, as last seen.
	ICloudPath string
	// LocalPath is the archived copy, relative to the archive's Drive folder.
	LocalPath  string
	Etag       string
	Size       int64
	ModifiedAt *time.Time
	Package    bool
	// AppleSignature is Apple's content signature (hex) of a plain file.
	AppleSignature string
	SHA256         string
	ArchivedAt     time.Time
	LastScrubAt    *time.Time
	DamagedAt      *time.Time
	DeletedAt      *time.Time
}

const driveFileColumns = `drivewsid, icloud_path, local_path, etag, size_bytes, modified_at, package,
	coalesce(apple_signature, ''), sha256, archived_at, last_scrub_at, damaged_at, deleted_in_icloud_at`

func scanDriveFile(row interface{ Scan(...any) error }) (DriveFile, error) {
	var f DriveFile
	var modified, scrubbed, damaged, deleted sql.NullString
	var archived string
	if err := row.Scan(&f.DriveWSID, &f.ICloudPath, &f.LocalPath, &f.Etag, &f.Size, &modified, &f.Package,
		&f.AppleSignature, &f.SHA256, &archived, &scrubbed, &damaged, &deleted); err != nil {
		return DriveFile{}, fmt.Errorf("scan drive file: %w", err)
	}
	var err error
	if f.ArchivedAt, err = parseTime(archived); err != nil {
		return DriveFile{}, err
	}
	for _, t := range []struct {
		dst **time.Time
		src sql.NullString
	}{{&f.ModifiedAt, modified}, {&f.LastScrubAt, scrubbed}, {&f.DamagedAt, damaged}, {&f.DeletedAt, deleted}} {
		if *t.dst, err = parseNullTime(t.src); err != nil {
			return DriveFile{}, err
		}
	}
	return f, nil
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

// DriveFiles returns every archived iCloud Drive file by drivewsid.
func (db *DB) DriveFiles(ctx context.Context) (_ map[string]DriveFile, err error) {
	rows, err := db.r.QueryContext(ctx, `SELECT `+driveFileColumns+` FROM icloud_drive_files`)
	if err != nil {
		return nil, fmt.Errorf("query drive files: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out := map[string]DriveFile{}
	for rows.Next() {
		f, scanErr := scanDriveFile(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out[f.DriveWSID] = f
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate drive files: %w", err)
	}
	return out, nil
}

// RecordDriveFile records a newly downloaded copy of f, as first archived or,
// when keptAs is set, replacing a previous copy that was moved to keptAs.
// A file seen again after its deletion in iCloud is restored.
func (db *DB) RecordDriveFile(ctx context.Context, f DriveFile, keptAs string, now time.Time) (err error) {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record drive file: begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	var existed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM icloud_drive_files WHERE drivewsid = ?)`, f.DriveWSID).Scan(&existed); err != nil {
		return fmt.Errorf("record drive file: %w", err)
	}
	ts := formatTime(now)
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO icloud_drive_files (drivewsid, icloud_path, local_path, etag, size_bytes, modified_at, package,
			apple_signature, sha256, archived_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, nullif(?, ''), ?, ?)
		ON CONFLICT (drivewsid) DO UPDATE SET
			icloud_path = excluded.icloud_path, local_path = excluded.local_path, etag = excluded.etag,
			size_bytes = excluded.size_bytes, modified_at = excluded.modified_at, package = excluded.package,
			apple_signature = excluded.apple_signature, sha256 = excluded.sha256, archived_at = excluded.archived_at,
			last_scrub_at = NULL, damaged_at = NULL, deleted_in_icloud_at = NULL`,
		f.DriveWSID, f.ICloudPath, f.LocalPath, f.Etag, f.Size, nullTime(f.ModifiedAt), f.Package,
		f.AppleSignature, f.SHA256, ts); err != nil {
		return fmt.Errorf("record drive file %s: %w", f.DriveWSID, err)
	}
	kind, detail := EventDriveFileArchived, map[string]any{"path": f.ICloudPath, "size": f.Size, "sha256": f.SHA256}
	if existed {
		kind = EventDriveFileChanged
		if keptAs != "" {
			detail["kept_as"] = keptAs
		}
	}
	if err = appendEvent(ctx, tx, ts, DriveLibrary, f.DriveWSID, kind, detail); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("record drive file: commit: %w", err)
	}
	return nil
}

// UpdateDriveFile records iCloud metadata that changed without the file's
// bytes changing: a new etag or modification time, or a move (when icloudPath
// or localPath differ, an event records it). A deleted file seen again is
// restored.
func (db *DB) UpdateDriveFile(ctx context.Context, f DriveFile, now time.Time) (err error) {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("update drive file: begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	prev, err := scanDriveFile(tx.QueryRowContext(ctx, `SELECT `+driveFileColumns+` FROM icloud_drive_files WHERE drivewsid = ?`, f.DriveWSID))
	if err != nil {
		return fmt.Errorf("update drive file %s: %w", f.DriveWSID, err)
	}
	ts := formatTime(now)
	if _, err = tx.ExecContext(ctx, `UPDATE icloud_drive_files SET icloud_path = ?, local_path = ?, etag = ?, modified_at = ?,
		deleted_in_icloud_at = NULL WHERE drivewsid = ?`,
		f.ICloudPath, f.LocalPath, f.Etag, nullTime(f.ModifiedAt), f.DriveWSID); err != nil {
		return fmt.Errorf("update drive file %s: %w", f.DriveWSID, err)
	}
	if prev.ICloudPath != f.ICloudPath || prev.LocalPath != f.LocalPath {
		if err = appendEvent(ctx, tx, ts, DriveLibrary, f.DriveWSID, EventDriveFileMoved,
			map[string]any{"from": prev.ICloudPath, "to": f.ICloudPath, "local_path": f.LocalPath}); err != nil {
			return err
		}
	}
	if prev.DeletedAt != nil {
		if err = appendEvent(ctx, tx, ts, DriveLibrary, f.DriveWSID, EventDriveFileRestored,
			map[string]any{"path": f.ICloudPath}); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("update drive file: commit: %w", err)
	}
	return nil
}

// MarkDriveFilesDeleted records that these files are gone from iCloud. Their
// rows and archived copies stay.
func (db *DB) MarkDriveFilesDeleted(ctx context.Context, ids []string, now time.Time) (err error) {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mark drive files deleted: begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	ts := formatTime(now)
	for _, id := range ids {
		var path string
		switch err = tx.QueryRowContext(ctx, `UPDATE icloud_drive_files SET deleted_in_icloud_at = ?
			WHERE drivewsid = ? AND deleted_in_icloud_at IS NULL RETURNING icloud_path`, ts, id).Scan(&path); {
		case errors.Is(err, sql.ErrNoRows):
			err = nil
			continue
		case err != nil:
			return fmt.Errorf("mark drive file %s deleted: %w", id, err)
		}
		if err = appendEvent(ctx, tx, ts, DriveLibrary, id, EventDriveFileDeleted, map[string]any{"path": path}); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("mark drive files deleted: commit: %w", err)
	}
	return nil
}

// DriveScrubBatch returns the share fraction of archived Drive files to
// re-check, those checked longest ago (or never) first, and how many are
// eligible. Damaged files wait for their re-download instead.
func (db *DB) DriveScrubBatch(ctx context.Context, fraction float64) (_ []DriveFile, eligible int, err error) {
	if err = db.r.QueryRowContext(ctx, `SELECT count(*) FROM icloud_drive_files WHERE damaged_at IS NULL`).Scan(&eligible); err != nil {
		return nil, 0, fmt.Errorf("count drive scrub: %w", err)
	}
	n, err := scrubLimit(eligible, fraction)
	if err != nil {
		return nil, 0, err
	}
	rows, err := db.r.QueryContext(ctx, `SELECT `+driveFileColumns+` FROM icloud_drive_files WHERE damaged_at IS NULL
		ORDER BY last_scrub_at NULLS FIRST, drivewsid LIMIT ?`, n)
	if err != nil {
		return nil, 0, fmt.Errorf("query drive scrub: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var out []DriveFile
	for rows.Next() {
		f, scanErr := scanDriveFile(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate drive scrub: %w", err)
	}
	return out, eligible, nil
}

// RecordDriveScrub records that a Drive file's copy matched its checksum.
func (db *DB) RecordDriveScrub(ctx context.Context, drivewsid string, now time.Time) error {
	if _, err := db.w.ExecContext(ctx, `UPDATE icloud_drive_files SET last_scrub_at = ? WHERE drivewsid = ?`,
		formatTime(now), drivewsid); err != nil {
		return fmt.Errorf("record drive scrub %s: %w", drivewsid, err)
	}
	return nil
}

// MarkDriveFileDamaged records that a Drive file's copy no longer matches its
// checksum; the next sync downloads it again and keeps the damaged copy aside.
func (db *DB) MarkDriveFileDamaged(ctx context.Context, drivewsid, reason string, now time.Time) (err error) {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mark drive file damaged: begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	ts := formatTime(now)
	var path string
	if err = tx.QueryRowContext(ctx, `UPDATE icloud_drive_files SET damaged_at = ? WHERE drivewsid = ? RETURNING icloud_path`,
		ts, drivewsid).Scan(&path); err != nil {
		return fmt.Errorf("mark drive file %s damaged: %w", drivewsid, err)
	}
	if err = appendEvent(ctx, tx, ts, DriveLibrary, drivewsid, EventDriveFileDamaged, map[string]any{"path": path, "reason": reason}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("mark drive file damaged: commit: %w", err)
	}
	return nil
}

// DriveTotals counts archived Drive files and bytes, and those deleted in iCloud.
func (db *DB) DriveTotals(ctx context.Context) (files, bytes, deleted int64, err error) {
	if err = db.r.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(size_bytes), 0),
		count(deleted_in_icloud_at) FROM icloud_drive_files`).Scan(&files, &bytes, &deleted); err != nil {
		return 0, 0, 0, fmt.Errorf("drive totals: %w", err)
	}
	return files, bytes, deleted, nil
}
