package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// IPhoneBackupRecord is what the ledger keeps about one local backup folder
// between checks.
type IPhoneBackupRecord struct {
	Folder     string
	UDID       string
	DeviceName string
	State      string
	// BaselineBytes is the size of the last backup judged to have a normal
	// size (0 before the first).
	BaselineBytes int64
	// PendingShrinkBytes is a much smaller backup awaiting confirmation by a
	// later backup (0 when none).
	PendingShrinkBytes int64
	// JudgedCompletedAt is the completion time of the backup last judged.
	JudgedCompletedAt *time.Time
	// LastFullReadAt is when every file was last read in full, and
	// UnreadableFiles how many files that read could not read.
	LastFullReadAt  *time.Time
	UnreadableFiles int
}

// IPhoneBackupRecords returns the record of every backup folder ever seen.
func (db *DB) IPhoneBackupRecords(ctx context.Context) (_ map[string]IPhoneBackupRecord, err error) {
	rows, err := db.r.QueryContext(ctx, `SELECT folder, udid, device_name, state, baseline_bytes,
		pending_shrink_bytes, judged_completed_at, last_full_read_at, unreadable_files FROM iphone_backups`)
	if err != nil {
		return nil, fmt.Errorf("query iphone backups: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out := map[string]IPhoneBackupRecord{}
	for rows.Next() {
		var r IPhoneBackupRecord
		var judged, read sql.NullString
		if scanErr := rows.Scan(&r.Folder, &r.UDID, &r.DeviceName, &r.State, &r.BaselineBytes,
			&r.PendingShrinkBytes, &judged, &read, &r.UnreadableFiles); scanErr != nil {
			return nil, fmt.Errorf("scan iphone backup: %w", scanErr)
		}
		if r.JudgedCompletedAt, err = parseNullTime(judged); err != nil {
			return nil, err
		}
		if r.LastFullReadAt, err = parseNullTime(read); err != nil {
			return nil, err
		}
		out[r.Folder] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate iphone backups: %w", err)
	}
	return out, nil
}

// SaveIPhoneBackup stores a folder's record after a check and, when its
// state changed, appends an iphone_backup_changed event with the problems.
func (db *DB) SaveIPhoneBackup(ctx context.Context, r IPhoneBackupRecord, problems []string, now time.Time) (err error) {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save iphone backup: begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	var previous string
	switch err = tx.QueryRowContext(ctx, `SELECT state FROM iphone_backups WHERE folder = ?`, r.Folder).Scan(&previous); {
	case errors.Is(err, sql.ErrNoRows):
		err = nil
	case err != nil:
		return fmt.Errorf("previous iphone backup state: %w", err)
	}
	ts := formatTime(now)
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO iphone_backups (folder, udid, device_name, state, baseline_bytes, pending_shrink_bytes,
			judged_completed_at, last_full_read_at, unreadable_files, checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (folder) DO UPDATE SET
			udid = excluded.udid, device_name = excluded.device_name, state = excluded.state,
			baseline_bytes = excluded.baseline_bytes, pending_shrink_bytes = excluded.pending_shrink_bytes,
			judged_completed_at = excluded.judged_completed_at, last_full_read_at = excluded.last_full_read_at,
			unreadable_files = excluded.unreadable_files,
			checked_at = excluded.checked_at`,
		r.Folder, r.UDID, r.DeviceName, r.State, r.BaselineBytes, r.PendingShrinkBytes,
		nullTime(r.JudgedCompletedAt), nullTime(r.LastFullReadAt), r.UnreadableFiles, ts); err != nil {
		return fmt.Errorf("save iphone backup %s: %w", r.Folder, err)
	}
	if previous != r.State {
		if err = appendEvent(ctx, tx, ts, "", "", EventIPhoneBackupChanged, map[string]any{
			"subject": r.Folder, "udid": r.UDID, "device_name": r.DeviceName,
			"from": previous, "to": r.State, "problems": problems,
		}); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("save iphone backup: commit: %w", err)
	}
	return nil
}

// AlertDue reports whether the throttled alert key may be sent now (never
// sent, or last sent at least every ago) and, if so, records it as sent.
func (db *DB) AlertDue(ctx context.Context, key string, every time.Duration, now time.Time) (due bool, err error) {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("alert throttle: begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	var sent string
	switch err = tx.QueryRowContext(ctx, `SELECT sent_at FROM alert_throttle WHERE key = ?`, key).Scan(&sent); {
	case errors.Is(err, sql.ErrNoRows):
		err = nil
	case err != nil:
		return false, fmt.Errorf("alert throttle %s: %w", key, err)
	default:
		last, parseErr := parseTime(sent)
		if parseErr != nil {
			return false, parseErr
		}
		if now.Sub(last) < every {
			if err = tx.Commit(); err != nil {
				return false, fmt.Errorf("alert throttle: commit: %w", err)
			}
			return false, nil
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO alert_throttle (key, sent_at) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET sent_at = excluded.sent_at`, key, formatTime(now)); err != nil {
		return false, fmt.Errorf("alert throttle %s: %w", key, err)
	}
	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("alert throttle: commit: %w", err)
	}
	return true, nil
}
