package web_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	agentv3 "github.com/vasilistotskas/homecloud/gen/homecloud/agent/v3"
)

// tus sends one tus 1.0 request with the agent token unless hdr sets
// Authorization (to "" for none).
func tus(t *testing.T, e *env, method, target string, body string, hdr map[string]string) tusResult {
	t.Helper()
	if !strings.HasPrefix(target, "http") {
		target = e.url + target
	}
	req, err := http.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Authorization", "Bearer "+e.token)
	if method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/offset+octet-stream")
	}
	for k, v := range hdr {
		switch {
		case k == "Host":
			req.Host = v
		case v == "":
			req.Header.Del(k)
		default:
			req.Header.Set(k, v)
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return tusResult{StatusCode: res.StatusCode, Header: res.Header}
}

type tusResult struct {
	StatusCode int
	Header     http.Header
}

func tusMeta(dir, name string) string {
	enc := base64.StdEncoding.EncodeToString
	return "filename " + enc([]byte(name)) + ",dir " + enc([]byte(dir))
}

func uploadPath(t *testing.T, e *env) string {
	t.Helper()
	res, err := e.client.ListFileLocations(context.Background(), &agentv3.ListFileLocationsRequest{})
	if err != nil || len(res.GetLocations()) != 1 || !res.GetLocations()[0].GetAvailable() {
		t.Fatalf("ListFileLocations = %v, %v", res, err)
	}
	return res.GetLocations()[0].GetUploadPath()
}

// An upload is created, interrupted, resumed from the offset the agent
// reports, saved beside an existing file of the same name without replacing
// it, and downloaded through a signed link.
func TestFilesUploadResumeAndDownload(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(e.files, "Phone"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.files, "Phone", "IMG_1.HEIC"), []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	const data = "0123456789"
	create := tus(t, e, http.MethodPost, uploadPath(t, e), "", map[string]string{
		"Upload-Length": strconv.Itoa(len(data)), "Upload-Metadata": tusMeta("Phone", "IMG_1.HEIC"),
	})
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", create.StatusCode)
	}
	loc := create.Header.Get("Location")
	if !strings.HasPrefix(loc, e.url+"/files/uploads/Uploads/") {
		t.Fatalf("Location = %q", loc)
	}
	if res := tus(t, e, http.MethodPatch, loc, data[:4], map[string]string{"Upload-Offset": "0"}); res.StatusCode != http.StatusNoContent {
		t.Fatalf("first part = %d", res.StatusCode)
	}
	head := tus(t, e, http.MethodHead, loc, "", nil)
	if head.Header.Get("Upload-Offset") != "4" {
		t.Fatalf("resume offset = %q", head.Header.Get("Upload-Offset"))
	}
	last := tus(t, e, http.MethodPatch, loc, data[4:], map[string]string{"Upload-Offset": "4"})
	if last.StatusCode != http.StatusNoContent {
		t.Fatalf("last part = %d", last.StatusCode)
	}
	saved, err := url.PathUnescape(last.Header.Get("Homecloud-Saved-As"))
	if err != nil || saved != "Phone/IMG_1 (2).HEIC" {
		t.Fatalf("saved as %q, %v", saved, err)
	}
	if got, _ := os.ReadFile(filepath.Join(e.files, "Phone", "IMG_1 (2).HEIC")); string(got) != data {
		t.Fatalf("saved %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(e.files, "Phone", "IMG_1.HEIC")); string(got) != "older" {
		t.Fatal("the existing file was replaced")
	}

	link, err := e.client.GetFileLink(ctx, &agentv3.GetFileLinkRequest{Location: "Uploads", Path: saved})
	if err != nil {
		t.Fatal(err)
	}
	status, header, body := get(t, e, link.GetUrl(), "")
	if status != http.StatusOK || body != data || !strings.HasPrefix(header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download = %d %q %v", status, body, header)
	}
	if !strings.Contains(header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("download CSP = %q", header.Get("Content-Security-Policy"))
	}
	if status, _, _ := get(t, e, strings.Split(link.GetUrl(), "?")[0], ""); status != http.StatusForbidden {
		t.Fatalf("unsigned download = %d", status)
	}
}

func TestFilesUploadRefusals(t *testing.T) {
	e := setup(t)
	up := uploadPath(t, e)
	cases := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"no credentials", map[string]string{"Authorization": "", "Upload-Length": "1", "Upload-Metadata": tusMeta("", "a")}, http.StatusUnauthorized},
		{"missing folder", map[string]string{"Upload-Length": "1", "Upload-Metadata": tusMeta("Missing", "a")}, http.StatusNotFound},
		{"bad name", map[string]string{"Upload-Length": "1", "Upload-Metadata": tusMeta("", "a/b")}, http.StatusBadRequest},
		{"into homecloud's folder", map[string]string{"Upload-Length": "1", "Upload-Metadata": tusMeta(".homecloud", "a")}, http.StatusBadRequest},
		{"unknown length", map[string]string{"Upload-Defer-Length": "1", "Upload-Metadata": tusMeta("", "a")}, http.StatusBadRequest},
		{"larger than the free space", map[string]string{"Upload-Length": strconv.Itoa(1<<30 + 1), "Upload-Metadata": tusMeta("", "a")}, http.StatusInsufficientStorage},
	}
	for _, c := range cases {
		if res := tus(t, e, http.MethodPost, up, "", c.hdr); res.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", c.name, res.StatusCode, c.want)
		}
	}
	if res := tus(t, e, http.MethodPost, "/files/uploads/Nope/", "", map[string]string{"Upload-Length": "1"}); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown location: %d", res.StatusCode)
	}
}

// Behind Tailscale Serve the upload URL is the https public origin, whatever
// forwarding headers a client sends.
func TestFilesUploadURLBehindProxy(t *testing.T) {
	const public = "https://nas.example.ts.net"
	e := setupWith(t, public)
	meta := map[string]string{"Upload-Length": "1", "Upload-Metadata": tusMeta("", "a")}
	meta["Host"] = "nas.example.ts.net"
	meta["Origin"] = public
	if loc := tus(t, e, http.MethodPost, uploadPath(t, e), "", meta).Header.Get("Location"); !strings.HasPrefix(loc, public+"/files/uploads/Uploads/") {
		t.Fatalf("public Location = %q", loc)
	}
	local := map[string]string{"Upload-Length": "1", "Upload-Metadata": tusMeta("", "b"), "X-Forwarded-Proto": "https", "X-Forwarded-Host": "evil.example"}
	if loc := tus(t, e, http.MethodPost, uploadPath(t, e), "", local).Header.Get("Location"); !strings.HasPrefix(loc, e.url+"/") {
		t.Fatalf("local Location = %q", loc)
	}
}

func TestFilesUploadPreflightFromDesktop(t *testing.T) {
	e := setup(t)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodOptions, e.url+uploadPath(t, e)+"abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://tauri.localhost")
	req.Header.Set("Access-Control-Request-Method", http.MethodPatch)
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type,tus-resumable,upload-offset")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight = %d", res.StatusCode)
	}
	// The upload's own responses let the page read the tus headers.
	create := tus(t, e, http.MethodPost, uploadPath(t, e), "", map[string]string{
		"Origin": "http://tauri.localhost", "Upload-Length": "1", "Upload-Metadata": tusMeta("", "a"),
	})
	if got := create.Header.Get("Access-Control-Expose-Headers"); create.StatusCode != http.StatusCreated ||
		!strings.Contains(got, "Location") || !strings.Contains(got, "Homecloud-Saved-As") || !strings.Contains(got, "Upload-Offset") {
		t.Fatalf("create from the desktop app = %d, expose-headers %q", create.StatusCode, got)
	}
}

func TestFilesManage(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := e.client
	if err := os.WriteFile(filepath.Join(e.files, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateFolder(ctx, &agentv3.CreateFolderRequest{Location: "Uploads", Name: "Docs"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateFolder(ctx, &agentv3.CreateFolderRequest{Location: "Uploads", Name: "Docs"}); code(err) != connect.CodeAlreadyExists {
		t.Fatalf("second folder = %v", err)
	}
	if _, err := c.RenameFile(ctx, &agentv3.RenameFileRequest{Location: "Uploads", Path: "a.txt", NewName: "b?.txt"}); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad rename = %v", err)
	}
	if _, err := c.RenameFile(ctx, &agentv3.RenameFileRequest{Location: "Uploads", Path: "a.txt", NewName: "b.txt"}); err != nil {
		t.Fatal(err)
	}
	if res, err := c.MoveFiles(ctx, &agentv3.MoveFilesRequest{Location: "Uploads", Paths: []string{"b.txt"}, Destination: "Docs"}); err != nil || res.GetMoved() != 1 {
		t.Fatalf("move = %v, %v", res, err)
	}
	list, err := c.ListFiles(ctx, &agentv3.ListFilesRequest{Location: "Uploads", Path: "Docs"})
	if err != nil || len(list.GetEntries()) != 1 || list.GetEntries()[0].GetName() != "b.txt" || list.GetEntries()[0].GetSizeBytes() != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	if trashed, trashErr := c.TrashFiles(ctx, &agentv3.TrashFilesRequest{Location: "Uploads", Paths: []string{"Docs/b.txt"}}); trashErr != nil || trashed.GetTrashed() != 1 {
		t.Fatalf("trash = %v, %v", trashed, trashErr)
	}
	trash, err := c.ListTrash(ctx, &agentv3.ListTrashRequest{Location: "Uploads"})
	if err != nil || len(trash.GetItems()) != 1 || trash.GetItems()[0].GetPath() != "Docs/b.txt" {
		t.Fatalf("trash list = %v, %v", trash, err)
	}
	restored, err := c.RestoreFiles(ctx, &agentv3.RestoreFilesRequest{Location: "Uploads", Ids: []string{trash.GetItems()[0].GetId()}})
	if err != nil || len(restored.GetPaths()) != 1 || restored.GetPaths()[0] != "Docs/b.txt" {
		t.Fatalf("restore = %v, %v", restored, err)
	}
	if _, err = c.ListFiles(ctx, &agentv3.ListFilesRequest{Location: "Uploads", Path: "../x"}); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("escape = %v", err)
	}
	if _, err = c.ListFiles(ctx, &agentv3.ListFilesRequest{Location: "Nope"}); code(err) != connect.CodeNotFound {
		t.Fatalf("unknown location = %v", err)
	}
	if err = os.RemoveAll(e.files); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ListFiles(ctx, &agentv3.ListFilesRequest{Location: "Uploads"}); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("missing location = %v", err)
	}
	locs, err := c.ListFileLocations(ctx, &agentv3.ListFileLocationsRequest{})
	if err != nil || locs.GetLocations()[0].GetAvailable() {
		t.Fatalf("missing location listed as available: %v, %v", locs, err)
	}
}
