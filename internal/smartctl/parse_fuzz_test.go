package smartctl

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzParse: whatever smartctl prints, parse returns a result or an error,
// never a panic. The real and constructed reports are the seeds.
func FuzzParse(f *testing.F) {
	for _, dir := range []string{"testdata/captured", "testdata/constructed"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil {
			f.Fatal(err)
		}
		for _, name := range files {
			data, err := os.ReadFile(name) //nolint:gosec // test fixtures
			if err != nil {
				f.Fatal(err)
			}
			f.Add(data)
		}
	}
	f.Fuzz(func(_ *testing.T, out []byte) {
		_, _ = parse(out)
	})
}
