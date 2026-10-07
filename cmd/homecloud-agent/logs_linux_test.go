package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/vasilistotskas/homecloud/internal/logging"
)

// TestJournalPriorities: under journald every line starts with its
// priority, attributes reach every level, and kei's lines are warnings
// unless kei calls them errors.
func TestJournalPriorities(t *testing.T) {
	var buf bytes.Buffer
	w := &lockedWriter{w: &buf}
	log := slog.New(logging.Handler(newPriorityHandler(w))).With("component", "test")
	ctx := logging.WithRun(context.Background(), 5, "cycle")
	log.ErrorContext(ctx, "e")
	log.WarnContext(ctx, "w")
	log.InfoContext(ctx, "i")
	log.DebugContext(ctx, "d") // below the default level: not written

	k := &keiLines{w: w}
	for _, part := range []string{"2026-10-07T09:00:10Z  WARN kei::sync: slow", "\n2026-10-07T09:00:11Z ERROR kei::download: failed\npart"} {
		if _, err := k.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	want := []string{`<3>{`, `<4>{`, `<6>{`, `<4>2026-10-07T09:00:10Z  WARN`, `<3>2026-10-07T09:00:11Z ERROR`}
	if len(lines) != len(want) {
		t.Fatalf("lines = %q", lines)
	}
	for i, prefix := range want {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Fatalf("line %d = %q, want prefix %q", i, lines[i], prefix)
		}
		if i < 3 && (!strings.Contains(lines[i], `"component":"test"`) || !strings.Contains(lines[i], `"run":5`)) {
			t.Fatalf("line %d = %q, want its attributes", i, lines[i])
		}
	}
}

// TestStderrIsJournal: only a JOURNAL_STREAM naming stderr's own device and
// inode counts.
func TestStderrIsJournal(t *testing.T) {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(os.Stderr.Fd()), &st); err != nil { //nolint:gosec // a file descriptor fits an int
		t.Fatal(err)
	}
	for v, want := range map[string]bool{
		"":                                     false,
		fmt.Sprintf("%d:%d", st.Dev, st.Ino):   true,
		fmt.Sprintf("%d:%d", st.Dev, st.Ino+1): false,
	} {
		t.Setenv("JOURNAL_STREAM", v)
		if got, err := stderrIsJournal(); err != nil || got != want {
			t.Fatalf("JOURNAL_STREAM=%q: %v, %v; want %v", v, got, err, want)
		}
	}
	t.Setenv("JOURNAL_STREAM", "garbage")
	if _, err := stderrIsJournal(); err == nil {
		t.Fatal("a malformed JOURNAL_STREAM was accepted")
	}
}
