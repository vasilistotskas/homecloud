package drive

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Apple's signature format
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/icloud"
	"github.com/vasilistotskas/homecloud/internal/ledger"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// fakeDrive serves a mutable iCloud Drive: folders by drivewsid, file bytes by
// document id.
type fakeDrive struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	children map[string][]Item // folder drivewsid -> items
	content  map[string][]byte // document id -> bytes
	packages map[string]bool
	badSig   map[string]bool
	failList bool
	signOut  bool
}

func appleSignature(data []byte) string {
	h := sha1.New() //nolint:gosec // Apple's signature format
	h.Write([]byte(signatureSalt))
	h.Write(data)
	return base64.StdEncoding.EncodeToString(append([]byte{signatureType}, h.Sum(nil)...))
}

func newFakeDrive(t *testing.T) *fakeDrive {
	t.Helper()
	f := &fakeDrive{t: t, children: map[string][]Item{}, content: map[string][]byte{}, packages: map[string]bool{}, badSig: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /drive/retrieveAppLibraries", func(w http.ResponseWriter, _ *http.Request) {
		f.write(w, map[string]any{"items": []Item{{DriveWSID: "FOLDER::com.apple.Pages::documents", Type: TypeAppLibrary, Name: "Pages"}}})
	})
	mux.HandleFunc("POST /drive/retrieveItemDetailsInFolders", func(w http.ResponseWriter, r *http.Request) {
		var req []struct {
			ID string `json:"drivewsid"`
		}
		data, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(data, &req); err != nil {
			t.Error(err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.signOut {
			w.WriteHeader(http.StatusMisdirectedRequest)
			return
		}
		out := make([]Item, len(req))
		for i, q := range req {
			if f.failList && q.ID != RootID {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			out[i] = Item{DriveWSID: q.ID, Type: TypeFolder, Items: f.children[q.ID]}
		}
		f.write(w, out)
	})
	mux.HandleFunc("GET /docs/ws/{zone}/download/by_id", func(w http.ResponseWriter, r *http.Request) {
		doc := r.URL.Query().Get("document_id")
		f.mu.Lock()
		data, ok := f.content[doc]
		pkg, bad := f.packages[doc], f.badSig[doc]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sig := appleSignature(data)
		if bad {
			sig = appleSignature([]byte("other bytes"))
		}
		tok := map[string]any{"url": f.srv.URL + "/content/" + doc, "signature": sig}
		if pkg {
			f.write(w, map[string]any{"package_token": tok})
			return
		}
		f.write(w, map[string]any{"data_token": tok})
	})
	mux.HandleFunc("GET /content/{doc}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		data := f.content[r.PathValue("doc")]
		f.mu.Unlock()
		_, _ = w.Write(data)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDrive) write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.MarshalWrite(w, v); err != nil {
		f.t.Error(err)
	}
}

// put places a file (bytes) in folder; etag changes with every put.
func (f *fakeDrive) put(folder, doc, name, ext string, data []byte, etag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	items := f.children[folder]
	id := "FILE::com.apple.CloudDocs::" + doc
	for i := range items {
		if items[i].DriveWSID == id {
			items = append(items[:i], items[i+1:]...)
			break
		}
	}
	f.children[folder] = append(items, Item{
		DriveWSID: id, Type: TypeFile, Name: name, Extension: ext, Size: int64(len(data)), Etag: etag,
		DateModified: t0,
	})
	f.content[doc] = data
}

func (f *fakeDrive) remove(folder, doc string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	items := f.children[folder]
	for i := range items {
		if strings.HasSuffix(items[i].DriveWSID, "::"+doc) {
			f.children[folder] = append(items[:i], items[i+1:]...)
			return
		}
	}
}

func (f *fakeDrive) folder(parent, id, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.children[parent] = append(f.children[parent], Item{DriveWSID: id, Type: TypeFolder, Name: name})
}

type harness struct {
	t     *testing.T
	drive *fakeDrive
	db    *ledger.DB
	a     *Archive
	clock time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	f := newFakeDrive(t)
	db, err := ledger.Open(context.Background(), filepath.Join(t.TempDir(), "homecloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c, err := icloud.New(icloud.AppleEndpoints, icloud.Session{Services: map[string]icloud.Service{
		icloud.ServiceDrive: {URL: f.srv.URL + "/drive"},
		icloud.ServiceDocs:  {URL: f.srv.URL + "/docs"},
	}}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, drive: f, db: db, clock: t0}
	h.a = &Archive{
		Root: filepath.Join(t.TempDir(), "Drive"), DB: db, Client: c, Workers: 3,
		Now: func() time.Time { return h.clock }, Log: slog.New(slog.DiscardHandler),
	}
	return h
}

func (h *harness) sync() Report {
	h.t.Helper()
	h.clock = h.clock.Add(time.Hour)
	rep, err := h.a.Sync(context.Background())
	if err != nil {
		h.t.Fatalf("sync: %v", err)
	}
	return rep
}

func (h *harness) read(local string) string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.a.Root, filepath.FromSlash(local)))
	if err != nil {
		h.t.Fatalf("read %s: %v", local, err)
	}
	return string(data)
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestArchiveLifecycle(t *testing.T) {
	h := newHarness(t)
	d := h.drive
	d.folder(RootID, "FOLDER::com.apple.CloudDocs::F1", "Docs")
	d.put("FOLDER::com.apple.CloudDocs::F1", "A", "tax", "pdf", []byte("tax v1"), "e1")
	d.put(RootID, "B", "notes", "txt", []byte("notes"), "e1")
	d.put(RootID, "E", "empty", "", nil, "e1")
	d.packages["P"] = true
	d.put("FOLDER::com.apple.Pages::documents", "P", "Essay", "pages", zipOf(t, map[string]string{"Index/Document.iwa": "x"}), "e1")

	rep := h.sync()
	if rep.Listed != 4 || rep.Archived != 4 || rep.Failed != 0 {
		t.Fatalf("first sync = %+v", rep)
	}
	if h.read("iCloud Drive/Docs/tax.pdf") != "tax v1" || h.read("iCloud Drive/empty") != "" ||
		!strings.HasPrefix(h.read("App Libraries/Pages (com.apple.Pages)/Essay.pages"), "PK") {
		t.Fatal("files are not where they belong")
	}

	// Nothing changed: nothing downloaded.
	if idle := h.sync(); idle.Archived+idle.Changed+idle.Moved+idle.Deleted != 0 || idle.Bytes != 0 {
		t.Fatalf("idle sync = %+v", idle)
	}

	// Changed in iCloud: new bytes, the old copy kept under .versions.
	d.put("FOLDER::com.apple.CloudDocs::F1", "A", "tax", "pdf", []byte("tax v2"), "e2")
	// A new etag with the same bytes keeps no version.
	d.put(RootID, "B", "notes", "txt", []byte("notes"), "e2")
	rep = h.sync()
	if rep.Changed != 1 || rep.Failed != 0 {
		t.Fatalf("change sync = %+v", rep)
	}
	versions, _ := filepath.Glob(filepath.Join(h.a.Root, VersionsDir, "*", "iCloud Drive", "*"))
	if h.read("iCloud Drive/Docs/tax.pdf") != "tax v2" || len(versions) != 1 {
		t.Fatalf("after a change: versions %v", versions)
	}

	// Renamed in iCloud: the copy moves, nothing is downloaded, the empty
	// folder it left is removed.
	d.remove("FOLDER::com.apple.CloudDocs::F1", "A")
	d.put(RootID, "A", "tax 2026", "pdf", []byte("tax v2"), "e2")
	rep = h.sync()
	if rep.Moved != 1 || rep.Bytes != 0 || h.read("iCloud Drive/tax 2026.pdf") != "tax v2" {
		t.Fatalf("rename sync = %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(h.a.Root, "iCloud Drive", "Docs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the emptied folder was kept")
	}

	// Deleted in iCloud: recorded, the copy stays. Back again: restored.
	d.remove(RootID, "B")
	if del := h.sync(); del.Deleted != 1 || h.read("iCloud Drive/notes.txt") != "notes" {
		t.Fatalf("delete sync = %+v", del)
	}
	d.put(RootID, "B", "notes", "txt", []byte("notes"), "e2")
	if back := h.sync(); back.Restored != 1 {
		t.Fatalf("restore sync = %+v", back)
	}

	// A damaged copy is downloaded again; the damaged one is kept aside.
	if err := os.WriteFile(filepath.Join(h.a.Root, "iCloud Drive", "notes.txt"), []byte("rot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.db.MarkDriveFileDamaged(context.Background(), "FILE::com.apple.CloudDocs::B", "test", h.clock); err != nil {
		t.Fatal(err)
	}
	if fix := h.sync(); fix.Repaired != 1 || h.read("iCloud Drive/notes.txt") != "notes" {
		t.Fatalf("repair sync = %+v", fix)
	}

	// Nothing under the archive was ever deleted: every version is still there.
	files, err := h.db.DriveFiles(context.Background())
	if err != nil || len(files) != 4 {
		t.Fatalf("ledger files = %d, %v", len(files), err)
	}
}

func TestArchiveRejectsBadDownloads(t *testing.T) {
	h := newHarness(t)
	h.drive.put(RootID, "X", "forged", "bin", []byte("data"), "e1")
	h.drive.badSig["X"] = true
	h.drive.packages["Z"] = true
	h.drive.put(RootID, "Z", "broken", "key", []byte("not a zip"), "e1")
	rep := h.sync()
	if rep.Failed != 2 || rep.Archived != 0 {
		t.Fatalf("sync = %+v", rep)
	}
	for _, name := range []string{"forged.bin", "broken.key"} {
		if _, err := os.Stat(filepath.Join(h.a.Root, "iCloud Drive", name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s was placed despite failing verification", name)
		}
	}
	if left, _ := os.ReadDir(filepath.Join(h.a.Root, partialDir)); len(left) != 0 {
		t.Fatalf("partial downloads left behind: %v", left)
	}
}

func TestArchiveNeverTombstonesOnAFailedListing(t *testing.T) {
	h := newHarness(t)
	h.drive.folder(RootID, "FOLDER::com.apple.CloudDocs::F1", "Docs")
	h.drive.put("FOLDER::com.apple.CloudDocs::F1", "A", "a", "txt", []byte("a"), "e1")
	h.sync()
	h.drive.failList = true
	if _, err := h.a.Sync(context.Background()); err == nil {
		t.Fatal("a failed listing did not fail the sync")
	}
	files, err := h.db.DriveFiles(context.Background())
	if err != nil || files["FILE::com.apple.CloudDocs::A"].DeletedAt != nil {
		t.Fatalf("a file was marked deleted after a failed listing: %+v, %v", files, err)
	}
}

func TestArchiveStopsWhenSignInIsNeeded(t *testing.T) {
	h := newHarness(t)
	h.drive.signOut = true
	if _, err := h.a.Sync(context.Background()); !errors.Is(err, icloud.ErrSignInRequired) {
		t.Fatalf("sync without a session: %v", err)
	}
}

func TestArchiveNamesAreSafeOnWindows(t *testing.T) {
	h := newHarness(t)
	h.drive.put(RootID, "C1", "Report", "pdf", []byte("1"), "e1")
	h.drive.put(RootID, "C2", "REPORT", "pdf", []byte("2"), "e1")
	h.drive.put(RootID, "C3", "a:b?", "txt", []byte("3"), "e1")
	h.drive.put(RootID, "C4", "con", "txt", []byte("4"), "e1")
	if rep := h.sync(); rep.Archived != 4 || rep.Failed != 0 {
		t.Fatalf("sync = %+v", rep)
	}
	files, err := h.db.DriveFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		key := strings.ToLower(f.LocalPath)
		if seen[key] {
			t.Fatalf("two files share %s", f.LocalPath)
		}
		seen[key] = true
		if strings.ContainsAny(strings.TrimPrefix(f.LocalPath, "iCloud Drive/"), `:?*"<>|\`) {
			t.Fatalf("unsafe name %q", f.LocalPath)
		}
	}
	if !seen["icloud drive/a_b_.txt"] || !seen["icloud drive/_con.txt"] {
		t.Fatalf("local paths = %v", seen)
	}
}
