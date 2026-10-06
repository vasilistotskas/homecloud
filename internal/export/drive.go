package export

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

var driveHeader = []string{
	"icloud_path", "path", "size_bytes", "sha256", "apple_signature", "package",
	"modified_at", "archived_at", "verified_at", "deleted_in_icloud_at",
}

// DriveLedger is the ledger query WriteDrive reads.
type DriveLedger interface {
	DriveFiles(ctx context.Context) (map[string]ledger.DriveFile, error)
}

// WriteDrive replaces manifest.csv in the archive's Drive folder: every
// archived iCloud Drive file, including those deleted in iCloud, sorted by
// path. It returns the number of rows.
func WriteDrive(ctx context.Context, db DriveLedger, driveRoot string) (int, error) {
	files, err := db.DriveFiles(ctx)
	if err != nil {
		return 0, fmt.Errorf("export drive manifest: %w", err)
	}
	rows := make([]ledger.DriveFile, 0, len(files))
	for _, f := range files {
		rows = append(rows, f)
	}
	slices.SortFunc(rows, func(a, b ledger.DriveFile) int { return strings.Compare(a.LocalPath, b.LocalPath) })
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(driveHeader); err != nil {
		return 0, fmt.Errorf("write drive manifest header: %w", err)
	}
	for _, f := range rows {
		verified := f.LastScrubAt
		if verified == nil {
			verified = &f.ArchivedAt
		}
		if err := w.Write([]string{
			f.ICloudPath, f.LocalPath, strconv.FormatInt(f.Size, 10), f.SHA256, f.AppleSignature,
			strconv.FormatBool(f.Package), timestamp(f.ModifiedAt), timestamp(&f.ArchivedAt),
			timestamp(verified), timestamp(f.DeletedAt),
		}); err != nil {
			return 0, fmt.Errorf("write drive manifest row: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return 0, fmt.Errorf("export drive manifest: %w", err)
	}
	if err := platform.WriteFile(filepath.Join(driveRoot, ManifestFile), buf.Bytes()); err != nil {
		return 0, err
	}
	return len(rows), nil
}
