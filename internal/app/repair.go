package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// DamagedDir is the archive folder (under storage.root) that keeps the files
// of repaired renditions, under the UTC time the repair was requested and
// their path inside the Photos folder. homecloud never deletes them.
const DamagedDir = "Damaged"

// prepareRepairs readies requested repairs for this cycle's download: it moves
// each failing file aside, has kei mark the missing files for download, and
// abandons the repairs kei will not download again. Completed repairs are
// recorded by Ingest.
func (a *Agent) prepareRepairs(ctx context.Context) error {
	repairs, err := a.db.Repairs(ctx)
	if err != nil || len(repairs) == 0 {
		return err
	}
	root, err := os.OpenRoot(a.cfg.Storage.Root)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer root.Close() //nolint:errcheck // nothing written through the handle is pending

	var errs []error
	setAside := repairs[:0]
	for _, p := range repairs {
		if p.SetAsideAt == nil {
			keptAs, moveErr := a.setAside(root, p)
			if moveErr == nil {
				now := a.now()
				moveErr = a.db.RecordSetAside(ctx, p.Key, keptAs, now)
				p.SetAsideAt, p.KeptAs = &now, keptAs
			}
			if moveErr != nil {
				errs = append(errs, fmt.Errorf("set aside %s/%s/%s: %w", p.Library, p.AssetID, p.Version, moveErr))
				continue
			}
			a.log.InfoContext(ctx, "file set aside for repair", "library", p.Library, "asset", p.AssetID,
				"version", p.Version, "kept_as", keptAs)
		}
		setAside = append(setAside, p)
	}
	if len(setAside) == 0 {
		return errors.Join(errs...)
	}

	if err = kei.Reconcile(ctx, a.cfg.Kei, a.keiLog); err != nil {
		return errors.Join(append(errs, err)...)
	}
	rows, err := kei.Manifest(ctx, a.cfg.Kei, kei.DownloadDir(a.cfg.Storage.Root))
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	return errors.Join(append(errs, a.abandonRefused(ctx, root, setAside, rows))...)
}

// abandonRefused abandons the repair of every asset with a set-aside
// rendition kei will not download again, after putting its files back.
// It runs after kei reconcile, which marks every file missing from its
// recorded path for download.
func (a *Agent) abandonRefused(ctx context.Context, root *os.Root, repairs []ledger.RepairRendition, rows []kei.ManifestRow) error {
	byKey := make(map[ledger.Key]kei.ManifestRow, len(rows))
	for _, r := range rows {
		byKey[ledger.Key{Library: r.Library, AssetID: r.AssetID, Version: r.Version}] = r
	}
	type asset struct{ library, id string }
	refused := map[asset]string{}
	for _, p := range repairs {
		if reason := refusal(byKey, p); reason != "" {
			refused[asset{p.Library, p.AssetID}] = reason
		}
	}

	var errs []error
	for _, p := range repairs {
		if _, ok := refused[asset{p.Library, p.AssetID}]; ok && p.KeptAs != "" {
			if err := a.restore(root, p); err != nil {
				errs = append(errs, err)
				delete(refused, asset{p.Library, p.AssetID}) // tried again next cycle
			}
		}
	}
	for as, reason := range refused {
		if err := a.db.AbandonRepair(ctx, as.library, as.id, reason, a.now()); err != nil {
			errs = append(errs, err)
			continue
		}
		a.log.WarnContext(ctx, "repair abandoned", "library", as.library, "asset", as.id, "reason", reason)
		a.alert(ctx, notify.Msg{
			Title:    "homecloud: repair not possible",
			Body:     "kei will not download a quarantined file again: " + reason + ". The file is back where it was and stays quarantined.",
			Priority: notify.PriorityDefault,
			Tags:     []string{"warning"},
		})
	}
	return errors.Join(errs...)
}

// refusal says why kei will not download p again, or "" when it will (or
// already did).
func refusal(rows map[ledger.Key]kei.ManifestRow, p ledger.RepairRendition) string {
	r, ok := rows[p.Key]
	switch {
	case !ok:
		return "kei no longer lists the file"
	case r.Status == kei.StatusPolicyExcluded:
		return "kei's settings exclude the file"
	case r.Status == kei.StatusDownloaded && !ledger.DownloadedSince(rendition(r), *p.SetAsideAt):
		// kei reconcile leaves the records of the files it keeps as history
		// (its protected legacy records) unchanged.
		return "kei keeps its record of the file as downloaded"
	}
	return ""
}

// setAside moves the file of p out of the Photos folder, to DamagedDir under
// the time the repair was requested, and returns where it went (relative to
// storage.root, '/'), or "" when no file is at the recorded path. A file
// already moved by an earlier attempt that stopped before recording it is
// found there.
func (a *Agent) setAside(root *os.Root, p ledger.RepairRendition) (string, error) {
	src, err := archivePath(a.cfg.Storage.Root, p.LocalPath)
	if err != nil {
		return "", err
	}
	inPhotos, err := filepath.Rel(kei.PhotosDir, src)
	if err != nil || !filepath.IsLocal(inPhotos) {
		return "", fmt.Errorf("%s is not in the archive's %s folder", p.LocalPath, kei.PhotosDir)
	}
	dest := filepath.Join(DamagedDir, p.RequestedAt.UTC().Format(platform.FileTimeLayout), inPhotos)
	keptAs := filepath.ToSlash(dest)

	srcInfo, srcErr := root.Lstat(src)
	_, destErr := root.Lstat(dest)
	switch {
	case srcErr == nil && !srcInfo.Mode().IsRegular():
		return "", fmt.Errorf("%s is not a file (%s); it is left in place", p.LocalPath, srcInfo.Mode().Type())
	case srcErr == nil && destErr == nil:
		return "", fmt.Errorf("both %s and %s exist; look into it", p.LocalPath, keptAs)
	case srcErr == nil && errors.Is(destErr, fs.ErrNotExist):
		if err := root.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return "", fmt.Errorf("create %s: %w", path.Dir(keptAs), err)
		}
		if err := root.Rename(src, dest); err != nil {
			return "", fmt.Errorf("move %s aside: %w", p.LocalPath, err)
		}
		return keptAs, nil
	case srcErr == nil:
		return "", fmt.Errorf("check %s: %w", keptAs, destErr)
	case !errors.Is(srcErr, fs.ErrNotExist):
		return "", fmt.Errorf("check %s: %w", p.LocalPath, srcErr)
	case destErr == nil:
		return keptAs, nil
	case errors.Is(destErr, fs.ErrNotExist):
		return "", nil
	default:
		return "", fmt.Errorf("check %s: %w", keptAs, destErr)
	}
}

// restore moves the set-aside file of p back to its recorded path and removes
// the folders it leaves empty, DamagedDir included.
func (a *Agent) restore(root *os.Root, p ledger.RepairRendition) error {
	dest, err := archivePath(a.cfg.Storage.Root, p.LocalPath)
	if err != nil {
		return err
	}
	kept := filepath.FromSlash(p.KeptAs)
	if _, err := root.Lstat(dest); err == nil {
		return fmt.Errorf("put back %s: a file is at %s again; both are kept", p.KeptAs, p.LocalPath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check %s: %w", p.LocalPath, err)
	}
	if err := root.Rename(kept, dest); err != nil {
		return fmt.Errorf("put back %s: %w", p.KeptAs, err)
	}
	removeEmptyFolders(root, filepath.Dir(kept))
	return nil
}

// removeEmptyFolders removes dir and then each parent below the archive root
// while they are empty. The first one that is not, or cannot be removed, ends
// it: an empty folder left behind does no harm.
func removeEmptyFolders(root *os.Root, dir string) {
	for ; dir != "."; dir = filepath.Dir(dir) {
		if root.Remove(dir) != nil {
			return
		}
	}
}

// archivePath returns file, an absolute path, relative to the archive root.
func archivePath(storageRoot, file string) (string, error) {
	rel, err := filepath.Rel(storageRoot, file)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%s is not inside the archive %s", file, storageRoot)
	}
	return rel, nil
}
