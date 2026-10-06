package iphonebackup

// A reader for the property lists of iPhone backups: the top-level dictionary
// of an XML plist (Info.plist) or a binary "bplist00" plist (Manifest.plist,
// Status.plist), decoding the strings, booleans and dates homecloud reads.
// Every other value is kept only by its type, never decoded or followed, so
// nested containers are not inspected and nothing recurses.
//
// The binary format and its checks follow Apple's CoreFoundation,
// CFBinaryPList.c (apple-oss-distributions/CF, tag CF-1153.18, its latest):
// the format description at lines 239-283, the trailer checks of
// __CFBinaryPlistGetTopLevelInfo at lines 759-847, and the object reading of
// __CFBinaryPlistCreateObjectFiltered at lines 1061-1559. The trailer layout
// is CFBinaryPlistTrailer in ForFoundationOnly.h (lines 404-412).

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"os"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// maxPlistSize bounds how much of a plist file is read. Info.plist grows with
// the apps on the phone: libimobiledevice's idevicebackup2.c (the open
// implementation of Apple's backup protocol) stores, for every installed app,
// its icon PNG, iTunes metadata and SINF (lines 380-415), plus iBooks data and
// iTunes files (lines 496-536), base64-encoded in XML. That reaches tens of
// megabytes on a phone with hundreds of apps; the bound is an order of
// magnitude above, while a file larger than this is not a backup's plist.
const maxPlistSize = 256 << 20

// kind is the type of a plist value. Only strings, booleans and dates are
// decoded; the other kinds are recorded so that reading one of them as a
// string, boolean or date is an error.
type kind uint8

const (
	kindString kind = iota + 1
	kindBool
	kindDate
	kindInteger
	kindReal
	kindData
	kindArray
	kindSet
	kindDict
	kindUID
	kindNull
)

var kindNames = [...]string{
	kindString:  "a string",
	kindBool:    "a boolean",
	kindDate:    "a date",
	kindInteger: "an integer",
	kindReal:    "a real number",
	kindData:    "data",
	kindArray:   "an array",
	kindSet:     "a set",
	kindDict:    "a dictionary",
	kindUID:     "a UID",
	kindNull:    "null",
}

func (k kind) String() string { return kindNames[k] }

// value is one value of a plist's top-level dictionary.
type value struct {
	kind kind
	str  string
	b    bool
	date time.Time
}

// dict is a plist's top-level dictionary.
type dict map[string]value

// utf8BOM is the UTF-8 encoding of U+FEFF, the byte order mark.
const utf8BOM = "\uFEFF"

// errNotPlist marks data that is neither an XML nor a binary plist.
var errNotPlist = errors.New("not a property list")

// readPlist reads the XML or binary plist at path.
func readPlist(path string) (dict, error) {
	f, err := os.Open(path) //nolint:gosec // a file of a configured backup folder
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle
	tooLarge := fmt.Errorf("read %s: larger than %d bytes", path, maxPlistSize)
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if info.Size() > maxPlistSize {
		return nil, tooLarge
	}
	// The file may grow while it is read (a backup being written).
	data, err := io.ReadAll(io.LimitReader(f, maxPlistSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxPlistSize {
		return nil, tooLarge
	}
	d, err := parsePlist(data)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return d, nil
}

// parsePlist decodes the top-level dictionary of an XML or binary plist.
func parsePlist(data []byte) (dict, error) {
	if bytes.HasPrefix(data, []byte(binaryMagic)) {
		return parseBinary(data)
	}
	if bytes.HasPrefix(data, []byte("bplist")) {
		return nil, fmt.Errorf("unsupported binary plist version %q", data[:min(len(data), len(binaryMagic))])
	}
	// XML plists are UTF-8: CoreFoundation and libplist write no byte order
	// mark, but CF's reader skips one (CFPropertyList.c lines 2251-2254) and
	// encoding/xml does not. They start with the declaration or the root
	// element, after optional white space.
	data = bytes.TrimPrefix(data, []byte(utf8BOM))
	if trimmed := bytes.TrimLeft(data, " \t\r\n"); bytes.HasPrefix(trimmed, []byte("<")) {
		return parseXML(data)
	}
	return nil, errNotPlist
}

// The accessors set *dst to the value at key. An absent key leaves *dst as
// it is; a value of another type is an error.

func (d dict) str(key string, dst *string) error {
	v, err := d.lookup(key, kindString)
	if v != nil {
		*dst = v.str
	}
	return err
}

// boolean sets *dst to a new boolean, so a nil *dst means an absent key.
func (d dict) boolean(key string, dst **bool) error {
	v, err := d.lookup(key, kindBool)
	if v != nil {
		*dst = new(v.b)
	}
	return err
}

func (d dict) date(key string, dst *time.Time) error {
	v, err := d.lookup(key, kindDate)
	if v != nil {
		*dst = v.date
	}
	return err
}

// lookup returns the value at key if it is of kind k, nil if it is absent,
// and an error if it is of another kind.
func (d dict) lookup(key string, k kind) (*value, error) {
	v, ok := d[key]
	switch {
	case !ok:
		return nil, nil //nolint:nilnil // an absent key is not an error
	case v.kind != k:
		return nil, fmt.Errorf("%s is %s, not %s", key, v.kind, k)
	}
	return &v, nil
}

// add stores a dictionary entry; a repeated key is an error, since which
// value counts would be ambiguous.
func (d dict) add(key string, v value) error {
	if _, ok := d[key]; ok {
		return fmt.Errorf("key %q appears twice", key)
	}
	d[key] = v
	return nil
}

// Binary plist dates are CFAbsoluteTime: seconds since
// 2001-01-01T00:00:00Z (kCFAbsoluteTimeIntervalSince1970, CFDate.c line 50).
var (
	appleEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	// Absolute times outside years 1-9999 are not dates of a backup.
	minAbsolute = float64(time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC).Unix() - appleEpoch)
	maxAbsolute = float64(time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC).Unix() - appleEpoch)
)

// dateFromAbsolute converts a CFAbsoluteTime, to the nanosecond.
func dateFromAbsolute(seconds float64) (time.Time, error) {
	if math.IsNaN(seconds) || seconds < minAbsolute || seconds > maxAbsolute {
		return time.Time{}, fmt.Errorf("date %v is out of range", seconds)
	}
	whole := math.Floor(seconds)
	nanos := math.Round((seconds - whole) * 1e9)
	return time.Unix(appleEpoch+int64(whole), int64(nanos)).UTC(), nil
}

// Binary plists ("bplist00").

const (
	binaryMagic = "bplist00"
	// headerSize is the magic and version; trailerSize is
	// CFBinaryPlistTrailer.
	headerSize  = 8
	trailerSize = 32

	// Object markers (ForFoundationOnly.h lines 380-395): the high nibble
	// is the type, the low one a size or count.
	markerNull    = 0x00
	markerFalse   = 0x08
	markerTrue    = 0x09
	markerInt     = 0x10
	markerReal    = 0x20
	markerDate    = 0x33
	markerData    = 0x40
	markerASCII   = 0x50
	markerUTF16   = 0x60
	markerUID     = 0x80
	markerArray   = 0xA0
	markerSet     = 0xC0
	markerDict    = 0xD0
	countFollows  = 0x0F
	maxSizedBytes = 8
)

// binaryPlist is a binary plist whose trailer and offset table were checked.
type binaryPlist struct {
	data []byte
	// offsetSize and refSize are the byte sizes of offset-table entries and
	// of object references.
	offsetSize, refSize uint64
	numObjects          uint64
	// tableOffset is where the offset table starts; objects lie in
	// [headerSize, tableOffset).
	tableOffset uint64
}

// sizedInt reads a big-endian unsigned integer of 1-8 bytes.
func sizedInt(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func parseBinary(data []byte) (dict, error) {
	p, top, err := checkTrailer(data)
	if err != nil {
		return nil, err
	}
	return p.topDict(top)
}

// checkTrailer validates the trailer and the offset table as
// __CFBinaryPlistGetTopLevelInfo does and returns the top object's offset.
func checkTrailer(data []byte) (binaryPlist, uint64, error) {
	bad := func(format string, args ...any) (binaryPlist, uint64, error) {
		return binaryPlist{}, 0, fmt.Errorf("binary plist: "+format, args...)
	}
	n := uint64(len(data))
	if n < trailerSize+headerSize+1 {
		return bad("%d bytes is too short", n)
	}
	t := data[n-trailerSize:]
	p := binaryPlist{
		data:        data,
		offsetSize:  uint64(t[6]),
		refSize:     uint64(t[7]),
		numObjects:  binary.BigEndian.Uint64(t[8:16]),
		tableOffset: binary.BigEndian.Uint64(t[24:32]),
	}
	top := binary.BigEndian.Uint64(t[16:24])
	switch {
	case p.numObjects > math.MaxInt64 || p.tableOffset > math.MaxInt64:
		return bad("trailer counts overflow")
	case p.numObjects < 1:
		return bad("no objects")
	case top >= p.numObjects:
		return bad("top object %d of %d", top, p.numObjects)
	case p.tableOffset < headerSize+1:
		return bad("offset table at %d precedes the objects", p.tableOffset)
	case p.tableOffset >= n-trailerSize:
		return bad("offset table at %d is past the data", p.tableOffset)
	// Apple writes 1, 2, 4 or 8; wider integers would not fit in 64 bits.
	case p.offsetSize < 1 || p.offsetSize > maxSizedBytes || p.refSize < 1 || p.refSize > maxSizedBytes:
		return bad("offset size %d or reference size %d", p.offsetSize, p.refSize)
	}
	hi, tableSize := bits.Mul64(p.numObjects, p.offsetSize)
	if hi != 0 {
		return bad("offset table size overflows")
	}
	// The objects, the offset table and the trailer fill the data exactly.
	end, carry := bits.Add64(p.tableOffset, tableSize, 0)
	if carry != 0 || end != n-trailerSize {
		return bad("offset table of %d entries does not end at the trailer", p.numObjects)
	}
	// References and offsets must be wide enough to reach every object and
	// the whole object area.
	if p.refSize < maxSizedBytes && uint64(1)<<(8*p.refSize) <= p.numObjects {
		return bad("%d-byte references cannot address %d objects", p.refSize, p.numObjects)
	}
	if p.offsetSize < maxSizedBytes && uint64(1)<<(8*p.offsetSize) <= p.tableOffset {
		return bad("%d-byte offsets cannot reach offset %d", p.offsetSize, p.tableOffset)
	}
	for i := range p.numObjects {
		if off := p.offset(i); off < headerSize || off >= p.tableOffset {
			return bad("object %d at offset %d is outside the objects", i, off)
		}
	}
	return p, p.offset(top), nil
}

// offset returns the checked offset-table entry of object i < numObjects.
func (p binaryPlist) offset(i uint64) uint64 {
	at := p.tableOffset + i*p.offsetSize
	return sizedInt(p.data[at : at+p.offsetSize])
}

// object returns the offset of the object a reference at position at names.
func (p binaryPlist) object(at uint64) (uint64, error) {
	ref := sizedInt(p.data[at : at+p.refSize])
	if ref >= p.numObjects {
		return 0, fmt.Errorf("binary plist: reference %d of %d objects", ref, p.numObjects)
	}
	return p.offset(ref), nil
}

// span checks that n bytes from off lie within the objects and returns them.
func (p binaryPlist) span(off, n uint64) ([]byte, error) {
	end, carry := bits.Add64(off, n, 0)
	if carry != 0 || end > p.tableOffset {
		return nil, fmt.Errorf("binary plist: %d bytes at offset %d run past the objects", n, off)
	}
	return p.data[off:end], nil
}

// count reads the element count of the object at off whose marker's low
// nibble is low: the nibble itself, or an integer object of 1-8 bytes after
// the marker. It returns the count and the offset of the object's content.
func (p binaryPlist) count(off uint64, low byte) (uint64, uint64, error) {
	if low != countFollows {
		return uint64(low), off + 1, nil
	}
	m, err := p.span(off+1, 1)
	if err != nil {
		return 0, 0, err
	}
	if m[0]&0xF0 != markerInt || m[0]&0x0F > 3 {
		return 0, 0, fmt.Errorf("binary plist: count marker 0x%02x at offset %d", m[0], off+1)
	}
	size := uint64(1) << (m[0] & 0x0F)
	b, err := p.span(off+2, size)
	if err != nil {
		return 0, 0, err
	}
	c := sizedInt(b)
	if c > math.MaxInt64 {
		return 0, 0, fmt.Errorf("binary plist: count %d at offset %d", c, off)
	}
	return c, off + 2 + size, nil
}

// topDict decodes the dictionary at off: keys are the first half of its
// references, values the second.
func (p binaryPlist) topDict(off uint64) (dict, error) {
	marker := p.data[off]
	if marker&0xF0 != markerDict {
		return nil, fmt.Errorf("binary plist: the top object is not a dictionary (marker 0x%02x)", marker)
	}
	n, refs, err := p.count(off, marker&0x0F)
	if err != nil {
		return nil, err
	}
	// Both halves of the references must lie within the objects before
	// anything is allocated for them.
	hi, width := bits.Mul64(n, 2*p.refSize)
	if hi != 0 {
		return nil, fmt.Errorf("binary plist: dictionary of %d entries overflows", n)
	}
	if _, err := p.span(refs, width); err != nil {
		return nil, err
	}
	d := make(dict, n)
	half := n * p.refSize
	for i := range n {
		at := i * p.refSize
		keyOff, err := p.object(refs + at)
		if err != nil {
			return nil, err
		}
		key, err := p.value(keyOff)
		if err != nil {
			return nil, err
		}
		if key.kind != kindString {
			return nil, fmt.Errorf("binary plist: a dictionary key is %s, not a string", key.kind)
		}
		valOff, err := p.object(refs + half + at)
		if err != nil {
			return nil, err
		}
		v, err := p.value(valOff)
		if err != nil {
			return nil, fmt.Errorf("binary plist: %s: %w", key.str, err)
		}
		if err := d.add(key.str, v); err != nil {
			return nil, fmt.Errorf("binary plist: %w", err)
		}
	}
	return d, nil
}

// value decodes the scalar at off, or notes the type of anything else.
func (p binaryPlist) value(off uint64) (value, error) {
	marker := p.data[off]
	switch marker & 0xF0 {
	case markerNull:
		switch marker {
		case markerNull:
			return value{kind: kindNull}, nil
		case markerFalse, markerTrue:
			return value{kind: kindBool, b: marker == markerTrue}, nil
		}
	case markerInt:
		return value{kind: kindInteger}, nil
	case markerReal:
		return value{kind: kindReal}, nil
	case markerDate & 0xF0:
		if marker != markerDate {
			break
		}
		b, err := p.span(off+1, 8)
		if err != nil {
			return value{}, err
		}
		date, err := dateFromAbsolute(math.Float64frombits(binary.BigEndian.Uint64(b)))
		if err != nil {
			return value{}, err
		}
		return value{kind: kindDate, date: date}, nil
	case markerData:
		return value{kind: kindData}, nil
	case markerASCII:
		n, start, err := p.count(off, marker&0x0F)
		if err != nil {
			return value{}, err
		}
		b, err := p.span(start, n)
		if err != nil {
			return value{}, err
		}
		// CoreFoundation decodes these as ASCII, which rejects other bytes.
		for _, c := range b {
			if c >= utf8.RuneSelf {
				return value{}, fmt.Errorf("binary plist: non-ASCII byte in an ASCII string at offset %d", off)
			}
		}
		return value{kind: kindString, str: string(b)}, nil
	case markerUTF16:
		n, start, err := p.count(off, marker&0x0F)
		if err != nil {
			return value{}, err
		}
		hi, size := bits.Mul64(n, 2)
		if hi != 0 {
			return value{}, fmt.Errorf("binary plist: string of %d units overflows", n)
		}
		b, err := p.span(start, size)
		if err != nil {
			return value{}, err
		}
		units := make([]uint16, n)
		for i := range units {
			units[i] = binary.BigEndian.Uint16(b[2*i:])
		}
		// Unpaired surrogates, which no device name or version holds, become
		// U+FFFD.
		return value{kind: kindString, str: string(utf16.Decode(units))}, nil
	case markerUID:
		return value{kind: kindUID}, nil
	case markerArray:
		return value{kind: kindArray}, nil
	case markerSet:
		return value{kind: kindSet}, nil
	case markerDict:
		return value{kind: kindDict}, nil
	}
	return value{}, fmt.Errorf("binary plist: unknown marker 0x%02x at offset %d", marker, off)
}

// XML plists.

// xmlDateLayout is how CoreFoundation writes dates in XML plists
// ("%04d-%02d-%02dT%02d:%02d:%02dZ", CFPropertyList.c line 637; read by
// parseDateTag, lines 1670-1720), as do Python's plistlib and libplist.
const xmlDateLayout = "2006-01-02T15:04:05Z"

// xmlSkipped are the value elements whose content is not read.
var xmlSkipped = map[string]kind{
	"integer": kindInteger,
	"real":    kindReal,
	"data":    kindData,
	"array":   kindArray,
	"dict":    kindDict,
}

// parseXML decodes the top-level dictionary of an XML plist. encoding/xml in
// strict mode (the default) resolves only the five predefined entities and
// character references, rejects any other entity, and returns a DOCTYPE as
// an uninterpreted Directive: no entity declaration is applied and nothing
// external is fetched. A declared encoding other than UTF-8 is rejected.
func parseXML(data []byte) (dict, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	root, err := nextElement(dec)
	if err != nil {
		return nil, fmt.Errorf("xml plist: %w", err)
	}
	if root.Name.Local != "plist" {
		return nil, fmt.Errorf("xml plist: root element <%s>, not <plist>", root.Name.Local)
	}
	top, err := nextElement(dec)
	if err != nil {
		return nil, fmt.Errorf("xml plist: %w", err)
	}
	if top.Name.Local != "dict" {
		return nil, fmt.Errorf("xml plist: the top value is <%s>, not a dictionary", top.Name.Local)
	}
	d, err := xmlDict(dec)
	if err != nil {
		return nil, fmt.Errorf("xml plist: %w", err)
	}
	// After the dictionary only </plist> may follow, then nothing.
	if err := expectEnd(dec, "plist"); err != nil {
		return nil, fmt.Errorf("xml plist: %w", err)
	}
	if t, err := significant(dec); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("xml plist: %w", err)
		}
		return nil, fmt.Errorf("xml plist: content after </plist>: %T", t)
	}
	return d, nil
}

// significant returns the next token that is not white space, a comment, a
// processing instruction or a directive. Plist elements have no namespace.
func significant(dec *xml.Decoder) (xml.Token, error) {
	for {
		t, err := dec.Token()
		if err != nil {
			return nil, err //nolint:wrapcheck // io.EOF is compared by callers; syntax errors are wrapped there
		}
		switch t := t.(type) {
		case xml.CharData:
			if len(bytes.TrimSpace(t)) == 0 {
				continue
			}
		case xml.Comment, xml.ProcInst, xml.Directive:
			continue
		case xml.StartElement:
			if t.Name.Space != "" {
				return nil, fmt.Errorf("element <%s:%s> has a namespace", t.Name.Space, t.Name.Local)
			}
		}
		return t, nil
	}
}

// nextElement returns the next start element.
func nextElement(dec *xml.Decoder) (xml.StartElement, error) {
	t, err := significant(dec)
	if err != nil {
		return xml.StartElement{}, err
	}
	s, ok := t.(xml.StartElement)
	if !ok {
		return xml.StartElement{}, fmt.Errorf("unexpected %T where an element belongs", t)
	}
	return s, nil
}

// expectEnd consumes the end element name.
func expectEnd(dec *xml.Decoder, name string) error {
	t, err := significant(dec)
	if err != nil {
		return err
	}
	if e, ok := t.(xml.EndElement); !ok || e.Name.Local != name {
		return fmt.Errorf("unexpected %T where </%s> belongs", t, name)
	}
	return nil
}

// text returns an element's character data; elements inside are an error.
func text(dec *xml.Decoder, name string) (string, error) {
	var b []byte
	for {
		t, err := dec.Token()
		if err != nil {
			return "", fmt.Errorf("<%s>: %w", name, err)
		}
		switch t := t.(type) {
		case xml.CharData:
			b = append(b, t...)
		case xml.Comment:
		case xml.EndElement:
			return string(b), nil
		default:
			return "", fmt.Errorf("<%s> holds %T, not text", name, t)
		}
	}
}

// xmlDict reads key/value pairs up to </dict>.
func xmlDict(dec *xml.Decoder) (dict, error) {
	d := dict{}
	for {
		t, err := significant(dec)
		if err != nil {
			return nil, err
		}
		switch t := t.(type) {
		case xml.EndElement:
			return d, nil
		case xml.StartElement:
			if t.Name.Local != "key" {
				return nil, fmt.Errorf("<%s> where a <key> belongs", t.Name.Local)
			}
			key, err := text(dec, "key")
			if err != nil {
				return nil, err
			}
			start, err := nextElement(dec)
			if err != nil {
				return nil, fmt.Errorf("value of %s: %w", key, err)
			}
			v, err := xmlValue(dec, start)
			if err != nil {
				return nil, fmt.Errorf("value of %s: %w", key, err)
			}
			if err := d.add(key, v); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unexpected %T in a dictionary", t)
		}
	}
}

// xmlValue decodes the value element start, or skips it noting its type.
func xmlValue(dec *xml.Decoder, start xml.StartElement) (value, error) {
	name := start.Name.Local
	switch name {
	case "string":
		s, err := text(dec, name)
		if err != nil {
			return value{}, err
		}
		return value{kind: kindString, str: s}, nil
	case "true", "false":
		if err := expectEnd(dec, name); err != nil {
			return value{}, err
		}
		return value{kind: kindBool, b: name == "true"}, nil
	case "date":
		s, err := text(dec, name)
		if err != nil {
			return value{}, err
		}
		date, err := time.Parse(xmlDateLayout, s)
		if err != nil {
			return value{}, fmt.Errorf("<date>: %w", err)
		}
		return value{kind: kindDate, date: date}, nil
	}
	k, ok := xmlSkipped[name]
	if !ok {
		return value{}, fmt.Errorf("unknown value element <%s>", name)
	}
	if err := dec.Skip(); err != nil {
		return value{}, fmt.Errorf("<%s>: %w", name, err)
	}
	return value{kind: k}, nil
}
