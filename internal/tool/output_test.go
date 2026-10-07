package tool

import (
	"strings"
	"testing"
)

func TestTailKeepsTheEnd(t *testing.T) {
	var tl Tail
	if _, err := tl.Write([]byte("  short\n")); err != nil || tl.String() != "short" {
		t.Fatalf("short output: %q, %v", tl.String(), err)
	}
	long := strings.Repeat("x", 3*diagnosticBytes) + "the reason"
	for i := 0; i < len(long); i += 100 { // as a program writes, in pieces
		if _, err := tl.Write([]byte(long[i:min(i+100, len(long))])); err != nil {
			t.Fatal(err)
		}
	}
	got := tl.String()
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "the reason") || len(tl.buf) != diagnosticBytes {
		t.Fatalf("long output kept %d bytes: %.40q…%q", len(tl.buf), got, got[len(got)-12:])
	}
}

func TestHeadKeepsTheStart(t *testing.T) {
	h := Head{Max: 8}
	for _, part := range []string{"12345", "6789", "0"} {
		if n, err := h.Write([]byte(part)); err != nil || n != len(part) {
			t.Fatalf("Write(%q) = %d, %v", part, n, err)
		}
	}
	if string(h.Bytes()) != "12345678" || !h.Truncated() {
		t.Fatalf("kept %q, truncated %v", h.Bytes(), h.Truncated())
	}
	small := Head{Max: 8}
	if _, err := small.Write([]byte("1234")); err != nil || small.Truncated() {
		t.Fatalf("an output that fits was truncated: %v", err)
	}
}
