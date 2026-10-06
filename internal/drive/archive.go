package drive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vasilistotskas/homecloud/internal/icloud"
	"github.com/vasilistotskas/homecloud/internal/ledger"
)

// Dir is the archive's Drive folder, under storage.root.
const Dir = "Drive"

// Folders inside the archive's Drive folder that are not part of the tree.
const (
	// VersionsDir keeps every copy a newer one replaced, under the UTC time of
	// the replacement.
	VersionsDir = ".versions"
	// partialDir holds downloads in progress; it is the only place homecloud
	// deletes from.
	partialDir = ".partial"
)

// versionStamp names a .versions folder; it has no characters Windows forbids.
const versionStamp = "20060102T150405Z"

// maxFailures bounds how many failure messages a report keeps.
const maxFailures = 20

// Archive keeps a local folder as a never-deleting copy of iCloud Drive.
type Archive struct {
	// Root is the archive's Drive folder.
	Root    string
	DB      *ledger.DB
	Client  *icloud.Client
	Now     func() time.Time
	Log     *slog.Logger
	Workers int
}

// Report is what one sync did.
type Report struct {
	// Listed counts the files in iCloud.
	Listed int `json:"listed"`
	// Archived counts files downloaded for the first time.
	Archived int `json:"archived"`
	// Changed counts files downloaded again because their bytes changed in
	// iCloud; the previous copies were kept.
	Changed int `json:"changed"`
	// Repaired counts damaged copies replaced by a fresh download.
	Repaired int `json:"repaired"`
	// Moved counts files moved or renamed in iCloud.
	Moved int `json:"moved"`
	// Deleted counts files newly gone from iCloud (their copies are kept).
	Deleted int `json:"deleted"`
	// Restored counts files back in iCloud after a deletion.
	Restored int `json:"restored"`
	// Bytes is how much was downloaded.
	Bytes int64 `json:"bytes"`
	// Failed counts files that could not be archived this time, and app
	// folders with archived files that could not be listed.
	Failed   int      `json:"failed"`
	Failures []string `json:"failures,omitempty"`
	// UnlistedApps counts app folders that could not be listed and hold
	// nothing archived: apps that keep no documents in iCloud Drive.
	UnlistedApps int `json:"unlisted_apps"`
}

func (r *Report) fail(mu *sync.Mutex, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	r.Failed++
	if len(r.Failures) < maxFailures {
		r.Failures = append(r.Failures, fmt.Sprintf(format, args...))
	}
}

// job is one file that needs downloading.
type job struct {
	file  File
	local string
	prev  *ledger.DriveFile
}

// Sync lists iCloud Drive and brings the archive up to date. An error means
// the sync could not run (listing failed, sign-in needed); single files that
// fail are counted in the report and retried next time.
func (a *Archive) Sync(ctx context.Context) (Report, error) {
	var rep Report
	if err := a.clearPartial(); err != nil {
		return rep, err
	}
	listing, err := Walk(ctx, a.Client)
	if err != nil {
		return rep, err
	}
	files := listing.Files
	rep.Listed = len(files)
	known, err := a.DB.DriveFiles(ctx)
	if err != nil {
		return rep, err
	}
	used := map[string]string{} // path key -> drivewsid
	for id, k := range known {
		used[pathKey(k.LocalPath)] = id
	}
	assign := func(f File) string {
		local := localPath(f.Path)
		if owner, taken := used[pathKey(local)]; taken && owner != f.DriveWSID {
			local = disambiguate(local, f.DriveWSID)
		}
		used[pathKey(local)] = f.DriveWSID
		return local
	}

	var jobs []job
	seen := map[string]bool{}
	var mu sync.Mutex
	for _, f := range files {
		seen[f.DriveWSID] = true
		icloudPath := strings.Join(f.Path, "/")
		k, ok := known[f.DriveWSID]
		if !ok {
			jobs = append(jobs, job{file: f, local: assign(f)})
			continue
		}
		local := k.LocalPath
		if k.ICloudPath != icloudPath {
			if local, err = a.move(k, assign(f)); err != nil {
				rep.fail(&mu, "%s: %v", icloudPath, err)
				continue
			}
			rep.Moved++
		}
		if k.Etag != f.Etag || k.DamagedAt != nil {
			jobs = append(jobs, job{file: f, local: local, prev: &k})
			continue
		}
		if local != k.LocalPath || k.ICloudPath != icloudPath || k.DeletedAt != nil || !sameTime(k.ModifiedAt, f.DateModified) {
			if k.DeletedAt != nil {
				rep.Restored++
			}
			if err := a.DB.UpdateDriveFile(ctx, a.metadata(k, f, local), a.Now()); err != nil {
				return rep, err
			}
		}
	}

	a.download(ctx, jobs, &rep, &mu)
	if err := ctx.Err(); err != nil {
		return rep, fmt.Errorf("drive sync: %w", err)
	}

	// Only a scope listed in full can show a deletion; an unlisted scope that
	// holds archived files is a failure to look into.
	var gone []string
	archivedIn := map[string]bool{}
	for id, k := range known {
		scope := Scope(k.ICloudPath)
		archivedIn[scope] = true
		if !seen[id] && k.DeletedAt == nil && listing.Complete[scope] {
			gone = append(gone, id)
		}
	}
	for scope, why := range listing.Unlisted {
		if archivedIn[scope] {
			rep.fail(&mu, "%s could not be listed: %s", scope, why)
		} else {
			rep.UnlistedApps++
		}
	}
	if err := a.DB.MarkDriveFilesDeleted(ctx, gone, a.Now()); err != nil {
		return rep, err
	}
	rep.Deleted = len(gone)
	return rep, nil
}

// metadata is k with the iCloud metadata of f and its archived copy at local.
func (a *Archive) metadata(k ledger.DriveFile, f File, local string) ledger.DriveFile {
	k.ICloudPath, k.LocalPath, k.Etag = strings.Join(f.Path, "/"), local, f.Etag
	k.ModifiedAt = optionalTime(f.DateModified)
	return k
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func sameTime(a *time.Time, b time.Time) bool {
	if a == nil {
		return b.IsZero()
	}
	return a.Equal(b)
}

// download runs the jobs on a few workers. A sign-in failure stops the rest.
func (a *Archive) download(ctx context.Context, jobs []job, rep *Report, mu *sync.Mutex) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	work := make(chan job)
	var wg sync.WaitGroup
	for range max(1, a.Workers) {
		wg.Go(func() {
			for j := range work {
				if ctx.Err() != nil {
					continue
				}
				err := a.archive(ctx, j, rep, mu)
				switch {
				case errors.Is(err, icloud.ErrSignInRequired):
					cancel(err)
				case err != nil:
					rep.fail(mu, "%s: %v", strings.Join(j.file.Path, "/"), err)
				}
			}
		})
	}
	for _, j := range jobs {
		work <- j
	}
	close(work)
	wg.Wait()
}

// archive downloads, verifies and places one file.
func (a *Archive) archive(ctx context.Context, j job, rep *Report, mu *sync.Mutex) error {
	partial, sum, sig, pkg, n, err := a.fetch(ctx, j.file)
	if err != nil {
		return err
	}
	defer os.Remove(partial) //nolint:errcheck // gone after a successful rename; a temp file otherwise

	if j.prev != nil && j.prev.DamagedAt == nil && j.prev.SHA256 == sum {
		// Only metadata changed: the archived copy already has these bytes.
		return a.DB.UpdateDriveFile(ctx, a.metadata(*j.prev, j.file, j.local), a.Now())
	}
	keptAs, err := a.place(partial, j.local)
	if err != nil {
		return err
	}
	f := ledger.DriveFile{
		DriveWSID: j.file.DriveWSID, ICloudPath: strings.Join(j.file.Path, "/"), LocalPath: j.local,
		Etag: j.file.Etag, Size: n, ModifiedAt: optionalTime(j.file.DateModified), Package: pkg,
		AppleSignature: sig, SHA256: sum,
	}
	if err := a.DB.RecordDriveFile(ctx, f, keptAs, a.Now()); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	rep.Bytes += n
	switch {
	case j.prev == nil:
		rep.Archived++
	case j.prev.DamagedAt != nil:
		rep.Repaired++
	default:
		rep.Changed++
	}
	return nil
}

// fetch downloads a file into the partial folder and verifies it: a plain
// file against its listed size and Apple's signature, a package as an intact
// ZIP. It returns the partial file, its SHA-256, Apple's signature (hex, plain
// files) and its size.
func (a *Archive) fetch(ctx context.Context, f File) (partial, sum, sig string, pkg bool, n int64, err error) {
	_, document, err := zoneAndDocument(f.DriveWSID)
	if err != nil {
		return "", "", "", false, 0, err
	}
	tmp := filepath.Join(a.Root, partialDir, document+".part")
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // inside the archive
	if err != nil {
		return "", "", "", false, 0, fmt.Errorf("create download file: %w", err)
	}
	defer func() {
		// Results are cleared on error, so remove by the local name.
		if err != nil {
			err = errors.Join(err, os.Remove(tmp))
		}
	}()
	contentHash := sha256.New()
	var signature hash.Hash
	var apple string
	if f.Size == 0 {
		// iCloud refuses to serve empty files; there is nothing to fetch.
		n = 0
	} else {
		var tok downloadToken
		tok, pkg, err = download(ctx, a.Client, f.Item)
		if err != nil {
			return "", "", "", false, 0, errors.Join(err, out.Close())
		}
		body, fetchErr := a.Client.Fetch(ctx, tok.URL)
		if fetchErr != nil {
			return "", "", "", false, 0, errors.Join(fetchErr, out.Close())
		}
		signature, apple = signatureHash(), tok.Signature
		n, err = io.Copy(io.MultiWriter(out, contentHash, signature), body)
		err = errors.Join(err, body.Close())
		if err != nil {
			return "", "", "", false, 0, errors.Join(fmt.Errorf("download: %w", err), out.Close())
		}
	}
	if err = errors.Join(out.Sync(), out.Close()); err != nil {
		return "", "", "", false, 0, fmt.Errorf("write download file: %w", err)
	}
	switch {
	case pkg:
		if err = checkPackage(tmp); err != nil {
			return "", "", "", false, 0, err
		}
	case f.Size != n:
		return "", "", "", false, 0, fmt.Errorf("downloaded %d bytes, iCloud lists %d", n, f.Size)
	case f.Size > 0:
		if sig, err = checkSignature(apple, signature); err != nil {
			return "", "", "", false, 0, err
		}
	}
	return tmp, hex.EncodeToString(contentHash.Sum(nil)), sig, pkg, n, nil
}

// place moves a verified download to local. Whatever is there already is
// moved under .versions first; keptAs names it (archive-relative, '/').
func (a *Archive) place(partial, local string) (keptAs string, err error) {
	dest := a.abs(local)
	keptAs, err = a.keep(local)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil { //nolint:gosec // archive folders are the owner's to read
		return "", fmt.Errorf("create folder for %s: %w", local, err)
	}
	if err := os.Rename(partial, dest); err != nil {
		return "", fmt.Errorf("place %s: %w", local, err)
	}
	return keptAs, nil
}

// keep moves the file at local, if any, to .versions/<now>/local.
func (a *Archive) keep(local string) (string, error) {
	src := a.abs(local)
	if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("check %s: %w", local, err)
	}
	kept := path.Join(VersionsDir, a.Now().UTC().Format(versionStamp), local)
	dest := a.abs(kept)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil { //nolint:gosec // archive folders are the owner's to read
		return "", fmt.Errorf("create version folder: %w", err)
	}
	if _, err := os.Lstat(dest); err == nil {
		return "", fmt.Errorf("version %s already exists", kept)
	}
	if err := os.Rename(src, dest); err != nil {
		return "", fmt.Errorf("keep previous %s: %w", local, err)
	}
	return kept, nil
}

// move follows a file moved or renamed in iCloud: its archived copy moves to
// local, and folders the move left empty are removed.
func (a *Archive) move(k ledger.DriveFile, local string) (string, error) {
	if local == k.LocalPath {
		return local, nil
	}
	src, dest := a.abs(k.LocalPath), a.abs(local)
	if _, err := a.keep(local); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil { //nolint:gosec // archive folders are the owner's to read
		return "", fmt.Errorf("create folder for %s: %w", local, err)
	}
	switch err := os.Rename(src, dest); {
	case errors.Is(err, fs.ErrNotExist):
		// The copy is missing; the scrub reports that. Record the new place so
		// the next download lands there.
	case err != nil:
		return "", fmt.Errorf("move %s to %s: %w", k.LocalPath, local, err)
	}
	a.removeEmptyParents(filepath.Dir(src))
	return local, nil
}

// removeEmptyParents removes dir and its parents while they are empty, up to
// the archive's Drive folder. Only empty folders are ever removed.
func (a *Archive) removeEmptyParents(dir string) {
	root := filepath.Clean(a.Root)
	for dir = filepath.Clean(dir); dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil { // fails unless empty
			return
		}
	}
}

// clearPartial removes downloads an interrupted sync left behind.
func (a *Archive) clearPartial() error {
	dir := filepath.Join(a.Root, partialDir)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clear %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return nil
}

func (a *Archive) abs(local string) string {
	return filepath.Join(a.Root, filepath.FromSlash(local))
}
