package tool

import (
	"bytes"
	"strings"
)

// diagnosticBytes bounds what Tail keeps: enough for the last few lines a
// program prints when it fails.
const diagnosticBytes = 1 << 10

// Tail keeps the last bytes a program writes to it (its diagnostics, whose
// end says what went wrong) without holding all of a long output.
type Tail struct {
	buf []byte
	cut bool
}

// Write keeps the end of what was written; it never fails.
func (t *Tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - diagnosticBytes; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
		t.cut = true
	}
	return len(p), nil
}

// String is what was kept, trimmed, starting with "…" when its start was cut.
func (t *Tail) String() string {
	s := strings.TrimSpace(string(t.buf))
	if t.cut {
		return "…" + s
	}
	return s
}

// Head keeps the first Max bytes a program writes to it (an output to parse)
// and notes whether there was more.
type Head struct {
	Max  int
	buf  bytes.Buffer
	more bool
}

// Write keeps what fits; it never fails.
func (h *Head) Write(p []byte) (int, error) {
	n := len(p)
	if room := h.Max - h.buf.Len(); len(p) > room {
		h.more = true
		p = p[:max(room, 0)]
	}
	h.buf.Write(p)
	return n, nil
}

// Bytes is what was kept.
func (h *Head) Bytes() []byte { return h.buf.Bytes() }

// Truncated reports whether the program wrote more than Max bytes.
func (h *Head) Truncated() bool { return h.more }
