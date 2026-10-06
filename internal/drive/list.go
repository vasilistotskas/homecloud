// Package drive reads iCloud Drive through iCloud.com's web service (drivews).
// It only reads: nothing here creates, changes or deletes anything in iCloud.
package drive

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/vasilistotskas/homecloud/internal/icloud"
)

// RootID is the iCloud Drive root folder.
const RootID = "FOLDER::com.apple.CloudDocs::root"

// ItemType is what a drive item is.
type ItemType string

// Item types iCloud Drive reports (rclone's iclouddrive backend).
const (
	TypeFolder ItemType = "FOLDER"
	TypeFile   ItemType = "FILE"
	// TypeAppLibrary is an app's folder, such as Pages or Numbers.
	TypeAppLibrary ItemType = "APP_LIBRARY"
	// TypeAppContainer is an app's container folder.
	TypeAppContainer ItemType = "APP_CONTAINER"
)

// IsFolder reports whether items of this type hold other items.
func (t ItemType) IsFolder() bool {
	return t == TypeFolder || t == TypeAppLibrary || t == TypeAppContainer
}

func (t ItemType) valid() bool { return t == TypeFile || t.IsFolder() }

// Item is one iCloud Drive item as drivews describes it.
type Item struct {
	// DriveWSID identifies the item in drivews ("TYPE::zone::id").
	DriveWSID string `json:"drivewsid"`
	// DocWSID identifies the item in docws, which serves file contents.
	DocWSID   string   `json:"docwsid"`
	Zone      string   `json:"zone"`
	ParentID  string   `json:"parentId"`
	Name      string   `json:"name"`
	Extension string   `json:"extension"`
	Type      ItemType `json:"type"`
	Etag      string   `json:"etag"`
	// Size is in bytes, for files.
	Size         int64     `json:"size"`
	DateModified time.Time `json:"dateModified"`
	// Items are a folder's children, when the folder was listed.
	Items []Item `json:"items"`
	// NumberOfItems counts a folder's children.
	NumberOfItems int64 `json:"numberOfItems"`
}

// FullName is the name with its extension, as iCloud Drive shows it.
func (i Item) FullName() string {
	if i.Extension == "" {
		return i.Name
	}
	return i.Name + "." + i.Extension
}

// Folders returns the given folders with their children. An item type
// homecloud does not know is an error: it would otherwise be skipped silently.
func Folders(ctx context.Context, c *icloud.Client, ids ...string) ([]Item, error) {
	svc, err := c.Service(icloud.ServiceDrive)
	if err != nil {
		return nil, err
	}
	req := make([]map[string]any, len(ids))
	for i, id := range ids {
		req[i] = map[string]any{"drivewsid": id, "partialData": false, "includeHierarchy": false}
	}
	var folders []Item
	if err := c.Call(ctx, http.MethodPost, svc.URL+"/retrieveItemDetailsInFolders", req, &folders); err != nil {
		return nil, fmt.Errorf("list iCloud Drive folders: %w", err)
	}
	if len(folders) != len(ids) {
		return nil, fmt.Errorf("list iCloud Drive folders: asked for %d, got %d", len(ids), len(folders))
	}
	for _, f := range folders {
		for _, it := range append([]Item{f}, f.Items...) {
			if !it.Type.valid() {
				return nil, fmt.Errorf("iCloud Drive item %s has unknown type %q", it.DriveWSID, it.Type)
			}
		}
	}
	return folders, nil
}
