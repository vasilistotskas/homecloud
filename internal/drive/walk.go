package drive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/vasilistotskas/homecloud/internal/icloud"
)

// listBatch is how many folders of one tree one listing request asks for.
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

// Listing is one walk over iCloud Drive. It is split into scopes, each
// listed on its own: the iCloud Drive tree, and every app's folder.
type Listing struct {
	Files []File
	// Complete holds the scopes listed in full. Only within them can a file
	// missing from the listing be taken as deleted in iCloud.
	Complete map[string]bool
	// Unlisted holds the app scopes that could not be listed, with the
	// reason. Apps that keep no documents in iCloud Drive (Messages, Notes,
	// ...) appear in the app list yet cannot be listed.
	Unlisted map[string]string
}

// Scope returns the scope an iCloud path belongs to: TreeDrive, or
// TreeApps/<app folder>.
func Scope(icloudPath string) string {
	parts := strings.SplitN(icloudPath, "/", 3)
	if parts[0] == TreeApps && len(parts) > 1 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
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

// Walk lists every file in iCloud Drive and in the apps' iCloud folders. The
// iCloud Drive tree must list completely or the walk fails; an app folder that
// cannot be listed is reported in Unlisted. A sign-in failure always fails the
// walk.
func Walk(ctx context.Context, c *icloud.Client) (Listing, error) {
	l := Listing{Complete: map[string]bool{}, Unlisted: map[string]string{}}
	seen := map[string]bool{}
	files, err := walkScope(ctx, c, RootID, []string{TreeDrive}, seen)
	if err != nil {
		return Listing{}, err
	}
	l.Files, l.Complete[TreeDrive] = files, true

	libs, err := appLibraries(ctx, c)
	if err != nil {
		return Listing{}, err
	}
	for _, lib := range libs {
		name, err := appFolderName(lib)
		if err != nil {
			return Listing{}, err
		}
		scope := TreeApps + "/" + name
		files, err := walkScope(ctx, c, lib.DriveWSID, []string{TreeApps, name}, seen)
		l.Files = append(l.Files, files...)
		switch {
		case errors.Is(err, icloud.ErrSignInRequired), ctx.Err() != nil:
			return Listing{}, errors.Join(err, ctx.Err())
		case err != nil:
			l.Unlisted[scope] = err.Error()
		default:
			l.Complete[scope] = true
		}
	}
	return l, nil
}

// walkScope lists the tree under one top folder. On an error it returns the
// files found so far with the error: they exist, but the scope is incomplete.
func walkScope(ctx context.Context, c *icloud.Client, top string, base []string, seen map[string]bool) ([]File, error) {
	type pending struct {
		id   string
		path []string
	}
	queue := []pending{{top, base}}
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
			return files, err
		}
		for i, folder := range folders {
			if folder.DriveWSID != batch[i].id {
				return files, fmt.Errorf("listing returned %s for %s", folder.DriveWSID, batch[i].id)
			}
			for _, it := range folder.Items {
				if seen[it.DriveWSID] {
					continue
				}
				seen[it.DriveWSID] = true
				path := append(append([]string{}, batch[i].path...), it.FullName())
				switch {
				case it.Type == TypeAppLibrary:
					// App folders inside iCloud Drive are their own scopes.
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
