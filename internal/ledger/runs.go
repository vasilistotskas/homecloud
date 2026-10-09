package ledger

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"
)

// Run is one recorded job execution.
type Run struct {
	ID         int64
	Kind       string
	StartedAt  time.Time
	FinishedAt *time.Time
	OK         *bool
	Detail     string
}

// StartRun records the start of a job and returns its id.
func (db *DB) StartRun(ctx context.Context, kind string, now time.Time) (int64, error) {
	res, err := db.w.ExecContext(ctx, `INSERT INTO runs (kind, started_at) VALUES (?, ?)`,
		kind, formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("start run %s: %w", kind, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("start run %s: %w", kind, err)
	}
	return id, nil
}

// FinishRun records the outcome of run id with a JSON-encodable detail.
func (db *DB) FinishRun(ctx context.Context, id int64, ok bool, detail any, now time.Time) error {
	payload, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode run detail: %w", err)
	}
	res, err := db.w.ExecContext(ctx, `UPDATE runs SET finished_at = ?, ok = ?, detail = ? WHERE id = ? AND finished_at IS NULL`,
		formatTime(now), ok, string(payload), id)
	if err != nil {
		return fmt.Errorf("finish run %d: %w", id, err)
	}
	if n, nerr := res.RowsAffected(); nerr != nil || n != 1 {
		return errors.Join(fmt.Errorf("finish run %d: not found or already finished", id), nerr)
	}
	return nil
}

// Runs returns up to limit runs of kind older than beforeID (from the newest
// when beforeID is 0), newest first.
func (db *DB) Runs(ctx context.Context, kind string, beforeID int64, limit int) ([]Run, error) {
	return db.queryRuns(ctx, `SELECT id, kind, started_at, finished_at, ok, detail FROM runs
		WHERE kind = ? AND (? = 0 OR id < ?) ORDER BY id DESC LIMIT ?`, kind, beforeID, beforeID, limit)
}

// LastFinishedRun returns the most recent finished run of kind, or ErrNotFound.
func (db *DB) LastFinishedRun(ctx context.Context, kind string) (Run, error) {
	runs, err := db.queryRuns(ctx, `SELECT id, kind, started_at, finished_at, ok, detail FROM runs
		WHERE kind = ? AND finished_at IS NOT NULL ORDER BY id DESC LIMIT 1`, kind)
	if err != nil {
		return Run{}, err
	}
	if len(runs) == 0 {
		return Run{}, fmt.Errorf("last finished %s run: %w", kind, ErrNotFound)
	}
	return runs[0], nil
}

// UnfinishedRuns returns the runs that started but never finished, oldest first.
func (db *DB) UnfinishedRuns(ctx context.Context) ([]Run, error) {
	return db.queryRuns(ctx, `SELECT id, kind, started_at, finished_at, ok, detail FROM runs
		WHERE finished_at IS NULL ORDER BY id`)
}

// LastCompletedRun returns the most recent finished run of kind that did its
// work, or ErrNotFound. A run reports a failure to do its work in the "error"
// field of its detail; findings such as mismatched files do not make it
// incomplete.
func (db *DB) LastCompletedRun(ctx context.Context, kind string) (Run, error) {
	runs, err := db.queryRuns(ctx, `SELECT id, kind, started_at, finished_at, ok, detail FROM runs
		WHERE kind = ? AND finished_at IS NOT NULL AND json_extract(detail, '$.error') IS NULL
		ORDER BY id DESC LIMIT 1`, kind)
	if err != nil {
		return Run{}, err
	}
	if len(runs) == 0 {
		return Run{}, fmt.Errorf("last completed %s run: %w", kind, ErrNotFound)
	}
	return runs[0], nil
}

func (db *DB) queryRuns(ctx context.Context, query string, args ...any) (out []Run, err error) {
	rows, err := db.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query runs: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var r Run
		var started string
		var finished sql.NullString
		var ok sql.NullBool
		if scanErr := rows.Scan(&r.ID, &r.Kind, &started, &finished, &ok, &r.Detail); scanErr != nil {
			return nil, fmt.Errorf("scan run: %w", scanErr)
		}
		if r.StartedAt, err = parseTime(started); err != nil {
			return nil, fmt.Errorf("run %d start: %w", r.ID, err)
		}
		if r.FinishedAt, err = parseNullTime(finished); err != nil {
			return nil, fmt.Errorf("run %d finish: %w", r.ID, err)
		}
		if ok.Valid {
			r.OK = &ok.Bool
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate runs: %w", err)
	}
	return out, nil
}

// Event is one audit entry. Filename is the asset's original rendition file
// name, empty when the event has no asset or the asset has no original.
type Event struct {
	ID                       int64
	At                       time.Time
	Library, Asset, Filename string
	Kind, Detail             string
}

// EventQuery selects events.
type EventQuery struct {
	// Kinds to include; at least one.
	Kinds []string
	// Text, when not empty, keeps the events whose subject contains it,
	// ignoring case and accents (see eventSubject and fold).
	Text string
}

// eventSubject is the SQL for the name an event is about, which the app shows
// for it: the original file name of a Photos asset, the iCloud Drive path (and
// the earlier one of a move), or the name of the iPhone a backup belongs to.
// Events about neither (drive health) have none.
const eventSubject = `CASE
	WHEN e.library = '` + DriveLibrary + `' THEN concat_ws(char(10), e.detail ->> '$.path', e.detail ->> '$.from', e.detail ->> '$.to')
	WHEN e.library IS NOT NULL THEN coalesce(r.filename, '')
	ELSE coalesce(e.detail ->> '$.device_name', '')
END`

// Events returns up to limit events matching q older than beforeID (all
// events when beforeID is 0), newest first.
func (db *DB) Events(ctx context.Context, q EventQuery, beforeID int64, limit int) (out []Event, err error) {
	payload, err := json.Marshal(q.Kinds)
	if err != nil {
		return nil, fmt.Errorf("encode kinds: %w", err)
	}
	text := fold(q.Text)
	rows, err := db.r.QueryContext(ctx, `
		SELECT e.id, e.at, coalesce(e.library, ''), coalesce(e.asset_id, ''), coalesce(r.filename, ''), e.kind, e.detail
		FROM events e
		LEFT JOIN resources r ON r.library = e.library AND r.asset_id = e.asset_id AND r.version = 'original'
		WHERE e.kind IN (SELECT value FROM json_each(?)) AND (? = 0 OR e.id < ?)
		  AND (? = '' OR instr(`+foldFunc+`(`+eventSubject+`), ?) > 0)
		ORDER BY e.id DESC LIMIT ?`, string(payload), beforeID, beforeID, text, text, limit)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var e Event
		var at string
		if scanErr := rows.Scan(&e.ID, &at, &e.Library, &e.Asset, &e.Filename, &e.Kind, &e.Detail); scanErr != nil {
			return nil, fmt.Errorf("scan event: %w", scanErr)
		}
		if e.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("event %d time: %w", e.ID, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return out, nil
}
