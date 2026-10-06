package drive

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
	"unicode/utf16"
)

// maxNameUnits is the longest file name NTFS accepts, in UTF-16 code units.
const maxNameUnits = 255

// reservedNames are Windows device names, unusable as a file's base name with
// any extension.
var reservedNames = func() map[string]bool {
	m := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}
	for _, digit := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³"} {
		m["COM"+digit], m["LPT"+digit] = true, true
	}
	return m
}()

// localName makes one iCloud name usable as a file or folder name on Windows,
// the strictest platform homecloud runs on. The mapping is deterministic.
func localName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	// Windows drops trailing dots and spaces; keep the name distinct instead.
	trimmed := strings.TrimRight(out, ". ")
	out = trimmed + strings.Repeat("_", len(out)-len(trimmed))
	if trimmed == "" {
		out = "_" + out
	}
	base, _, _ := strings.Cut(out, ".")
	if reservedNames[strings.ToUpper(strings.TrimRight(base, " "))] {
		out = "_" + out
	}
	if len(utf16.Encode([]rune(out))) > maxNameUnits {
		out = shorten(name, out)
	}
	return out
}

// shorten cuts an over-long name, keeping its extension and adding a hash of
// the original so that different long names stay different.
func shorten(original, name string) string {
	sum := sha256.Sum256([]byte(original))
	tag := "~" + hex.EncodeToString(sum[:4])
	ext := path.Ext(name)
	if len(utf16.Encode([]rune(ext))) > 16 {
		ext = ""
	}
	runes := []rune(strings.TrimSuffix(name, ext))
	budget := maxNameUnits - len(utf16.Encode([]rune(tag+ext)))
	for len(utf16.Encode(runes)) > budget {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + tag + ext
}

// localPath is the archive path ('/'-separated, relative to the Drive folder)
// for an iCloud path.
func localPath(icloud []string) string {
	parts := make([]string, len(icloud))
	for i, p := range icloud {
		parts[i] = localName(p)
	}
	return strings.Join(parts, "/")
}

// disambiguate gives a local path that another file already uses (names
// differing only in case, or mapping to the same safe name) a suffix taken
// from the file's own id, before its extension: "a (1A2B3C4D).pdf".
func disambiguate(local, drivewsid string) string {
	_, document, err := zoneAndDocument(drivewsid)
	if err != nil {
		document = drivewsid
	}
	id := document[:min(8, len(document))]
	dir, file := path.Split(local)
	ext := path.Ext(file)
	if ext == file {
		ext = ""
	}
	return dir + strings.TrimSuffix(file, ext) + " (" + id + ")" + ext
}

// pathKey compares local paths the way Windows does: without case.
func pathKey(local string) string {
	return strings.ToLower(local)
}
