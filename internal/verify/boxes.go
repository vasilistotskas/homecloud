package verify

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Structure walks the top-level boxes of an ISO-BMFF / QuickTime file (tier 2).
//
// Box header (ISO/IEC 14496-12 §4.2, shared by QuickTime): a 32-bit big-endian
// size and a 4-character type; size 1 means a 64-bit size follows, size 0 means
// the box extends to end of file. The chain must be contiguous, end exactly at
// EOF and contain both moov and mdat. ISO-BMFF extensions must start with ftyp;
// QuickTime .mov may start with any top-level atom (kei wiki "Content Validation").
func Structure(path string) error {
	f, err := os.Open(path) //nolint:gosec // path comes from the ledger
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle; close errors cannot affect the result

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	boxes, err := topLevelBoxes(f, info.Size())
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return checkBoxes(path, boxes)
}

// box is a top-level box type and its declared size in bytes.
type box struct {
	typ  string
	size int64
}

func topLevelBoxes(r io.ReaderAt, fileSize int64) ([]box, error) {
	var boxes []box
	var hdr [16]byte
	for off := int64(0); off < fileSize; {
		if fileSize-off < 8 {
			return nil, fmt.Errorf("%w: %d trailing bytes at offset %d are not a box", ErrMismatch, fileSize-off, off)
		}
		if _, err := r.ReadAt(hdr[:8], off); err != nil {
			return nil, fmt.Errorf("read box header at %d: %w", off, err)
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		headerLen := int64(8)
		switch size {
		case 0:
			size = fileSize - off
		case 1:
			if fileSize-off < 16 {
				return nil, fmt.Errorf("%w: truncated 64-bit size of %q at offset %d", ErrMismatch, typ, off)
			}
			if _, err := r.ReadAt(hdr[8:16], off+8); err != nil {
				return nil, fmt.Errorf("read largesize at %d: %w", off, err)
			}
			large := binary.BigEndian.Uint64(hdr[8:16])
			if large > math.MaxInt64 {
				return nil, fmt.Errorf("%w: box %q at offset %d declares impossible size %d", ErrMismatch, typ, off, large)
			}
			size = int64(large) // bounded by the check above; truncation is caught below
			headerLen = 16
		}
		if size < headerLen {
			return nil, fmt.Errorf("%w: box %q at offset %d has invalid size %d", ErrMismatch, typ, off, size)
		}
		if size > fileSize-off {
			return nil, fmt.Errorf("%w: box %q at offset %d declares %d bytes, only %d remain (truncated)", ErrMismatch, typ, off, size, fileSize-off)
		}
		boxes = append(boxes, box{typ: typ, size: size})
		off += size
	}
	return boxes, nil
}

func checkBoxes(path string, boxes []box) error {
	if len(boxes) == 0 {
		return fmt.Errorf("%w: %s is empty", ErrMismatch, path)
	}
	isoBMFF := strings.ToLower(filepath.Ext(path)) != ".mov"
	if isoBMFF && boxes[0].typ != "ftyp" {
		return fmt.Errorf("%w: %s starts with %q, ISO-BMFF requires ftyp", ErrMismatch, path, boxes[0].typ)
	}
	var errs []error
	for _, need := range []string{"moov", "mdat"} {
		if !hasBox(boxes, need) {
			errs = append(errs, fmt.Errorf("%w: %s has no top-level %s box", ErrMismatch, path, need))
		}
	}
	return errors.Join(errs...)
}

func hasBox(boxes []box, typ string) bool {
	for _, b := range boxes {
		if b.typ == typ {
			return true
		}
	}
	return false
}
