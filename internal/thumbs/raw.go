package thumbs

import (
	"fmt"
	"os"
)

// rawPreview returns the largest JPEG preview embedded in path when path is a
// DNG (including Apple ProRAW); isRaw is false, with no error, for every
// other file.
//
// FFmpeg cannot develop raw sensor data, and its TIFF decoder renders a
// ProRAW's JPEG-compressed preview as a blank frame. A DNG carries the
// camera's own rendering as an embedded JPEG (ProRAW keeps a full-size one in
// IFD0), so that preview is what the library shows. The preview keeps its
// EXIF orientation, which FFmpeg applies when rendering it.
func rawPreview(path string) (preview []byte, isRaw bool, err error) {
	f, err := os.Open(path) //nolint:gosec // path comes from the ledger
	if err != nil {
		return nil, false, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle; close errors cannot affect the result

	info, err := f.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("stat %s: %w", path, err)
	}
	ref, isDNG, err := dngPreview(f, info.Size())
	switch {
	case err != nil:
		return nil, isDNG, fmt.Errorf("find the preview in %s: %w", path, err)
	case !isDNG:
		return nil, false, nil
	}
	preview = make([]byte, ref.length)
	if _, err := f.ReadAt(preview, ref.offset); err != nil {
		return nil, true, fmt.Errorf("extract the preview of %s: %w", path, err)
	}
	return preview, true, nil
}
