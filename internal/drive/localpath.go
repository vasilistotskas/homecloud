package drive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
	"unicode/utf16"
)

// maxNameUnits is the longest file name NTFS accepts, in UTF-16 code units.
const maxNameUnits = 255

// reservedNames are Windows device names, unusable as a file's base name with
// any extension.
var reservedNames = func() map[string]bool {
	m := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true}
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

// withSuffix inserts suffix before the extension of a file name and keeps
// the result within maxNameUnits, shortening the base when needed.
func withSuffix(name, suffix string) string {
	ext := path.Ext(name)
	if ext == name {
		ext = ""
	}
	base := []rune(strings.TrimSuffix(name, ext))
	budget := maxNameUnits - len(utf16.Encode([]rune(suffix+ext)))
	for len(base) > 0 && len(utf16.Encode(base)) > budget {
		base = base[:len(base)-1]
	}
	return string(base) + suffix + ext
}

// pathKey compares local paths the way Windows does: without case.
func pathKey(local string) string {
	return strings.ToLower(local)
}

// parents returns the folders containing local ('/'-separated), outermost
// first: "a/b/c.txt" has "a" and "a/b".
func parents(local string) []string {
	var out []string
	for i, r := range local {
		if r == '/' {
			out = append(out, local[:i])
		}
	}
	return out
}

// planner hands out local paths so that no two files share one and no file
// takes the place of a folder another file needs, or the reverse. Paths
// compare without case, as on Windows.
type planner struct {
	files   map[string]string // path key -> drivewsid
	folders map[string]int    // folder path key -> files below it
}

func newPlanner() *planner {
	return &planner{files: map[string]string{}, folders: map[string]int{}}
}

// hold records that drivewsid uses local.
func (p *planner) hold(local, drivewsid string) {
	p.files[pathKey(local)] = drivewsid
	for _, dir := range parents(local) {
		p.folders[pathKey(dir)]++
	}
}

// release records that local is no longer used.
func (p *planner) release(local string) {
	delete(p.files, pathKey(local))
	for _, dir := range parents(local) {
		key := pathKey(dir)
		if p.folders[key]--; p.folders[key] <= 0 {
			delete(p.folders, key)
		}
	}
}

// free reports whether drivewsid may use local.
func (p *planner) free(local, drivewsid string) bool {
	key := pathKey(local)
	if owner, taken := p.files[key]; taken && owner != drivewsid {
		return false
	}
	if p.folders[key] > 0 {
		return false
	}
	for _, dir := range parents(local) {
		if _, isFile := p.files[pathKey(dir)]; isFile {
			return false
		}
	}
	return true
}

// assign returns a free local path for f: the safe form of its iCloud path
// or, where that clashes, the clashing name with " (<first 8 of its document
// id>)" and then its whole document id. A folder whose name a file holds takes
// the suffix of its own id, so every file of that folder lands in the same
// place; otherwise the file takes its own.
func (p *planner) assign(f File) (string, error) {
	parts := strings.Split(localPath(f.Path), "/")
	if len(f.IDs) != len(parts) {
		return "", fmt.Errorf("%s: %d ids for %d path parts", f.DriveWSID, len(f.IDs), len(parts))
	}
	for _, length := range []int{0, idSuffixLen, -1} {
		candidate := append([]string{}, parts...)
		if length != 0 {
			i, suffixed := p.fileAmongFolders(candidate), folderWithSuffix
			if i < 0 {
				i, suffixed = len(candidate)-1, withSuffix
			}
			id, err := idSuffix(f.IDs[i], length)
			if err != nil {
				return "", err
			}
			candidate[i] = suffixed(candidate[i], id)
		}
		local := strings.Join(candidate, "/")
		if p.free(local, f.DriveWSID) {
			p.hold(local, f.DriveWSID)
			return local, nil
		}
	}
	return "", fmt.Errorf("no free local name for %s", strings.Join(f.Path, "/"))
}

// idSuffixLen is how much of a document id disambiguates a name first.
const idSuffixLen = 8

// idSuffix is " (<document id>)" for a drivewsid, cut to length (-1: whole).
func idSuffix(drivewsid string, length int) (string, error) {
	_, document, err := zoneAndDocument(drivewsid)
	if err != nil {
		return "", err
	}
	if length >= 0 {
		document = document[:min(length, len(document))]
	}
	return " (" + document + ")", nil
}

// fileAmongFolders returns the index of the first folder of a path that
// another file holds as its own path, or -1.
func (p *planner) fileAmongFolders(parts []string) int {
	for i := 1; i < len(parts); i++ {
		if _, isFile := p.files[pathKey(strings.Join(parts[:i], "/"))]; isFile {
			return i - 1
		}
	}
	return -1
}

// folderWithSuffix appends suffix to a folder name within maxNameUnits.
func folderWithSuffix(name, suffix string) string {
	base := []rune(name)
	budget := maxNameUnits - len(utf16.Encode([]rune(suffix)))
	for len(base) > 0 && len(utf16.Encode(base)) > budget {
		base = base[:len(base)-1]
	}
	return string(base) + suffix
}
