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

// TIFF tags the fixture writes besides those the walker reads (TIFF 6.0).
const (
	tagBitsPerSample   = 258
	tagPhotometric     = 262
	tagSamplesPerPixel = 277
	tagRowsPerStrip    = 278
)

// photometricYCbCr is the PhotometricInterpretation of a JPEG preview.
const photometricYCbCr = 6

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

// writeTIFF writes a little-endian TIFF whose IFD0 is a w×h primary preview
// stored as one JPEG strip, the layout of an Apple ProRAW DNG's preview. With
// dng, IFD0 also carries DNGVersion, which makes the file a DNG.
func writeTIFF(t *testing.T, dir string, preview []byte, w, h int, dng bool) string {
	t.Helper()
	fields := append(stripImage(subfilePrimaryPreview, compressionJPEG, "preview"),
		long(tagImageWidth, uint32(w)),   //nolint:gosec // small test dimensions
		long(tagImageLength, uint32(h)),  //nolint:gosec // small test dimensions
		long(tagRowsPerStrip, uint32(h)), //nolint:gosec // small test dimensions
		short(tagBitsPerSample, 8),
		short(tagPhotometric, photometricYCbCr),
		short(tagSamplesPerPixel, 3),
	)
	name := "image.tif"
	if dng {
		fields = append(fields, dngVersion())
		name = "IMG_0001.DNG"
	}
	b := &tiffBuilder{order: binary.LittleEndian}
	b.ifd("ifd0", "", fields...)
	b.data("preview", preview)

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b.bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRawPreview(t *testing.T) {
	dir := t.TempDir()
	preview := testJPEG(t, 64, 48)

	got, format, isRaw, err := rawPreview(writeTIFF(t, dir, preview, 64, 48, true))
	if err != nil || !isRaw {
		t.Fatalf("DNG: isRaw=%v err=%v, want the embedded preview", isRaw, err)
	}
	if format != demuxerJPEG {
		t.Fatalf("DNG demuxer = %s, want %s", format, demuxerJPEG)
	}
	if !bytes.Equal(got, preview) {
		t.Fatalf("DNG preview = %d bytes, want the embedded %d-byte JPEG", len(got), len(preview))
	}

	jxl := []byte{0xff, 0x0a, 1, 2, 3, 4}
	jxlPath := filepath.Join(dir, "IMG_JXL.DNG")
	if writeErr := os.WriteFile(jxlPath, jxlDNG(jxl, long(tagImageWidth, 64), long(tagImageLength, 48)), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	gotJXL, jxlFormat, isRawJXL, jxlErr := rawPreview(jxlPath)
	if jxlErr != nil || !isRawJXL {
		t.Fatalf("JPEG XL DNG: isRaw=%v err=%v", isRawJXL, jxlErr)
	}
	if jxlFormat != demuxerJPEGXL {
		t.Fatalf("JPEG XL demuxer = %s, want %s", jxlFormat, demuxerJPEGXL)
	}
	if !bytes.Equal(gotJXL, jxl) {
		t.Fatalf("JPEG XL preview = %x, want %x", gotJXL, jxl)
	}

	if _, _, isRaw, err := rawPreview(writeTIFF(t, dir, preview, 64, 48, false)); err != nil || isRaw {
		t.Fatalf("plain TIFF: isRaw=%v err=%v, want it rendered as itself", isRaw, err)
	}

	jpg := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(jpg, preview, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, isRaw, err := rawPreview(jpg); err != nil || isRaw {
		t.Fatalf("JPEG: isRaw=%v err=%v", isRaw, err)
	}

	// A QuickTime movie starts with an ftyp box, not a TIFF header.
	mov := filepath.Join(dir, "clip.mov")
	if err := os.WriteFile(mov, append([]byte{0, 0, 0, 0x14, 'f', 't', 'y', 'p', 'q', 't', ' ', ' '}, make([]byte, 64)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, isRaw, err := rawPreview(mov); err != nil || isRaw {
		t.Fatalf("movie: isRaw=%v err=%v, want it rendered as itself", isRaw, err)
	}

	if _, _, _, err := rawPreview(filepath.Join(dir, "missing.dng")); err == nil {
		t.Fatal("missing file: want an error")
	}
}
