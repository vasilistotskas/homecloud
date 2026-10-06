package drive

import (
	"context"
	"fmt"
	"net/http"

	"github.com/vasilistotskas/homecloud/internal/icloud"
)

// listBatch is how many folders one listing request asks for.
const listBatch = 10

// Top-level folders of the archive's Drive tree.
const (
	// TreeDrive holds the iCloud Drive folder as the Files app shows it.
	TreeDrive = "iCloud Drive"
	// TreeApps holds each app's own iCloud folder (Pages, Shortcuts, ...).
	TreeApps = "App Libraries"
)

// File is one iCloud Drive file and its place in the archive's tree.
type File struct {
	Item
	// Path is the file's place as iCloud names it: the tree folder, then each
	// folder name, then the file's full name.
	Path []string
}

// appLibraries lists every app's iCloud folder.
func appLibraries(ctx context.Context, c *icloud.Client) ([]Item, error) {
	svc, err := c.Service(icloud.ServiceDrive)
	if err != nil {
		return nil, err
	}
	var r struct {
		Items []Item `json:"items"`
	}
	if err := c.Call(ctx, http.MethodGet, svc.URL+"/retrieveAppLibraries", nil, &r); err != nil {
		return nil, fmt.Errorf("list iCloud app libraries: %w", err)
	}
	for _, it := range r.Items {
		if it.Type != TypeAppLibrary {
			return nil, fmt.Errorf("app library %s has type %q", it.DriveWSID, it.Type)
		}
	}
	return r.Items, nil
}

// appFolderName names an app's folder in the archive: its display name and
// its zone (the middle part of its drivewsid), because several apps share a
// display name ("Books", "Settings").
func appFolderName(lib Item) (string, error) {
	zone, _, err := zoneAndDocument(lib.DriveWSID)
	if err != nil {
		return "", err
	}
	return lib.Name + " (" + zone + ")", nil
}

// Walk lists every file in iCloud Drive and in the apps' iCloud folders. It
// fails as a whole when any folder cannot be listed, so a partial listing can
// never be mistaken for deleted files.
func Walk(ctx context.Context, c *icloud.Client) ([]File, error) {
	libs, err := appLibraries(ctx, c)
	if err != nil {
		return nil, err
	}
	type pending struct {
		id   string
		path []string
	}
	queue := []pending{{RootID, []string{TreeDrive}}}
	for _, lib := range libs {
		name, err := appFolderName(lib)
		if err != nil {
			return nil, err
		}
		queue = append(queue, pending{lib.DriveWSID, []string{TreeApps, name}})
	}
	seen := map[string]bool{}
	var files []File
	for len(queue) > 0 {
		batch := queue[:min(listBatch, len(queue))]
		queue = queue[len(batch):]
		ids := make([]string, len(batch))
		for i, p := range batch {
			ids[i] = p.id
		}
		folders, err := Folders(ctx, c, ids...)
		if err != nil {
			return nil, err
		}
		for i, folder := range folders {
			if folder.DriveWSID != batch[i].id {
				return nil, fmt.Errorf("listing returned %s for %s", folder.DriveWSID, batch[i].id)
			}
			for _, it := range folder.Items {
				if seen[it.DriveWSID] {
					continue
				}
				seen[it.DriveWSID] = true
				path := append(append([]string{}, batch[i].path...), it.FullName())
				switch {
				case it.Type == TypeAppLibrary:
					// App folders inside iCloud Drive are listed under TreeApps.
					continue
				case it.Type.IsFolder():
					queue = append(queue, pending{it.DriveWSID, path})
				default:
					files = append(files, File{Item: it, Path: path})
				}
			}
		}
	}
	return files, nil
}
