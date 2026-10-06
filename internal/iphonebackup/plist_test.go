package iphonebackup

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/iphonebackup/iphonebackuptest"
)

// golden reads a plist written by Python's plistlib (testdata/plist/generate.py).
func golden(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "plist", name)) //nolint:gosec // a fixture the test names
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustParse(t *testing.T, data []byte) dict {
	t.Helper()
	d, err := parsePlist(data)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func wantString(t *testing.T, d dict, key, want string) {
	t.Helper()
	var got string
	if err := d.str(key, &got); err != nil || got != want {
		t.Errorf("%s = %q, %v; want %q", key, got, err, want)
	}
}

func wantDate(t *testing.T, d dict, key string, want time.Time) {
	t.Helper()
	var got time.Time
	if err := d.date(key, &got); err != nil || !got.Equal(want) {
		t.Errorf("%s = %v, %v; want %v", key, got, err, want)
	}
}

func wantBool(t *testing.T, d dict, key string, want bool) {
	t.Helper()
	var got *bool
	if err := d.boolean(key, &got); err != nil || got == nil || *got != want {
		t.Errorf("%s = %v, %v; want %v", key, got, err, want)
	}
}

func wantKind(t *testing.T, d dict, key string, want kind) {
	t.Helper()
	if v, ok := d[key]; !ok || v.kind != want {
		t.Errorf("%s = %+v; want %s", key, v, want)
	}
}

// The values generate.py wrote.
const deviceName = "Βασίλης’s iPhone 📱"

func TestXMLFixture(t *testing.T) {
	d := mustParse(t, golden(t, "info.xml"))
	wantString(t, d, "Device Name", deviceName)
	wantString(t, d, "Product Version", "26.1")
	wantString(t, d, "Unique Identifier", "00008110-001A2B3C4D5E801E")
	wantString(t, d, "Serial Number", `&<>"'`)
	wantDate(t, d, "Last Backup Date", time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	wantKind(t, d, "Applications", kindDict)
	wantKind(t, d, "Installed Applications", kindArray)
	wantKind(t, d, "iTunes Settings", kindDict)
	wantKind(t, d, "Big Int", kindInteger)
	wantKind(t, d, "Negative", kindInteger)
	wantKind(t, d, "Ratio", kindReal)
	if len(d) != 13 {
		t.Errorf("%d keys, want 13", len(d))
	}
}

func TestBinaryFixtures(t *testing.T) {
	t.Run("manifest", func(t *testing.T) {
		d := mustParse(t, golden(t, "manifest.bplist"))
		wantBool(t, d, "IsEncrypted", true)
		wantBool(t, d, "WasPasscodeSet", false)
		wantString(t, d, "Version", "10.0")
		wantDate(t, d, "Date", time.Date(2026, 10, 6, 11, 59, 58, 0, time.UTC))
		wantKind(t, d, "BackupKeyBag", kindData)
		wantKind(t, d, "Lockdown", kindDict)
		wantKind(t, d, "Applications", kindDict)
	})
	t.Run("status", func(t *testing.T) {
		d := mustParse(t, golden(t, "status.bplist"))
		wantString(t, d, "SnapshotState", "finished")
		wantDate(t, d, "Date", time.Date(2026, 10, 6, 12, 0, 0, 250_000_000, time.UTC))
		wantBool(t, d, "IsFullBackup", false)
		wantString(t, d, "UUID", "5E1F2A6C-8D3B-4C1E-9A7F-0B2C3D4E5F60")
	})
	t.Run("wide", func(t *testing.T) {
		// More than 15 entries, more than 255 objects (2-byte references),
		// UTF-16 and long strings, a date before 2001, a 16-byte integer.
		d := mustParse(t, golden(t, "wide.bplist"))
		if len(d) != 305 {
			t.Errorf("%d keys, want 305", len(d))
		}
		wantString(t, d, "key299", "value 299")
		wantString(t, d, "Device Name", strings.Repeat("Ünïcødé ✓ ", 30))
		wantString(t, d, "Long ASCII", strings.Repeat("x", 1000))
		wantDate(t, d, "Old Date", time.Date(1999, 12, 31, 23, 59, 59, 0, time.UTC))
		wantKind(t, d, "Huge", kindInteger)
		wantBool(t, d, "Flag", true)
	})
}

func TestTypeMismatchAndAbsentKeys(t *testing.T) {
	d := mustParse(t, golden(t, "info.xml"))
	var (
		s  = "unchanged"
		b  *bool
		tm time.Time
	)
	if err := d.str("Big Int", &s); err == nil || s != "unchanged" {
		t.Errorf("an integer read as a string: %q, %v", s, err)
	}
	if err := d.boolean("Device Name", &b); err == nil || b != nil {
		t.Errorf("a string read as a boolean: %v, %v", b, err)
	}
	if err := d.date("Product Version", &tm); err == nil || !tm.IsZero() {
		t.Errorf("a string read as a date: %v, %v", tm, err)
	}
	if err := d.str("Absent", &s); err != nil || s != "unchanged" {
		t.Errorf("absent string = %q, %v", s, err)
	}
	if err := d.boolean("Absent", &b); err != nil || b != nil {
		t.Errorf("absent boolean = %v, %v", b, err)
	}
	if err := d.date("Absent", &tm); err != nil || !tm.IsZero() {
		t.Errorf("absent date = %v, %v", tm, err)
	}
}

func TestRoundTrip(t *testing.T) {
	entries := map[string]any{
		"Device Name":   deviceName,
		"Escaped":       `a&b<c>"d'`,
		"Empty":         "",
		"IsEncrypted":   true,
		"IsFullBackup":  false,
		"Date":          time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC),
		"SnapshotState": "finished",
	}
	// Enough entries for 2-byte references and offsets.
	for i := range 300 {
		entries[fmt.Sprintf("key %03d", i)] = strings.Repeat("v", i)
	}
	for name, data := range map[string][]byte{
		"xml":    iphonebackuptest.XML(t, entries),
		"binary": iphonebackuptest.Binary(t, entries),
	} {
		t.Run(name, func(t *testing.T) {
			d := mustParse(t, data)
			if len(d) != len(entries) {
				t.Fatalf("%d keys, want %d", len(d), len(entries))
			}
			for k, v := range entries {
				switch v := v.(type) {
				case string:
					wantString(t, d, k, v)
				case bool:
					wantBool(t, d, k, v)
				case time.Time:
					wantDate(t, d, k, v)
				}
			}
		})
	}
}

// The trailer bytes holding the offset and reference sizes, counted back
// from the end of the data.
const (
	offsetSizeFromEnd = trailerSize - 6
	refSizeFromEnd    = trailerSize - 7
)

// layout is a binary plist's trailer, for tests that corrupt the file.
type layout struct {
	offsetSize, refSize uint64
	numObjects, top     uint64
	table               uint64
}

func trailerOf(b []byte) layout {
	t := b[len(b)-trailerSize:]
	return layout{
		offsetSize: uint64(t[6]), refSize: uint64(t[7]),
		numObjects: binary.BigEndian.Uint64(t[8:]), top: binary.BigEndian.Uint64(t[16:]), table: binary.BigEndian.Uint64(t[24:]),
	}
}

// object returns the offset of object i.
func (l layout) object(b []byte, i uint64) uint64 {
	at := l.table + i*l.offsetSize
	return sizedInt(b[at : at+l.offsetSize])
}

func (l layout) setOffset(b []byte, i, v uint64) {
	at := l.table + i*l.offsetSize
	var full [8]byte
	binary.BigEndian.PutUint64(full[:], v)
	copy(b[at:at+l.offsetSize], full[8-l.offsetSize:])
}

// putTrailer overwrites the 8-byte trailer field at index at (8: object
// count, 16: top object, 24: offset table).
func putTrailer(b []byte, at int, v uint64) {
	binary.BigEndian.PutUint64(b[len(b)-trailerSize+at:], v)
}

// craft lays out a binary plist of objects, the first being the top one,
// with 1-byte offsets and references.
func craft(tb testing.TB, objects ...[]byte) []byte {
	tb.Helper()
	b := []byte(binaryMagic)
	table := make([]byte, 0, len(objects))
	for _, o := range objects {
		off := len(b)
		if off > math.MaxUint8 {
			tb.Fatalf("craft: object at %d needs offsets wider than 1 byte", off)
			return nil
		}
		table = append(table, byte(off))
		b = append(b, o...)
	}
	trailer := make([]byte, trailerSize)
	trailer[6], trailer[7] = 1, 1
	binary.BigEndian.PutUint64(trailer[8:], uint64(len(objects)))
	binary.BigEndian.PutUint64(trailer[24:], uint64(len(b)))
	return append(append(b, table...), trailer...)
}

// malformedBinary returns corrupt binary plists and the error each must
// give. Most corrupt status.bplist, located through its own offset table.
func malformedBinary(tb testing.TB) map[string]struct {
	data []byte
	want string
} {
	tb.Helper()
	status := golden(tb, "status.bplist")
	l := trailerOf(status)
	if l.offsetSize != 1 || l.refSize != 1 || l.object(status, l.top) != headerSize || status[headerSize]&0x0F == countFollows {
		tb.Fatalf("status.bplist layout changed: %+v", l)
	}
	refs, n := uint64(headerSize+1), uint64(status[headerSize]&0x0F)
	size := uint64(len(status))
	trailerAt, offsetSizeAt, refSizeAt := size-trailerSize, size-offsetSizeFromEnd, size-refSizeFromEnd
	// value returns the reference of the first value whose marker matches.
	value := func(match func(marker byte) bool) byte {
		for i := range n {
			if ref := status[refs+n+i]; match(status[l.object(status, uint64(ref))]) {
				return ref
			}
		}
		tb.Fatal("status.bplist has no such value")
		return 0
	}
	ascii := value(func(m byte) bool { return m&0xF0 == markerASCII && m&0x0F < countFollows && m&0x0F > 0 })
	date := value(func(m byte) bool { return m == markerDate })
	notString := value(func(m byte) bool { return m&0xF0 != markerASCII && m&0xF0 != markerUTF16 })
	corrupt := func(mutate func(b []byte) []byte) []byte { return mutate(slices.Clone(status)) }
	setByte := func(at uint64, v byte) []byte { return corrupt(func(b []byte) []byte { b[at] = v; return b }) }
	setTrailer := func(at int, v uint64) []byte {
		return corrupt(func(b []byte) []byte { putTrailer(b, at, v); return b })
	}
	setDate := func(seconds float64) []byte {
		return corrupt(func(b []byte) []byte {
			binary.BigEndian.PutUint64(b[l.object(b, uint64(date))+1:], math.Float64bits(seconds))
			return b
		})
	}

	// 1-byte offsets cannot reach an object area of 300 bytes: the table is
	// re-sized to stay consistent so that only the width check fails.
	longString := iphonebackuptest.Binary(tb, map[string]any{"k": strings.Repeat("x", 300)})
	narrowOffsets := slices.Clone(longString)
	narrowOffsets[len(narrowOffsets)-offsetSizeFromEnd] = 1
	putTrailer(narrowOffsets, 8, 2*trailerOf(longString).numObjects)
	// 1-byte references cannot address the 611 objects of wide.bplist.
	narrowRefs := golden(tb, "wide.bplist")
	narrowRefs[len(narrowRefs)-refSizeFromEnd] = 1

	huge := []byte{0x13, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xF8}
	return map[string]struct {
		data []byte
		want string
	}{
		// __CFBinaryPlistGetTopLevelInfo's checks, in its order.
		"truncated":                         {status[:len(status)-1], "binary plist:"},
		"too short":                         {status[:trailerSize+headerSize], "too short"},
		"unsupported version":               {setByte(7, '1'), "unsupported binary plist version"},
		"object count beyond int64":         {setTrailer(8, math.MaxInt64+1), "overflow"},
		"no objects":                        {setTrailer(8, 0), "no objects"},
		"top object beyond the objects":     {setTrailer(16, l.numObjects), "top object"},
		"offset table inside the header":    {setTrailer(24, headerSize), "precedes the objects"},
		"offset table at the trailer":       {setTrailer(24, trailerAt), "past the data"},
		"zero offset size":                  {setByte(offsetSizeAt, 0), "offset size 0"},
		"zero reference size":               {setByte(refSizeAt, 0), "reference size 0"},
		"nine-byte references":              {setByte(refSizeAt, 9), "reference size 9"},
		"offset table size overflows":       {corrupt(func(b []byte) []byte { b[offsetSizeAt] = 8; putTrailer(b, 8, math.MaxInt64); return b }), "offset table size overflows"},
		"object count disagrees":            {setTrailer(8, l.numObjects+1), "does not end at the trailer"},
		"references too narrow":             {narrowRefs, "cannot address"},
		"offsets too narrow":                {narrowOffsets, "cannot reach"},
		"offset into the offset table":      {corrupt(func(b []byte) []byte { l.setOffset(b, 1, l.table); return b }), "outside the objects"},
		"offset into the header":            {corrupt(func(b []byte) []byte { l.setOffset(b, 1, 3); return b }), "outside the objects"},
		"top object not a dictionary":       {corrupt(func(b []byte) []byte { l.setOffset(b, l.top, l.object(b, 1)); return b }), "not a dictionary"},
		"reference past the objects":        {setByte(refs, 0xFF), "reference 255 of"},
		"key that is not a string":          {setByte(refs, notString), "not a string"},
		"repeated key":                      {setByte(refs+1, status[refs]), "appears twice"},
		"non-ASCII byte in an ASCII string": {setByte(l.object(status, uint64(ascii))+1, 0xE9), "non-ASCII"},
		"count that is not an integer":      {setByte(l.object(status, uint64(ascii)), markerASCII|countFollows), "count marker"},
		"fill marker":                       {setByte(l.object(status, uint64(ascii)), 0x0F), "unknown marker"},
		"unknown marker":                    {setByte(l.object(status, uint64(ascii)), 0x90), "unknown marker"},
		"real-sized date marker":            {setByte(l.object(status, uint64(date)), 0x32), "unknown marker"},
		"date that is not a number":         {setDate(math.NaN()), "out of range"},
		"date after year 9999":              {setDate(1e12), "out of range"},
		"date before year 1":                {setDate(-1e11), "out of range"},

		// Counts far beyond the data are refused before anything is
		// allocated. howett.net/plist issue #90: an array of 2^64-8 elements
		// escaped as a runtime panic; these are its bytes.
		"howett issue 90": {[]byte{
			'b', 'p', 'l', 'i', 's', 't', '0', '0',
			0xAF, 0x13, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xF8,
			0x08,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x01,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x12,
		}, "not a dictionary"},
		"dictionary of 2^64-8 entries":  {craft(tb, append([]byte{markerDict | countFollows}, huge...)), "count"},
		"dictionary of 2^40 entries":    {craft(tb, []byte{markerDict | countFollows, 0x13, 0, 0, 1, 0, 0, 0, 0, 0}), "run past the objects"},
		"references past the objects":   {craft(tb, []byte{markerDict | 3, 1, 2}), "run past the objects"},
		"string of 2^64-8 bytes":        {craft(tb, []byte{markerDict | 1, 1, 2}, append([]byte{markerASCII | countFollows}, huge...), []byte{markerTrue}), "count"},
		"string of 2^63-1 bytes":        {craft(tb, []byte{markerDict | 1, 1, 2}, []byte{markerASCII | countFollows, 0x13, 0x7F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, []byte{markerTrue}), "run past the objects"},
		"UTF-16 string of 2^63-1 units": {craft(tb, []byte{markerDict | 1, 1, 2}, []byte{markerUTF16 | countFollows, 0x13, 0x7F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, []byte{markerTrue}), "run past the objects"},
		"16-byte count":                 {craft(tb, []byte{markerDict | countFollows, 0x14, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}), "count marker"},
		"date past the objects":         {craft(tb, []byte{markerDict | 1, 1, 2}, []byte{markerASCII | 1, 'a'}, []byte{markerDate, 0, 0}), "run past the objects"},
		"key that is the dictionary":    {craft(tb, []byte{markerDict | 1, 0, 1}, []byte{markerTrue}), "not a string"},
	}
}

// maxMalformedAlloc bounds what parsing any malformed input may allocate:
// the inputs are at most a few kilobytes, while their counts claim up to
// 2^64 elements.
const maxMalformedAlloc = 1 << 20

func TestMalformedBinary(t *testing.T) {
	for name, tt := range malformedBinary(t) {
		t.Run(name, func(t *testing.T) {
			var (
				d   dict
				err error
			)
			if n := allocated(func() { d, err = parsePlist(tt.data) }); n > maxMalformedAlloc {
				t.Errorf("allocated %d bytes", n)
			}
			if err == nil {
				t.Fatalf("accepted: %v", d)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// allocated returns how many heap bytes f allocates.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// Containers are never followed, so a dictionary holding itself cannot loop,
// and a null is kept as a value homecloud does not read.
func TestBinarySelfReferenceAndNull(t *testing.T) {
	d := mustParse(t, craft(t, []byte{markerDict | 2, 1, 2, 0, 3}, []byte{markerASCII | 1, 'a'}, []byte{markerASCII | 1, 'b'}, []byte{markerNull}))
	wantKind(t, d, "a", kindDict)
	wantKind(t, d, "b", kindNull)
}

func TestMalformedXML(t *testing.T) {
	const head = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"
	for name, tt := range map[string]struct{ doc, want string }{
		"root is not plist":       {head + `<dict></dict>`, "not <plist>"},
		"top is not a dict":       {head + `<plist><array/></plist>`, "not a dictionary"},
		"unknown element":         {head + `<plist><dict><key>a</key><foo/></dict></plist>`, "unknown value element <foo>"},
		"key without a value":     {head + `<plist><dict><key>a</key></dict></plist>`, "where an element belongs"},
		"value without a key":     {head + `<plist><dict><string>x</string></dict></plist>`, "where a <key> belongs"},
		"text in a dict":          {head + `<plist><dict>hello<key>a</key><true/></dict></plist>`, "in a dictionary"},
		"element in a string":     {head + `<plist><dict><key>a</key><string><b/></string></dict></plist>`, "not text"},
		"content in a boolean":    {head + `<plist><dict><key>a</key><true>yes</true></dict></plist>`, "where </true> belongs"},
		"date in another format":  {head + `<plist><dict><key>a</key><date>2026-10-06 12:00</date></dict></plist>`, "<date>"},
		"repeated key":            {head + `<plist><dict><key>a</key><true/><key>a</key><false/></dict></plist>`, "appears twice"},
		"content after the plist": {head + `<plist><dict/></plist><plist><dict/></plist>`, "content after </plist>"},
		"truncated":               {head + `<plist><dict><key>a</key><string>x`, "unexpected EOF"},
		// encoding/xml applies no DTD: a declared entity, internal or
		// external, is as unknown as an undeclared one.
		"external entity": {head + `<!DOCTYPE plist [<!ENTITY x SYSTEM "file:///etc/passwd">]><plist><dict><key>a</key><string>&x;</string></dict></plist>`, "invalid character entity &x;"},
		"declared entity": {head + `<!DOCTYPE plist [<!ENTITY x "boom">]><plist><dict><key>a</key><string>&x;</string></dict></plist>`, "invalid character entity &x;"},
		"other encoding":  {`<?xml version="1.0" encoding="ISO-8859-1"?><plist><dict/></plist>`, "CharsetReader is nil"},
		"UTF-16":          {"\xFF\xFE<\x00", "not a property list"},
		"not a plist":     {"hello", "not a property list"},
	} {
		t.Run(name, func(t *testing.T) {
			d, err := parsePlist([]byte(tt.doc))
			if err == nil {
				t.Fatalf("accepted: %v", d)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// Apple's DOCTYPE, comments, a byte order mark, predefined entities and
// character references are what an XML plist may hold besides the values.
func TestXMLPredefinedEntitiesAndComments(t *testing.T) {
	d := mustParse(t, []byte(utf8BOM+`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- a comment before the root -->
<plist version="1.0"><!-- and inside -->
<dict>
	<key>a&amp;b</key><string>x &lt;y&gt; &#x1F4F1;</string>
	<!-- between entries -->
	<key>empty</key><string/>
</dict>
</plist>`))
	wantString(t, d, "a&b", "x <y> 📱")
	wantString(t, d, "empty", "")
}

func TestReadPlistRefusesAnOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Info.plist")
	f, err := os.Create(path) //nolint:gosec // test temp file
	if err != nil {
		t.Fatal(err)
	}
	// A sparse file: nothing is written or read.
	if err := f.Truncate(maxPlistSize + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readPlist(path); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversized plist: %v", err)
	}
}

func FuzzBinary(f *testing.F) {
	for _, name := range []string{"manifest.bplist", "status.bplist", "wide.bplist"} {
		f.Add(golden(f, name))
	}
	f.Add(iphonebackuptest.Binary(f, map[string]any{"SnapshotState": "finished", "IsEncrypted": true, "Date": time.Unix(0, 0)}))
	for _, tt := range malformedBinary(f) {
		f.Add(tt.data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		readAll(t, data)
	})
}

func FuzzXML(f *testing.F) {
	f.Add(golden(f, "info.xml"))
	f.Add(iphonebackuptest.XML(f, map[string]any{"Device Name": deviceName, "IsEncrypted": false, "Date": time.Unix(0, 0)}))
	f.Fuzz(func(t *testing.T, data []byte) {
		readAll(t, data)
	})
}

// readAll parses data and reads every key with every accessor: whatever the
// input, the result is a dictionary or an error, never a panic.
func readAll(t *testing.T, data []byte) {
	t.Helper()
	d, err := parsePlist(data)
	if err != nil {
		return
	}
	var (
		s  string
		b  *bool
		tm time.Time
	)
	for k := range d {
		_ = d.str(k, &s)
		_ = d.boolean(k, &b)
		_ = d.date(k, &tm)
	}
}
