// Package export keeps the archive usable without homecloud: a plain CSV
// manifest of every file and a README describing the layout, both at the
// archive root.
package export

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/csv"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// Files written at the archive root.
const (
	ManifestFile = "manifest.csv"
	ReadmeFile   = "README.txt"
)

//go:embed README.txt
var readme []byte

var header = []string{
	"library", "asset_id", "version", "filename", "path", "media_type",
	"size_bytes", "sha256", "state", "created_at", "added_at", "verified_at",
}

// Ledger is the ledger query export reads.
type Ledger interface {
	EachManifestRow(ctx context.Context, fn func(ledger.ManifestRow) error) error
}

// Write replaces manifest.csv and README.txt under root atomically and
// returns the number of manifest rows.
func Write(ctx context.Context, db Ledger, root string) (int, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(header); err != nil {
		return 0, fmt.Errorf("write manifest header: %w", err)
	}
	rows := 0
	err := db.EachManifestRow(ctx, func(m ledger.ManifestRow) error {
		rel, err := filepath.Rel(root, m.LocalPath)
		if err != nil {
			return fmt.Errorf("path of %s/%s/%s: %w", m.Library, m.AssetID, m.Version, err)
		}
		rows++
		if err := w.Write([]string{
			m.Library, m.AssetID, m.Version, m.Filename, filepath.ToSlash(rel), m.MediaType,
			strconv.FormatInt(m.SizeBytes, 10), m.SHA256, string(m.State),
			timestamp(m.CreatedAt), timestamp(m.AddedAt), timestamp(m.VerifiedAt),
		}); err != nil {
			return fmt.Errorf("write manifest row: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("export manifest: %w", err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return 0, fmt.Errorf("export manifest: %w", err)
	}
	if err := platform.WriteFile(filepath.Join(root, ManifestFile), buf.Bytes()); err != nil {
		return 0, err
	}
	if _, err := platform.WriteFileIfChanged(filepath.Join(root, ReadmeFile), readme, platform.WriteFile); err != nil {
		return 0, err
	}
	return rows, nil
}

func timestamp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
