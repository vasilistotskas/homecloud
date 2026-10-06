package drive

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/vasilistotskas/homecloud/internal/icloud"
)

// downloadToken is docws's answer for one file: a signed URL to its bytes.
type downloadToken struct {
	URL       string `json:"url"`
	Signature string `json:"signature"`
}

type downloadResponse struct {
	DocumentID string `json:"document_id"`
	// DataToken is set for plain files, PackageToken for packages (iWork
	// documents and other bundles), which download as a ZIP.
	DataToken    *downloadToken `json:"data_token"`
	PackageToken *downloadToken `json:"package_token"`
}

// zoneAndDocument splits a drivewsid ("TYPE::zone::document") into its zone
// and document id.
func zoneAndDocument(drivewsid string) (zone, document string, err error) {
	parts := strings.Split(drivewsid, "::")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return "", "", fmt.Errorf("malformed drivewsid %q", drivewsid)
	}
	return parts[1], parts[2], nil
}

// download asks docws for a file's download token. package reports whether
// the file is a package that downloads as a ZIP.
func download(ctx context.Context, c *icloud.Client, it Item) (tok downloadToken, pkg bool, err error) {
	svc, err := c.Service(icloud.ServiceDocs)
	if err != nil {
		return downloadToken{}, false, err
	}
	zone, document, err := zoneAndDocument(it.DriveWSID)
	if err != nil {
		return downloadToken{}, false, err
	}
	u := svc.URL + "/ws/" + url.PathEscape(zone) + "/download/by_id?document_id=" + url.QueryEscape(document)
	var r downloadResponse
	if err := c.Call(ctx, http.MethodGet, u, nil, &r); err != nil {
		return downloadToken{}, false, fmt.Errorf("download token for %s: %w", it.DriveWSID, err)
	}
	switch {
	case r.DataToken != nil && r.DataToken.URL != "":
		return *r.DataToken, false, nil
	case r.PackageToken != nil && r.PackageToken.URL != "":
		return *r.PackageToken, true, nil
	default:
		return downloadToken{}, false, fmt.Errorf("download token for %s: no URL", it.DriveWSID)
	}
}
