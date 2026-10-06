package ledger

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"time"
)

// RecordDriveHealth records state as the archive drive's health if it differs
// from the last recorded state, and returns that previous state ("" when none
// was recorded). Repeated checks with the same result add nothing.
func (db *DB) RecordDriveHealth(ctx context.Context, state string, reasons []string, now time.Time) (previous string, changed bool, err error) {
	return db.recordStateChange(ctx, EventDriveHealthChanged, "", state, map[string]any{"reasons": reasons}, now)
}

// RecordIPhoneBackupState records state for the device with the given UDID if it
// differs from that device's last recorded state, and returns the previous
// state ("" when none was recorded).
func (db *DB) RecordIPhoneBackupState(ctx context.Context, udid, deviceName, state string, problems []string, now time.Time) (previous string, changed bool, err error) {
	return db.recordStateChange(ctx, EventIPhoneBackupChanged, udid, state,
		map[string]any{"device_name": deviceName, "problems": problems}, now)
}

// recordStateChange appends a kind event {"subject", "from", "to", ...detail}
// when state differs from the last state recorded for subject ("" for a single,
// subject-less thing such as the archive drive). It reports the previous state
// and whether an event was appended.
func (db *DB) recordStateChange(ctx context.Context, kind, subject, state string, detail map[string]any, now time.Time) (previous string, changed bool, err error) {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("record %s: begin: %w", kind, err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()

	var last string
	switch err = tx.QueryRowContext(ctx, `SELECT detail FROM events
		WHERE kind = ? AND coalesce(json_extract(detail, '$.subject'), '') = ?
		ORDER BY id DESC LIMIT 1`, kind, subject).Scan(&last); {
	case errors.Is(err, sql.ErrNoRows):
		err = nil
	case err != nil:
		return "", false, fmt.Errorf("last %s: %w", kind, err)
	default:
		var d struct {
			To string `json:"to"`
		}
		if err = json.Unmarshal([]byte(last), &d); err != nil {
			return "", false, fmt.Errorf("decode last %s: %w", kind, err)
		}
		previous = d.To
	}
	if previous == state {
		if err = tx.Commit(); err != nil {
			return "", false, fmt.Errorf("record %s: commit: %w", kind, err)
		}
		return previous, false, nil
	}
	payload := maps.Clone(detail)
	payload["from"], payload["to"] = previous, state
	if subject != "" {
		payload["subject"] = subject
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", false, fmt.Errorf("encode %s: %w", kind, err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO events (at, kind, detail) VALUES (?, ?, ?)`,
		formatTime(now), kind, string(encoded)); err != nil {
		return "", false, fmt.Errorf("append %s event: %w", kind, err)
	}
	if err = tx.Commit(); err != nil {
		return "", false, fmt.Errorf("record %s: commit: %w", kind, err)
	}
	return previous, true, nil
}
