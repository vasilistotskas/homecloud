package drive

import (
	"archive/zip"
	"bytes"
	"crypto/sha1" //nolint:gosec // Apple's signature format; homecloud's own checksum is SHA-256
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
)

// signatureSalt prefixes the bytes Apple hashes for a plain file's content
// signature: 0x01 followed by SHA-1(salt || content). Confirmed against real
// iCloud Drive downloads (2026-10-06).
const signatureSalt = "com.apple.XattrObjectSalt\x00com.apple.DataObjectSalt\x00"

// signatureType is the first byte of a signature in this format.
const signatureType = 0x01

// signatureHash returns a hash to feed the downloaded bytes into.
func signatureHash() hash.Hash {
	h := sha1.New() //nolint:gosec // Apple's signature format
	h.Write([]byte(signatureSalt))
	return h
}

// ErrBadSignature means the downloaded bytes are not the ones iCloud signed.
var ErrBadSignature = errors.New("downloaded bytes do not match Apple's signature")

// checkSignature compares a download's signature hash with Apple's signature
// (base64, as docws returns it) and returns the signature as hex.
func checkSignature(apple string, h hash.Hash) (string, error) {
	sig, err := base64.StdEncoding.DecodeString(apple)
	if err != nil {
		return "", fmt.Errorf("decode Apple signature: %w", err)
	}
	if len(sig) != 1+sha1.Size || sig[0] != signatureType {
		return "", fmt.Errorf("unknown Apple signature format (%d bytes, type %#x)", len(sig), sig[:1])
	}
	if !bytes.Equal(sig[1:], h.Sum(nil)) {
		return "", ErrBadSignature
	}
	return hex.EncodeToString(sig), nil
}

// ErrBadPackage means a package download is not an intact ZIP archive.
var ErrBadPackage = errors.New("package download is not an intact ZIP archive")

// checkPackage reads every entry of the ZIP in file (size bytes), which makes
// the ZIP reader verify each entry's CRC-32 and size.
func checkPackage(file io.ReaderAt, size int64) error {
	r, err := zip.NewReader(file, size)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadPackage, err)
	}
	for _, f := range r.File {
		if err := readEntry(f); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrBadPackage, f.Name, err)
		}
	}
	return nil
}

func readEntry(f *zip.File) error {
	if f.FileInfo().IsDir() {
		return nil
	}
	rc, err := f.Open()
	if err != nil {
		return err //nolint:wrapcheck // wrapped by checkPackage
	}
	defer rc.Close() //nolint:errcheck // read-only
	// The reader fails an entry longer than it declares; reading one byte past
	// the declared size bounds the work and still lets it notice.
	_, err = io.CopyN(io.Discard, rc, int64(min(f.UncompressedSize64, math.MaxInt64-1))+1) //nolint:gosec // bounded by the declared size
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err //nolint:wrapcheck // wrapped by checkPackage
}
