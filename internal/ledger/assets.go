package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ArchiveTotals counts the downloaded renditions and their bytes.
func (db *DB) ArchiveTotals(ctx context.Context) (files, bytes int64, err error) {
	if err := db.r.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(size_bytes), 0) FROM resources`).
		Scan(&files, &bytes); err != nil {
		return 0, 0, fmt.Errorf("archive totals: %w", err)
	}
	return files, bytes, nil
}

// AssetDetail is one asset with all of its renditions.
type AssetDetail struct {
	Library, AssetID string
	State            AssetState
	StateChangedAt   time.Time
	CreatedAt        *time.Time
	AddedAt          *time.Time
	LastSeenAt       *time.Time
	Renditions       []RenditionDetail
}

// RenditionDetail is a rendition with its verification evidence.
type RenditionDetail struct {
	Version, Filename, LocalPath, MediaType string
	SizeBytes                               int64
	SHA256                                  string
	RequiresStructure, RequiresProbe        bool
	// Passed holds the time each tier passed; absent tiers have not passed.
	Passed  map[Tier]time.Time
	Failure *Failure
}

// Asset returns one asset and its renditions, or ErrNotFound.
func (db *DB) Asset(ctx context.Context, library, assetID string) (AssetDetail, error) {
	d := AssetDetail{Library: library, AssetID: assetID}
	var changed string
	var created, added, seen sql.NullString
	err := db.r.QueryRowContext(ctx, `SELECT state, state_changed_at, created_at, added_at, last_seen_at
		FROM assets WHERE library = ? AND asset_id = ?`, library, assetID).
		Scan(&d.State, &changed, &created, &added, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetDetail{}, fmt.Errorf("asset %s/%s: %w", library, assetID, ErrNotFound)
	}
	if err != nil {
		return AssetDetail{}, fmt.Errorf("asset %s/%s: %w", library, assetID, err)
	}
	if d.StateChangedAt, err = parseTime(changed); err != nil {
		return AssetDetail{}, err
	}
	for dst, src := range map[**time.Time]sql.NullString{&d.CreatedAt: created, &d.AddedAt: added, &d.LastSeenAt: seen} {
		if *dst, err = parseNullTime(src); err != nil {
			return AssetDetail{}, fmt.Errorf("asset %s/%s: %w", library, assetID, err)
		}
	}
	if d.Renditions, err = db.renditions(ctx, library, assetID); err != nil {
		return AssetDetail{}, err
	}
	return d, nil
}

func (db *DB) renditions(ctx context.Context, library, assetID string) (out []RenditionDetail, err error) {
	rows, err := db.r.QueryContext(ctx, `SELECT version, filename, local_path, media_type, size_bytes, kei_sha256,
			requires_structure, requires_probe, verified_content_at, verified_structure_at, verified_probe_at, failure
		FROM resources WHERE library = ? AND asset_id = ? ORDER BY version`, library, assetID)
	if err != nil {
		return nil, fmt.Errorf("query renditions of %s/%s: %w", library, assetID, err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var r RenditionDetail
		var content, structure, probe, failure sql.NullString
		if scanErr := rows.Scan(&r.Version, &r.Filename, &r.LocalPath, &r.MediaType, &r.SizeBytes, &r.SHA256,
			&r.RequiresStructure, &r.RequiresProbe, &content, &structure, &probe, &failure); scanErr != nil {
			return nil, fmt.Errorf("scan rendition: %w", scanErr)
		}
		passed, perr := passes(content, structure, probe)
		if perr != nil {
			return nil, fmt.Errorf("rendition %s of %s/%s: %w", r.Version, library, assetID, perr)
		}
		r.Passed = passed
		if failure.Valid {
			f, perr := parseFailure(failure.String)
			if perr != nil {
				return nil, perr
			}
			r.Failure = &f
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate renditions: %w", err)
	}
	return out, nil
}
