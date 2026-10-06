package export

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/vasilistotskas/homecloud/internal/ledger"
)

type fakeLedger []ledger.ManifestRow

func (f fakeLedger) EachManifestRow(_ context.Context, fn func(ledger.ManifestRow) error) error {
	for _, r := range f {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

func TestWrite(t *testing.T) {
	root := t.TempDir()
	created := time.Date(2024, 3, 14, 9, 30, 0, 0, time.FixedZone("EET", 2*3600))
	db := fakeLedger{{
		Library: "PrimarySync", AssetID: "A1", Version: "original", Filename: "IMG_1, \"quoted\".HEIC",
		LocalPath: filepath.Join(root, "Photos", "2024", "03", "IMG_1.HEIC"), MediaType: "photo",
		SizeBytes: 1234, SHA256: "ab", State: ledger.StateVerified, CreatedAt: &created,
	}}
	n, err := Write(context.Background(), db, root)
	if err != nil || n != 1 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	f, err := os.Open(filepath.Join(root, ManifestFile)) //nolint:gosec // test temp path
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{header, {
		"PrimarySync", "A1", "original", "IMG_1, \"quoted\".HEIC", "Photos/2024/03/IMG_1.HEIC", "photo",
		"1234", "ab", "verified", "2024-03-14T07:30:00Z", "", "",
	}}
	if diff := cmp.Diff(want, records); diff != "" {
		t.Fatalf("manifest (-want +got):\n%s", diff)
	}
	if got, err := os.ReadFile(filepath.Join(root, ReadmeFile)); err != nil || string(got) != string(readme) { //nolint:gosec // test temp path
		t.Fatalf("README = %d bytes, %v", len(got), err)
	}
	if left, _ := filepath.Glob(filepath.Join(root, ".*.tmp-*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

type driveRows map[string]ledger.DriveFile

func (d driveRows) DriveFiles(context.Context) (map[string]ledger.DriveFile, error) { return d, nil }

func TestWriteDrive(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	deleted := at.Add(time.Hour)
	rows, err := WriteDrive(context.Background(), driveRows{
		"b": {ICloudPath: "iCloud Drive/b.txt", LocalPath: "iCloud Drive/b.txt", Size: 1, SHA256: "s", ArchivedAt: at, DeletedAt: &deleted},
		"a": {ICloudPath: "iCloud Drive/a:b.txt", LocalPath: "iCloud Drive/a_b.txt", Size: 2, SHA256: "t", ArchivedAt: at, Package: true},
	}, root)
	if err != nil || rows != 2 {
		t.Fatalf("WriteDrive = %d, %v", rows, err)
	}
	data, err := os.ReadFile(filepath.Join(root, ManifestFile)) //nolint:gosec // test temp path
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "iCloud Drive/a:b.txt,iCloud Drive/a_b.txt,2,t,,true") ||
		!strings.HasSuffix(lines[2], "2026-10-06T13:00:00Z") {
		t.Fatalf("manifest:\n%s", data)
	}
}
