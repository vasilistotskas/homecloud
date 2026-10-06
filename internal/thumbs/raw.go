package thumbs

import (
	"errors"
	"fmt"
	"os"

	"github.com/evanoberholster/imagemeta"
	"github.com/evanoberholster/imagemeta/imagetype"
)

// rawPreview returns the largest JPEG preview embedded in path when path is a
// TIFF-based camera raw (DNG, including Apple ProRAW, NEF, CR2, ARW, ...);
// isRaw is false, with no error, for every other file.
//
// FFmpeg cannot develop raw sensor data. Raws carry the camera's own rendering
// as an embedded JPEG (ProRAW keeps a full-size one in IFD0), so that preview
// is what the library shows. The preview keeps its EXIF orientation, which
// FFmpeg applies when rendering it.
func rawPreview(path string) (jpeg []byte, isRaw bool, err error) {
	f, err := os.Open(path) //nolint:gosec // path comes from the ledger
	if err != nil {
		return nil, false, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle; close errors cannot affect the result

	ft, err := imagetype.ReadAt(f)
	switch {
	case errors.Is(err, imagetype.ErrImageTypeNotFound), errors.Is(err, imagetype.ErrDataLength):
		return nil, false, nil // not an image format imagemeta knows (videos): not a raw
	case err != nil:
		return nil, false, fmt.Errorf("sniff %s: %w", path, err)
	}
	// A DNG starts like any TIFF; DecodeTiff tells them apart by DNGVersion.
	if !ft.IsRAW() && ft != imagetype.ImageTiff {
		return nil, false, nil
	}
	meta, err := imagemeta.DecodeTiff(f)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	if !meta.ImageType.IsRAW() {
		return nil, false, nil
	}

	previews, err := imagemeta.TIFFPreviews(f)
	if err != nil {
		return nil, true, fmt.Errorf("find previews in %s: %w", path, err)
	}
	if len(previews) == 0 {
		return nil, true, fmt.Errorf("raw %s has no embedded JPEG preview", path)
	}
	// TIFFPreviews sorts by length, largest (the full-size rendering) first.
	jpeg, err = imagemeta.ExtractPreview(f, previews[0])
	if err != nil {
		return nil, true, fmt.Errorf("extract preview of %s: %w", path, err)
	}
	return jpeg, true, nil
}
