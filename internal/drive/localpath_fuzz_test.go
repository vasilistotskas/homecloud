package drive

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// FuzzLocalName: whatever iCloud names an item, its local name is one safe
// component of a Windows path, always the same for the same name.
func FuzzLocalName(f *testing.F) {
	for _, seed := range []string{
		"Notes.txt", "", ".", "..", "a/b", `a\b`, "CON", "con.txt", "CONIN$", "LPT¹.log",
		"name. ", "trailing...", "ctrl\x01char", `<>:"|?*`, strings.Repeat("é", 300) + ".pages",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		out := localName(name)
		switch {
		case out == "" || out == "." || out == "..":
			t.Fatalf("localName(%q) = %q", name, out)
		case strings.ContainsAny(out, `<>:"/\|?*`) || strings.ContainsFunc(out, func(r rune) bool { return r < 0x20 }):
			t.Fatalf("localName(%q) = %q holds a character Windows refuses", name, out)
		case strings.HasSuffix(out, ".") || strings.HasSuffix(out, " "):
			t.Fatalf("localName(%q) = %q ends in a dot or space, which Windows drops", name, out)
		case len(utf16.Encode([]rune(out))) > maxNameUnits:
			t.Fatalf("localName(%q) is %d UTF-16 units long", name, len(utf16.Encode([]rune(out))))
		case !filepath.IsLocal(out) || filepath.Base(out) != out:
			t.Fatalf("localName(%q) = %q is not one local path component", name, out)
		case localName(name) != out:
			t.Fatalf("localName(%q) is not deterministic", name)
		}
		base, _, _ := strings.Cut(out, ".")
		if reservedNames[strings.ToUpper(strings.TrimRight(base, " "))] {
			t.Fatalf("localName(%q) = %q is a reserved device name", name, out)
		}
	})
}
