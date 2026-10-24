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

// LibraryFilter selects the assets a library view shows.
type LibraryFilter struct {
	// Kind restricts the view to one kind; empty means all kinds.
	Kind AssetKind
	// HiddenDir is the directory (with trailing separator) that holds hidden
	// assets; Hidden selects the assets inside it instead of those outside.
	HiddenDir string
	Hidden    bool
	// Taken restricts the view to assets taken within one of these spans;
	// nil means any time.
	Taken []TimeSpan
	// TakenBefore restricts the view to assets taken before it; nil means no
	// bound. With Taken or TakenBefore set, assets without a capture time are
	// left out.
	TakenBefore *time.Time
}

// TimeSpan is the capture times from From up to, not including, To.
type TimeSpan struct{ From, To time.Time }

// LibraryQuery selects a page of a library view, newest first.
type LibraryQuery struct {
	LibraryFilter
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
	// the picture of a Live Photo, the video of a video. It is nil while no such
	// rendition is on disk yet, e.g. a Live Photo whose video arrived first.
	Display *RenditionFile
	Cursor  LibraryCursor
}

// RenditionFile locates one rendition's file and content.
type RenditionFile struct {
	Version, LocalPath, SHA256, MediaType string
}

// libraryView is the assets a LibraryFilter selects, with their kind, bound
// by libraryViewArgs. Capture times are compared as instants
// (unixepoch(..., 'subsec')): the stored RFC 3339 text has a varying number of
// fractional digits, so its text order is not chronological within a second.
const libraryView = `
	WITH k AS (
		SELECT a.library, a.asset_id, coalesce(a.created_at, '') AS created_at, a.state,
		       unixepoch(a.created_at, 'subsec') AS taken,
		       max(r.media_type IN ('live_photo_image', 'live_photo_video')) AS live,
		       max(r.media_type = 'video') AS video,
		       max(substr(r.local_path, 1, length(:hidden_dir)) = :hidden_dir) AS hidden
		FROM assets a JOIN resources r USING (library, asset_id)
		GROUP BY a.library, a.asset_id),
	v AS (
		SELECT library, asset_id, created_at, state,
		       CASE WHEN live THEN 'live' WHEN video THEN 'video' ELSE 'photo' END AS kind
		FROM k
		WHERE hidden = :hidden
		  AND (:kind = '' OR :kind = CASE WHEN live THEN 'live' WHEN video THEN 'video' ELSE 'photo' END)
		  AND (:taken IS NULL OR EXISTS (
		        SELECT 1 FROM json_each(:taken) s
		        WHERE k.taken >= s.value ->> '$[0]' AND k.taken < s.value ->> '$[1]'))
		  AND (:taken_before IS NULL OR k.taken < :taken_before))`

// libraryViewArgs binds libraryView's parameters for f.
func libraryViewArgs(f LibraryFilter) ([]any, error) {
	var taken any
	if f.Taken != nil {
		spans := make([][2]float64, 0, len(f.Taken))
		for _, s := range f.Taken {
			if !s.From.Before(s.To) {
				return nil, fmt.Errorf("library view: empty capture span %v-%v", s.From, s.To)
			}
			spans = append(spans, [2]float64{epochSeconds(s.From), epochSeconds(s.To)})
		}
		b, err := json.Marshal(spans)
		if err != nil {
			return nil, fmt.Errorf("library view: encode spans: %w", err)
		}
		taken = string(b)
	}
	var before any
	if f.TakenBefore != nil {
		before = epochSeconds(*f.TakenBefore)
	}
	return []any{
		sql.Named("hidden_dir", f.HiddenDir), sql.Named("hidden", f.Hidden), sql.Named("kind", string(f.Kind)),
		sql.Named("taken", taken), sql.Named("taken_before", before),
	}, nil
}

// epochSeconds is t as unixepoch(..., 'subsec') gives it: seconds since the
// Unix epoch, to the millisecond.
func epochSeconds(t time.Time) float64 {
	return float64(t.UnixMilli()) / 1000
}

// LibraryPage returns a page of assets for the browser.
func (db *DB) LibraryPage(ctx context.Context, q LibraryQuery) (out []LibraryAsset, err error) {
	if q.Limit <= 0 {
		return nil, errors.New("library page: limit must be positive")
	}
	args, err := libraryViewArgs(q.LibraryFilter)
	if err != nil {
		return nil, err
	}
	after := LibraryCursor{}
	hasAfter := q.After != nil
	if hasAfter {
		after = *q.After
	}
	args = append(args, sql.Named("has_after", hasAfter), sql.Named("after_created", after.CreatedAt),
		sql.Named("after_asset", after.AssetID), sql.Named("limit", q.Limit))
	rows, err := db.r.QueryContext(ctx, libraryView+`
		SELECT library, asset_id, created_at, state, kind
		FROM v
		WHERE NOT :has_after OR (created_at, asset_id) < (:after_created, :after_asset)
		ORDER BY created_at DESC, asset_id DESC
		LIMIT :limit`, args...)
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

// CaptureTimes returns when each asset f selects was taken, in no order, and
// how many have no capture time.
func (db *DB) CaptureTimes(ctx context.Context, f LibraryFilter) (taken []time.Time, unknown int, err error) {
	args, err := libraryViewArgs(f)
	if err != nil {
		return nil, 0, err
	}
	rows, err := db.r.QueryContext(ctx, libraryView+`SELECT created_at FROM v`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query capture times: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var created string
		if scanErr := rows.Scan(&created); scanErr != nil {
			return nil, 0, fmt.Errorf("scan capture time: %w", scanErr)
		}
		if created == "" {
			unknown++
			continue
		}
		t, perr := parseTime(created)
		if perr != nil {
			return nil, 0, perr
		}
		taken = append(taken, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate capture times: %w", err)
	}
	return taken, unknown, nil
}

// FirstCapture returns the earliest capture time of any asset, or nil when no
// asset has one.
func (db *DB) FirstCapture(ctx context.Context) (*time.Time, error) {
	var created string
	err := db.r.QueryRowContext(ctx, `SELECT created_at FROM assets WHERE created_at IS NOT NULL
		ORDER BY unixepoch(created_at, 'subsec') LIMIT 1`).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // no asset has a capture time
	}
	if err != nil {
		return nil, fmt.Errorf("first capture: %w", err)
	}
	t, err := parseTime(created)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// fillDisplay picks the rendition to show, if any, and the original's file name.
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
		if f.Version == VersionOriginal {
			a.Filename = filename
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate renditions: %w", err)
	}
	if display, ok := pickDisplay(a.Kind, files); ok {
		a.Display = &display
	}
	return nil
}

// pickDisplay prefers the edited ("adjusted") version of the medium that
// represents the asset, then the original.
func pickDisplay(kind AssetKind, files []RenditionFile) (RenditionFile, bool) {
	return pickRendition(kind, files, VersionAdjusted, VersionOriginal)
}

// pickRendition returns the first of versions present for the medium that
// represents the asset: the picture of a Live Photo, the video of a video.
func pickRendition(kind AssetKind, files []RenditionFile, versions ...string) (RenditionFile, bool) {
	wanted := map[AssetKind]string{KindPhoto: "photo", KindLivePhoto: "live_photo_image", KindVideo: "video"}[kind]
	for _, version := range versions {
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
