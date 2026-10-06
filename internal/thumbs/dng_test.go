package thumbs

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"slices"
	"testing"
)

// byteOrder is binary.LittleEndian or binary.BigEndian.
type byteOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

// tiffBuilder assembles a classic TIFF from named blocks (IFDs and data)
// laid out one after another. A field may point at another block: its value
// is then that block's offset, or its length, once the layout is known.
type tiffBuilder struct {
	order  byteOrder
	blocks []tiffBlock
	first  string // IFD0
}

type tiffBlock struct {
	name   string
	data   []byte      // a data block
	fields []tiffField // an IFD
	next   string      // the next IFD in the chain, or ""
}

type tiffField struct {
	tag, typ uint16
	values   []uint32
	// offsetOf / lengthOf make the single value the named block's offset or
	// length; subIFDs makes the values the named blocks' offsets.
	offsetOf, lengthOf string
	subIFDs            []string
}

// ifd adds an IFD, its entries sorted by tag as TIFF 6.0 requires.
func (b *tiffBuilder) ifd(name, next string, fields ...tiffField) {
	if b.first == "" {
		b.first = name
	}
	fields = slices.SortedFunc(slices.Values(fields), func(a, b tiffField) int { return cmp.Compare(a.tag, b.tag) })
	b.blocks = append(b.blocks, tiffBlock{name: name, fields: fields, next: next})
}

func (b *tiffBuilder) data(name string, data []byte) {
	b.blocks = append(b.blocks, tiffBlock{name: name, data: data})
}

func long(tag uint16, v uint32) tiffField {
	return tiffField{tag: tag, typ: typeLong, values: []uint32{v}}
}

func short(tag uint16, v uint32) tiffField {
	return tiffField{tag: tag, typ: typeShort, values: []uint32{v}}
}

func dngVersion() tiffField {
	return tiffField{tag: tagDNGVersion, typ: typeByte, values: []uint32{1, 7, 1, 0}}
}

func subIFDs(names ...string) tiffField {
	return tiffField{tag: tagSubIFDs, typ: typeLong, subIFDs: names}
}

// stripImage is an IFD's image stored in the named data block as one strip.
func stripImage(subfile, compression uint32, data string) []tiffField {
	return []tiffField{
		long(tagNewSubfileType, subfile),
		short(tagCompression, compression),
		{tag: tagStripOffsets, typ: typeLong, offsetOf: data},
		{tag: tagStripByteCounts, typ: typeLong, lengthOf: data},
	}
}

func (b *tiffBuilder) bytes() []byte {
	valueLen := func(f tiffField) int {
		n := len(f.values)
		switch {
		case f.subIFDs != nil:
			n = len(f.subIFDs)
		case f.offsetOf != "" || f.lengthOf != "":
			n = 1
		}
		return n * int(fieldSize[f.typ])
	}
	blockLen := func(bl tiffBlock) int {
		if bl.fields == nil {
			return len(bl.data)
		}
		n := 2 + 12*len(bl.fields) + 4
		for _, f := range bl.fields {
			if s := valueLen(f); s > 4 {
				n += s
			}
		}
		return n
	}
	offset, length := map[string]uint32{}, map[string]uint32{}
	pos := 8
	for _, bl := range b.blocks {
		if _, dup := offset[bl.name]; dup {
			panic("tiffBuilder: two blocks named " + bl.name)
		}
		offset[bl.name], length[bl.name] = uint32(pos), uint32(blockLen(bl)) //nolint:gosec // small test files
		pos += blockLen(bl)
	}

	var out bytes.Buffer
	if b.order == binary.LittleEndian {
		out.WriteString("II")
	} else {
		out.WriteString("MM")
	}
	out.Write(b.order.AppendUint16(nil, tiffVersion))
	out.Write(b.order.AppendUint32(nil, offset[b.first]))
	for _, bl := range b.blocks {
		if bl.fields == nil {
			out.Write(bl.data)
			continue
		}
		var extra []byte
		extraAt := offset[bl.name] + uint32(2+12*len(bl.fields)+4)   //nolint:gosec // small test files
		out.Write(b.order.AppendUint16(nil, uint16(len(bl.fields)))) //nolint:gosec // a few fields
		for _, f := range bl.fields {
			vals := f.values
			switch {
			case f.offsetOf != "":
				vals = []uint32{offset[f.offsetOf]}
			case f.lengthOf != "":
				vals = []uint32{length[f.lengthOf]}
			case f.subIFDs != nil:
				vals = nil
				for _, s := range f.subIFDs {
					vals = append(vals, offset[s])
				}
			}
			var v []byte
			for _, x := range vals {
				switch fieldSize[f.typ] {
				case 1:
					v = append(v, byte(x)) //nolint:gosec // BYTE fields hold 8 bits
				case 2:
					v = b.order.AppendUint16(v, uint16(x)) //nolint:gosec // SHORT fields hold 16 bits
				default:
					v = b.order.AppendUint32(v, x)
				}
			}
			out.Write(b.order.AppendUint16(nil, f.tag))
			out.Write(b.order.AppendUint16(nil, f.typ))
			out.Write(b.order.AppendUint32(nil, uint32(len(vals)))) //nolint:gosec // a few values
			if len(v) > 4 {
				out.Write(b.order.AppendUint32(nil, extraAt+uint32(len(extra)))) //nolint:gosec // small test files
				extra = append(extra, v...)
			} else {
				out.Write(append(v, make([]byte, 4-len(v))...))
			}
		}
		var next uint32
		if bl.next != "" {
			next = offset[bl.next]
		}
		out.Write(b.order.AppendUint32(nil, next))
		out.Write(extra)
	}
	return out.Bytes()
}

func preview(t *testing.T, file []byte) (imageRef, bool, error) {
	t.Helper()
	return dngPreview(bytes.NewReader(file), int64(len(file)))
}

func previewBytes(t *testing.T, file []byte) []byte {
	t.Helper()
	ref, isDNG, err := preview(t, file)
	if err != nil || !isDNG {
		t.Fatalf("isDNG=%v err=%v", isDNG, err)
	}
	return file[ref.offset : ref.offset+ref.length]
}

// proRAW lays out an Apple ProRAW-like DNG: IFD0 is the full-size primary
// preview; SubIFDs hold the raw image (NewSubFileType 0, lossless JPEG; here
// bytes a JPEG decoder rejects), a small primary-preview thumbnail, a larger
// alternate rendering, a lower-resolution depth map and a semantic mask.
// Only the two primary previews are candidates, and the larger one wins.
func proRAW(order byteOrder, full, thumb, other []byte) *tiffBuilder {
	b := &tiffBuilder{order: order}
	b.ifd("ifd0", "", append(stripImage(subfilePrimaryPreview, compressionJPEG, "full"),
		subIFDs("raw", "thumb", "alternate", "depth", "mask"), dngVersion())...)
	b.ifd("raw", "", stripImage(0, compressionJPEG, "rawdata")...)
	b.ifd("thumb", "", stripImage(subfilePrimaryPreview, compressionJPEG, "thumbdata")...)
	b.ifd("alternate", "", stripImage(0x10001, compressionJPEG, "other")...)
	b.ifd("depth", "", stripImage(9, compressionJPEG, "other")...)
	b.ifd("mask", "", stripImage(0x10004, compressionJPEG, "other")...)
	b.data("full", full)
	b.data("thumbdata", thumb)
	b.data("other", other)
	b.data("rawdata", []byte{0xff, 0xd8, 0xff, 0xc3, 0, 0}) // a lossless (SOF3) start
	return b
}

func TestDNGPreviewPicksTheLargestPrimaryPreview(t *testing.T) {
	full, thumb, other := testJPEG(t, 64, 48), testJPEG(t, 16, 12), testJPEG(t, 128, 96)
	for _, order := range []byteOrder{binary.BigEndian, binary.LittleEndian} {
		if got := previewBytes(t, proRAW(order, full, thumb, other).bytes()); !bytes.Equal(got, full) {
			t.Fatalf("%v: picked %d bytes, want the 64×48 primary preview", order, len(got))
		}
	}
}

// The picture's area decides, not its byte length: a larger JPEG file of a
// smaller picture loses.
func TestDNGPreviewComparesPicturesNotBytes(t *testing.T) {
	wide := testJPEG(t, 64, 48)
	padded := append(testJPEG(t, 32, 24), make([]byte, len(wide)*2)...) // trailing bytes after EOI
	b := &tiffBuilder{order: binary.LittleEndian}
	b.ifd("ifd0", "", append(stripImage(subfilePrimaryPreview, compressionJPEG, "padded"), subIFDs("sub"), dngVersion())...)
	b.ifd("sub", "", stripImage(subfilePrimaryPreview, compressionJPEG, "wide")...)
	b.data("padded", padded)
	b.data("wide", wide)
	if got := previewBytes(t, b.bytes()); !bytes.Equal(got, wide) {
		t.Fatalf("picked %d bytes, want the 64×48 picture", len(got))
	}
}

func TestDNGPreviewInOneTile(t *testing.T) {
	jpg := testJPEG(t, 32, 24)
	b := &tiffBuilder{order: binary.BigEndian}
	b.ifd("ifd0", "", long(tagNewSubfileType, subfilePrimaryPreview), short(tagCompression, compressionJPEG), dngVersion(),
		tiffField{tag: tagTileOffsets, typ: typeLong, offsetOf: "tile"},
		tiffField{tag: tagTileByteCounts, typ: typeLong, lengthOf: "tile"})
	b.data("tile", jpg)
	if got := previewBytes(t, b.bytes()); !bytes.Equal(got, jpg) {
		t.Fatalf("picked %d bytes, want the tile", len(got))
	}
}

// DNG 1.7 allows JPEG XL previews (Compression 52546). A JPEG preview beside
// one is used; a DNG whose only primary preview is JPEG XL is reported as
// such, since homecloud's FFmpeg does not decode JPEG XL.
func TestDNGPreviewJPEGXL(t *testing.T) {
	jxl := []byte{0xff, 0x0a, 1, 2, 3, 4} // a bare JPEG XL codestream signature
	only := &tiffBuilder{order: binary.LittleEndian}
	only.ifd("ifd0", "", append(stripImage(subfilePrimaryPreview, compressionJPEGXL, "jxl"), dngVersion())...)
	only.data("jxl", jxl)
	if _, isDNG, err := preview(t, only.bytes()); !isDNG || !errors.Is(err, errJPEGXLPreview) {
		t.Fatalf("JPEG XL only: isDNG=%v err=%v, want errJPEGXLPreview", isDNG, err)
	}

	jpg := testJPEG(t, 16, 12)
	both := &tiffBuilder{order: binary.LittleEndian}
	both.ifd("ifd0", "", append(stripImage(subfilePrimaryPreview, compressionJPEGXL, "jxl"), subIFDs("thumb"), dngVersion())...)
	both.ifd("thumb", "", stripImage(subfilePrimaryPreview, compressionJPEG, "jpg")...)
	both.data("jxl", jxl)
	both.data("jpg", jpg)
	if got := previewBytes(t, both.bytes()); !bytes.Equal(got, jpg) {
		t.Fatalf("JPEG XL and JPEG: picked %d bytes, want the JPEG", len(got))
	}
}

func TestDNGPreviewNeedsAPrimaryJPEGPreview(t *testing.T) {
	jpg := testJPEG(t, 32, 24)
	cases := map[string]*tiffBuilder{}
	for name, subfile := range map[string]uint32{
		"main image only": 0, "alternate rendering only": 0x10001, "depth map only": 9, "transparency only": 5,
	} {
		b := &tiffBuilder{order: binary.BigEndian}
		b.ifd("ifd0", "", append(stripImage(subfile, compressionJPEG, "img"), dngVersion())...)
		b.data("img", jpg)
		cases[name] = b
	}
	uncompressed := &tiffBuilder{order: binary.BigEndian}
	uncompressed.ifd("ifd0", "", append(stripImage(subfilePrimaryPreview, 1, "img"), dngVersion())...)
	uncompressed.data("img", jpg)
	cases["uncompressed preview"] = uncompressed
	// DNG stores images in SubIFD trees, not IFD chains.
	chained := &tiffBuilder{order: binary.BigEndian}
	chained.ifd("ifd0", "ifd1", append(stripImage(0, compressionJPEG, "raw"), dngVersion())...)
	chained.ifd("ifd1", "", stripImage(subfilePrimaryPreview, compressionJPEG, "img")...)
	chained.data("raw", []byte{0xff, 0xd8, 0xff, 0xc3})
	chained.data("img", jpg)
	cases["preview only in an IFD chain"] = chained

	for name, b := range cases {
		if _, isDNG, err := preview(t, b.bytes()); !isDNG || !errors.Is(err, errNoJPEGPreview) {
			t.Errorf("%s: isDNG=%v err=%v; want errNoJPEGPreview", name, isDNG, err)
		}
	}
}

func TestDNGPreviewIgnoresWhatIsNotADNG(t *testing.T) {
	jpg := testJPEG(t, 8, 8)
	plain := &tiffBuilder{order: binary.LittleEndian}
	plain.ifd("ifd0", "", stripImage(subfilePrimaryPreview, compressionJPEG, "img")...)
	plain.data("img", jpg)
	for name, file := range map[string][]byte{
		"plain TIFF": plain.bytes(),
		"BigTIFF":    {'I', 'I', 43, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		"JPEG":       jpg,
		"short":      {'M', 'M', 0},
		"empty":      nil,
		"HEIF":       []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"),
	} {
		if _, isDNG, err := preview(t, file); isDNG || err != nil {
			t.Errorf("%s: isDNG=%v err=%v; want neither", name, isDNG, err)
		}
	}
}

func TestDNGPreviewRejectsBrokenStructure(t *testing.T) {
	broken := func(fields ...tiffField) []byte {
		b := &tiffBuilder{order: binary.LittleEndian}
		b.ifd("ifd0", "", append(fields, dngVersion())...)
		return b.bytes()
	}
	hugeSubIFDs := broken(long(tagSubIFDs, 0))
	// The first entry is SubIFDs; give it an impossible value count.
	binary.LittleEndian.PutUint32(hugeSubIFDs[8+2+4:], 1<<28)

	for name, file := range map[string][]byte{
		"IFD0 outside the file":        {'I', 'I', 42, 0, 0xff, 0xff, 0, 0},
		"IFD0 without entries":         {'I', 'I', 42, 0, 8, 0, 0, 0, 0, 0},
		"strip outside the file":       broken(long(tagNewSubfileType, subfilePrimaryPreview), short(tagCompression, compressionJPEG), long(tagStripOffsets, 1<<30), long(tagStripByteCounts, 100)),
		"SubIFD outside the file":      broken(long(tagSubIFDs, 1<<30)),
		"SubIFDs longer than the file": hugeSubIFDs,
	} {
		if _, _, err := preview(t, file); !errors.Is(err, errMalformed) {
			t.Errorf("%s: err=%v, want errMalformed", name, err)
		}
	}

	// A SubIFD that points back at IFD0, or at itself, is read once.
	jpg := testJPEG(t, 8, 8)
	loop := &tiffBuilder{order: binary.BigEndian}
	loop.ifd("ifd0", "", append(stripImage(subfilePrimaryPreview, compressionJPEG, "img"), subIFDs("ifd0", "sub"), dngVersion())...)
	loop.ifd("sub", "", long(tagNewSubfileType, 0), subIFDs("ifd0", "sub"))
	loop.data("img", jpg)
	if got := previewBytes(t, loop.bytes()); !bytes.Equal(got, jpg) {
		t.Fatalf("looping SubIFDs: picked %d bytes", len(got))
	}
}

// FuzzPreview feeds arbitrary bytes to the walker (seeds in testdata/fuzz):
// it must never panic, and a preview it returns must lie inside the input.
func FuzzPreview(f *testing.F) {
	f.Fuzz(func(t *testing.T, file []byte) {
		ref, _, err := dngPreview(bytes.NewReader(file), int64(len(file)))
		if err == nil && ref.length > 0 && (ref.offset < 0 || ref.offset+ref.length > int64(len(file))) {
			t.Fatalf("preview %+v outside a %d-byte input", ref, len(file))
		}
	})
}
