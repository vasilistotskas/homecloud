package ledger

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"
)

// AssetKind classifies an asset for browsing.
type AssetKind string

// Asset kinds. A Live Photo is an image with its short video.
const (
	KindPhoto     AssetKind = "photo"
	KindLivePhoto AssetKind = "live"
	KindVideo     AssetKind = "video"
)

// LibraryQuery selects a page of assets, newest first.
type LibraryQuery struct {
	// Kind restricts the page to one kind; empty means all kinds.
	Kind AssetKind
	// HiddenDir is the directory (with trailing separator) that holds hidden
	// assets; Hidden selects the assets inside it instead of those outside.
	HiddenDir string
	Hidden    bool
	// After continues from the last asset of the previous page; nil starts at the newest.
	After *LibraryCursor
	Limit int
}

// LibraryCursor is the position after the last asset of a page.
type LibraryCursor struct {
	CreatedAt string `json:"c"`
	AssetID   string `json:"a"`
}

// LibraryAsset is one asset as the library browser shows it.
type LibraryAsset struct {
	Library, AssetID string
	Kind             AssetKind
	CreatedAt        *time.Time
	// Filename is the original rendition's file name.
	Filename string
	State    AssetState
	// Display is the rendition to show: the edited version when there is one,
	// the picture of a Live Photo, the video of a video.
	Display RenditionFile
	Cursor  LibraryCursor
}

// RenditionFile locates one rendition's file and content.
type RenditionFile struct {
	Version, LocalPath, SHA256, MediaType string
}

// LibraryPage returns a page of assets for the browser.
func (db *DB) LibraryPage(ctx context.Context, q LibraryQuery) (out []LibraryAsset, err error) {
	if q.Limit <= 0 {
		return nil, errors.New("library page: limit must be positive")
	}
	after := LibraryCursor{}
	hasAfter := q.After != nil
	if hasAfter {
		after = *q.After
	}
	rows, err := db.r.QueryContext(ctx, `
		WITH k AS (
			SELECT a.library, a.asset_id, coalesce(a.created_at, '') AS created_at, a.state,
			       max(r.media_type = 'live_photo_image') AS live,
			       max(r.media_type = 'video') AS video,
			       max(substr(r.local_path, 1, length(?1)) = ?1) AS hidden
			FROM assets a JOIN resources r USING (library, asset_id)
			GROUP BY a.library, a.asset_id)
		SELECT library, asset_id, created_at, state,
		       CASE WHEN live THEN 'live' WHEN video THEN 'video' ELSE 'photo' END AS kind
		FROM k
		WHERE hidden = ?2
		  AND (?3 = '' OR ?3 = CASE WHEN live THEN 'live' WHEN video THEN 'video' ELSE 'photo' END)
		  AND (NOT ?4 OR (created_at, asset_id) < (?5, ?6))
		ORDER BY created_at DESC, asset_id DESC
		LIMIT ?7`,
		q.HiddenDir, q.Hidden, string(q.Kind), hasAfter, after.CreatedAt, after.AssetID, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("query library: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var a LibraryAsset
		var created string
		if scanErr := rows.Scan(&a.Library, &a.AssetID, &created, &a.State, &a.Kind); scanErr != nil {
			return nil, fmt.Errorf("scan library asset: %w", scanErr)
		}
		a.Cursor = LibraryCursor{CreatedAt: created, AssetID: a.AssetID}
		if created != "" {
			t, perr := parseTime(created)
			if perr != nil {
				return nil, perr
			}
			a.CreatedAt = &t
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate library: %w", err)
	}
	for i := range out {
		if err := db.fillDisplay(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// fillDisplay picks the rendition to show and the original's file name.
func (db *DB) fillDisplay(ctx context.Context, a *LibraryAsset) (err error) {
	rows, err := db.r.QueryContext(ctx, `SELECT version, local_path, kei_sha256, media_type, filename
		FROM resources WHERE library = ? AND asset_id = ?`, a.Library, a.AssetID)
	if err != nil {
		return fmt.Errorf("renditions of %s/%s: %w", a.Library, a.AssetID, err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var files []RenditionFile
	for rows.Next() {
		var f RenditionFile
		var filename string
		if scanErr := rows.Scan(&f.Version, &f.LocalPath, &f.SHA256, &f.MediaType, &filename); scanErr != nil {
			return fmt.Errorf("scan rendition: %w", scanErr)
		}
		if f.Version == "original" {
			a.Filename = filename
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate renditions: %w", err)
	}
	display, ok := pickDisplay(a.Kind, files)
	if !ok {
		return fmt.Errorf("asset %s/%s has no displayable rendition", a.Library, a.AssetID)
	}
	a.Display = display
	return nil
}

// pickDisplay prefers the edited ("adjusted") version of the medium that
// represents the asset, then the original.
func pickDisplay(kind AssetKind, files []RenditionFile) (RenditionFile, bool) {
	wanted := map[AssetKind]string{KindPhoto: "photo", KindLivePhoto: "live_photo_image", KindVideo: "video"}[kind]
	for _, version := range []string{"adjusted", "original"} {
		for _, f := range files {
			if f.MediaType == wanted && f.Version == version {
				return f, true
			}
		}
	}
	return RenditionFile{}, false
}

// Rendition returns one rendition's file, or ErrNotFound.
func (db *DB) Rendition(ctx context.Context, library, assetID, version string) (RenditionFile, error) {
	f := RenditionFile{Version: version}
	err := db.r.QueryRowContext(ctx, `SELECT local_path, kei_sha256, media_type FROM resources
		WHERE library = ? AND asset_id = ? AND version = ?`, library, assetID, version).
		Scan(&f.LocalPath, &f.SHA256, &f.MediaType)
	if errors.Is(err, sql.ErrNoRows) {
		return RenditionFile{}, fmt.Errorf("rendition %s/%s/%s: %w", library, assetID, version, ErrNotFound)
	}
	if err != nil {
		return RenditionFile{}, fmt.Errorf("rendition %s/%s/%s: %w", library, assetID, version, err)
	}
	return f, nil
}

// EncodeCursor and DecodeCursor turn a cursor into an opaque page token body.
func EncodeCursor(c LibraryCursor) ([]byte, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode cursor: %w", err)
	}
	return b, nil
}

// DecodeCursor parses a cursor produced by EncodeCursor.
func DecodeCursor(b []byte) (LibraryCursor, error) {
	var c LibraryCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return LibraryCursor{}, fmt.Errorf("decode cursor: %w", err)
	}
	return c, nil
}
