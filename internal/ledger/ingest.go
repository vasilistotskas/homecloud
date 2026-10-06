package ledger

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"
)

// AssetState is the lifecycle state of an asset in the ledger.
type AssetState string

// Asset states (P1). Deletion states are introduced with the deleter.
const (
	// StateDiscovered: at least one rendition is not yet downloaded.
	StateDiscovered AssetState = "discovered"
	// StateDownloaded: every rendition is on disk; verification pending.
	StateDownloaded AssetState = "downloaded"
	// StateVerified: every rendition passed its required verification tiers.
	StateVerified AssetState = "verified"
	// StateQuarantined: a verification mismatch was found; needs review. Sticky.
	StateQuarantined AssetState = "quarantined"
)

// Rendition is one downloaded or pending file of an asset, as reported by the
// downloader. It is source-agnostic: callers map their own records into it.
type Rendition struct {
	Library    string
	AssetID    string
	Version    string
	Filename   string
	Downloaded bool
	// LocalPath, SizeBytes and SHA256 are meaningful only when Downloaded.
	LocalPath string
	SizeBytes uint64
	SHA256    string
	MediaType string
	// RequiresStructure / RequiresProbe name the tiers beyond content this
	// rendition needs; the caller decides them (see verify.RequiredTiers).
	RequiresStructure, RequiresProbe bool
	CreatedAt                        time.Time
	AddedAt                          *time.Time
	SeenAt                           time.Time
}

// IngestStats summarises one ingest.
type IngestStats struct {
	Assets, Renditions, NewRenditions, ChangedRenditions, MovedRenditions int
}

// Ingest records renditions in one transaction. A rendition whose size or hash
// changed loses its verification evidence. Asset states are recomputed, except
// that quarantine is sticky.
func (db *DB) Ingest(ctx context.Context, rs []Rendition, now time.Time) (stats IngestStats, err error) {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("ingest: begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()

	ts := formatTime(now)
	byAsset := map[[2]string][]Rendition{}
	var order [][2]string
	for _, r := range rs {
		k := [2]string{r.Library, r.AssetID}
		if _, seen := byAsset[k]; !seen {
			order = append(order, k)
		}
		byAsset[k] = append(byAsset[k], r)
	}

	for _, k := range order {
		group := byAsset[k]
		if err = upsertAsset(ctx, tx, group, ts); err != nil {
			return stats, err
		}
		for _, r := range group {
			stats.Renditions++
			if !r.Downloaded {
				continue
			}
			var res upsertResult
			if res, err = upsertResource(ctx, tx, r); err != nil {
				return stats, err
			}
			if res.created {
				stats.NewRenditions++
			}
			if res.movedFrom != "" {
				stats.MovedRenditions++
				if err = appendEvent(ctx, tx, ts, r.Library, r.AssetID, EventRenditionMoved,
					map[string]any{"version": r.Version, "from": res.movedFrom, "to": r.LocalPath}); err != nil {
					return stats, err
				}
			}
			if res.changed {
				stats.ChangedRenditions++
				if err = appendEvent(ctx, tx, ts, r.Library, r.AssetID, EventRenditionChanged,
					map[string]any{"version": r.Version, "size": r.SizeBytes, "sha256": r.SHA256}); err != nil {
					return stats, err
				}
			}
		}
		if err = recomputeState(ctx, tx, k[0], k[1], group, ts); err != nil {
			return stats, err
		}
		stats.Assets++
	}

	if err = tx.Commit(); err != nil {
		return stats, fmt.Errorf("ingest: commit: %w", err)
	}
	return stats, nil
}

func upsertAsset(ctx context.Context, tx *sql.Tx, group []Rendition, ts string) error {
	first := group[0]
	created := formatTime(first.CreatedAt)
	var added sql.NullString
	seen := first.SeenAt
	for _, r := range group {
		if r.AddedAt != nil && !added.Valid {
			added = sql.NullString{String: formatTime(*r.AddedAt), Valid: true}
		}
		if r.SeenAt.After(seen) {
			seen = r.SeenAt
		}
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO assets (library, asset_id, state, state_changed_at, created_at, added_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (library, asset_id) DO UPDATE SET
			created_at   = excluded.created_at,
			added_at     = COALESCE(excluded.added_at, assets.added_at),
			last_seen_at = excluded.last_seen_at`,
		first.Library, first.AssetID, StateDiscovered, ts, created, added, formatTime(seen))
	if err != nil {
		return fmt.Errorf("upsert asset %s/%s: %w", first.Library, first.AssetID, err)
	}
	return nil
}

// upsertResource inserts or updates a downloaded rendition. It reports whether
// the row is new and whether an existing row's size or hash changed (which
// clears its verification evidence).
// upsertResult says how an upsert changed a rendition.
type upsertResult struct {
	created bool
	// changed: size or hash differ, the bytes in iCloud changed.
	changed bool
	// movedFrom is the previous local path when the downloader moved the file;
	// the file at the new path has not been verified by homecloud.
	movedFrom string
}

// upsertResource records r. Any change of bytes or location clears the
// rendition's verification evidence, so every tier runs again on the file
// that is actually at local_path now. A recorded failure is kept: quarantine
// lasts until an operator releases it, and the failure is what they review.
func upsertResource(ctx context.Context, tx *sql.Tx, r Rendition) (res upsertResult, err error) {
	if r.SizeBytes > 1<<62 {
		return res, fmt.Errorf("rendition %s/%s/%s: implausible size %d", r.Library, r.AssetID, r.Version, r.SizeBytes)
	}
	size := int64(r.SizeBytes)

	var oldSize int64
	var oldSHA, oldPath string
	err = tx.QueryRowContext(ctx,
		`SELECT size_bytes, kei_sha256, local_path FROM resources WHERE library = ? AND asset_id = ? AND version = ?`,
		r.Library, r.AssetID, r.Version).Scan(&oldSize, &oldSHA, &oldPath)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `
			INSERT INTO resources (library, asset_id, version, filename, local_path, media_type, size_bytes, kei_sha256,
				requires_structure, requires_probe)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Library, r.AssetID, r.Version, r.Filename, r.LocalPath, r.MediaType, size, r.SHA256,
			r.RequiresStructure, r.RequiresProbe)
		if err != nil {
			return res, fmt.Errorf("insert rendition %s/%s/%s: %w", r.Library, r.AssetID, r.Version, err)
		}
		res.created = true
		return res, nil
	case err != nil:
		return res, fmt.Errorf("read rendition %s/%s/%s: %w", r.Library, r.AssetID, r.Version, err)
	}

	res.changed = oldSize != size || oldSHA != r.SHA256
	if oldPath != r.LocalPath {
		res.movedFrom = oldPath
	}
	reset := res.changed || res.movedFrom != ""
	_, err = tx.ExecContext(ctx, `
		UPDATE resources SET
			filename = ?, local_path = ?, media_type = ?, size_bytes = ?, kei_sha256 = ?,
			requires_structure = ?, requires_probe = ?,
			verified_content_at   = CASE WHEN ? THEN NULL ELSE verified_content_at END,
			verified_structure_at = CASE WHEN ? THEN NULL ELSE verified_structure_at END,
			verified_probe_at     = CASE WHEN ? THEN NULL ELSE verified_probe_at END,
			last_scrub_at         = CASE WHEN ? THEN NULL ELSE last_scrub_at END
		WHERE library = ? AND asset_id = ? AND version = ?`,
		r.Filename, r.LocalPath, r.MediaType, size, r.SHA256,
		r.RequiresStructure, r.RequiresProbe,
		reset, reset, reset, reset,
		r.Library, r.AssetID, r.Version)
	if err != nil {
		return res, fmt.Errorf("update rendition %s/%s/%s: %w", r.Library, r.AssetID, r.Version, err)
	}
	return res, nil
}

// recomputeState derives the asset state from its renditions. Quarantine is sticky.
func recomputeState(ctx context.Context, tx *sql.Tx, library, assetID string, group []Rendition, ts string) error {
	var current AssetState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM assets WHERE library = ? AND asset_id = ?`,
		library, assetID).Scan(&current); err != nil {
		return fmt.Errorf("read state %s/%s: %w", library, assetID, err)
	}
	if current == StateQuarantined {
		return nil
	}

	next := StateDownloaded
	for _, r := range group {
		if !r.Downloaded {
			next = StateDiscovered
			break
		}
	}
	if next == StateDownloaded {
		var unverified int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM resources
			WHERE library = ? AND asset_id = ? AND NOT `+verifiedPredicate,
			library, assetID).Scan(&unverified); err != nil {
			return fmt.Errorf("count unverified %s/%s: %w", library, assetID, err)
		}
		if unverified == 0 {
			next = StateVerified
		}
	}
	if next == current {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE assets SET state = ?, state_changed_at = ? WHERE library = ? AND asset_id = ?`,
		next, ts, library, assetID); err != nil {
		return fmt.Errorf("set state %s/%s: %w", library, assetID, err)
	}
	return appendEvent(ctx, tx, ts, library, assetID, EventStateChanged, map[string]any{"from": current, "to": next})
}

// verifiedPredicate is true for a resource that passed every tier it requires:
// content always, plus structure and probe where the rendition requires them.
const verifiedPredicate = `(verified_content_at IS NOT NULL
	AND (requires_structure = 0 OR verified_structure_at IS NOT NULL)
	AND (requires_probe = 0 OR verified_probe_at IS NOT NULL))`

func appendEvent(ctx context.Context, tx *sql.Tx, ts, library, assetID, kind string, detail map[string]any) error {
	payload, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode event %s: %w", kind, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO events (at, library, asset_id, kind, detail) VALUES (?, ?, ?, ?, ?)`,
		ts, library, assetID, kind, string(payload)); err != nil {
		return fmt.Errorf("append event %s: %w", kind, err)
	}
	return nil
}
