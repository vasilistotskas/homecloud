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

// boxes is what the check needs of a file's top-level boxes. Nothing is kept
// per box: a crafted file of 8-byte boxes would otherwise cost memory several
// times its size.
type boxes struct {
	count      int
	first      string
	moov, mdat bool
}

func topLevelBoxes(r io.ReaderAt, fileSize int64) (boxes, error) {
	var found boxes
	var hdr [16]byte
	for off := int64(0); off < fileSize; {
		if fileSize-off < 8 {
			return boxes{}, fmt.Errorf("%w: %d trailing bytes at offset %d are not a box", ErrMismatch, fileSize-off, off)
		}
		if _, err := r.ReadAt(hdr[:8], off); err != nil {
			return boxes{}, fmt.Errorf("read box header at %d: %w", off, err)
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		// The type is only compared, as an array: a string or a slice of it per
		// box would be allocated per box.
		typ := [4]byte(hdr[4:8])
		headerLen := int64(8)
		switch size {
		case 0:
			size = fileSize - off
		case 1:
			if fileSize-off < 16 {
				return boxes{}, fmt.Errorf("%w: truncated 64-bit size of %q at offset %d", ErrMismatch, hdr[4:8], off)
			}
			if _, err := r.ReadAt(hdr[8:16], off+8); err != nil {
				return boxes{}, fmt.Errorf("read largesize at %d: %w", off, err)
			}
			large := binary.BigEndian.Uint64(hdr[8:16])
			if large > math.MaxInt64 {
				return boxes{}, fmt.Errorf("%w: box %q at offset %d declares impossible size %d", ErrMismatch, hdr[4:8], off, large)
			}
			size = int64(large) // bounded by the check above; truncation is caught below
			headerLen = 16
		}
		if size < headerLen {
			return boxes{}, fmt.Errorf("%w: box %q at offset %d has invalid size %d", ErrMismatch, hdr[4:8], off, size)
		}
		if size > fileSize-off {
			return boxes{}, fmt.Errorf("%w: box %q at offset %d declares %d bytes, only %d remain (truncated)", ErrMismatch, hdr[4:8], off, size, fileSize-off)
		}
		if found.count == 0 {
			found.first = string(hdr[4:8])
		}
		found.count++
		found.moov = found.moov || typ == [4]byte{'m', 'o', 'o', 'v'}
		found.mdat = found.mdat || typ == [4]byte{'m', 'd', 'a', 't'}
		off += size
	}
	return found, nil
}

func checkBoxes(path string, b boxes) error {
	if b.count == 0 {
		return fmt.Errorf("%w: %s is empty", ErrMismatch, path)
	}
	isoBMFF := strings.ToLower(filepath.Ext(path)) != ".mov"
	if isoBMFF && b.first != "ftyp" {
		return fmt.Errorf("%w: %s starts with %q, ISO-BMFF requires ftyp", ErrMismatch, path, b.first)
	}
	var errs []error
	for _, need := range []struct {
		typ   string
		found bool
	}{{"moov", b.moov}, {"mdat", b.mdat}} {
		if !need.found {
			errs = append(errs, fmt.Errorf("%w: %s has no top-level %s box", ErrMismatch, path, need.typ))
		}
	}
	return errors.Join(errs...)
}
