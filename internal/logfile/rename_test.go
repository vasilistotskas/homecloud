package logfile

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// hold opens the log the way a viewer would. Go opens files without
// FILE_SHARE_DELETE, so on Windows the handle blocks renaming the file.
func hold(t *testing.T, dir string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "agent.log")) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// When another process holds the log (someone tailing it on Windows), the
// rotation fails: the log keeps being written, the failure is reported
// once, and the rotation happens at a later write once the file is free.
func TestRotationRetriesWhileTheLogIsHeld(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows refuses to rename a file another handle holds; POSIX renames open files")
	}
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		f, reported := newFile(t, dir, 4, 2)
		write(t, f, "aaaa")
		held := hold(t, dir)
		write(t, f, "bbbb")
		time.Sleep(time.Second)
		write(t, f, "cccc")
		if got := read(t, filepath.Join(dir, "agent.log")); got != "aaaabbbbcccc" {
			t.Fatalf("while held, the log = %q; want every write kept", got)
		}
		if reported.len() != 1 || !strings.Contains(reported.list[0].Error(), "retried at the next write") {
			t.Fatalf("reported %v; want one report for the run of failures", reported.list)
		}
		if closeErr := held.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		time.Sleep(time.Second)
		write(t, f, "dddd")
		if got := names(t, dir); !slices.Equal(got, []string{"agent-" + stamp2 + ".log", "agent.log"}) {
			t.Fatalf("files = %v", got)
		}
		if got := read(t, filepath.Join(dir, "agent.log")); got != "dddd" {
			t.Fatalf("current = %q", got)
		}

		// A new run of failures is reported again.
		held = hold(t, dir)
		t.Cleanup(func() { _ = held.Close() })
		write(t, f, "eeee")
		if reported.len() != 2 {
			t.Fatalf("reported %d times, want 2", reported.len())
		}
	})
}

// OnError may write to the same File without deadlocking or recursing.
func TestReportingIntoTheSameLog(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("needs a failing rotation, which only Windows produces (a held file)")
	}
	dir := t.TempDir()
	var f *File
	var err error
	f, err = New(filepath.Join(dir, "agent.log"), Options{
		MaxBytes:   4,
		MaxBackups: 2,
		OnError: func(e error) {
			if _, werr := f.Write([]byte("rotation failed: " + e.Error() + "\n")); werr != nil {
				t.Error(werr)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	write(t, f, "aaaa")
	held := hold(t, dir)
	t.Cleanup(func() { _ = held.Close() })
	write(t, f, "bbbb")
	// The report follows the write that triggered it, once.
	got := read(t, filepath.Join(dir, "agent.log"))
	if !strings.HasPrefix(got, "aaaabbbbrotation failed: ") || strings.Count(got, "rotation failed") != 1 {
		t.Fatalf("log = %q", got)
	}
}
