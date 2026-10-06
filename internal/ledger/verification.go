package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Key identifies one rendition.
type Key struct {
	Library, AssetID, Version string
}

// PendingResource is a rendition that still lacks required verification.
type PendingResource struct {
	Key
	LocalPath string
	MediaType string
	SizeBytes uint64
	SHA256    string
	// NeedsContent/NeedsStructure/NeedsProbe report which tiers are outstanding.
	NeedsContent, NeedsStructure, NeedsProbe bool
}

// PendingVerification returns up to limit renditions (all when limit < 0) of
// downloaded assets that are missing a required tier, oldest state change first.
func (db *DB) PendingVerification(ctx context.Context, limit int) (out []PendingResource, err error) {
	rows, err := db.r.QueryContext(ctx, `
		SELECT r.library, r.asset_id, r.version, r.local_path, r.media_type, r.size_bytes, r.kei_sha256,
		       r.verified_content_at IS NULL,
		       r.requires_structure = 1 AND r.verified_structure_at IS NULL,
		       r.requires_probe = 1 AND r.verified_probe_at IS NULL
		FROM resources r JOIN assets a USING (library, asset_id)
		WHERE a.state = ? AND r.failure IS NULL AND NOT `+verifiedPredicate+`
		ORDER BY a.state_changed_at, r.library, r.asset_id, r.version
		LIMIT ?`, StateDownloaded, limit)
	if err != nil {
		return nil, fmt.Errorf("query pending verification: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var p PendingResource
		var size int64
		if err := rows.Scan(&p.Library, &p.AssetID, &p.Version, &p.LocalPath, &p.MediaType, &size, &p.SHA256,
			&p.NeedsContent, &p.NeedsStructure, &p.NeedsProbe); err != nil {
			return nil, fmt.Errorf("scan pending verification: %w", err)
		}
		p.SizeBytes = uint64(size) //nolint:gosec // CHECK (size_bytes >= 0) in schema
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending verification: %w", err)
	}
	return out, nil
}

// Tier names a verification tier.
type Tier string

// Verification tiers.
const (
	TierContent   Tier = "content"
	TierStructure Tier = "structure"
	TierProbe     Tier = "probe"
)

// passStatement returns the complete UPDATE that records a pass for the tier.
// Each statement is a constant so no SQL is assembled at runtime.
func (t Tier) passStatement() (string, error) {
	switch t {
	case TierContent:
		return `UPDATE resources SET verified_content_at = ?, last_scrub_at = ?
			WHERE library = ? AND asset_id = ? AND version = ?`, nil
	case TierStructure:
		return `UPDATE resources SET verified_structure_at = ?, last_scrub_at = ?
			WHERE library = ? AND asset_id = ? AND version = ?`, nil
	case TierProbe:
		return `UPDATE resources SET verified_probe_at = ?, last_scrub_at = ?
			WHERE library = ? AND asset_id = ? AND version = ?`, nil
	default:
		return "", fmt.Errorf("unknown tier %q", t)
	}
}

// failStatement returns the complete UPDATE that records a failure for the tier.
// It also clears the tier's pass: a file that no longer matches is not
// verified, and releasing the quarantine must run the tier again.
func (t Tier) failStatement() (string, error) {
	switch t {
	case TierContent:
		return `UPDATE resources SET failure = ?, verified_content_at = NULL
			WHERE library = ? AND asset_id = ? AND version = ?`, nil
	case TierStructure:
		return `UPDATE resources SET failure = ?, verified_structure_at = NULL
			WHERE library = ? AND asset_id = ? AND version = ?`, nil
	case TierProbe:
		return `UPDATE resources SET failure = ?, verified_probe_at = NULL
			WHERE library = ? AND asset_id = ? AND version = ?`, nil
	default:
		return "", fmt.Errorf("unknown tier %q", t)
	}
}

// RecordPass marks tier as passed for k and promotes the asset to verified
// once every rendition satisfies its required tiers.
func (db *DB) RecordPass(ctx context.Context, k Key, tier Tier, now time.Time) (err error) {
	stmt, err := tier.passStatement()
	if err != nil {
		return err
	}
	ts := formatTime(now)
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record pass: begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()

	res, err := tx.ExecContext(ctx, stmt, ts, ts, k.Library, k.AssetID, k.Version)
	if err != nil {
		return fmt.Errorf("record %s pass for %v: %w", tier, k, err)
	}
	if n, nerr := res.RowsAffected(); nerr != nil || n != 1 {
		return errors.Join(fmt.Errorf("record %s pass for %v: rendition not found", tier, k), nerr)
	}

	var current AssetState
	var unverified int
	if err = tx.QueryRowContext(ctx, `
		SELECT a.state, (SELECT count(*) FROM resources r
		                 WHERE r.library = a.library AND r.asset_id = a.asset_id AND NOT `+verifiedPredicate+`)
		FROM assets a WHERE a.library = ? AND a.asset_id = ?`, k.Library, k.AssetID).Scan(&current, &unverified); err != nil {
		return fmt.Errorf("read asset %s/%s: %w", k.Library, k.AssetID, err)
	}
	if current == StateDownloaded && unverified == 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE assets SET state = ?, state_changed_at = ? WHERE library = ? AND asset_id = ?`,
			StateVerified, ts, k.Library, k.AssetID); err != nil {
			return fmt.Errorf("promote %s/%s: %w", k.Library, k.AssetID, err)
		}
		if err = appendEvent(ctx, tx, ts, k.Library, k.AssetID, "state_changed",
			map[string]any{"from": current, "to": StateVerified}); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("record pass: commit: %w", err)
	}
	return nil
}

// RecordMismatch records a verification mismatch for k and quarantines its asset.
func (db *DB) RecordMismatch(ctx context.Context, k Key, tier Tier, reason string, now time.Time) (err error) {
	stmt, err := tier.failStatement()
	if err != nil {
		return err
	}
	ts := formatTime(now)
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record mismatch: begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()

	failure := Failure{Tier: tier, Reason: reason}.String()
	res, err := tx.ExecContext(ctx, stmt, failure, k.Library, k.AssetID, k.Version)
	if err != nil {
		return fmt.Errorf("record mismatch for %v: %w", k, err)
	}
	if n, nerr := res.RowsAffected(); nerr != nil || n != 1 {
		return errors.Join(fmt.Errorf("record mismatch for %v: rendition not found", k), nerr)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE assets SET state = ?, state_changed_at = ? WHERE library = ? AND asset_id = ?`,
		StateQuarantined, ts, k.Library, k.AssetID); err != nil {
		return fmt.Errorf("quarantine %s/%s: %w", k.Library, k.AssetID, err)
	}
	if err = appendEvent(ctx, tx, ts, k.Library, k.AssetID, "quarantined",
		map[string]any{"version": k.Version, "tier": tier, "reason": reason}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("record mismatch: commit: %w", err)
	}
	return nil
}

// ReleaseQuarantine is the operator action that lifts a quarantine after review.
// The asset returns to downloaded with its failures cleared, so every required
// tier runs again before it can count as verified. The reason is audited.
func (db *DB) ReleaseQuarantine(ctx context.Context, library, assetID, reason string, now time.Time) (err error) {
	if strings.TrimSpace(reason) == "" {
		return ErrReasonRequired
	}
	ts := formatTime(now)
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("release quarantine: begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()

	res, err := tx.ExecContext(ctx, `UPDATE assets SET state = ?, state_changed_at = ?
		WHERE library = ? AND asset_id = ? AND state = ?`, StateDownloaded, ts, library, assetID, StateQuarantined)
	if err != nil {
		return fmt.Errorf("release quarantine %s/%s: %w", library, assetID, err)
	}
	if n, nerr := res.RowsAffected(); nerr != nil || n != 1 {
		return errors.Join(fmt.Errorf("release quarantine %s/%s: %w", library, assetID, ErrNotQuarantined), nerr)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE resources SET failure = NULL WHERE library = ? AND asset_id = ?`,
		library, assetID); err != nil {
		return fmt.Errorf("clear failures %s/%s: %w", library, assetID, err)
	}
	if err = appendEvent(ctx, tx, ts, library, assetID, "quarantine_released", map[string]any{"reason": reason}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("release quarantine: commit: %w", err)
	}
	return nil
}

// Failure is a recorded verification failure of one rendition.
type Failure struct {
	Tier   Tier
	Reason string
}

func (f Failure) String() string { return string(f.Tier) + ": " + f.Reason }

// parseFailure decodes the stored "tier: reason" form written by RecordMismatch.
func parseFailure(s string) (Failure, error) {
	tier, reason, ok := strings.Cut(s, ": ")
	if !ok {
		return Failure{}, fmt.Errorf("malformed failure %q", s)
	}
	f := Failure{Tier: Tier(tier), Reason: reason}
	if _, err := f.Tier.passStatement(); err != nil {
		return Failure{}, fmt.Errorf("failure %q: %w", s, err)
	}
	return f, nil
}

// QuarantinedRendition is a failing rendition of a quarantined asset.
type QuarantinedRendition struct {
	Key
	Filename, LocalPath string
	// QuarantinedAt is when the asset entered quarantine.
	QuarantinedAt time.Time
	Failure       Failure
}

// Quarantined lists the failing renditions of all quarantined assets.
func (db *DB) Quarantined(ctx context.Context) (out []QuarantinedRendition, err error) {
	rows, err := db.r.QueryContext(ctx, `
		SELECT r.library, r.asset_id, r.version, r.filename, r.local_path, a.state_changed_at, r.failure
		FROM resources r JOIN assets a USING (library, asset_id)
		WHERE a.state = ? AND r.failure IS NOT NULL
		ORDER BY a.state_changed_at DESC, r.library, r.asset_id, r.version`, StateQuarantined)
	if err != nil {
		return nil, fmt.Errorf("query quarantined: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var q QuarantinedRendition
		var at, failure string
		if scanErr := rows.Scan(&q.Library, &q.AssetID, &q.Version, &q.Filename, &q.LocalPath, &at, &failure); scanErr != nil {
			return nil, fmt.Errorf("scan quarantined: %w", scanErr)
		}
		if q.QuarantinedAt, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("quarantined %v: %w", q.Key, err)
		}
		if q.Failure, err = parseFailure(failure); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate quarantined: %w", err)
	}
	return out, nil
}

// StateCounts returns the number of assets in each state.
func (db *DB) StateCounts(ctx context.Context) (out map[AssetState]int, err error) {
	rows, err := db.r.QueryContext(ctx, `SELECT state, count(*) FROM assets GROUP BY state`)
	if err != nil {
		return nil, fmt.Errorf("count states: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = map[AssetState]int{}
	for rows.Next() {
		var s AssetState
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, fmt.Errorf("scan state count: %w", err)
		}
		out[s] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate state counts: %w", err)
	}
	return out, nil
}
