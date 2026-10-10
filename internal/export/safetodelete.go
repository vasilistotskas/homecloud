package export

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// SafeToDeleteFile is the safe-to-delete report, written at the archive root.
const SafeToDeleteFile = "safe-to-delete.csv"

var safeToDeleteHeader = []string{
	"created_at", "added_at", "filename", "kind", "size_bytes", "path", "library", "asset_id",
}

// WriteSafeToDelete replaces safe-to-delete.csv under root with items, in
// their order: one row per iCloud Photos item, its size being that of all of
// its archived files and its path that of its original.
func WriteSafeToDelete(root string, items []ledger.ArchivedAsset) error {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(safeToDeleteHeader); err != nil {
		return fmt.Errorf("write safe-to-delete header: %w", err)
	}
	for _, a := range items {
		original, ok := a.Original()
		if !ok {
			return fmt.Errorf("safe-to-delete %s/%s: no original on disk", a.Library, a.AssetID)
		}
		rel, err := filepath.Rel(root, original.LocalPath)
		if err != nil {
			return fmt.Errorf("path of %s/%s: %w", a.Library, a.AssetID, err)
		}
		if err := w.Write([]string{
			timestamp(a.CreatedAt), timestamp(a.AddedAt), a.Filename, string(a.Kind),
			strconv.FormatInt(a.SizeBytes(), 10), filepath.ToSlash(rel), a.Library, a.AssetID,
		}); err != nil {
			return fmt.Errorf("write safe-to-delete row: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("export safe-to-delete: %w", err)
	}
	return platform.WriteFile(filepath.Join(root, SafeToDeleteFile), buf.Bytes())
}
