package kei

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Row shape captured from `kei manifest --format json` (kei 0.24.1) on 2026-10-05,
// plus an unknown member to prove forward compatibility with schema growth.
const goldenManifest = `[
  {
    "library": "PrimarySync",
    "asset_id": "1419CD61-91C9-4734-B29E-90F525924BEF",
    "version": "original",
    "filename": "IMG_8249_MTQxOUN.HEIC",
    "local_path": "E:\\HomeCloud\\Photos\\2026\\04\\IMG_8249_MTQxOUN.HEIC",
    "checksum": "AUq603PXXLFDGAkchKEQCWlCS3HI",
    "local_checksum": "42f67d4e55111b266d57d57275dfd813d742bb5395294e5b132096bf53d43195",
    "download_checksum": "42f67d4e55111b266d57d57275dfd813d742bb5395294e5b132096bf53d43195",
    "size_bytes": 1241823,
    "created_at": "2026-04-08T21:32:56.374Z",
    "added_at": "2026-04-08T21:32:56.681Z",
    "downloaded_at": "2026-10-05T13:36:12Z",
    "last_seen_at": "2026-10-05T13:36:15Z",
    "media_type": "photo",
    "status": "downloaded",
    "albums": [],
    "future_field": {"anything": true}
  },
  {
    "library": "PrimarySync",
    "asset_id": "AAAA0000-0000-0000-0000-000000000000",
    "version": "original",
    "filename": "IMG_0001_QUFBQTA.MOV",
    "local_path": null,
    "checksum": "AQ",
    "local_checksum": null,
    "download_checksum": null,
    "size_bytes": 10,
    "created_at": "2022-06-03T10:00:00Z",
    "added_at": null,
    "downloaded_at": null,
    "last_seen_at": "2026-10-05T13:36:15Z",
    "media_type": "video",
    "status": "pending",
    "albums": ["Trip"]
  }
]`

// capturedPath is the golden row's local_path as kei wrote it on Windows.
const capturedPath = `"E:\\HomeCloud\\Photos\\2026\\04\\IMG_8249_MTQxOUN.HEIC"`

// golden is goldenManifest with its downloaded row inside dir, an absolute
// download folder on the OS the tests run on, and dir.
func golden(t *testing.T) (doc, dir string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "Photos")
	path, err := json.Marshal(filepath.Join(dir, "2026", "04", "IMG_8249_MTQxOUN.HEIC"))
	if err != nil {
		t.Fatal(err)
	}
	doc = strings.Replace(goldenManifest, capturedPath, string(path), 1)
	if doc == goldenManifest {
		t.Fatal("the golden manifest has no captured local_path")
	}
	return doc, dir
}

func TestParseManifestGolden(t *testing.T) {
	doc, dir := golden(t)
	rows, err := ParseManifest(strings.NewReader(doc), dir)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	r := rows[0]
	if r.AssetID != "1419CD61-91C9-4734-B29E-90F525924BEF" || r.Version != "original" || r.SizeBytes != 1241823 {
		t.Errorf("unexpected identity/size: %+v", r)
	}
	if r.Status != StatusDownloaded || r.MediaType != MediaPhoto {
		t.Errorf("status/media = %q/%q", r.Status, r.MediaType)
	}
	if want := time.Date(2026, 4, 8, 21, 32, 56, 681_000_000, time.UTC); r.AddedAt == nil || !r.AddedAt.Equal(want) {
		t.Errorf("added_at = %v, want %v", r.AddedAt, want)
	}
	if rows[1].LocalPath != nil || rows[1].AddedAt != nil || rows[1].Albums[0] != "Trip" {
		t.Errorf("nullable fields mis-decoded: %+v", rows[1])
	}
}

// TestParseRealManifest validates a manifest exported from a real kei state DB.
// Opt-in: HOMECLOUD_KEI_MANIFEST=<path to `kei manifest --format json` output>
// and HOMECLOUD_KEI_DOWNLOAD_DIR=<kei's download folder>.
func TestParseRealManifest(t *testing.T) {
	path, dir := os.Getenv("HOMECLOUD_KEI_MANIFEST"), os.Getenv("HOMECLOUD_KEI_DOWNLOAD_DIR")
	if path == "" || dir == "" {
		t.Skip("HOMECLOUD_KEI_MANIFEST and HOMECLOUD_KEI_DOWNLOAD_DIR not set")
	}
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied fixture path
	if err != nil {
		t.Fatal(err)
	}
	rows, err := ParseManifest(bytes.NewReader(data), dir)
	if err != nil {
		t.Fatalf("ParseManifest(%s): %v", path, err)
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[string(r.Status)+"/"+string(r.MediaType)+"/"+r.Version]++
	}
	t.Logf("%d rows: %v", len(rows), counts)
}

func TestParseManifestRejects(t *testing.T) {
	doc, dir := golden(t)
	inside, err := json.Marshal(filepath.Join(dir, "2026", "04", "IMG_8249_MTQxOUN.HEIC"))
	if err != nil {
		t.Fatal(err)
	}
	outside, err := json.Marshal(filepath.Join(filepath.Dir(dir), "elsewhere", "IMG_8249_MTQxOUN.HEIC"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, replace, with, wantErr string
	}{
		{"unknown status", `"status": "pending"`, `"status": "archived"`, `unknown status "archived"`},
		{"unknown media type", `"media_type": "video"`, `"media_type": "hologram"`, `unknown media_type "hologram"`},
		{"downloaded without checksum", `"download_checksum": "42f67d4e55111b266d57d57275dfd813d742bb5395294e5b132096bf53d43195"`, `"download_checksum": null`, "SHA-256"},
		{"uppercase checksum", `"download_checksum": "42f67d4e55111b266d57d57275dfd813d742bb5395294e5b132096bf53d43195"`, `"download_checksum": "42F67D4E55111B266D57D57275DFD813D742BB5395294E5B132096BF53D43195"`, "SHA-256"},
		{"downloaded without path", `"local_path": ` + string(inside), `"local_path": null`, "no local_path"},
		{"path outside the download folder", `"local_path": ` + string(inside), `"local_path": ` + string(outside), "not inside kei's download folder"},
		{"relative path", `"local_path": ` + string(inside), `"local_path": "2026/04/IMG_8249_MTQxOUN.HEIC"`, "not inside kei's download folder"},
		{"missing asset id", `"asset_id": "AAAA0000-0000-0000-0000-000000000000"`, `"asset_id": ""`, "required"},
		{"malformed json", `[`, `{`, "decode kei manifest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := strings.Replace(doc, tt.replace, tt.with, 1)
			if changed == doc {
				t.Fatalf("fixture replacement %q did not apply", tt.replace)
			}
			_, err := ParseManifest(strings.NewReader(changed), dir)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}
