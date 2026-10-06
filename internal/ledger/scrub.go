package ledger

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// ScrubTarget is a verified rendition whose file is re-hashed against the
// checksum recorded when it was downloaded.
type ScrubTarget struct {
	Key
	LocalPath string
	SizeBytes uint64
	SHA256    string
}

// ScrubBatch returns the share fraction (0, 1] of verified renditions whose
// content was confirmed longest ago, never-scrubbed first, and the number of
// renditions eligible for scrubbing. A non-empty archive always yields at
// least one target, so every rendition is re-checked within 1/fraction runs.
func (db *DB) ScrubBatch(ctx context.Context, fraction float64) (_ []ScrubTarget, eligible int, err error) {
	if fraction <= 0 || fraction > 1 {
		return nil, 0, fmt.Errorf("scrub fraction %v outside (0, 1]", fraction)
	}
	if countErr := db.r.QueryRowContext(ctx, `
		SELECT count(*) FROM resources r JOIN assets a USING (library, asset_id)
		WHERE a.state = ? AND r.failure IS NULL`, StateVerified).Scan(&eligible); countErr != nil {
		return nil, 0, fmt.Errorf("count scrub candidates: %w", countErr)
	}
	limit := int(math.Ceil(float64(eligible) * fraction))

	rows, err := db.r.QueryContext(ctx, `
		SELECT r.library, r.asset_id, r.version, r.local_path, r.size_bytes, r.kei_sha256
		FROM resources r JOIN assets a USING (library, asset_id)
		WHERE a.state = ? AND r.failure IS NULL
		ORDER BY r.last_scrub_at NULLS FIRST, r.library, r.asset_id, r.version
		LIMIT ?`, StateVerified, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("query scrub batch: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	out := make([]ScrubTarget, 0, limit)
	for rows.Next() {
		var t ScrubTarget
		var size int64
		if err := rows.Scan(&t.Library, &t.AssetID, &t.Version, &t.LocalPath, &size, &t.SHA256); err != nil {
			return nil, 0, fmt.Errorf("scan scrub batch: %w", err)
		}
		t.SizeBytes = uint64(size) //nolint:gosec // CHECK (size_bytes >= 0) in schema
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate scrub batch: %w", err)
	}
	return out, eligible, nil
}

// RecordScrub records that k's file still matched its checksum at now.
func (db *DB) RecordScrub(ctx context.Context, k Key, now time.Time) error {
	res, err := db.w.ExecContext(ctx, `UPDATE resources SET last_scrub_at = ?
		WHERE library = ? AND asset_id = ? AND version = ?`, formatTime(now), k.Library, k.AssetID, k.Version)
	if err != nil {
		return fmt.Errorf("record scrub for %v: %w", k, err)
	}
	if n, nerr := res.RowsAffected(); nerr != nil || n != 1 {
		return errors.Join(fmt.Errorf("record scrub for %v: rendition not found", k), nerr)
	}
	return nil
}
