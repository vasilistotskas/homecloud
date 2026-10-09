package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// A repair downloads the failing renditions of a quarantined asset again:
//
//  1. RequestRepair (operator) records one row per failing rendition.
//  2. The agent moves each file aside and records it (RecordSetAside), so the
//     downloader finds the file missing and marks it for download again.
//  3. Ingest completes the repair once the downloader reports every rendition
//     downloaded again after it was set aside: the asset leaves quarantine and
//     every tier runs again on the new files.
//
// When the downloader will not download a rendition again, the agent puts the
// files back and abandons the repair (AbandonRepair); the asset stays
// quarantined.

// RequestRepair asks for the failing renditions of a quarantined asset to be
// downloaded again. The failures stay recorded until the repair completes.
func (db *DB) RequestRepair(ctx context.Context, library, assetID string, now time.Time) error {
	ts := formatTime(now)
	return db.inTx(ctx, "request repair", func(tx *sql.Tx) error {
		if err := requireRepairable(ctx, tx, library, assetID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO rendition_repairs (library, asset_id, version, requested_at)
			SELECT library, asset_id, version, ? FROM resources
			WHERE library = ? AND asset_id = ? AND failure IS NOT NULL`, ts, library, assetID)
		if err != nil {
			return fmt.Errorf("request repair %s/%s: %w", library, assetID, err)
		}
		if n, nerr := res.RowsAffected(); nerr != nil || n == 0 {
			return errors.Join(fmt.Errorf("request repair %s/%s: no failing rendition recorded", library, assetID), nerr)
		}
		versions, err := repairVersions(ctx, tx, library, assetID)
		if err != nil {
			return err
		}
		return appendEvent(ctx, tx, ts, library, assetID, EventRepairRequested, map[string]any{"versions": versions})
	})
}

// requireRepairable reports ErrNotQuarantined or ErrRepairInProgress unless
// the asset is quarantined with no repair under way.
func requireRepairable(ctx context.Context, tx *sql.Tx, library, assetID string) error {
	var state AssetState
	var repairs int
	err := tx.QueryRowContext(ctx, `
		SELECT a.state, (SELECT count(*) FROM rendition_repairs r WHERE r.library = a.library AND r.asset_id = a.asset_id)
		FROM assets a WHERE a.library = ? AND a.asset_id = ?`, library, assetID).Scan(&state, &repairs)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("asset %s/%s: %w", library, assetID, ErrNotFound)
	case err != nil:
		return fmt.Errorf("read asset %s/%s: %w", library, assetID, err)
	case state != StateQuarantined:
		return fmt.Errorf("asset %s/%s: %w", library, assetID, ErrNotQuarantined)
	case repairs > 0:
		return fmt.Errorf("asset %s/%s: %w", library, assetID, ErrRepairInProgress)
	}
	return nil
}

func repairVersions(ctx context.Context, tx *sql.Tx, library, assetID string) (out []string, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT version FROM rendition_repairs
		WHERE library = ? AND asset_id = ? ORDER BY version`, library, assetID)
	if err != nil {
		return nil, fmt.Errorf("read repair of %s/%s: %w", library, assetID, err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan repair of %s/%s: %w", library, assetID, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repair of %s/%s: %w", library, assetID, err)
	}
	return out, nil
}

// RepairRendition is one rendition under repair.
type RepairRendition struct {
	Key
	// LocalPath is where the downloader keeps the rendition.
	LocalPath string
	Repair
}

// Repairs lists every rendition under repair, oldest request first.
func (db *DB) Repairs(ctx context.Context) (out []RepairRendition, err error) {
	rows, err := db.r.QueryContext(ctx, `
		SELECT p.library, p.asset_id, p.version, r.local_path, p.requested_at, p.set_aside_at, coalesce(p.kept_as, '')
		FROM rendition_repairs p JOIN resources r USING (library, asset_id, version)
		ORDER BY p.requested_at, p.library, p.asset_id, p.version`)
	if err != nil {
		return nil, fmt.Errorf("query repairs: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var p RepairRendition
		var requested string
		var setAside sql.NullString
		if err = rows.Scan(&p.Library, &p.AssetID, &p.Version, &p.LocalPath, &requested, &setAside, &p.KeptAs); err != nil {
			return nil, fmt.Errorf("scan repair: %w", err)
		}
		if p.RequestedAt, err = parseTime(requested); err != nil {
			return nil, fmt.Errorf("repair %v: %w", p.Key, err)
		}
		if p.SetAsideAt, err = parseNullTime(setAside); err != nil {
			return nil, fmt.Errorf("repair %v: %w", p.Key, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repairs: %w", err)
	}
	return out, nil
}

// RecordSetAside records that the file of k left its recorded path: moved to
// keptAs (relative to storage.root, '/'), or already missing when keptAs is
// empty.
func (db *DB) RecordSetAside(ctx context.Context, k Key, keptAs string, now time.Time) error {
	ts := formatTime(now)
	return db.inTx(ctx, "record set aside", func(tx *sql.Tx) error {
		var from string
		if err := tx.QueryRowContext(ctx, `SELECT local_path FROM resources WHERE library = ? AND asset_id = ? AND version = ?`,
			k.Library, k.AssetID, k.Version).Scan(&from); err != nil {
			return fmt.Errorf("read rendition %v: %w", k, err)
		}
		res, err := tx.ExecContext(ctx, `UPDATE rendition_repairs SET set_aside_at = ?, kept_as = nullif(?, '')
			WHERE library = ? AND asset_id = ? AND version = ? AND set_aside_at IS NULL`,
			ts, keptAs, k.Library, k.AssetID, k.Version)
		if err != nil {
			return fmt.Errorf("record set aside %v: %w", k, err)
		}
		if n, nerr := res.RowsAffected(); nerr != nil || n != 1 {
			return errors.Join(fmt.Errorf("record set aside %v: no repair waiting for it", k), nerr)
		}
		return appendEvent(ctx, tx, ts, k.Library, k.AssetID, EventRenditionSetAside,
			map[string]any{"version": k.Version, "from": from, "kept_as": keptAs})
	})
}

// AbandonRepair ends the repair of an asset whose files the downloader will
// not download again; the caller has put the set-aside files back. The asset
// stays quarantined with its failures.
func (db *DB) AbandonRepair(ctx context.Context, library, assetID, reason string, now time.Time) error {
	ts := formatTime(now)
	return db.inTx(ctx, "abandon repair", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM rendition_repairs WHERE library = ? AND asset_id = ?`, library, assetID)
		if err != nil {
			return fmt.Errorf("abandon repair %s/%s: %w", library, assetID, err)
		}
		if n, nerr := res.RowsAffected(); nerr != nil || n == 0 {
			return errors.Join(fmt.Errorf("abandon repair %s/%s: no repair recorded", library, assetID), nerr)
		}
		return appendEvent(ctx, tx, ts, library, assetID, EventRepairAbandoned, map[string]any{"reason": reason})
	})
}

// completeRepair ends the repair of an asset once the downloader reports
// every rendition of it downloaded again after it was set aside (group is
// the asset's renditions from this ingest). The renditions lose their
// failures and verification evidence, and the asset leaves quarantine.
func completeRepair(ctx context.Context, tx *sql.Tx, library, assetID string, group []Rendition, ts string) (bool, error) {
	pending, err := pendingRepair(ctx, tx, library, assetID)
	if err != nil || len(pending) == 0 {
		return false, err
	}
	byVersion := make(map[string]Rendition, len(group))
	for _, r := range group {
		byVersion[r.Version] = r
	}
	versions := make([]string, 0, len(pending))
	for version, setAside := range pending {
		if setAside == nil || !DownloadedSince(byVersion[version], *setAside) {
			return false, nil
		}
		versions = append(versions, version)
	}

	if _, err = tx.ExecContext(ctx, `
		UPDATE resources SET failure = NULL, verified_content_at = NULL, verified_structure_at = NULL,
			verified_probe_at = NULL, last_scrub_at = NULL
		WHERE (library, asset_id, version) IN (SELECT library, asset_id, version FROM rendition_repairs
		                                       WHERE library = ? AND asset_id = ?)`, library, assetID); err != nil {
		return false, fmt.Errorf("reset repaired renditions of %s/%s: %w", library, assetID, err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM rendition_repairs WHERE library = ? AND asset_id = ?`, library, assetID); err != nil {
		return false, fmt.Errorf("finish repair of %s/%s: %w", library, assetID, err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE assets SET state = ?, state_changed_at = ?
		WHERE library = ? AND asset_id = ? AND state = ?`, StateDownloaded, ts, library, assetID, StateQuarantined)
	if err != nil {
		return false, fmt.Errorf("release repaired %s/%s: %w", library, assetID, err)
	}
	if n, nerr := res.RowsAffected(); nerr != nil || n != 1 {
		return false, errors.Join(fmt.Errorf("release repaired %s/%s: %w", library, assetID, ErrNotQuarantined), nerr)
	}
	return true, appendEvent(ctx, tx, ts, library, assetID, EventRepaired, map[string]any{"versions": versions})
}

// pendingRepair maps each rendition under repair of the asset to when it was
// set aside (nil when it is not yet).
func pendingRepair(ctx context.Context, tx *sql.Tx, library, assetID string) (out map[string]*time.Time, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT version, set_aside_at FROM rendition_repairs
		WHERE library = ? AND asset_id = ?`, library, assetID)
	if err != nil {
		return nil, fmt.Errorf("read repair of %s/%s: %w", library, assetID, err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = map[string]*time.Time{}
	for rows.Next() {
		var version string
		var setAside sql.NullString
		if err = rows.Scan(&version, &setAside); err != nil {
			return nil, fmt.Errorf("scan repair of %s/%s: %w", library, assetID, err)
		}
		if out[version], err = parseNullTime(setAside); err != nil {
			return nil, fmt.Errorf("repair of %s/%s: %w", library, assetID, err)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repair of %s/%s: %w", library, assetID, err)
	}
	return out, nil
}

// DownloadedSince reports whether r was downloaded at or after setAside. The
// downloader records whole seconds, so setAside is truncated to the second;
// the earlier download it replaces is from before the file was quarantined.
func DownloadedSince(r Rendition, setAside time.Time) bool {
	return r.Downloaded && r.DownloadedAt != nil && !r.DownloadedAt.Before(setAside.Truncate(time.Second))
}
