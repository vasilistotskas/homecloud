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
		kind, now.UTC().Format(time.RFC3339Nano))
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
		now.UTC().Format(time.RFC3339Nano), ok, string(payload), id)
	if err != nil {
		return fmt.Errorf("finish run %d: %w", id, err)
	}
	if n, nerr := res.RowsAffected(); nerr != nil || n != 1 {
		return errors.Join(fmt.Errorf("finish run %d: not found or already finished", id), nerr)
	}
	return nil
}

// RecentRuns returns the latest runs, newest first.
func (db *DB) RecentRuns(ctx context.Context, limit int) (out []Run, err error) {
	rows, err := db.r.QueryContext(ctx,
		`SELECT id, kind, started_at, finished_at, ok, detail FROM runs ORDER BY id DESC LIMIT ?`, limit)
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
		if r.StartedAt, err = time.Parse(time.RFC3339Nano, started); err != nil {
			return nil, fmt.Errorf("parse run %d start: %w", r.ID, err)
		}
		if finished.Valid {
			t, perr := time.Parse(time.RFC3339Nano, finished.String)
			if perr != nil {
				return nil, fmt.Errorf("parse run %d finish: %w", r.ID, perr)
			}
			r.FinishedAt = &t
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

// Event is one audit entry.
type Event struct {
	At             time.Time
	Library, Asset string
	Kind, Detail   string
}

// RecentEvents returns the latest events of the given kinds, newest first.
func (db *DB) RecentEvents(ctx context.Context, kinds []string, limit int) (out []Event, err error) {
	payload, err := json.Marshal(kinds)
	if err != nil {
		return nil, fmt.Errorf("encode kinds: %w", err)
	}
	rows, err := db.r.QueryContext(ctx, `
		SELECT at, coalesce(library, ''), coalesce(asset_id, ''), kind, detail FROM events
		WHERE kind IN (SELECT value FROM json_each(?))
		ORDER BY id DESC LIMIT ?`, string(payload), limit)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var e Event
		var at string
		if scanErr := rows.Scan(&at, &e.Library, &e.Asset, &e.Kind, &e.Detail); scanErr != nil {
			return nil, fmt.Errorf("scan event: %w", scanErr)
		}
		if e.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("parse event time: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return out, nil
}
