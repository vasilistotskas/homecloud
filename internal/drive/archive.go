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
	"github.com/vasilistotskas/homecloud/internal/parallel"
	"github.com/vasilistotskas/homecloud/internal/platform"
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

// maxFailures bounds how many failure messages a report keeps.
const maxFailures = 20

// Archive keeps a local folder as a never-deleting copy of iCloud Drive.
type Archive struct {
	// Root is the archive's Drive folder.
	Root string
	// dir is Root, open for the length of a sync (open): every file
	// operation goes through it, so names from iCloud never reach outside it.
	dir     *os.Root
	DB      *ledger.DB
	Client  *icloud.Client
	Now     func() time.Time
	Log     *slog.Logger
	Workers int
	// Stall abandons a download that receives no data for this long.
	Stall time.Duration
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
	// prev is the file's row when it was archived before.
	prev *ledger.DriveFile
	// repair is set when the archived copy is missing or damaged: download
	// again even if the bytes did not change in iCloud.
	repair bool
}

// Sync lists iCloud Drive and brings the archive up to date. An error means
// the sync could not run or had to stop (listing failed, sign-in needed);
// single files that fail are counted in the report and retried next time.
func (a *Archive) Sync(ctx context.Context) (rep Report, err error) {
	closeDir, err := a.open()
	if err != nil {
		return rep, err
	}
	defer func() { err = errors.Join(err, closeDir()) }()
	if err = a.clearPartial(); err != nil {
		return rep, err
	}
	listing, err := Walk(ctx, a.Client)
	if err != nil {
		return rep, err
	}
	rep.Listed = len(listing.Files)
	known, err := a.DB.DriveFiles(ctx)
	if err != nil {
		return rep, err
	}
	paths := newPlanner()
	for id, k := range known {
		paths.hold(k.LocalPath, id)
	}

	var (
		jobs []job
		mu   sync.Mutex
	)
	seen := map[string]bool{}
	for _, f := range listing.Files {
		seen[f.DriveWSID] = true
		k, ok := known[f.DriveWSID]
		if !ok {
			local, err := paths.assign(f)
			if err != nil {
				rep.fail(&mu, "%s: %v", strings.Join(f.Path, "/"), err)
				continue
			}
			jobs = append(jobs, job{file: f, local: local})
			continue
		}
		j, err := a.reconcile(ctx, k, f, paths, &rep)
		switch {
		case err != nil:
			rep.fail(&mu, "%s: %v", strings.Join(f.Path, "/"), err)
		case j != nil:
			jobs = append(jobs, *j)
		}
	}

	if err := a.download(ctx, jobs, &rep, &mu); err != nil {
		return rep, err
	}

	// Only a scope listed in full can show a deletion. A scope that holds
	// archived files but could not be listed is a failure, and while there is
	// one, files that left a listed scope may have moved into it: no deletion
	// is recorded this time.
	archivedIn := map[string]bool{}
	for _, k := range known {
		archivedIn[Scope(k.ICloudPath)] = true
	}
	blocked := false
	for scope, why := range listing.Unlisted {
		if archivedIn[scope] {
			rep.fail(&mu, "%s could not be listed: %s", scope, why)
			blocked = true
		} else {
			rep.UnlistedApps++
		}
	}
	if blocked {
		return rep, nil
	}
	var gone []string
	for id, k := range known {
		if !seen[id] && k.DeletedAt == nil && listing.Complete[Scope(k.ICloudPath)] {
			gone = append(gone, id)
		}
	}
	if err := a.DB.MarkDriveFilesDeleted(ctx, gone, a.Now()); err != nil {
		return rep, err
	}
	rep.Deleted = len(gone)
	return rep, nil
}

// reconcile brings an archived file in line with its listing: it follows a
// move or rename at once (and records it, so the ledger always names where
// the copy is), records metadata-only changes, and returns a download job when
// the bytes changed in iCloud or the copy is missing or damaged.
func (a *Archive) reconcile(ctx context.Context, k ledger.DriveFile, f File, paths *planner, rep *Report) (*job, error) {
	icloudPath := strings.Join(f.Path, "/")
	present := true
	if k.ICloudPath != icloudPath {
		paths.release(k.LocalPath)
		local, err := paths.assign(f)
		if err != nil {
			paths.hold(k.LocalPath, f.DriveWSID)
			return nil, err
		}
		if present, err = a.move(k.LocalPath, local); err != nil {
			paths.release(local)
			paths.hold(k.LocalPath, f.DriveWSID)
			return nil, err
		}
		// Record the new place now, with the old etag: a later failure leaves
		// the ledger naming where the copy really is.
		k.ICloudPath, k.LocalPath = icloudPath, local
		if err := a.DB.UpdateDriveFile(ctx, k, a.Now()); err != nil {
			return nil, err
		}
		rep.Moved++
		if k.DeletedAt != nil {
			rep.Restored++
			k.DeletedAt = nil
		}
	}
	if k.Etag != f.Etag || k.DamagedAt != nil || !present {
		return &job{file: f, local: k.LocalPath, prev: &k, repair: k.DamagedAt != nil || !present}, nil
	}
	if k.DeletedAt != nil || !sameTime(k.ModifiedAt, f.DateModified) {
		if k.DeletedAt != nil {
			rep.Restored++
		}
		if err := a.DB.UpdateDriveFile(ctx, a.metadata(k, f, k.LocalPath), a.Now()); err != nil {
			return nil, err
		}
	}
	return nil, nil //nolint:nilnil // nothing to download
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

// download runs the jobs on a few workers. A sign-in failure stops the rest
// and is returned: the sync cannot go on.
func (a *Archive) download(ctx context.Context, jobs []job, rep *Report, mu *sync.Mutex) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	parallel.ForEach(ctx, jobs, a.Workers, func(j job) {
		if ctx.Err() != nil {
			return // handed out just before the sync stopped
		}
		err := a.archive(ctx, j, rep, mu)
		switch {
		case errors.Is(err, icloud.ErrSignInRequired):
			cancel(err)
		case err != nil && ctx.Err() == nil:
			rep.fail(mu, "%s: %v", strings.Join(j.file.Path, "/"), err)
		}
	})
	if err := context.Cause(ctx); err != nil {
		return fmt.Errorf("drive sync: %w", err)
	}
	return nil
}

// archive downloads, verifies and places one file.
func (a *Archive) archive(ctx context.Context, j job, rep *Report, mu *sync.Mutex) error {
	partial, sum, sig, pkg, n, err := a.fetch(ctx, j.file)
	if err != nil {
		return err
	}
	defer a.dir.Remove(partial) //nolint:errcheck // gone after a successful rename; a temp file otherwise

	if j.prev != nil && !j.repair && j.prev.SHA256 == sum {
		// Only metadata changed: the archived copy already has these bytes.
		if err = a.DB.UpdateDriveFile(ctx, a.metadata(*j.prev, j.file, j.local), a.Now()); err != nil {
			return err
		}
		if j.prev.DeletedAt != nil {
			mu.Lock()
			rep.Restored++
			mu.Unlock()
		}
		return nil
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
	case j.repair:
		rep.Repaired++
	default:
		rep.Changed++
	}
	if j.prev != nil && j.prev.DeletedAt != nil {
		rep.Restored++
	}
	return nil
}

// partialName names a download in progress after the file's whole drivewsid
// (zone and document), so that no two files share one.
func partialName(drivewsid string) string {
	sum := sha256.Sum256([]byte(drivewsid))
	return hex.EncodeToString(sum[:16]) + ".part"
}

// fetch downloads a file into the partial folder and verifies it: a plain
// file against its listed size and Apple's signature, a package as an intact
// ZIP. It returns the partial file (its name in the Drive folder), its
// SHA-256, Apple's signature (hex, plain files) and its size.
func (a *Archive) fetch(ctx context.Context, f File) (partial, sum, sig string, pkg bool, n int64, err error) {
	tmp := filepath.Join(partialDir, partialName(f.DriveWSID))
	out, err := a.dir.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return "", "", "", false, 0, fmt.Errorf("create download file: %w", err)
	}
	defer func() {
		// Results are cleared on error, so remove by the local name.
		if err != nil {
			err = errors.Join(err, a.dir.Remove(tmp))
		}
	}()
	contentHash := sha256.New()
	var signature hash.Hash
	var apple string
	// iCloud lists packages with their size and refuses to serve empty files:
	// an empty listed file has nothing to fetch.
	if f.Size > 0 {
		var tok downloadToken
		tok, pkg, err = download(ctx, a.Client, f.Item)
		if err != nil {
			return "", "", "", false, 0, errors.Join(err, out.Close())
		}
		body, fetchErr := a.Client.Fetch(ctx, tok.URL, a.Stall)
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
	if err = out.Sync(); err != nil {
		return "", "", "", false, 0, errors.Join(fmt.Errorf("write download file: %w", err), out.Close())
	}
	if pkg {
		err = checkPackage(out, n)
	}
	if err = errors.Join(err, out.Close()); err != nil {
		return "", "", "", false, 0, err
	}
	switch {
	case pkg:
	case f.Size != n:
		return "", "", "", false, 0, fmt.Errorf("downloaded %d bytes, iCloud lists %d", n, f.Size)
	case f.Size > 0:
		if sig, err = checkSignature(apple, signature); err != nil {
			return "", "", "", false, 0, err
		}
	}
	return tmp, hex.EncodeToString(contentHash.Sum(nil)), sig, pkg, n, nil
}

// place moves a verified download to local. A file already there is moved
// under .versions first; keptAs names it (archive-relative, '/').
func (a *Archive) place(partial, local string) (keptAs string, err error) {
	dest := name(local)
	keptAs, err = a.keep(local)
	if err != nil {
		return "", err
	}
	if err := a.dir.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", fmt.Errorf("create folder for %s: %w", local, err)
	}
	if err := a.dir.Rename(partial, dest); err != nil {
		return "", fmt.Errorf("place %s: %w", local, err)
	}
	return keptAs, nil
}

// maxVersionSuffix bounds the search for an unused .versions name.
const maxVersionSuffix = 1000

// keep moves the file at local, if any, to .versions/<now>/local (adding
// "-2", "-3", ... to the time folder when that name is taken). It never moves
// a folder: a folder where a file belongs is an error to look into.
func (a *Archive) keep(local string) (string, error) {
	src := name(local)
	info, err := a.dir.Lstat(src)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("check %s: %w", local, err)
	case !info.Mode().IsRegular():
		return "", fmt.Errorf("%s is not a file (%s); it is left in place", local, info.Mode().Type())
	}
	stamp := a.Now().UTC().Format(platform.FileTimeLayout)
	for n := 1; n <= maxVersionSuffix; n++ {
		folder := stamp
		if n > 1 {
			folder = fmt.Sprintf("%s-%d", stamp, n)
		}
		kept := path.Join(VersionsDir, folder, local)
		dest := name(kept)
		if _, err := a.dir.Lstat(dest); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("check %s: %w", kept, err)
		}
		if err := a.dir.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return "", fmt.Errorf("create version folder: %w", err)
		}
		if err := a.dir.Rename(src, dest); err != nil {
			return "", fmt.Errorf("keep previous %s: %w", local, err)
		}
		return kept, nil
	}
	return "", fmt.Errorf("no free .versions name for %s", local)
}

// move follows a file moved or renamed in iCloud: its archived copy moves
// from local to target, and folders the move left empty are removed. It is
// safe to repeat after a crash at any point. present reports whether the copy
// exists at target afterwards; a missing copy is downloaded again.
func (a *Archive) move(from, target string) (present bool, err error) {
	src, dest := name(from), name(target)
	srcInfo, srcExists, err := a.lstat(src)
	if err != nil {
		return false, err
	}
	destInfo, destExists, err := a.lstat(dest)
	if err != nil {
		return false, err
	}
	switch {
	case !srcExists:
		// Already moved by an interrupted sync, or the copy is gone.
		return destExists, nil
	case destExists && os.SameFile(srcInfo, destInfo):
		// The same file under another case: a rename in place.
		if err := a.dir.Rename(src, dest); err != nil {
			return false, fmt.Errorf("rename %s to %s: %w", from, target, err)
		}
		return true, nil
	case destExists:
		// Another file stands where this one goes: keep it.
		if _, err := a.keep(target); err != nil {
			return false, err
		}
	}
	if err := a.dir.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return false, fmt.Errorf("create folder for %s: %w", target, err)
	}
	if err := a.dir.Rename(src, dest); err != nil {
		return false, fmt.Errorf("move %s to %s: %w", from, target, err)
	}
	a.removeEmptyParents(filepath.Dir(src))
	return true, nil
}

// lstat reports whether a name exists in the Drive folder; any error but
// "not there" is returned.
func (a *Archive) lstat(file string) (fs.FileInfo, bool, error) {
	info, err := a.dir.Lstat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("check %s: %w", file, err)
	}
	return info, true, nil
}

// removeEmptyParents removes dir (a folder in the Drive folder) and its
// parents while they are empty, up to the Drive folder itself. Only empty
// folders are ever removed.
func (a *Archive) removeEmptyParents(dir string) {
	for dir = filepath.Clean(dir); filepath.IsLocal(dir); dir = filepath.Dir(dir) {
		if a.dir.Remove(dir) != nil { // fails unless empty
			return
		}
	}
}

// clearPartial removes downloads an interrupted sync left behind.
func (a *Archive) clearPartial() error {
	if err := a.dir.RemoveAll(partialDir); err != nil {
		return fmt.Errorf("clear %s: %w", partialDir, err)
	}
	if err := a.dir.MkdirAll(partialDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", partialDir, err)
	}
	return nil
}

// open opens Root (creating it) as the folder every file operation of a sync
// goes through: os.Root refuses names, links and junctions that lead outside
// it, so neither a name from iCloud nor a link planted in the archive can make
// a sync write elsewhere. The returned func closes it.
func (a *Archive) open() (func() error, error) {
	if err := os.MkdirAll(a.Root, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", a.Root, err)
	}
	root, err := os.OpenRoot(a.Root)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", a.Root, err)
	}
	a.dir = root
	return func() error {
		a.dir = nil
		if err := root.Close(); err != nil {
			return fmt.Errorf("close %s: %w", a.Root, err)
		}
		return nil
	}, nil
}

// name is the Drive folder name of an archive-relative local path ('/').
func name(local string) string {
	return filepath.FromSlash(local)
}
