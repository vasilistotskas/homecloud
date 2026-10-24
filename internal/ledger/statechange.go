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
	return db.recordStateChange(ctx, EventDriveHealthChanged, state, map[string]any{"reasons": reasons}, now)
}

// PhotosActivity is whether iCloud Photos keeps receiving new items.
type PhotosActivity string

// The states of PhotosActivity.
const (
	// PhotosActive: an item was added within icloud.quiet_after.
	PhotosActive PhotosActivity = "active"
	// PhotosQuiet: nothing was added for longer than icloud.quiet_after.
	PhotosQuiet PhotosActivity = "quiet"
)

// RecordPhotosActivity records state as iCloud Photos' activity if it differs
// from the last recorded state, with the newest addition and the threshold it
// was judged by, and returns the previous state ("" when none was recorded).
func (db *DB) RecordPhotosActivity(ctx context.Context, state PhotosActivity, newest time.Time, quietAfter time.Duration,
	now time.Time,
) (previous PhotosActivity, changed bool, err error) {
	p, changed, err := db.recordStateChange(ctx, EventPhotosActivityChanged, string(state), map[string]any{
		"newest_added_at": formatTime(newest), "quiet_after_seconds": int64(quietAfter.Seconds()),
	}, now)
	return PhotosActivity(p), changed, err
}

// LastPhotosActivity returns the last recorded activity of iCloud Photos, or
// "" when none was recorded yet.
func (db *DB) LastPhotosActivity(ctx context.Context) (PhotosActivity, error) {
	last, err := lastState(ctx, db.r, EventPhotosActivityChanged)
	return PhotosActivity(last), err
}

// NewestAddition returns when the newest item was added to iCloud Photos, or
// nil when no asset records it.
func (db *DB) NewestAddition(ctx context.Context) (*time.Time, error) {
	var added string
	// Compared as instants: the RFC 3339 text has a varying number of
	// fractional digits.
	err := db.r.QueryRowContext(ctx, `SELECT added_at FROM assets WHERE added_at IS NOT NULL
		ORDER BY unixepoch(added_at, 'subsec') DESC LIMIT 1`).Scan(&added)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // no asset records when it was added
	}
	if err != nil {
		return nil, fmt.Errorf("newest addition: %w", err)
	}
	t, err := parseTime(added)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// querier is what lastState reads through: the reader pool or a transaction.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// lastState returns the "to" of the newest event of kind, or "" when there is none.
func lastState(ctx context.Context, q querier, kind string) (string, error) {
	var last string
	// events_kind (kind, id) serves this lookup.
	switch err := q.QueryRowContext(ctx, `SELECT detail FROM events WHERE kind = ? ORDER BY id DESC LIMIT 1`, kind).Scan(&last); {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("last %s: %w", kind, err)
	}
	var d struct {
		To string `json:"to"`
	}
	if err := json.Unmarshal([]byte(last), &d); err != nil {
		return "", fmt.Errorf("decode last %s: %w", kind, err)
	}
	return d.To, nil
}

// recordStateChange appends an event of kind, with detail and its "from" and
// "to", when state differs from the "to" of the last event of that kind, and
// returns that previous state ("" when none was recorded).
func (db *DB) recordStateChange(ctx context.Context, kind, state string, detail map[string]any, now time.Time) (previous string, changed bool, err error) {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("record %s: begin: %w", kind, err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if previous, err = lastState(ctx, tx, kind); err != nil {
		return "", false, err
	}
	if previous != state {
		detail["from"], detail["to"] = previous, state
		if err = appendEvent(ctx, tx, formatTime(now), "", "", kind, detail); err != nil {
			return "", false, err
		}
		changed = true
	}
	if err = tx.Commit(); err != nil {
		return "", false, fmt.Errorf("record %s: commit: %w", kind, err)
	}
	return previous, changed, nil
}
