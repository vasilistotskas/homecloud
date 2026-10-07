package kei

import (
	"bytes"
	"path/filepath"
	"testing"
)

// FuzzParseManifest: kei's manifest names the files homecloud reads and
// serves. Whatever it holds, parsing never panics, and every downloaded row
// it accepts lies inside kei's download folder.
func FuzzParseManifest(f *testing.F) {
	dir := filepath.Join(f.TempDir(), "Photos")
	f.Add([]byte(goldenManifest))
	f.Add([]byte(`[{"status":"downloaded","local_path":"../x"}]`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		rows, err := ParseManifest(bytes.NewReader(data), dir)
		if err != nil {
			return
		}
		for _, r := range rows {
			if r.Status == StatusDownloaded && !within(dir, *r.LocalPath) {
				t.Fatalf("accepted %q outside %s", *r.LocalPath, dir)
			}
		}
	})
}
