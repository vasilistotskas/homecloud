package iphonecopy

import (
	"bytes"
	"maps"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzParseManifest: a manifest pushed from another computer is untrusted. A
// manifest it accepts names only paths inside a backup and survives a round
// trip through Bytes.
func FuzzParseManifest(f *testing.F) {
	digest := strings.Repeat("ab", 32)
	for _, seed := range []string{
		digest + " *Info.plist\n" + digest + " *0a/0a1b2c\n",
		digest + "  Status.plist\n",
		digest + " *../escape\n",
		digest + " *a/b/c/d/e\n",
		digest + " *name.\n",
		digest + " *Info.plist\n" + digest + " *Info.plist\n",
		"not a manifest",
		"",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := ParseManifest(bytes.NewReader(data))
		if err != nil {
			return
		}
		for rel := range m {
			if pathErr := validPath(rel); pathErr != nil {
				t.Fatalf("accepted %q: %v", rel, pathErr)
			}
			if !filepath.IsLocal(filepath.FromSlash(rel)) {
				t.Fatalf("accepted %q, which is not inside the backup", rel)
			}
		}
		again, err := ParseManifest(bytes.NewReader(m.Bytes()))
		if err != nil || !maps.Equal(m, again) {
			t.Fatalf("round trip: %v", err)
		}
	})
}
