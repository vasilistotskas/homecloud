package verify

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vasilistotskas/homecloud/internal/config"
)

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // path is under t.TempDir()
		t.Fatal(err)
	}
	return path
}

func TestContent(t *testing.T) {
	data := []byte("homecloud verification fixture")
	sum := sha256.Sum256(data)
	good := hex.EncodeToString(sum[:])
	path := writeFile(t, "f.bin", data)

	if err := Content(path, uint64(len(data)), good); err != nil {
		t.Fatalf("matching file: %v", err)
	}
	if err := Content(path, uint64(len(data))+1, good); !errors.Is(err, ErrMismatch) {
		t.Errorf("wrong size: want ErrMismatch, got %v", err)
	}
	bad := "0000000000000000000000000000000000000000000000000000000000000000"
	if err := Content(path, uint64(len(data)), bad); !errors.Is(err, ErrMismatch) {
		t.Errorf("wrong hash: want ErrMismatch, got %v", err)
	}
	err := Content(filepath.Join(t.TempDir(), "missing"), 1, good)
	if err == nil || errors.Is(err, ErrMismatch) {
		t.Errorf("missing file: want I/O error that is not ErrMismatch, got %v", err)
	}
}

// boxBytes builds a box with a 32-bit size header and payload of n bytes.
func boxBytes(typ string, n int) []byte {
	b := make([]byte, 8+n)
	binary.BigEndian.PutUint32(b, uint32(8+n)) //nolint:gosec // test sizes are tiny
	copy(b[4:8], typ)
	return b
}

// largeBox builds a box using the 64-bit largesize form.
func largeBox(typ string, n int) []byte {
	b := make([]byte, 16+n)
	binary.BigEndian.PutUint32(b, 1)
	copy(b[4:8], typ)
	binary.BigEndian.PutUint64(b[8:16], uint64(16+n)) //nolint:gosec // test sizes are tiny
	return b
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestStructure(t *testing.T) {
	toEOF := boxBytes("mdat", 32)
	binary.BigEndian.PutUint32(toEOF, 0) // size 0: extends to end of file

	tests := []struct {
		name     string
		file     string
		data     []byte
		mismatch bool
	}{
		{"mp4 ftyp moov mdat", "a.mp4", concat(boxBytes("ftyp", 8), boxBytes("moov", 4), boxBytes("mdat", 64)), false},
		{"mov starting with wide", "a.MOV", concat(boxBytes("wide", 0), boxBytes("mdat", 64), boxBytes("moov", 4)), false},
		{"64-bit mdat", "a.mov", concat(boxBytes("ftyp", 8), largeBox("mdat", 40), boxBytes("moov", 4)), false},
		{"size zero runs to eof", "a.mov", concat(boxBytes("moov", 4), toEOF), false},
		{"mp4 not starting with ftyp", "a.mp4", concat(boxBytes("wide", 0), boxBytes("moov", 4), boxBytes("mdat", 8)), true},
		{"truncated mdat", "a.mov", concat(boxBytes("moov", 4), boxBytes("mdat", 64))[:40], true},
		{"trailing garbage", "a.mov", concat(boxBytes("moov", 4), boxBytes("mdat", 8), []byte{1, 2, 3}), true},
		{"missing moov", "a.mov", concat(boxBytes("ftyp", 8), boxBytes("mdat", 8)), true},
		{"missing mdat", "a.mov", concat(boxBytes("ftyp", 8), boxBytes("moov", 8)), true},
		{"impossible size", "a.mov", concat(func() []byte { b := boxBytes("moov", 0); binary.BigEndian.PutUint32(b, 4); return b }()), true},
		{"empty file", "a.mov", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Structure(writeFile(t, tt.file, tt.data))
			switch {
			case tt.mismatch && !errors.Is(err, ErrMismatch):
				t.Fatalf("want ErrMismatch, got %v", err)
			case !tt.mismatch && err != nil:
				t.Fatalf("want ok, got %v", err)
			}
		})
	}
}

func TestCheckProbe(t *testing.T) {
	type stream = struct {
		CodecType string `json:"codec_type"`
		Duration  string `json:"duration"`
	}
	report := func(format string, streams ...stream) *probeReport {
		r := &probeReport{Streams: streams}
		r.Format.Duration = format
		return r
	}
	tests := []struct {
		name string
		rep  *probeReport
		ok   bool
	}{
		{"video with duration", report("1.24", stream{"video", "1.24"}, stream{"data", "1.24"}), true},
		{"zero container duration", report("0", stream{"video", "1.0"}), false},
		{"missing container duration", report("", stream{"video", "1.0"}), false},
		{"audio only", report("3.0", stream{"audio", "3.0"}), false},
		{"video without duration", report("3.0", stream{"video", "N/A"}), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkProbe("x.mov", tt.rep)
			if tt.ok != (err == nil) {
				t.Fatalf("ok=%v, got %v", tt.ok, err)
			}
			if err != nil && !errors.Is(err, ErrMismatch) {
				t.Fatalf("want ErrMismatch, got %v", err)
			}
		})
	}
}

// TestProbeReal exercises tier 2 and tier 3 on a real video and a truncated copy.
// Opt-in: HOMECLOUD_FFPROBE=<ffprobe.exe> HOMECLOUD_SAMPLE_VIDEO=<.mov/.mp4>.
func TestProbeReal(t *testing.T) {
	bin, sample := os.Getenv("HOMECLOUD_FFPROBE"), os.Getenv("HOMECLOUD_SAMPLE_VIDEO")
	if bin == "" || sample == "" {
		t.Skip("HOMECLOUD_FFPROBE / HOMECLOUD_SAMPLE_VIDEO not set")
	}
	cfg := config.FFmpeg{FFprobe: bin}
	ctx := context.Background()
	if err := Structure(sample); err != nil {
		t.Fatalf("Structure(real): %v", err)
	}
	if err := Probe(ctx, cfg, sample); err != nil {
		t.Fatalf("Probe(real): %v", err)
	}
	data, err := os.ReadFile(sample) //nolint:gosec // operator-supplied fixture path
	if err != nil {
		t.Fatal(err)
	}
	truncated := writeFile(t, "truncated"+filepath.Ext(sample), data[:len(data)/2])
	if err := Structure(truncated); !errors.Is(err, ErrMismatch) {
		t.Errorf("Structure(truncated): want ErrMismatch, got %v", err)
	}
	if err := Probe(ctx, cfg, truncated); !errors.Is(err, ErrMismatch) {
		t.Errorf("Probe(truncated): want ErrMismatch, got %v", err)
	}
}
