package thumbs

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"iter"
	"slices"
)

// The DNG preview walker follows the Digital Negative Specification 1.7.1.0
// (Adobe, September 2023), which builds on TIFF 6.0 (Adobe, 1992) and TIFF
// Technical Note 1 (SubIFDs).

// Tags the walker reads.
const (
	tagNewSubfileType  = 254
	tagImageWidth      = 256
	tagImageLength     = 257
	tagCompression     = 259
	tagStripOffsets    = 273
	tagStripByteCounts = 279
	tagTileOffsets     = 324
	tagTileByteCounts  = 325
	tagSubIFDs         = 330
	tagDNGVersion      = 50706
)

const (
	// subfilePrimaryPreview is the NewSubFileType of a DNG's primary rendered
	// preview. DNG readers display it by default; alternate renderings
	// (0x10001), transparency (4, 5), depth (8, 9), enhanced (16) and
	// semantic-mask (0x10004) IFDs are not it (DNG 1.7.1.0, NewSubFileType).
	subfilePrimaryPreview = 1
	// compressionJPEG is "JPEG compressed data"; for a YCbCr or 8-bit gray
	// preview it must be baseline DCT JPEG (DNG 1.7.1.0, Compression).
	compressionJPEG = 7
	// compressionJPEGXL is JPEG XL, new in DNG 1.7.0.0, which may also hold a
	// preview (DNG 1.7.1.0, Compression).
	compressionJPEGXL = 52546
	// tiffVersion is a classic TIFF's version number. BigTIFF (43), which DNG
	// allows for files over 4 GB, is not read: no file in the archive needs
	// it, so a BigTIFF DNG goes to FFmpeg like any other file.
	tiffVersion = 42
)

// Limits against malformed or hostile files. Real DNGs stay far below them:
// the 812 DNGs in the archive have at most 32 IFD0 entries and five IFDs.
const (
	// maxEntries bounds one IFD's entries (the count is a 16-bit field).
	maxEntries = 1024
	// maxIFDs bounds the IFDs read from one file.
	maxIFDs = 64
)

// Integer field types (TIFF 6.0 section 2; IFD from TIFF Technical Note 1).
const (
	typeByte  = 1
	typeShort = 3
	typeLong  = 4
	typeIFD   = 13
)

// fieldSize is each TIFF field type's size in bytes.
var fieldSize = map[uint16]int64{
	1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 4, 10: 8, 11: 4, 12: 8, 13: 4,
}

var (
	// errMalformed marks a TIFF whose structure is broken.
	errMalformed = errors.New("malformed TIFF")
	// errNoPreview: the DNG has no primary preview a JPEG or JPEG XL decoder renders.
	errNoPreview = errors.New("no JPEG or JPEG XL preview")
)

// imageRef is a stream inside the file.
type imageRef struct {
	offset, length int64
	format         string
}

// tiff reads a classic TIFF's directories through bounds-checked reads.
type tiff struct {
	r     io.ReaderAt
	size  int64
	order binary.ByteOrder
}

// read returns n bytes at off, which must lie inside the file.
func (t tiff) read(off, n int64) ([]byte, error) {
	if off < 0 || n < 0 || off > t.size || n > t.size-off {
		return nil, fmt.Errorf("%w: %d bytes at %d are outside the %d-byte file", errMalformed, n, off, t.size)
	}
	buf := make([]byte, n)
	if _, err := t.r.ReadAt(buf, off); err != nil {
		return nil, fmt.Errorf("read %d bytes at %d: %w", n, off, err)
	}
	return buf, nil
}

// ifdEntry is one 12-byte IFD entry as stored (TIFF 6.0 section 2).
type ifdEntry struct {
	Tag, Type uint16
	Count     uint32
	Value     [4]byte // the value itself if it fits, else its offset
}

// field is an entry as stored. Its values are read only for the few tags the
// walker uses (uints): a directory holds up to maxEntries fields, each of
// which may claim nearly the whole file.
type field struct {
	typ   uint16
	count int64
	raw   [4]byte // the value itself if it fits, else its offset
}

// directory is one IFD's fields by tag.
type directory map[uint16]field

// ifd reads the directory at off.
func (t tiff) ifd(off int64) (directory, error) {
	head, err := t.read(off, 2)
	if err != nil {
		return nil, err
	}
	n := int64(t.order.Uint16(head))
	if n == 0 || n > maxEntries {
		return nil, fmt.Errorf("%w: the IFD at %d has %d entries", errMalformed, off, n)
	}
	raw, err := t.read(off+2, n*12)
	if err != nil {
		return nil, err
	}
	d := make(directory, n)
	for i := range n {
		var e ifdEntry
		if _, err = binary.Decode(raw[i*12:], t.order, &e); err != nil {
			return nil, fmt.Errorf("decode IFD entry: %w", err)
		}
		size, known := fieldSize[e.Type]
		if !known {
			continue // TIFF 6.0: readers skip fields of unknown types
		}
		if total := size * int64(e.Count); total > t.size {
			return nil, fmt.Errorf("%w: tag %d claims %d bytes", errMalformed, e.Tag, total)
		}
		d[e.Tag] = field{typ: e.Type, count: int64(e.Count), raw: e.Value}
	}
	return d, nil
}

// uints reads an integer field's values, refusing more than limit of them
// before reading anything.
func (t tiff) uints(f field, limit int64) ([]int64, error) {
	if f.typ != typeByte && f.typ != typeShort && f.typ != typeLong && f.typ != typeIFD {
		return nil, fmt.Errorf("%w: an integer field of type %d", errMalformed, f.typ)
	}
	if f.count > limit {
		return nil, fmt.Errorf("%w: %d values where at most %d belong", errMalformed, f.count, limit)
	}
	size := fieldSize[f.typ]
	value := f.raw[:]
	if total := size * f.count; total > 4 {
		var err error
		if value, err = t.read(int64(t.order.Uint32(f.raw[:])), total); err != nil {
			return nil, err
		}
	}
	out := make([]int64, f.count)
	for i := range out {
		v := value[int64(i)*size:]
		switch size {
		case 1:
			out[i] = int64(v[0])
		case 2:
			out[i] = int64(t.order.Uint16(v))
		default:
			out[i] = int64(t.order.Uint32(v))
		}
	}
	return out, nil
}

// uint returns a single-valued integer field (held in the entry itself).
func (t tiff) uint(d directory, tag uint16) (int64, bool) {
	f, ok := d[tag]
	if !ok || f.count != 1 {
		return 0, false
	}
	v, err := t.uints(f, 1)
	if err != nil {
		return 0, false
	}
	return v[0], true
}

// tree yields IFD0 and every SubIFD below it, each directory once. DNG
// stores its images in this tree; IFD chains are not used (DNG 1.7.1.0,
// SubIFD Trees).
func (t tiff) tree(ifd0Off int64, ifd0 directory) iter.Seq2[directory, error] {
	return func(yield func(directory, error) bool) {
		visited := map[int64]bool{ifd0Off: true}
		pending := []directory{ifd0}
		for len(pending) > 0 {
			d := pending[0]
			pending = pending[1:]
			if !yield(d, nil) {
				return
			}
			f, ok := d[tagSubIFDs]
			if !ok {
				continue
			}
			offs, err := t.uints(f, maxIFDs)
			if err != nil {
				yield(nil, fmt.Errorf("SubIFDs: %w", err))
				return
			}
			for _, off := range offs {
				if visited[off] {
					continue
				}
				if len(visited) == maxIFDs {
					yield(nil, fmt.Errorf("%w: more than %d IFDs", errMalformed, maxIFDs))
					return
				}
				visited[off] = true
				sub, err := t.ifd(off)
				if err != nil {
					yield(nil, err)
					return
				}
				pending = append(pending, sub)
			}
		}
	}
}

// image returns where a directory's image is stored when it is one strip or
// one tile; an image in several pieces is no single stream.
func (t tiff) image(d directory) (imageRef, bool) {
	for _, tags := range [][2]uint16{{tagStripOffsets, tagStripByteCounts}, {tagTileOffsets, tagTileByteCounts}} {
		off, okOff := t.uint(d, tags[0])
		n, okLen := t.uint(d, tags[1])
		if okOff && okLen {
			return imageRef{offset: off, length: n}, true
		}
	}
	return imageRef{}, false
}

// isJXL reports whether data at off begins with a JPEG XL bare codestream
// (0xFF, 0x0A) or ISOBMFF container (0x00, 0x00, 0x00, 0x0C, 'J', 'X', 'L', ' ', 0x0D, 0x0A, 0x87, 0x0A).
func isJXL(r io.ReaderAt, off, n int64) bool {
	if n < 2 {
		return false
	}
	var head [12]byte
	toRead := min(n, int64(len(head)))
	if _, err := r.ReadAt(head[:toRead], off); err != nil {
		return false
	}
	if head[0] == 0xff && head[1] == 0x0a {
		return true
	}
	if toRead >= 12 && bytes.Equal(head[:12], []byte{0, 0, 0, 0x0c, 'J', 'X', 'L', ' ', 0x0d, 0x0a, 0x87, 0x0a}) {
		return true
	}
	return false
}

// dngPreview finds a DNG's primary preview as a JPEG or JPEG XL stream: the IFD in the
// SubIFD tree with NewSubFileType 1, stored as one baseline JPEG or JPEG XL strip or
// tile, the largest picture if there are several (a thumbnail and a
// full-size rendering). Each candidate must decode as baseline or
// progressive JPEG or match the JPEG XL signature, so lossless JPEG (the raw image's encoding) is never
// picked. A DNG is a classic TIFF whose IFD0 has DNGVersion; isDNG is false,
// without error, for anything else.
func dngPreview(r io.ReaderAt, size int64) (preview imageRef, isDNG bool, err error) {
	head := make([]byte, 8)
	if _, err = r.ReadAt(head, 0); err != nil {
		if errors.Is(err, io.EOF) {
			return imageRef{}, false, nil // shorter than a TIFF header
		}
		return imageRef{}, false, fmt.Errorf("read the header: %w", err)
	}
	t := tiff{r: r, size: size}
	switch string(head[:2]) {
	case "II":
		t.order = binary.LittleEndian
	case "MM":
		t.order = binary.BigEndian
	default:
		return imageRef{}, false, nil
	}
	var h struct {
		Version uint16
		IFD0    uint32
	}
	if _, err = binary.Decode(head[2:], t.order, &h); err != nil {
		return imageRef{}, false, fmt.Errorf("decode the header: %w", err)
	}
	if h.Version != tiffVersion {
		return imageRef{}, false, nil
	}
	ifd0, err := t.ifd(int64(h.IFD0))
	if err != nil {
		return imageRef{}, false, err
	}
	if _, ok := ifd0[tagDNGVersion]; !ok {
		return imageRef{}, false, nil
	}

	type candidate struct {
		imageRef
		area int
	}
	var candidates []candidate
	for d, err := range t.tree(int64(h.IFD0), ifd0) {
		if err != nil {
			return imageRef{}, true, err
		}
		if sub, _ := t.uint(d, tagNewSubfileType); sub != subfilePrimaryPreview {
			continue
		}
		ref, ok := t.image(d)
		if !ok {
			continue
		}
		if ref.offset < 0 || ref.length <= 0 || ref.offset > size || ref.length > size-ref.offset {
			return imageRef{}, true, fmt.Errorf("%w: image data at %d (%d bytes) is outside the file", errMalformed, ref.offset, ref.length)
		}
		switch c, _ := t.uint(d, tagCompression); c {
		case compressionJPEG:
			cfg, err := jpeg.DecodeConfig(io.NewSectionReader(r, ref.offset, ref.length))
			if err == nil {
				ref.format = "jpeg_pipe"
				candidates = append(candidates, candidate{ref, cfg.Width * cfg.Height})
			}
		case compressionJPEGXL:
			if isJXL(r, ref.offset, ref.length) {
				ref.format = "jpegxl_pipe"
				w, _ := t.uint(d, tagImageWidth)
				h, _ := t.uint(d, tagImageLength)
				candidates = append(candidates, candidate{ref, int(w * h)})
			}
		}
	}
	if len(candidates) == 0 {
		return imageRef{}, true, errNoPreview
	}
	best := slices.MaxFunc(candidates, func(a, b candidate) int {
		return cmp.Or(cmp.Compare(a.area, b.area), cmp.Compare(a.length, b.length))
	})
	return best.imageRef, true, nil
}
