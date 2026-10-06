package logfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// Tests run in synctest bubbles: time.Now starts at midnight UTC 2000-01-01
// and time.Sleep moves the bubble's clock, so backup names are exact.
const (
	stamp0 = "20000101T000000Z"
	stamp1 = "20000101T000001Z"
	stamp2 = "20000101T000002Z"
)

// errs collects what OnError reports.
type errs struct {
	mu   sync.Mutex
	list []error
}

func (e *errs) add(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, err)
}

func (e *errs) len() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.list)
}

func newFile(t *testing.T, dir string, maxBytes int64, maxBackups int) (*File, *errs) {
	t.Helper()
	reported := &errs{}
	f, err := New(filepath.Join(dir, "agent.log"), Options{MaxBytes: maxBytes, MaxBackups: maxBackups, OnError: reported.add})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	return f, reported
}

func write(t *testing.T, f *File, s string) {
	t.Helper()
	if n, err := f.Write([]byte(s)); err != nil || n != len(s) {
		t.Fatalf("Write(%q) = %d, %v", s, n, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

func TestRotatesAtTheLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		f, reported := newFile(t, dir, 10, 3)
		write(t, f, "12345")
		write(t, f, "67890") // exactly the limit: no rotation yet
		time.Sleep(time.Second)
		write(t, f, "abc") // would exceed: rotate first
		if got := names(t, dir); !slices.Equal(got, []string{"agent-" + stamp1 + ".log", "agent.log"}) {
			t.Fatalf("files = %v", got)
		}
		if got := read(t, filepath.Join(dir, "agent-"+stamp1+".log")); got != "1234567890" {
			t.Fatalf("backup = %q", got)
		}
		if got := read(t, filepath.Join(dir, "agent.log")); got != "abc" {
			t.Fatalf("current = %q", got)
		}
		if reported.len() != 0 {
			t.Fatalf("reported %v", reported.list)
		}
	})
}

func TestKeepsMaxBackupsAndNothingElse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		// Files that are not this log's backups are never touched.
		for _, other := range []string{"kei.log", "agent-2026-10-05T17-46-00.000-size.log", "notes.txt"} {
			if err := os.WriteFile(filepath.Join(dir, other), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		f, reported := newFile(t, dir, 4, 2)
		write(t, f, "aaaa")
		write(t, f, "bbbb") // rotation 1: aaaa
		write(t, f, "cccc") // rotation 2 in the same second: bbbb
		time.Sleep(time.Second)
		write(t, f, "dddd") // rotation 3: cccc, aaaa pruned
		time.Sleep(time.Second)
		write(t, f, "eeee") // rotation 4: dddd, bbbb pruned
		want := []string{
			"agent-" + stamp1 + ".log",
			"agent-" + stamp2 + ".log",
			"agent-2026-10-05T17-46-00.000-size.log",
			"agent.log",
			"kei.log",
			"notes.txt",
		}
		if got := names(t, dir); !slices.Equal(got, want) {
			t.Fatalf("files = %v, want %v", got, want)
		}
		if got := read(t, filepath.Join(dir, "agent-"+stamp2+".log")); got != "dddd" {
			t.Fatalf("newest backup = %q", got)
		}
		if reported.len() != 0 {
			t.Fatalf("reported %v", reported.list)
		}
	})
}

func TestSecondRotationInTheSameSecond(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		f, _ := newFile(t, dir, 4, 5)
		write(t, f, "aaaa")
		write(t, f, "bbbb")
		write(t, f, "cccc")
		want := []string{"agent-" + stamp0 + "-2.log", "agent-" + stamp0 + ".log", "agent.log"}
		if got := names(t, dir); !slices.Equal(got, want) {
			t.Fatalf("files = %v, want %v", got, want)
		}
		if got := read(t, filepath.Join(dir, "agent-"+stamp0+"-2.log")); got != "bbbb" {
			t.Fatalf("second backup = %q", got)
		}
	})
}

func TestLargeWriteGoesWholeIntoAFreshFile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		f, _ := newFile(t, dir, 4, 2)
		write(t, f, "ab")
		big := strings.Repeat("x", 10)
		write(t, f, big)
		if got := read(t, filepath.Join(dir, "agent.log")); got != big {
			t.Fatalf("current = %q", got)
		}
		if got := read(t, filepath.Join(dir, "agent-"+stamp0+".log")); got != "ab" {
			t.Fatalf("backup = %q", got)
		}
	})
}

func TestAppendsToAnExistingLog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "agent.log")
		if err := os.WriteFile(path, []byte("earlier\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f, _ := newFile(t, dir, 10, 2)
		write(t, f, "now") // 8 + 3 > 10: the earlier content is rotated away
		if got := read(t, path); got != "now" {
			t.Fatalf("current = %q", got)
		}
		if got := read(t, filepath.Join(dir, "agent-"+stamp0+".log")); got != "earlier\n" {
			t.Fatalf("backup = %q", got)
		}
	})
}

func TestConcurrentWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		f, _ := newFile(t, dir, 100, 1000)
		const writers, lines = 8, 200
		var wg sync.WaitGroup
		for w := range writers {
			wg.Go(func() {
				for i := range lines {
					if i%50 == 0 {
						time.Sleep(time.Second)
					}
					if _, err := fmt.Fprintf(f, "w%d-%03d\n", w, i); err != nil {
						t.Error(err)
						return
					}
				}
			})
		}
		wg.Wait()
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		var all strings.Builder
		for _, name := range names(t, dir) {
			all.WriteString(read(t, filepath.Join(dir, name)))
		}
		got := strings.Split(strings.TrimSuffix(all.String(), "\n"), "\n")
		if len(got) != writers*lines {
			t.Fatalf("%d lines across the files, want %d (none lost or torn)", len(got), writers*lines)
		}
		for _, line := range got {
			if len(line) != len("w0-000") {
				t.Fatalf("torn line %q", line)
			}
		}
	})
}

func TestClose(t *testing.T) {
	dir := t.TempDir()
	f, _ := newFile(t, dir, 10, 2)
	write(t, f, "x")
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if _, err := f.Write([]byte("y")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Write after Close = %v, want os.ErrClosed", err)
	}
}

func TestOpensOnceTheFolderCanBeCreated(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "drive")
	// A file where the log folder should be: the folder cannot be created.
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, _ := newFile(t, filepath.Join(parent, "logs"), 10, 2)
	if _, err := f.Write([]byte("lost")); err == nil {
		t.Fatal("write without a folder succeeded")
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	write(t, f, "back")
	if got := read(t, filepath.Join(parent, "logs", "agent.log")); got != "back" {
		t.Fatalf("log = %q", got)
	}
}

// A symbolic link placed in the log folder cannot redirect the log outside it.
func TestStaysInsideTheLogFolder(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "logs")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "agent.log")); err != nil {
		t.Skipf("symbolic links are not available here: %v", err)
	}
	f, _ := newFile(t, dir, 10, 2)
	if _, err := f.Write([]byte("x")); err == nil {
		t.Fatal("wrote through a link that leaves the log folder")
	}
	if got := read(t, outside); got != "keep" {
		t.Fatalf("outside file = %q", got)
	}
}

func TestOptionsAreRequired(t *testing.T) {
	report := func(error) {}
	for name, o := range map[string]Options{
		"no size":    {MaxBackups: 1, OnError: report},
		"no backups": {MaxBytes: 1, OnError: report},
		"no OnError": {MaxBytes: 1, MaxBackups: 1},
	} {
		if _, err := New(filepath.Join(t.TempDir(), "a.log"), o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := New(filepath.Join(t.TempDir(), "a.log"), Options{MaxBytes: 1, MaxBackups: 1, OnError: report}); err != nil {
		t.Fatal(err)
	}
}
