package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ArchivedAsset is an asset with the evidence that decides whether its
// iCloud copy is no longer needed (policy.Judge).
type ArchivedAsset struct {
	LibraryAsset
	AddedAt *time.Time
	// UnderRepair: an operator asked for its damaged files to be downloaded again.
	UnderRepair bool
	Renditions  []RenditionEvidence
}

// RenditionEvidence is one downloaded rendition and its verification record.
type RenditionEvidence struct {
	Version, LocalPath, MediaType    string
	SizeBytes                        int64
	Failed                           bool
	RequiresStructure, RequiresProbe bool
	// Passed holds the time each tier passed; absent tiers have not passed.
	Passed map[Tier]time.Time
	// ConfirmedAt is when the file's checksum was last confirmed: by its
	// verification, or by a later scrub. Nil while unconfirmed.
	ConfirmedAt *time.Time
}

// Original is the original rendition of the asset's own medium (the picture
// of a Live Photo), the file the asset was taken as; ok is false while it is
// not on disk.
func (a ArchivedAsset) Original() (RenditionFile, bool) {
	files := make([]RenditionFile, 0, len(a.Renditions))
	for _, r := range a.Renditions {
		files = append(files, RenditionFile{Version: r.Version, LocalPath: r.LocalPath, MediaType: r.MediaType})
	}
	return pickRendition(a.Kind, files, VersionOriginal)
}

// SizeBytes is the size of every archived file of the asset.
func (a ArchivedAsset) SizeBytes() int64 {
	var n int64
	for _, r := range a.Renditions {
		n += r.SizeBytes
	}
	return n
}

// EachArchivedAsset calls fn for every asset with at least one rendition on
// disk, newest first in the library's order (capture time, then asset id),
// stopping at the first error.
func (db *DB) EachArchivedAsset(ctx context.Context, fn func(ArchivedAsset) error) (err error) {
	rows, err := db.r.QueryContext(ctx, `
		SELECT a.library, a.asset_id, coalesce(a.created_at, ''), a.added_at, a.state,
		       EXISTS (SELECT 1 FROM rendition_repairs p WHERE p.library = a.library AND p.asset_id = a.asset_id),
		       r.version, r.filename, r.local_path, r.media_type, r.size_bytes, r.kei_sha256, r.failure IS NOT NULL,
		       r.requires_structure, r.requires_probe,
		       r.verified_content_at, r.verified_structure_at, r.verified_probe_at, r.last_scrub_at
		FROM assets a JOIN resources r USING (library, asset_id)
		ORDER BY coalesce(a.created_at, '') DESC, a.asset_id DESC, a.library, r.version`)
	if err != nil {
		return fmt.Errorf("query archived assets: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	var cur *ArchivedAsset
	var files []RenditionFile
	flush := func() error {
		if cur == nil {
			return nil
		}
		cur.Kind = kindOf(cur.Renditions)
		if display, ok := pickDisplay(cur.Kind, files); ok {
			cur.Display = &display
		}
		a := *cur
		cur, files = nil, nil
		return fn(a)
	}
	for rows.Next() {
		var (
			library, assetID, created, filename, sha string
			added, content, structure, probe, scrub  sql.NullString
			state                                    AssetState
			repair                                   bool
			r                                        RenditionEvidence
		)
		if scanErr := rows.Scan(&library, &assetID, &created, &added, &state, &repair,
			&r.Version, &filename, &r.LocalPath, &r.MediaType, &r.SizeBytes, &sha, &r.Failed,
			&r.RequiresStructure, &r.RequiresProbe, &content, &structure, &probe, &scrub); scanErr != nil {
			return fmt.Errorf("scan archived asset: %w", scanErr)
		}
		if cur == nil || cur.Library != library || cur.AssetID != assetID {
			if flushErr := flush(); flushErr != nil {
				return flushErr
			}
			cur = &ArchivedAsset{
				LibraryAsset: LibraryAsset{
					Library: library, AssetID: assetID, State: state,
					Cursor: LibraryCursor{CreatedAt: created, AssetID: assetID},
				},
				UnderRepair: repair,
			}
			if created != "" {
				t, perr := parseTime(created)
				if perr != nil {
					return perr
				}
				cur.CreatedAt = &t
			}
			if cur.AddedAt, err = parseNullTime(added); err != nil {
				return err
			}
		}
		if r.Passed, err = passes(content, structure, probe); err != nil {
			return err
		}
		if r.ConfirmedAt, err = parseNullTime(scrub); err != nil {
			return err
		}
		if r.Version == VersionOriginal && cur.Filename == "" {
			cur.Filename = filename
		}
		cur.Renditions = append(cur.Renditions, r)
		files = append(files, RenditionFile{Version: r.Version, LocalPath: r.LocalPath, SHA256: sha, MediaType: r.MediaType})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate archived assets: %w", err)
	}
	return flush()
}

// passes maps each tier's recorded pass time.
func passes(content, structure, probe sql.NullString) (map[Tier]time.Time, error) {
	out := map[Tier]time.Time{}
	for tier, v := range map[Tier]sql.NullString{TierContent: content, TierStructure: structure, TierProbe: probe} {
		t, err := parseNullTime(v)
		if err != nil {
			return nil, err
		}
		if t != nil {
			out[tier] = *t
		}
	}
	return out, nil
}

// kindOf classifies an asset by its renditions as LibraryPage does: any Live
// Photo part makes a Live Photo, else any video a video.
func kindOf(rs []RenditionEvidence) AssetKind {
	kind := KindPhoto
	for _, r := range rs {
		switch r.MediaType {
		case "live_photo_image", "live_photo_video":
			return KindLivePhoto
		case "video":
			kind = KindVideo
		}
	}
	return kind
}
