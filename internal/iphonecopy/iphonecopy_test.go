package iphonecopy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/iphonebackup"
	"github.com/vasilistotskas/homecloud/internal/iphonebackup/iphonebackuptest"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

const udid = "00008110-001A2B3C4D5E801E"

var t1 = time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)

// files are a backup's hashed files; the plists come from WriteBackup.
func files(changed string) map[string]string {
	return map[string]string{
		"Manifest.db": "encrypted manifest " + changed,
		"0a/0a1b2c3d": "photo library",
		"ff/ffeeddcc": "messages " + changed,
		"12/12345678": "notes",
	}
}

// backup writes a finished backup in a fresh folder and inspects it as the
// agent does.
func backup(t *testing.T, dir string, completed time.Time, changed string) iphonebackup.Backup {
	t.Helper()
	folder := iphonebackuptest.WriteBackup(t, dir, iphonebackuptest.Backup{
		UDID: udid, DeviceName: "Test iPhone", Completed: completed, Encrypted: true, Files: files(changed),
	})
	b := iphonebackup.Inspect(t.Context(), folder, udid)
	if b.Err != nil {
		t.Fatal(b.Err)
	}
	return b
}

func openArchive(t *testing.T) *Archive {
	t.Helper()
	a, err := Open(filepath.Join(t.TempDir(), "iPhone backups"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	return a
}

func read(t *testing.T, a *Archive, rel string) string {
	t.Helper()
	b, err := os.ReadFile(a.Path(rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fileID(t *testing.T, path string) platform.FileIdentity {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // a test file
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only
	id, err := platform.FileID(f)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestManifest(t *testing.T) {
	m := Manifest{"Info.plist": sha256.Sum256([]byte("a")), "0a/0a1b": sha256.Sum256([]byte("b"))}
	got, err := ParseManifest(bytes.NewReader(m.Bytes()))
	if err != nil || len(got) != 2 || got["0a/0a1b"] != m["0a/0a1b"] {
		t.Fatalf("round trip = %v, %v", got, err)
	}
	if !strings.HasPrefix(string(m.Bytes()), m["0a/0a1b"].String()+" *0a/0a1b\n") {
		t.Fatalf("not sorted sha256sum lines:\n%s", m.Bytes())
	}
	// sha256sum's text-mode marker is accepted too.
	if _, err := ParseManifest(strings.NewReader(m["0a/0a1b"].String() + "  0a/0a1b\n")); err != nil {
		t.Fatal(err)
	}
	hash := m["0a/0a1b"].String()
	for name, text := range map[string]string{
		"empty":       "",
		"bad hash":    "abc *x\n",
		"no marker":   hash + " x\n",
		"parent":      hash + " *../x\n",
		"inner dots":  hash + " *a/../../x\n",
		"absolute":    hash + " */etc/passwd\n",
		"backslash":   hash + " *a\\b\n",
		"drive":       hash + " *C:x\n",
		"duplicate":   hash + " *x\n" + hash + " *x\n",
		"too deep":    hash + " *a/b/c/d/e\n",
		"empty part":  hash + " *a//b\n",
		"hidden dots": hash + " *..\n",
	} {
		if _, err := ParseManifest(strings.NewReader(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCopyLocalLinksWhatDidNotChange(t *testing.T) {
	ctx := t.Context()
	a := openArchive(t)
	b1 := backup(t, t.TempDir(), t1, "v1")
	s1, st, err := a.CopyLocal(ctx, b1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 7 || st.Copied != 7 || st.Linked != 0 || s1.Stamp != "20261007T030000Z" {
		t.Fatalf("first copy = %+v %+v", s1, st)
	}
	if got := read(t, a, s1.Dir()+"/ff/ffeeddcc"); got != "messages v1" {
		t.Fatalf("copied file = %q", got)
	}

	b2 := backup(t, t.TempDir(), t1.Add(24*time.Hour), "v2")
	s2, st, err := a.CopyLocal(ctx, b2, &s1)
	if err != nil {
		t.Fatal(err)
	}
	// Manifest.db, the messages file and Status.plist changed.
	if st.Copied != 3 || st.Linked != 4 || st.Files != 7 {
		t.Fatalf("second copy = %+v", st)
	}
	if fileID(t, a.Path(s1.Dir()+"/0a/0a1b2c3d")) != fileID(t, a.Path(s2.Dir()+"/0a/0a1b2c3d")) {
		t.Fatal("an unchanged file must be a hard link to the previous copy")
	}
	snaps, err := a.Snapshots(udid)
	if err != nil || len(snaps) != 2 || snaps[0] != s2 {
		t.Fatalf("snapshots = %+v, %v", snaps, err)
	}
	has, err := a.Has(udid, t1)
	if err != nil || !has {
		t.Fatalf("Has = %v, %v", has, err)
	}

	removed, err := a.Prune(udid, 1)
	if err != nil || len(removed) != 1 || removed[0] != s1 {
		t.Fatalf("pruned = %+v, %v", removed, err)
	}
	if got := read(t, a, s2.Dir()+"/0a/0a1b2c3d"); got != "photo library" {
		t.Fatalf("a file shared with the removed copy = %q", got)
	}
	if _, err := os.Stat(a.Path(s1.Dir())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pruned copy still there: %v", err)
	}
}

func TestCopyLocalRefusesAChangedBackup(t *testing.T) {
	a := openArchive(t)
	b := backup(t, t.TempDir(), t1, "v1")
	// A new backup finished after the folder was inspected.
	iphonebackuptest.WriteBackup(t, filepath.Dir(b.Dir), iphonebackuptest.Backup{
		UDID: udid, DeviceName: "Test iPhone", Completed: t1.Add(time.Hour), Encrypted: true, Files: files("v1"),
	})
	if _, _, err := a.CopyLocal(t.Context(), b, nil); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	if snaps, _ := a.Snapshots(udid); len(snaps) != 0 {
		t.Fatalf("snapshots = %+v", snaps)
	}
	if err := a.Recover(udid); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(a.Path(udid))
	if err != nil || len(entries) != 0 {
		t.Fatalf("left after recovery: %v %v", entries, err)
	}
}

func TestRecover(t *testing.T) {
	a := openArchive(t)
	s, _, err := a.CopyLocal(t.Context(), backup(t, t.TempDir(), t1, "v1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Interrupted between renaming the folder and the manifest.
	if err = os.Rename(a.Path(s.manifest()), a.Path(udid+"/"+partialPrefix+s.Stamp+manifestExt)); err != nil {
		t.Fatal(err)
	}
	// An abandoned partial copy and a folder without a manifest.
	for _, dir := range []string{udid + "/" + partialPrefix + "20261008T000000Z/0a", udid + "/20261009T000000Z/0a"} {
		if err = os.MkdirAll(a.Path(dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err = a.Recover(udid); err != nil {
		t.Fatal(err)
	}
	snaps, err := a.Snapshots(udid)
	if err != nil || len(snaps) != 1 || snaps[0] != s {
		t.Fatalf("snapshots = %+v, %v", snaps, err)
	}
	entries, err := os.ReadDir(a.Path(udid))
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries after recovery = %v, %v", entries, err)
	}
}

func TestVerify(t *testing.T) {
	ctx := t.Context()
	a := openArchive(t)
	s1, _, err := a.CopyLocal(ctx, backup(t, t.TempDir(), t1, "v1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s2, _, err := a.CopyLocal(ctx, backup(t, t.TempDir(), t1.Add(time.Hour), "v2"), &s1)
	if err != nil {
		t.Fatal(err)
	}
	snaps := []Snapshot{s2, s1}
	damaged, err := a.Verify(ctx, snaps)
	if err != nil || len(damaged[s1.Stamp]) != 0 || len(damaged[s2.Stamp]) != 0 {
		t.Fatalf("healthy copies = %v, %v", damaged, err)
	}
	// A shared file damaged on disk is damaged in both copies; a file only
	// the older copy has is damaged only there.
	if err = os.WriteFile(a.Path(s2.Dir()+"/12/12345678"), []byte("NOTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(a.Path(s1.Dir()+"/ff/ffeeddcc"), []byte("messages v0"), 0o600); err != nil {
		t.Fatal(err)
	}
	damaged, err = a.Verify(ctx, snaps)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(damaged[s2.Stamp], ",") != "12/12345678" || strings.Join(damaged[s1.Stamp], ",") != "12/12345678,ff/ffeeddcc" {
		t.Fatalf("damaged = %v", damaged)
	}
}

// newest returns the archive's newest snapshot of the test device.
func newest(a *Archive) func(string) (*Snapshot, error) {
	return func(udid string) (*Snapshot, error) {
		snaps, err := a.Snapshots(udid)
		if err != nil || len(snaps) == 0 {
			return nil, err
		}
		return &snaps[0], nil
	}
}

func TestPushAndIngest(t *testing.T) {
	ctx := t.Context()
	a := openArchive(t)
	incoming := t.TempDir()

	b1 := backup(t, t.TempDir(), t1, "v1")
	res, err := Push(ctx, b1, incoming)
	if err != nil || res.Outcome != PushSent || res.Sent != 7 {
		t.Fatalf("first push = %+v, %v", res, err)
	}
	if res, err = Push(ctx, b1, incoming); err != nil || res.Outcome != PushPending {
		t.Fatalf("push before ingest = %+v, %v", res, err)
	}
	got, err := a.Ingest(ctx, incoming, newest(a))
	if err != nil || len(got) != 1 || got[0].Err != nil || got[0].Stats.Copied != 7 {
		t.Fatalf("ingest = %+v, %v", got, err)
	}
	if res, err = Push(ctx, b1, incoming); err != nil || res.Outcome != PushArchived {
		t.Fatalf("push after ingest = %+v, %v", res, err)
	}

	// The next backup sends only what changed; the archive links the rest.
	b2 := backup(t, t.TempDir(), t1.Add(24*time.Hour), "v2")
	if res, err = Push(ctx, b2, incoming); err != nil || res.Sent != 3 {
		t.Fatalf("second push = %+v, %v", res, err)
	}
	got, err = a.Ingest(ctx, incoming, newest(a))
	if err != nil || len(got) != 1 || got[0].Err != nil || got[0].Stats.Copied != 3 || got[0].Stats.Linked != 4 {
		t.Fatalf("second ingest = %+v, %v", got, err)
	}
	if got := read(t, a, got[0].Snapshot.Dir()+"/ff/ffeeddcc"); got != "messages v2" {
		t.Fatalf("ingested file = %q", got)
	}
	entries, err := os.ReadDir(filepath.Join(incoming, udid))
	if err != nil || len(entries) != 1 || entries[0].Name() != archivedPrefix+Stamp(b2.CompletedAt)+manifestExt {
		t.Fatalf("incoming after ingest = %v, %v", entries, err)
	}
}

func TestIngestRefusesADamagedPush(t *testing.T) {
	ctx := t.Context()
	a := openArchive(t)
	incoming := t.TempDir()
	b := backup(t, t.TempDir(), t1, "v1")
	if _, err := Push(ctx, b, incoming); err != nil {
		t.Fatal(err)
	}
	pushed := filepath.Join(incoming, udid, Stamp(t1), "ff", "ffeeddcc")
	if err := os.WriteFile(pushed, []byte("flipped bits"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := a.Ingest(ctx, incoming, newest(a))
	if err != nil || len(got) != 1 || !errors.Is(got[0].Err, ErrMismatch) {
		t.Fatalf("ingest = %+v, %v", got, err)
	}
	if snaps, _ := a.Snapshots(udid); len(snaps) != 0 {
		t.Fatalf("a damaged push was archived: %+v", snaps)
	}
	// The refused push is gone, so the next push sends it again.
	if res, err := Push(ctx, b, incoming); err != nil || res.Outcome != PushSent || res.Sent != 7 {
		t.Fatalf("push again = %+v, %v", res, err)
	}
}

func TestIngestRefusesAHostileManifest(t *testing.T) {
	a := openArchive(t)
	incoming := t.TempDir()
	dir := filepath.Join(incoming, udid)
	if err := os.MkdirAll(filepath.Join(dir, Stamp(t1)), 0o700); err != nil {
		t.Fatal(err)
	}
	hash := Digest(sha256.Sum256([]byte("x"))).String()
	if err := os.WriteFile(filepath.Join(dir, Stamp(t1)+manifestExt), []byte(hash+" *../../outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := a.Ingest(t.Context(), incoming, newest(a))
	if err != nil || len(got) != 1 || got[0].Err == nil {
		t.Fatalf("ingest = %+v, %v", got, err)
	}
}

func TestPushResumes(t *testing.T) {
	ctx := t.Context()
	incoming := t.TempDir()
	b := backup(t, t.TempDir(), t1, "v1")
	// An interrupted push left one complete and one truncated file.
	partial := filepath.Join(incoming, udid, pushPrefix+Stamp(t1))
	for name, content := range map[string]string{"0a/0a1b2c3d": "photo library", "ff/ffeeddcc": "mess"} {
		p := filepath.Join(partial, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Push(ctx, b, incoming)
	if err != nil || res.Outcome != PushSent || res.Sent != 6 {
		t.Fatalf("resumed push = %+v, %v", res, err)
	}
	m, err := os.ReadFile(filepath.Join(incoming, udid, Stamp(t1), "ff", "ffeeddcc")) //nolint:gosec // a test file
	if err != nil || string(m) != "messages v1" {
		t.Fatalf("resent file = %q, %v", m, err)
	}
}

func TestHashTreeRefusesUnexpectedEntries(t *testing.T) {
	b := backup(t, t.TempDir(), t1, "v1")
	if err := os.WriteFile(filepath.Join(b.Dir, "notes about this backup.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(b.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close() //nolint:errcheck // read-only
	if _, err := HashTree(context.Background(), root); err == nil {
		t.Fatal("a file Apple's tools do not write was accepted")
	}
}

// TestWriteFileNeverWritesThroughALink: a hard link planted at the name of a
// file about to be written (incoming is shared with another computer) must
// not let the write reach the file it links to.
func TestWriteFileNeverWritesThroughALink(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if err := root.WriteFile("copy", []byte("archived"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := root.Link("copy", "receipt.tmp"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(root, "receipt.tmp", []byte("receipt")); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"copy": "archived", "receipt.tmp": "receipt"} {
		if got, err := root.ReadFile(name); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
}
