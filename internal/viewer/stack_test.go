package viewer

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/ledger"
)

const testKey = Key("abcDEF0123456789abcDEF0123456789abcDEF01234")

// fakeImmich answers the calls the agent makes, as Immich v3.3.5 does.
type fakeImmich struct {
	t           *testing.T
	mu          sync.Mutex
	paths       []string          // asset id i+1 is paths[i]
	stackOf     map[string]string // asset id -> stack id
	primary     map[string]string // stack id -> top asset id
	nextStack   int
	permissions []string
	creates     int
	libraries   map[string][]string // library id -> import paths
	scans       []string            // library ids, in the order scanned
}

func newFakeImmich(t *testing.T, paths ...string) *fakeImmich {
	return &fakeImmich{t: t, paths: paths, stackOf: map[string]string{}, primary: map[string]string{}}
}

func (f *fakeImmich) id(path string) string {
	return strconv.Itoa(slices.Index(f.paths, path) + 1)
}

// stack stacks ids with the first on top, as POST /stacks does: stacks whose
// top asset is among ids are folded into the new one.
func (f *fakeImmich) stack(ids ...string) {
	members := slices.Clone(ids)
	for s, top := range f.primary {
		if slices.Contains(ids, top) {
			for a, as := range f.stackOf {
				if as == s && !slices.Contains(members, a) {
					members = append(members, a)
				}
			}
			delete(f.primary, s)
		}
	}
	f.nextStack++
	s := "s" + strconv.Itoa(f.nextStack)
	for _, a := range members {
		f.stackOf[a] = s
	}
	f.primary[s] = ids[0]
}

func (f *fakeImmich) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("x-api-key") != string(testKey) {
		http.Error(w, `{"message":"Invalid API key"}`, http.StatusUnauthorized)
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /api/api-keys/me":
		f.write(w, map[string]any{"permissions": f.permissions})
	case "POST /api/search/metadata":
		var req map[string]any
		if err := json.UnmarshalRead(r.Body, &req); err != nil {
			f.t.Errorf("search body: %v", err)
		}
		// The new request shape: orderBy and a cursor, never page.
		if req["orderBy"] == nil || req["page"] != nil || req["withStacked"] != true {
			f.t.Errorf("search request %v, want orderBy and withStacked, without page", req)
		}
		start := 0
		if c, ok := req["cursor"].(string); ok {
			start, _ = strconv.Atoi(c)
		}
		size := int(req["size"].(float64))
		// Pages of two, so paging is exercised whatever size is asked.
		end := min(start+min(size, 2), len(f.paths))
		items := []map[string]string{}
		for i := start; i < end; i++ {
			items = append(items, map[string]string{"id": strconv.Itoa(i + 1), "originalPath": f.paths[i]})
		}
		var next any
		if end < len(f.paths) {
			next = strconv.Itoa(end)
		}
		f.write(w, map[string]any{"assets": map[string]any{"items": items, "nextCursor": next}})
	case "GET /api/libraries":
		out := []map[string]any{}
		for id, paths := range f.libraries {
			out = append(out, map[string]any{"id": id, "name": id, "importPaths": paths})
		}
		f.write(w, out)
	case "GET /api/stacks":
		out := []map[string]any{}
		for s, top := range f.primary {
			assets := []map[string]string{}
			for a, as := range f.stackOf {
				if as == s {
					assets = append(assets, map[string]string{"id": a})
				}
			}
			out = append(out, map[string]any{"id": s, "primaryAssetId": top, "assets": assets})
		}
		f.write(w, out)
	case "POST /api/stacks":
		var req struct {
			AssetIDs []string `json:"assetIds"`
		}
		if err := json.UnmarshalRead(r.Body, &req); err != nil || len(req.AssetIDs) < 2 {
			http.Error(w, `{"message":"assetIds must have at least 2 items"}`, http.StatusBadRequest)
			return
		}
		f.creates++
		f.stack(req.AssetIDs...)
		w.WriteHeader(http.StatusCreated)
		f.write(w, map[string]any{"id": "s" + strconv.Itoa(f.nextStack)})
	default:
		if id, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/api/libraries/"), "/scan"); ok && r.Method == http.MethodPost && f.libraries[id] != nil {
			f.scans = append(f.scans, id)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}
}

func (f *fakeImmich) write(w http.ResponseWriter, v any) {
	if err := json.MarshalWrite(w, v); err != nil {
		f.t.Errorf("write: %v", err)
	}
}

func testClient(t *testing.T, f *fakeImmich, key Key) *Client {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return New(config.Viewer{URL: srv.URL, PhotosPath: "/mnt/archive/Photos", Timeout: config.Duration(10 * time.Second)}, key)
}

func TestStackEdits(t *testing.T) {
	photos := filepath.Join(t.TempDir(), "Photos")
	local := func(rel string) string { return filepath.Join(photos, filepath.FromSlash(rel)) }
	pairs := []ledger.EditPair{
		{Edit: local("2024/01/A_edited.HEIC"), Original: local("2024/01/A.HEIC")}, // not stacked yet
		{Edit: local("2024/01/B_edited.HEIC"), Original: local("2024/01/B.HEIC")}, // stacked, edit on top
		{Edit: local("2024/01/C_edited.HEIC"), Original: local("2024/01/C.HEIC")}, // stacked, original on top
		{Edit: local("2024/01/D_edited.HEIC"), Original: local("2024/01/D.HEIC")}, // the edit not scanned yet
	}
	f := newFakeImmich(t,
		"/mnt/archive/Photos/2024/01/A.HEIC", "/mnt/archive/Photos/2024/01/A_edited.HEIC",
		"/mnt/archive/Photos/2024/01/B.HEIC", "/mnt/archive/Photos/2024/01/B_edited.HEIC",
		"/mnt/archive/Photos/2024/01/C.HEIC", "/mnt/archive/Photos/2024/01/C_edited.HEIC",
		"/mnt/archive/Photos/2024/01/D.HEIC",
	)
	f.stack(f.id("/mnt/archive/Photos/2024/01/B_edited.HEIC"), f.id("/mnt/archive/Photos/2024/01/B.HEIC"))
	f.stack(f.id("/mnt/archive/Photos/2024/01/C.HEIC"), f.id("/mnt/archive/Photos/2024/01/C_edited.HEIC"))
	c := testClient(t, f, testKey)
	ctx := context.Background()

	res, err := StackEdits(ctx, c, pairs, photos, "/mnt/archive/Photos")
	if err != nil {
		t.Fatal(err)
	}
	if want := (Result{Pairs: 4, Stacked: 1, Created: 2, NotInViewer: 1}); res != want {
		t.Fatalf("first pass = %+v, want %+v", res, want)
	}
	for _, name := range []string{"A", "C"} {
		edit, original := f.id("/mnt/archive/Photos/2024/01/"+name+"_edited.HEIC"), f.id("/mnt/archive/Photos/2024/01/"+name+".HEIC")
		if s := f.stackOf[edit]; s == "" || s != f.stackOf[original] || f.primary[s] != edit {
			t.Errorf("%s: edit and original not stacked with the edit on top", name)
		}
	}

	// The edit is scanned later; everything else is left as it is.
	f.paths = append(f.paths, "/mnt/archive/Photos/2024/01/D_edited.HEIC")
	res, err = StackEdits(ctx, c, pairs, photos, "/mnt/archive/Photos")
	if err != nil {
		t.Fatal(err)
	}
	if want := (Result{Pairs: 4, Stacked: 3, Created: 1}); res != want {
		t.Fatalf("second pass = %+v, want %+v", res, want)
	}
	if f.creates != 3 {
		t.Fatalf("stacks created = %d, want 3", f.creates)
	}
}

func TestStackEditsRejectsAFileOutsidePhotos(t *testing.T) {
	photos := filepath.Join(t.TempDir(), "Photos")
	c := testClient(t, newFakeImmich(t), testKey)
	pairs := []ledger.EditPair{{Edit: filepath.Join(filepath.Dir(photos), "Drive", "x.HEIC"), Original: filepath.Join(photos, "x.HEIC")}}
	if _, err := StackEdits(context.Background(), c, pairs, photos, "/mnt/archive/Photos"); err == nil || !strings.Contains(err.Error(), "is not in") {
		t.Fatalf("err = %v, want the file named as outside Photos", err)
	}
}

func TestStackEditsReportsImmichErrors(t *testing.T) {
	c := testClient(t, newFakeImmich(t), "wrongkey")
	_, err := StackEdits(context.Background(), c, nil, t.TempDir(), "/mnt/archive/Photos")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "Invalid API key") {
		t.Fatalf("err = %v, want Immich's status and message", err)
	}
}

func TestScanPhotos(t *testing.T) {
	f := newFakeImmich(t)
	f.libraries = map[string][]string{"photos": {"/mnt/archive/Photos"}, "other": {"/mnt/elsewhere"}}
	c := testClient(t, f, testKey)
	if err := c.ScanPhotos(context.Background(), "/mnt/archive/Photos"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.scans, []string{"photos"}) {
		t.Fatalf("scanned %v, want only the library over Photos", f.scans)
	}
	if err := c.ScanPhotos(context.Background(), "/mnt/archive/Elsewhere"); err == nil || !strings.Contains(err.Error(), "no Immich library imports") {
		t.Fatalf("no library over the path: err = %v", err)
	}
}

func TestMissingPermissions(t *testing.T) {
	tests := []struct {
		granted, missing []string
	}{
		{[]string{"asset.read", "stack.read", "stack.create", "library.read", "library.update"}, nil},
		{[]string{"all"}, nil},
		{[]string{"asset.read", "library.read"}, []string{"stack.read", "stack.create", "library.update"}},
	}
	for _, tt := range tests {
		f := newFakeImmich(t)
		f.permissions = tt.granted
		missing, err := testClient(t, f, testKey).MissingPermissions(context.Background())
		if err != nil || !slices.Equal(missing, tt.missing) {
			t.Errorf("granted %v: missing = %v, %v; want %v", tt.granted, missing, err, tt.missing)
		}
	}
}
