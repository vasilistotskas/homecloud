package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ManifestRow is one downloaded rendition, as exported to manifest.csv.
type ManifestRow struct {
	Library, AssetID, Version string
	Filename, LocalPath       string
	MediaType                 string
	SizeBytes                 int64
	SHA256                    string
	State                     AssetState
	CreatedAt, AddedAt        *time.Time
	// VerifiedAt is when the file's checksum was last confirmed (first
	// verification or a later scrub); nil while the current file is unconfirmed.
	VerifiedAt *time.Time
}

// EachManifestRow calls fn for every rendition, ordered by asset creation time,
// then asset and version, stopping at the first error.
func (db *DB) EachManifestRow(ctx context.Context, fn func(ManifestRow) error) (err error) {
	rows, err := db.r.QueryContext(ctx, `
		SELECT r.library, r.asset_id, r.version, r.filename, r.local_path, r.media_type, r.size_bytes, r.kei_sha256,
		       a.state, a.created_at, a.added_at, r.last_scrub_at
		FROM resources r JOIN assets a USING (library, asset_id)
		ORDER BY a.created_at, r.library, r.asset_id, r.version`)
	if err != nil {
		return fmt.Errorf("query manifest: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var m ManifestRow
		var created, added, verified sql.NullString
		if scanErr := rows.Scan(&m.Library, &m.AssetID, &m.Version, &m.Filename, &m.LocalPath, &m.MediaType,
			&m.SizeBytes, &m.SHA256, &m.State, &created, &added, &verified); scanErr != nil {
			return fmt.Errorf("scan manifest row: %w", scanErr)
		}
		if m.CreatedAt, err = parseNullTime(created); err != nil {
			return err
		}
		if m.AddedAt, err = parseNullTime(added); err != nil {
			return err
		}
		if m.VerifiedAt, err = parseNullTime(verified); err != nil {
			return err
		}
		if err = fn(m); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate manifest: %w", err)
	}
	return nil
}
