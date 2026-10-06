package thumbs

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

// TIFF tags used by the fixture (TIFF 6.0 and DNG 1.6 specifications).
const (
	tagNewSubfileType  = 254
	tagImageWidth      = 256
	tagImageLength     = 257
	tagBitsPerSample   = 258
	tagCompression     = 259
	tagPhotometric     = 262
	tagStripOffsets    = 273
	tagSamplesPerPixel = 277
	tagRowsPerStrip    = 278
	tagStripByteCounts = 279
	tagDNGVersion      = 50706

	typeByte  = 1
	typeShort = 3
	typeLong  = 4
)

type ifdEntry struct {
	tag, typ uint16
	count    uint32
	value    uint32 // inline value (4 bytes or fewer)
}

// testJPEG encodes a w×h JPEG.
func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255}) //nolint:gosec // test pattern, wraps by design
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// writeTIFF writes a little-endian TIFF whose IFD0 is a reduced-resolution
// image stored as one JPEG strip, the layout of an Apple ProRAW DNG's preview.
// With dng, IFD0 also carries DNGVersion 1.6.0.0, which makes the file a DNG.
func writeTIFF(t *testing.T, dir string, preview []byte, w, h int, dng bool) string {
	t.Helper()
	entries := []ifdEntry{
		{tagNewSubfileType, typeLong, 1, 1},
		{tagImageWidth, typeLong, 1, uint32(w)},  //nolint:gosec // small test dimensions
		{tagImageLength, typeLong, 1, uint32(h)}, //nolint:gosec // small test dimensions
		{tagBitsPerSample, typeShort, 1, 8},
		{tagCompression, typeShort, 1, 7},
		{tagPhotometric, typeShort, 1, 6},
		{tagStripOffsets, typeLong, 1, 0}, // patched below
		{tagSamplesPerPixel, typeShort, 1, 3},
		{tagRowsPerStrip, typeLong, 1, uint32(h)},               //nolint:gosec // small test dimensions
		{tagStripByteCounts, typeLong, 1, uint32(len(preview))}, //nolint:gosec // small test JPEG
	}
	if dng {
		entries = append(entries, ifdEntry{tagDNGVersion, typeByte, 4, binary.LittleEndian.Uint32([]byte{1, 6, 0, 0})})
	}
	const ifdOffset = 8
	dataOffset := uint32(ifdOffset + 2 + 12*len(entries) + 4) //nolint:gosec // small fixed layout
	for i := range entries {
		if entries[i].tag == tagStripOffsets {
			entries[i].value = dataOffset
		}
	}

	var buf bytes.Buffer
	le := binary.LittleEndian
	buf.WriteString("II")
	_ = binary.Write(&buf, le, uint16(42))
	_ = binary.Write(&buf, le, uint32(ifdOffset))
	_ = binary.Write(&buf, le, uint16(len(entries))) //nolint:gosec // a dozen entries
	for _, e := range entries {
		_ = binary.Write(&buf, le, e.tag)
		_ = binary.Write(&buf, le, e.typ)
		_ = binary.Write(&buf, le, e.count)
		if e.typ == typeShort {
			_ = binary.Write(&buf, le, uint16(e.value)) //nolint:gosec // SHORT tags hold 16-bit values
			_ = binary.Write(&buf, le, uint16(0))
		} else {
			_ = binary.Write(&buf, le, e.value)
		}
	}
	_ = binary.Write(&buf, le, uint32(0)) // no further IFDs
	buf.Write(preview)

	name := "image.tif"
	if dng {
		name = "IMG_0001.DNG"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRawPreview(t *testing.T) {
	dir := t.TempDir()
	preview := testJPEG(t, 64, 48)

	got, isRaw, err := rawPreview(writeTIFF(t, dir, preview, 64, 48, true))
	if err != nil || !isRaw {
		t.Fatalf("DNG: isRaw=%v err=%v, want the embedded preview", isRaw, err)
	}
	if !bytes.Equal(got, preview) {
		t.Fatalf("DNG preview = %d bytes, want the embedded %d-byte JPEG", len(got), len(preview))
	}

	if _, isRaw, err := rawPreview(writeTIFF(t, dir, preview, 64, 48, false)); err != nil || isRaw {
		t.Fatalf("plain TIFF: isRaw=%v err=%v, want it rendered as itself", isRaw, err)
	}

	jpg := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(jpg, preview, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, isRaw, err := rawPreview(jpg); err != nil || isRaw {
		t.Fatalf("JPEG: isRaw=%v err=%v", isRaw, err)
	}

	// A QuickTime movie starts with an ftyp box imagemeta does not classify.
	mov := filepath.Join(dir, "clip.mov")
	if err := os.WriteFile(mov, append([]byte{0, 0, 0, 0x14, 'f', 't', 'y', 'p', 'q', 't', ' ', ' '}, make([]byte, 64)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, isRaw, err := rawPreview(mov); err != nil || isRaw {
		t.Fatalf("movie: isRaw=%v err=%v, want it rendered as itself", isRaw, err)
	}

	if _, _, err := rawPreview(filepath.Join(dir, "missing.dng")); err == nil {
		t.Fatal("missing file: want an error")
	}
}
