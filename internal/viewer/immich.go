// Package viewer keeps the optional viewer, Immich reading the archive's
// Photos/ (README, "The viewer"), in step with the archive: Immich shows an
// edit and its original as two items, and only the ledger knows which edit
// belongs to which original, so the agent stacks them, the edit on top; and
// Immich scans the folder only nightly, so the agent queues a scan when files
// land. It uses Immich's API (v3.3.5) with a key granting Permissions.
package viewer

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/vasilistotskas/homecloud/internal/config"
)

// Permissions are what the agent's API key must grant; Immich's "all" grants
// every one. The library ones queue a scan when files land, and Immich allows
// them to an administrator's key only.
var Permissions = []string{"asset.read", "stack.read", "stack.create", "library.read", "library.update"}

const permissionAll = "all"

// searchPage is the most assets one search answers (Immich's maximum).
const searchPage = 1000

// Client talks to one Immich server.
type Client struct {
	origin string
	key    Key
	http   *http.Client
}

// New returns a client for cfg, authenticated with key.
func New(cfg config.Viewer, key Key) *Client {
	return &Client{origin: strings.TrimSuffix(cfg.URL, "/"), key: key, http: &http.Client{Timeout: cfg.Timeout.Std()}}
}

// call sends body (when not nil) as JSON and decodes a 2xx answer into out
// (when not nil); any other answer is an error with Immich's message.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+"/api"+path, reader)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set("x-api-key", string(c.key))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("immich %s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response fully handled below
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512)) //nolint:errcheck // best-effort error detail
		return fmt.Errorf("immich %s %s: HTTP %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(detail))
	}
	if out == nil {
		return nil
	}
	if err := json.UnmarshalRead(resp.Body, out); err != nil {
		return fmt.Errorf("decode immich %s %s: %w", method, path, err)
	}
	return nil
}

// MissingPermissions returns which of Permissions the client's key lacks.
func (c *Client) MissingPermissions(ctx context.Context) ([]string, error) {
	var key struct {
		Permissions []string `json:"permissions"`
	}
	if err := c.call(ctx, http.MethodGet, "/api-keys/me", nil, &key); err != nil {
		return nil, err
	}
	if slices.Contains(key.Permissions, permissionAll) {
		return nil, nil
	}
	var missing []string
	for _, p := range Permissions {
		if !slices.Contains(key.Permissions, p) {
			missing = append(missing, p)
		}
	}
	return missing, nil
}

// PhotosLibraries returns the external libraries that import photosPath, the
// archive's Photos/ as Immich sees it; none is an error, since nothing of the
// archive is then in the viewer.
func (c *Client) PhotosLibraries(ctx context.Context, photosPath string) ([]string, error) {
	var libs []struct {
		ID          string   `json:"id"`
		ImportPaths []string `json:"importPaths"`
	}
	if err := c.call(ctx, http.MethodGet, "/libraries", nil, &libs); err != nil {
		return nil, err
	}
	var ids []string
	for _, l := range libs {
		if slices.Contains(l.ImportPaths, photosPath) {
			ids = append(ids, l.ID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no Immich library imports %s: add an external library with that import path", photosPath)
	}
	return ids, nil
}

// ScanPhotos queues a scan of every library that imports photosPath, so files
// that just landed in the archive show in the viewer without waiting for
// Immich's nightly scan.
func (c *Client) ScanPhotos(ctx context.Context, photosPath string) error {
	ids, err := c.PhotosLibraries(ctx, photosPath)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := c.call(ctx, http.MethodPost, "/libraries/"+url.PathEscape(id)+"/scan", nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// searchRequest is a metadata search in the shape Immich v3.2 introduced:
// orderBy makes it page by cursor.
type searchRequest struct {
	OrderBy     searchOrder `json:"orderBy"`
	Size        int         `json:"size"`
	WithStacked bool        `json:"withStacked"`
	Cursor      string      `json:"cursor,omitempty"`
}

type searchOrder struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

type searchResponse struct {
	Assets struct {
		Items []struct {
			ID           string `json:"id"`
			OriginalPath string `json:"originalPath"`
		} `json:"items"`
		NextCursor *string `json:"nextCursor"`
	} `json:"assets"`
}

// assetIDs returns every asset the key's owner sees (outside Immich's locked
// folder), stacked or not, by the path of its file as Immich sees it.
func (c *Client) assetIDs(ctx context.Context) (map[string]string, error) {
	ids := map[string]string{}
	req := searchRequest{OrderBy: searchOrder{Field: "fileCreatedAt", Direction: "desc"}, Size: searchPage, WithStacked: true}
	for {
		var res searchResponse
		if err := c.call(ctx, http.MethodPost, "/search/metadata", req, &res); err != nil {
			return nil, err
		}
		for _, a := range res.Assets.Items {
			ids[a.OriginalPath] = a.ID
		}
		if res.Assets.NextCursor == nil {
			return ids, nil
		}
		req.Cursor = *res.Assets.NextCursor
	}
}

// stack is an Immich stack; its primary asset is the one the timeline shows.
type stack struct {
	ID             string `json:"id"`
	PrimaryAssetID string `json:"primaryAssetId"`
	Assets         []struct {
		ID string `json:"id"`
	} `json:"assets"`
}

func (c *Client) stacks(ctx context.Context) ([]stack, error) {
	var out []stack
	if err := c.call(ctx, http.MethodGet, "/stacks", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// createStack stacks ids with the first on top. Immich folds in any stack
// whose top asset is among ids.
func (c *Client) createStack(ctx context.Context, ids []string) error {
	return c.call(ctx, http.MethodPost, "/stacks", struct {
		AssetIDs []string `json:"assetIds"`
	}{ids}, nil)
}
