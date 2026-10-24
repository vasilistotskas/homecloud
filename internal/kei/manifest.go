package kei

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/tool"
)

// Status is kei's per-version download state (kei src/state/types.rs AssetStatus).
type Status string

// Known statuses.
const (
	StatusPending        Status = "pending"
	StatusPolicyExcluded Status = "policy_excluded"
	StatusDownloaded     Status = "downloaded"
	StatusFailed         Status = "failed"
)

// MediaType is kei's media classification (kei src/state/types.rs MediaType).
type MediaType string

// Known media types.
const (
	MediaPhoto          MediaType = "photo"
	MediaVideo          MediaType = "video"
	MediaLivePhotoImage MediaType = "live_photo_image"
	MediaLivePhotoVideo MediaType = "live_photo_video"
)

// ManifestRow is one asset version from `kei manifest --format json`.
// Nullability mirrors kei's ManifestRow (src/commands/manifest.rs, v0.26.0).
// kei documents that the schema may grow, so unknown members are ignored.
type ManifestRow struct {
	Library          string     `json:"library"`
	AssetID          string     `json:"asset_id"`
	Version          string     `json:"version"`
	Filename         string     `json:"filename"`
	LocalPath        *string    `json:"local_path"`
	Checksum         string     `json:"checksum"` // Apple MMCS signature, not a content hash
	LocalChecksum    *string    `json:"local_checksum"`
	DownloadChecksum *string    `json:"download_checksum"`
	SizeBytes        uint64     `json:"size_bytes"`
	CreatedAt        time.Time  `json:"created_at"`
	AddedAt          *time.Time `json:"added_at"`
	DownloadedAt     *time.Time `json:"downloaded_at"`
	LastSeenAt       time.Time  `json:"last_seen_at"`
	MediaType        MediaType  `json:"media_type"`
	Status           Status     `json:"status"`
	Albums           []string   `json:"albums"`
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Validate checks invariants homecloud relies on. Unknown enum values are
// rejected: a new status or media type changes semantics and must be reviewed.
// A downloaded file must lie in dir, kei's download folder (DownloadDir):
// homecloud reads, renders and serves the file at that path.
func (r *ManifestRow) Validate(dir string) error {
	var errs []error
	if r.Library == "" || r.AssetID == "" || r.Version == "" {
		errs = append(errs, errors.New("library, asset_id and version are required"))
	}
	switch r.Status {
	case StatusPending, StatusPolicyExcluded, StatusDownloaded, StatusFailed:
	default:
		errs = append(errs, fmt.Errorf("unknown status %q", r.Status))
	}
	switch r.MediaType {
	case MediaPhoto, MediaVideo, MediaLivePhotoImage, MediaLivePhotoVideo:
	default:
		errs = append(errs, fmt.Errorf("unknown media_type %q", r.MediaType))
	}
	if r.Status == StatusDownloaded {
		switch {
		case r.LocalPath == nil || *r.LocalPath == "":
			errs = append(errs, errors.New("downloaded row has no local_path"))
		case !within(dir, *r.LocalPath):
			errs = append(errs, fmt.Errorf("local_path %s is not inside kei's download folder %s", *r.LocalPath, dir))
		}
		if r.DownloadChecksum == nil || !sha256Hex.MatchString(*r.DownloadChecksum) {
			errs = append(errs, errors.New("downloaded row has no lowercase hex SHA-256 download_checksum"))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("manifest row %s/%s/%s: %w", r.Library, r.AssetID, r.Version, err)
	}
	return nil
}

// within reports whether path is an absolute path inside dir.
func within(dir, path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && filepath.IsLocal(rel)
}

// ParseManifest decodes and validates a JSON manifest of the library kei
// downloads into dir.
func ParseManifest(r io.Reader, dir string) ([]ManifestRow, error) {
	var rows []ManifestRow
	if err := json.UnmarshalRead(r, &rows); err != nil {
		return nil, fmt.Errorf("decode kei manifest: %w", err)
	}
	var errs []error
	for i := range rows {
		if err := rows[i].Validate(dir); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return rows, nil
}

// manifestTimeout bounds `kei manifest`, which reads kei's local state: 6,041
// renditions take about a second, so this leaves room for a library a hundred
// times larger on a slow disk.
const manifestTimeout = 10 * time.Minute

// Manifest runs `kei manifest --format json` (read-only; does not contact
// iCloud) for the library kei downloads into dir.
func Manifest(ctx context.Context, cfg config.Kei, dir string) ([]ManifestRow, error) {
	ctx, cancel := context.WithTimeout(ctx, manifestTimeout)
	defer cancel()
	var stdout bytes.Buffer
	stderr := &tool.Tail{}
	cmd := platform.Command(ctx, cfg.Binary, "manifest", "--format", "json", "--config", cfg.Config)
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kei manifest: %w: %s", err, stderr)
	}
	return ParseManifest(&stdout, dir)
}
