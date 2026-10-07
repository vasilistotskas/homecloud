// Package iphonebackuptest writes the property lists of an iPhone backup for
// tests: Info.plist as XML, Manifest.plist and Status.plist as binary plists,
// as Apple's tools do. Values are strings, booleans and dates.
package iphonebackuptest

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// appleEpoch is the zero of plist dates (CFAbsoluteTime).
var appleEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

// Binary plist markers and sizes (CoreFoundation, ForFoundationOnly.h). They
// are spelled out here rather than shared with the reader, so that a wrong
// constant there is not mirrored here.
const (
	magic        = "bplist00"
	trailerSize  = 32
	markerFalse  = 0x08
	markerTrue   = 0x09
	markerInt    = 0x10
	markerDate   = 0x33
	markerASCII  = 0x50
	markerUTF16  = 0x60
	markerDict   = 0xD0
	countFollows = 0x0F
)

// XML encodes entries as an XML plist dictionary.
func XML(tb testing.TB, entries map[string]any) []byte {
	tb.Helper()
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	esc := func(s string) string {
		var e bytes.Buffer
		if err := xml.EscapeText(&e, []byte(s)); err != nil {
			tb.Fatal(err)
		}
		return e.String()
	}
	for _, k := range slices.Sorted(maps.Keys(entries)) {
		fmt.Fprintf(&b, "\t<key>%s</key>\n", esc(k))
		switch v := entries[k].(type) {
		case string:
			fmt.Fprintf(&b, "\t<string>%s</string>\n", esc(v))
		case bool:
			fmt.Fprintf(&b, "\t<%t/>\n", v)
		case time.Time:
			fmt.Fprintf(&b, "\t<date>%s</date>\n", v.UTC().Format("2006-01-02T15:04:05Z"))
		default:
			tb.Fatalf("iphonebackuptest: %s: unsupported %T", k, v)
		}
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

// Binary encodes entries as a binary ("bplist00") plist dictionary: object 0
// is the dictionary, then its keys, then its values.
func Binary(tb testing.TB, entries map[string]any) []byte {
	tb.Helper()
	keys := slices.Sorted(maps.Keys(entries))
	n := unsigned(len(keys))
	numObjects := 1 + 2*n
	if numObjects > math.MaxUint16 {
		tb.Fatalf("iphonebackuptest: %d entries", n)
	}
	refSize := byte(1)
	if numObjects > math.MaxUint8 {
		refSize = 2
	}

	var objects bytes.Buffer
	objects.WriteString(magic)
	offsets := make([]uint64, 0, numObjects)
	start := func() { offsets = append(offsets, unsigned(objects.Len())) }

	start()
	writeCount(&objects, markerDict, n)
	for i := range 2 * n {
		writeSized(&objects, 1+i, refSize)
	}
	for _, k := range keys {
		start()
		writeString(&objects, k)
	}
	for _, k := range keys {
		start()
		switch v := entries[k].(type) {
		case string:
			writeString(&objects, v)
		case bool:
			if v {
				objects.WriteByte(markerTrue)
			} else {
				objects.WriteByte(markerFalse)
			}
		case time.Time:
			objects.WriteByte(markerDate)
			seconds := float64(v.Sub(appleEpoch)) / float64(time.Second)
			_ = binary.Write(&objects, binary.BigEndian, math.Float64bits(seconds)) //nolint:errcheck // bytes.Buffer writes never fail
		default:
			tb.Fatalf("iphonebackuptest: %s: unsupported %T", k, v)
		}
	}

	tableOffset := unsigned(objects.Len())
	offsetSize := byte(1)
	for tableOffset >= uint64(1)<<(8*offsetSize) {
		offsetSize *= 2
	}
	for _, off := range offsets {
		writeSized(&objects, off, offsetSize)
	}
	trailer := make([]byte, trailerSize)
	trailer[6], trailer[7] = offsetSize, refSize
	binary.BigEndian.PutUint64(trailer[8:], numObjects)
	binary.BigEndian.PutUint64(trailer[24:], tableOffset)
	objects.Write(trailer)
	return objects.Bytes()
}

// unsigned converts a length, which is never negative.
func unsigned(n int) uint64 {
	return uint64(n) //nolint:gosec // lengths are never negative
}

// writeCount writes a marker whose low nibble is the count, or 0xF and the
// count as an integer object.
func writeCount(b *bytes.Buffer, marker byte, n uint64) {
	if n < countFollows {
		b.WriteByte(marker | byte(n))
		return
	}
	b.WriteByte(marker | countFollows)
	size, log := byte(1), byte(0)
	for n >= uint64(1)<<(8*size) {
		size *= 2
		log++
	}
	b.WriteByte(markerInt | log)
	writeSized(b, n, size)
}

// writeSized writes the low size (1-8) bytes of v, big-endian.
func writeSized(b *bytes.Buffer, v uint64, size byte) {
	var full [8]byte
	binary.BigEndian.PutUint64(full[:], v)
	b.Write(full[8-size:])
}

// writeString writes ASCII strings as such and others as UTF-16, as
// CoreFoundation does.
func writeString(b *bytes.Buffer, s string) {
	ascii := true
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		writeCount(b, markerASCII, unsigned(len(s)))
		b.WriteString(s)
		return
	}
	units := utf16.Encode([]rune(s))
	writeCount(b, markerUTF16, unsigned(len(units)))
	for _, u := range units {
		writeSized(b, uint64(u), 2)
	}
}

// Backup is a finished device backup for WriteBackup.
type Backup struct {
	UDID, DeviceName string
	Completed        time.Time
	Encrypted        bool
	// Files maps paths inside the folder (Apple's hashed names, e.g.
	// "ab/ab12…") to their contents.
	Files map[string]string
}

// WriteBackup lays out b as Apple's tools leave a finished backup, in
// dir/<UDID>, and returns that folder.
func WriteBackup(tb testing.TB, dir string, b Backup) string {
	tb.Helper()
	root := filepath.Join(dir, b.UDID)
	put := func(name string, data []byte) {
		tb.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			tb.Fatal(err)
		}
	}
	put("Info.plist", XML(tb, map[string]any{"Device Name": b.DeviceName, "Product Version": "26.1", "Unique Identifier": b.UDID}))
	put("Manifest.plist", Binary(tb, map[string]any{"Version": "10.0", "IsEncrypted": b.Encrypted}))
	put("Status.plist", Binary(tb, map[string]any{
		"SnapshotState": "finished", "Date": b.Completed, "IsFullBackup": false, "BackupState": "new", "Version": "3.3",
	}))
	for name, content := range b.Files {
		put(name, []byte(content))
	}
	return root
}
