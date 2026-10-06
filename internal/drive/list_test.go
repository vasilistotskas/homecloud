package drive

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/icloud"
)

func driveClient(t *testing.T, handler http.HandlerFunc) *icloud.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := icloud.New(icloud.AppleEndpoints, icloud.Session{
		Services: map[string]icloud.Service{icloud.ServiceDrive: {URL: srv.URL}},
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFoldersListsChildren(t *testing.T) {
	c := driveClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/retrieveItemDetailsInFolders" {
			t.Errorf("path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), RootID) {
			t.Errorf("request %s does not name the root", body)
		}
		_, _ = io.WriteString(w, `[{"drivewsid":"`+RootID+`","type":"FOLDER","name":"root","items":[
			{"drivewsid":"FILE::com.apple.CloudDocs::A","docwsid":"A","type":"FILE","name":"tax","extension":"pdf","size":1024,"dateModified":"2026-09-01T10:00:00Z","etag":"e1"},
			{"drivewsid":"FOLDER::com.apple.Pages::documents","type":"APP_LIBRARY","name":"Pages"}]}]`)
	})
	folders, err := Folders(context.Background(), c, RootID)
	if err != nil {
		t.Fatal(err)
	}
	items := folders[0].Items
	if len(items) != 2 || items[0].FullName() != "tax.pdf" || items[0].Size != 1024 || items[0].DateModified.Month() != time.September ||
		!items[1].Type.IsFolder() {
		t.Fatalf("items = %+v", items)
	}
}

func TestFoldersRejectsUnknownTypes(t *testing.T) {
	c := driveClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"drivewsid":"`+RootID+`","type":"FOLDER","items":[{"drivewsid":"X","type":"SYMLINK"}]}]`)
	})
	if _, err := Folders(context.Background(), c, RootID); err == nil || !strings.Contains(err.Error(), "SYMLINK") {
		t.Fatalf("unknown type: %v", err)
	}
}

func TestFoldersWithoutASessionNeedsSignIn(t *testing.T) {
	c, err := icloud.New(icloud.AppleEndpoints, icloud.Session{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Folders(context.Background(), c, RootID); !errors.Is(err, icloud.ErrSignInRequired) {
		t.Fatalf("Folders before sign-in: %v", err)
	}
}
