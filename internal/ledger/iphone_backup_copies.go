package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// IPhoneBackupCopy is a copy of an iPhone backup kept in the archive.
type IPhoneBackupCopy struct {
	UDID string
	// Stamp is when the copied backup finished, in UTC (the copy's folder).
	Stamp      string
	DeviceName string
	Files      int64
	Bytes      int64
	// CopiedBytes were written for this copy; the rest is shared with the
	// copy before it.
	CopiedBytes int64
	CreatedAt   time.Time
	// VerifiedAt is when every file was last checked against the copy's
	// manifest (nil: not yet); DamagedFiles did not match.
	VerifiedAt   *time.Time
	DamagedFiles int64
}

// IPhoneBackupCopies returns every copy, by device and newest first.
func (db *DB) IPhoneBackupCopies(ctx context.Context) (_ []IPhoneBackupCopy, err error) {
	rows, err := db.r.QueryContext(ctx, `SELECT udid, stamp, device_name, files, bytes, copied_bytes, created_at,
		verified_at, damaged_files FROM iphone_backup_copies ORDER BY udid, stamp DESC`)
	if err != nil {
		return nil, fmt.Errorf("query iphone backup copies: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var out []IPhoneBackupCopy
	for rows.Next() {
		var c IPhoneBackupCopy
		var created string
		var verified sql.NullString
		if scanErr := rows.Scan(&c.UDID, &c.Stamp, &c.DeviceName, &c.Files, &c.Bytes, &c.CopiedBytes, &created,
			&verified, &c.DamagedFiles); scanErr != nil {
			return nil, fmt.Errorf("scan iphone backup copy: %w", scanErr)
		}
		if c.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if c.VerifiedAt, err = parseNullTime(verified); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate iphone backup copies: %w", err)
	}
	return out, nil
}

// AddIPhoneBackupCopy records a new copy and appends iphone_backup_copied.
func (db *DB) AddIPhoneBackupCopy(ctx context.Context, c IPhoneBackupCopy) error {
	return db.inTx(ctx, "add iphone backup copy", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO iphone_backup_copies (udid, stamp, device_name, files, bytes,
			copied_bytes, created_at, verified_at, damaged_files) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.UDID, c.Stamp, c.DeviceName, c.Files, c.Bytes, c.CopiedBytes, formatTime(c.CreatedAt),
			nullTime(c.VerifiedAt), c.DamagedFiles); err != nil {
			return fmt.Errorf("insert copy %s/%s: %w", c.UDID, c.Stamp, err)
		}
		return appendEvent(ctx, tx, formatTime(c.CreatedAt), "", "", EventIPhoneBackupCopied, map[string]any{
			"udid": c.UDID, "device_name": c.DeviceName, "stamp": c.Stamp,
			"files": c.Files, "bytes": c.Bytes, "copied_bytes": c.CopiedBytes,
		})
	})
}

// Reasons a copy leaves the archive.
const (
	// CopyRemovedRetention: older than the newest iphone_backup.keep copies.
	CopyRemovedRetention = "retention"
	// CopyRemovedMissing: no longer on disk.
	CopyRemovedMissing = "missing"
)

// RemoveIPhoneBackupCopy forgets a copy and appends iphone_backup_copy_removed.
func (db *DB) RemoveIPhoneBackupCopy(ctx context.Context, c IPhoneBackupCopy, reason string, now time.Time) error {
	return db.inTx(ctx, "remove iphone backup copy", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM iphone_backup_copies WHERE udid = ? AND stamp = ?`,
			c.UDID, c.Stamp); err != nil {
			return fmt.Errorf("delete copy %s/%s: %w", c.UDID, c.Stamp, err)
		}
		return appendEvent(ctx, tx, formatTime(now), "", "", EventIPhoneBackupCopyRemoved, map[string]any{
			"udid": c.UDID, "device_name": c.DeviceName, "stamp": c.Stamp, "reason": reason,
		})
	})
}

// SetIPhoneBackupCopyVerified records a check of every file of a copy and,
// when the number of damaged files changed, appends
// iphone_backup_copy_damaged.
func (db *DB) SetIPhoneBackupCopyVerified(ctx context.Context, c IPhoneBackupCopy, damaged int64, now time.Time) error {
	return db.inTx(ctx, "verify iphone backup copy", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE iphone_backup_copies SET verified_at = ?, damaged_files = ?
			WHERE udid = ? AND stamp = ?`, formatTime(now), damaged, c.UDID, c.Stamp); err != nil {
			return fmt.Errorf("update copy %s/%s: %w", c.UDID, c.Stamp, err)
		}
		if damaged == c.DamagedFiles {
			return nil
		}
		return appendEvent(ctx, tx, formatTime(now), "", "", EventIPhoneBackupCopyDamaged, map[string]any{
			"udid": c.UDID, "device_name": c.DeviceName, "stamp": c.Stamp, "files": damaged,
		})
	})
}

// RecordIPhoneBackupPushRefused appends iphone_backup_push_refused.
func (db *DB) RecordIPhoneBackupPushRefused(ctx context.Context, udid, stamp, reason string, now time.Time) error {
	return db.inTx(ctx, "record refused push", func(tx *sql.Tx) error {
		return appendEvent(ctx, tx, formatTime(now), "", "", EventIPhoneBackupPushRefused, map[string]any{
			"udid": udid, "stamp": stamp, "reason": reason,
		})
	})
}

// inTx runs fn in a write transaction, committing when it succeeds.
func (db *DB) inTx(ctx context.Context, what string, fn func(*sql.Tx) error) (err error) {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: begin: %w", what, err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("%s: commit: %w", what, err)
	}
	return nil
}
