package files

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
)

const day = 24 * time.Hour

type fixture struct {
	svc *Service
	dir string
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{dir: t.TempDir(), now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
	f.svc = New(config.Files{
		Locations:      []config.Location{{Name: "Uploads", Path: f.dir}},
		TrashRetention: config.Duration(30 * day),
		UploadExpiry:   config.Duration(7 * day),
	}, func(string) (uint64, error) { return 1 << 30, nil }, func() time.Time { return f.now }, slog.New(slog.DiscardHandler))
	return f
}

func (f *fixture) write(t *testing.T, rel, data string) {
	t.Helper()
	p := filepath.Join(f.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *fixture) names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := f.svc.List("Uploads", dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		n := e.Name
		if e.Dir {
			n += "/"
		}
		out = append(out, n)
	}
	return out
}

func TestListFoldersFirstAndHidesReserved(t *testing.T) {
	f := newFixture(t)
	f.write(t, "b.txt", "b")
	f.write(t, "A.txt", "a")
	f.write(t, "zz/x", "x")
	f.write(t, ".homecloud/trash/x", "x")
	got := f.names(t, "")
	want := []string{"zz/", "A.txt", "b.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	entries, _ := f.svc.List("Uploads", "")
	if entries[1].Size != 1 {
		t.Fatalf("size %d, want 1", entries[1].Size)
	}
}

func TestPathsAreConfined(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"..", "../x", "a/../../x", "a//b", "/abs", `a\b`, ".homecloud", ".HomeCloud/trash", "a/./b", "CON", "nul.txt", "x.", "x ", "a:b"} {
		if _, err := f.svc.List("Uploads", p); !errors.Is(err, ErrInvalid) {
			t.Errorf("List(%q): want ErrInvalid, got %v", p, err)
		}
	}
	if _, err := f.svc.List("Nope", ""); !errors.Is(err, ErrNoLocation) {
		t.Fatalf("unknown location: %v", err)
	}
}

func TestSymlinkOutsideIsRefused(t *testing.T) {
	f := newFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.dir, "link")); err != nil {
		t.Skipf("cannot create symbolic links here: %v", err)
	}
	if slices.Contains(f.names(t, ""), "link/") {
		t.Fatal("a symbolic link is listed")
	}
	if _, _, err := f.svc.Open("Uploads", "link/secret"); err == nil {
		t.Fatal("opened a file through a link out of the location")
	}
}

func TestUnavailableLocation(t *testing.T) {
	f := newFixture(t)
	f.svc.locations[0].Path = filepath.Join(f.dir, "missing")
	if _, err := f.svc.List("Uploads", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	if err := f.svc.CreateFolder("Uploads", "", "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	if _, err := os.Stat(f.svc.locations[0].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a missing location was created")
	}
}

func TestCreateFolderAndRenameNeverOverwrite(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "a")
	f.write(t, "b.txt", "b")
	if err := f.svc.CreateFolder("Uploads", "", "Docs"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.CreateFolder("Uploads", "", "Docs"); !errors.Is(err, ErrExists) {
		t.Fatalf("second folder: want ErrExists, got %v", err)
	}
	if err := f.svc.CreateFolder("Uploads", "missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("folder in a missing folder: want ErrNotFound, got %v", err)
	}
	if err := f.svc.Rename("Uploads", "a.txt", "b.txt"); !errors.Is(err, ErrExists) {
		t.Fatalf("rename onto b.txt: want ErrExists, got %v", err)
	}
	if f.read(t, "b.txt") != "b" {
		t.Fatal("b.txt was overwritten")
	}
	if err := f.svc.Rename("Uploads", "a.txt", "c.txt"); err != nil {
		t.Fatal(err)
	}
	if f.read(t, "c.txt") != "a" {
		t.Fatal("rename lost the content")
	}
	if err := f.svc.Rename("Uploads", "c.txt", "bad/name"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rename to a path: want ErrInvalid, got %v", err)
	}
}

func TestRenameChangingOnlyCase(t *testing.T) {
	f := newFixture(t)
	f.write(t, "photo.jpg", "p")
	if err := f.svc.Rename("Uploads", "photo.jpg", "Photo.jpg"); err != nil {
		t.Fatal(err)
	}
	if got := f.names(t, ""); !slices.Equal(got, []string{"Photo.jpg"}) {
		t.Fatalf("got %v", got)
	}
}

func TestMove(t *testing.T) {
	f := newFixture(t)
	f.write(t, "a.txt", "a")
	f.write(t, "b.txt", "b")
	f.write(t, "Docs/b.txt", "other b")
	f.write(t, "Sub/x", "x")
	if _, err := f.svc.Move("Uploads", []string{"Sub"}, "Sub"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("move into itself: want ErrInvalid, got %v", err)
	}
	n, err := f.svc.Move("Uploads", []string{"a.txt", "b.txt", "Sub"}, "Docs")
	if !errors.Is(err, ErrExists) || n != 1 {
		t.Fatalf("want 1 moved then ErrExists, got %d, %v", n, err)
	}
	if f.read(t, "Docs/b.txt") != "other b" || f.read(t, "Docs/a.txt") != "a" {
		t.Fatal("wrong contents after move")
	}
	if n, err := f.svc.Move("Uploads", []string{"Sub"}, "Docs"); err != nil || n != 1 {
		t.Fatalf("move folder: %d, %v", n, err)
	}
	if f.read(t, "Docs/Sub/x") != "x" {
		t.Fatal("folder not moved")
	}
}

func TestTrashRestoreAndPurge(t *testing.T) {
	f := newFixture(t)
	f.write(t, "Docs/a.txt", "a")
	f.write(t, "Docs/Old/x", "x")
	if n, err := f.svc.Trash("Uploads", []string{"Docs/a.txt", "Docs/Old"}); err != nil || n != 2 {
		t.Fatalf("trash: %d, %v", n, err)
	}
	if got := f.names(t, "Docs"); len(got) != 0 {
		t.Fatalf("still listed: %v", got)
	}
	items, err := f.svc.ListTrash("Uploads")
	if err != nil || len(items) != 2 {
		t.Fatalf("trash list: %v, %v", items, err)
	}
	var file TrashItem
	for _, it := range items {
		if it.Path == "Docs/a.txt" {
			file = it
		}
	}
	if file.Size != 1 || file.Dir || !file.Deleted.Equal(f.now) {
		t.Fatalf("trash item %+v", file)
	}

	// The name came back while it was in the trash, and its folder went.
	f.write(t, "Docs/a.txt", "new a")
	restored, err := f.svc.Restore("Uploads", []string{file.ID})
	if err != nil || !slices.Equal(restored, []string{"Docs/a (2).txt"}) {
		t.Fatalf("restore: %v, %v", restored, err)
	}
	if f.read(t, "Docs/a (2).txt") != "a" || f.read(t, "Docs/a.txt") != "new a" {
		t.Fatal("restore overwrote or lost a file")
	}
	if _, err := f.svc.Restore("Uploads", []string{file.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restore twice: want ErrNotFound, got %v", err)
	}
	if _, err := f.svc.Restore("Uploads", []string{"../../x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restore a path: want ErrNotFound, got %v", err)
	}

	f.now = f.now.Add(31 * day)
	f.svc.Purge(t.Context())
	if items, _ := f.svc.ListTrash("Uploads"); len(items) != 0 {
		t.Fatalf("purge left %v", items)
	}
	if left, _ := os.ReadDir(filepath.Join(f.dir, ".homecloud", "trash")); len(left) != 0 {
		t.Fatalf("purge left %d slots", len(left))
	}
}

func TestTrashRestoreRecreatesFolder(t *testing.T) {
	f := newFixture(t)
	f.write(t, "Docs/a.txt", "a")
	if _, err := f.svc.Trash("Uploads", []string{"Docs/a.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.dir, "Docs")); err != nil {
		t.Fatal(err)
	}
	items, _ := f.svc.ListTrash("Uploads")
	if _, err := f.svc.Restore("Uploads", []string{items[0].ID}); err != nil {
		t.Fatal(err)
	}
	if f.read(t, "Docs/a.txt") != "a" {
		t.Fatal("not restored")
	}
}

func TestUploads(t *testing.T) {
	f := newFixture(t)
	f.write(t, "Phone/IMG_1.HEIC", "existing")
	dir, err := f.svc.UploadDir("Uploads")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.CheckUpload("Uploads", "Phone", "IMG_1.HEIC"); err != nil {
		t.Fatal(err)
	}
	if err = f.svc.CheckUpload("Uploads", "Missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing folder: want ErrNotFound, got %v", err)
	}
	if err = f.svc.CheckUpload("Uploads", "Phone", "a/b"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad name: want ErrInvalid, got %v", err)
	}
	id := strings.Repeat("ab", 16)
	if err = os.WriteFile(filepath.Join(dir, id), []byte("uploaded"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, id+".info"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved, err := f.svc.FinishUpload("Uploads", id, "Phone", "IMG_1.HEIC")
	if err != nil || saved != "Phone/IMG_1 (2).HEIC" {
		t.Fatalf("finish: %q, %v", saved, err)
	}
	if f.read(t, saved) != "uploaded" || f.read(t, "Phone/IMG_1.HEIC") != "existing" {
		t.Fatal("upload overwrote or lost a file")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("upload left %d files behind", len(left))
	}
	if _, err := f.svc.FinishUpload("Uploads", "../../x", "Phone", "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad id: want ErrInvalid, got %v", err)
	}
}

func TestPurgeExpiredUploads(t *testing.T) {
	f := newFixture(t)
	dir, err := f.svc.UploadDir("Uploads")
	if err != nil {
		t.Fatal(err)
	}
	old, fresh := filepath.Join(dir, "old"), filepath.Join(dir, "fresh")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(old, time.Time{}, f.now.Add(-8*day)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, time.Time{}, f.now.Add(-6*day)); err != nil {
		t.Fatal(err)
	}
	f.svc.Purge(t.Context())
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired upload kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh upload removed")
	}
}

func TestOpen(t *testing.T) {
	f := newFixture(t)
	f.write(t, "Docs/a.txt", "hello")
	file, info, err := f.svc.Open("Uploads", "Docs/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close() //nolint:errcheck // test
	b, _ := io.ReadAll(file)
	if string(b) != "hello" || info.Size() != 5 {
		t.Fatalf("read %q", b)
	}
	if _, _, err := f.svc.Open("Uploads", "Docs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("open a folder: want ErrNotFound, got %v", err)
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"IMG_0001.HEIC", "Τραγούδια", "a b.txt", ".bashrc", "COM10", "console.log", "con1"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "a?b", "a\x00b", "a\tb", "x.", "x ", "CON", "con.txt", "LPT1.log", "COM9", strings.Repeat("a", 256)} {
		if err := ValidName(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestFreeNameForDotFiles(t *testing.T) {
	f := newFixture(t)
	f.write(t, ".bashrc", "x")
	root, err := os.OpenRoot(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close() //nolint:errcheck // test
	got, err := freeName(root, "", ".bashrc")
	if err != nil || got != ".bashrc (2)" {
		t.Fatalf("got %q, %v", got, err)
	}
}
