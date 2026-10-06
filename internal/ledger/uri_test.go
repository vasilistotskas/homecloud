package ledger

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Characters that mean something in a URI stay part of the ledger's path:
// "#" once made SQLite create the database as a file named after the part
// before it.
func TestOpenWhereThePathHasURICharacters(t *testing.T) {
	name := "Home#Cloud 100% done"
	if runtime.GOOS != "windows" {
		name += "?"
	}
	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "homecloud.db")
	db, openErr := Open(context.Background(), file)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the ledger is not where it was asked for: %v", err)
	}
	siblings, err := os.ReadDir(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(siblings) != 1 {
		t.Fatalf("Open created %d entries beside the archive folder, want only the folder", len(siblings))
	}
	if err := checkFile(context.Background(), file); err != nil {
		t.Fatalf("read-only check of the same path: %v", err)
	}
}

func TestFileURI(t *testing.T) {
	cases := map[string]string{
		"/srv/home#cloud/homecloud.db": "file:///srv/home%23cloud/homecloud.db?mode=ro",
		"/srv/a?b/c 100%/x.db":         "file:///srv/a%3Fb/c%20100%25/x.db?mode=ro",
	}
	if runtime.GOOS == "windows" {
		cases = map[string]string{
			`E:\HomeCloud\homecloud.db`: "file:///E:/HomeCloud/homecloud.db?mode=ro",
			`C:\Home#Cloud 1%\x.db`:     "file:///C:/Home%23Cloud%201%25/x.db?mode=ro",
		}
	}
	for in, want := range cases {
		if got := fileURI(in, "mode=ro"); got != want {
			t.Errorf("fileURI(%q) = %q, want %q", in, got, want)
		}
	}
}
