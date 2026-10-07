package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
)

// TestCrashReportedAtNextStart: the crash a previous process left is logged
// once with its cause and kept aside, the oldest kept reports are pruned,
// and this process's crashes go to a fresh file.
func TestCrashReportedAtNextStart(t *testing.T) {
	dir := t.TempDir()
	// Registered after TempDir, so it runs first: Windows deletes no open file.
	t.Cleanup(func() {
		if err := debug.SetCrashOutput(nil, debug.CrashOptions{}); err != nil {
			t.Error(err)
		}
	})
	for _, old := range []string{"20261001T000000Z", "20261002T000000Z"} {
		if err := os.WriteFile(filepath.Join(dir, crashPrefix+old+".log"), []byte("panic: old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	report := "\npanic: runtime error: index out of range [3] with length 3\n\ngoroutine 1 [running]:\nmain.main()\n"
	if err := os.WriteFile(filepath.Join(dir, crashFile), []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	l := &agentLogs{log: slog.New(slog.NewJSONHandler(&buf, nil))}
	if err := l.catchCrashes(dir, 2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"msg":"the previous agent process crashed"`) ||
		!strings.Contains(buf.String(), `"cause":"panic: runtime error: index out of range [3] with length 3"`) {
		t.Fatalf("log = %s", buf.String())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 3 || slices.Contains(names, crashPrefix+"20261001T000000Z.log") || !slices.Contains(names, crashFile) {
		t.Fatalf("log folder = %v; want the 2 newest reports and a fresh crash file", names)
	}
	if fresh, err := os.ReadFile(filepath.Join(dir, crashFile)); err != nil || len(fresh) != 0 { //nolint:gosec // the test's own temporary folder
		t.Fatalf("crash file = %q, %v; want it empty", fresh, err)
	}

	// An empty crash file means the previous process did not crash.
	buf.Reset()
	if err := l.catchCrashes(dir, 2); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("logged without a crash: %s", buf.String())
	}
}
