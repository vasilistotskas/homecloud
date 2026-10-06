package ledger

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"
)

// RecordDriveHealth records state as the archive drive's health if it differs
// from the last recorded state, and returns that previous state ("" when none
// was recorded). Repeated checks with the same result add nothing.
func (db *DB) RecordDriveHealth(ctx context.Context, state string, reasons []string, now time.Time) (previous string, changed bool, err error) {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("record drive health: begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()

	var last string
	switch err = tx.QueryRowContext(ctx, `SELECT detail FROM events WHERE kind = ? ORDER BY id DESC LIMIT 1`,
		EventDriveHealthChanged).Scan(&last); {
	case errors.Is(err, sql.ErrNoRows):
		err = nil
	case err != nil:
		return "", false, fmt.Errorf("last drive health: %w", err)
	default:
		var d struct {
			To string `json:"to"`
		}
		if err = json.Unmarshal([]byte(last), &d); err != nil {
			return "", false, fmt.Errorf("decode last drive health: %w", err)
		}
		previous = d.To
	}
	if previous == state {
		if err = tx.Commit(); err != nil {
			return "", false, fmt.Errorf("record drive health: commit: %w", err)
		}
		return previous, false, nil
	}
	payload, err := json.Marshal(map[string]any{"from": previous, "to": state, "reasons": reasons})
	if err != nil {
		return "", false, fmt.Errorf("encode drive health: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO events (at, kind, detail) VALUES (?, ?, ?)`,
		formatTime(now), EventDriveHealthChanged, string(payload)); err != nil {
		return "", false, fmt.Errorf("append drive health event: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return "", false, fmt.Errorf("record drive health: commit: %w", err)
	}
	return previous, true, nil
}
