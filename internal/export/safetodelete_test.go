package export

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/vasilistotskas/homecloud/internal/ledger"
)

func TestWriteSafeToDelete(t *testing.T) {
	root := t.TempDir()
	taken := time.Date(2024, 3, 14, 9, 30, 0, 0, time.FixedZone("EET", 2*3600))
	added := taken.Add(time.Minute)
	live := ledger.ArchivedAsset{
		LibraryAsset: ledger.LibraryAsset{
			Library: "PrimarySync", AssetID: "A1", Kind: ledger.KindLivePhoto, Filename: "IMG_1.HEIC", CreatedAt: &taken,
		},
		AddedAt: &added,
		Renditions: []ledger.RenditionEvidence{
			{Version: "adjusted", MediaType: "live_photo_image", SizeBytes: 300, LocalPath: filepath.Join(root, "Photos", "2024", "03", "IMG_E1.HEIC")},
			{Version: "live", MediaType: "live_photo_video", SizeBytes: 200, LocalPath: filepath.Join(root, "Photos", "2024", "03", "IMG_1.MOV")},
			{Version: "original", MediaType: "live_photo_image", SizeBytes: 100, LocalPath: filepath.Join(root, "Photos", "2024", "03", "IMG_1.HEIC")},
		},
	}
	if err := WriteSafeToDelete(root, []ledger.ArchivedAsset{live}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(root, SafeToDeleteFile)) //nolint:gosec // test temp path
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		safeToDeleteHeader,
		{"2024-03-14T07:30:00Z", "2024-03-14T07:31:00Z", "IMG_1.HEIC", "live", "600", "Photos/2024/03/IMG_1.HEIC", "PrimarySync", "A1"},
	}
	if diff := cmp.Diff(want, rows); diff != "" {
		t.Errorf("safe-to-delete.csv (-want +got):\n%s", diff)
	}

	if err := WriteSafeToDelete(root, []ledger.ArchivedAsset{{LibraryAsset: ledger.LibraryAsset{AssetID: "X", Kind: ledger.KindPhoto}}}); err == nil {
		t.Fatal("an item without its original was written")
	}
}
