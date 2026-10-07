package app

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/vasilistotskas/homecloud/internal/iphonebackup"
	"github.com/vasilistotskas/homecloud/internal/iphonecopy"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
)

// IPhoneCopiesFolder is the folder of the archive that keeps copies of
// iPhone backups (iphone_backup.keep).
const IPhoneCopiesFolder = "iPhone backups"

// IPhoneBackupCopy is one copy of a device's backup in the archive.
type IPhoneBackupCopy struct {
	// CompletedAt is when the copied backup finished.
	CompletedAt time.Time `json:"completed_at"`
	// Dir is the copy's folder.
	Dir   string `json:"dir"`
	Files int64  `json:"files"`
	Bytes int64  `json:"bytes"`
	// CopiedBytes were written for this copy; the rest it shares with the
	// copy before.
	CopiedBytes int64 `json:"copied_bytes"`
	// VerifiedAt is when every file was last checked against the copy's
	// manifest; DamagedFiles did not match.
	VerifiedAt   *time.Time `json:"verified_at,omitempty"`
	DamagedFiles int64      `json:"damaged_files,omitempty"`
}

// copies is the archive's copies of iPhone backups during one check.
type copies struct {
	a       *Agent
	archive *iphonecopy.Archive
	// rows are the ledger's records by device, newest first.
	rows map[string][]ledger.IPhoneBackupCopy
}

// openCopies opens the copies folder, finishes or discards what an
// interrupted copy left, and brings the ledger in line with the disk.
func (a *Agent) openCopies(ctx context.Context) (*copies, error) {
	archive, err := iphonecopy.Open(filepath.Join(a.cfg.Storage.Root, IPhoneCopiesFolder))
	if err != nil {
		return nil, err
	}
	c := &copies{a: a, archive: archive}
	if err := c.recover(ctx); err != nil {
		return nil, fmt.Errorf("%w (%w)", err, archive.Close())
	}
	return c, nil
}

func (c *copies) close() error { return c.archive.Close() }

func (c *copies) recover(ctx context.Context) error {
	devices, err := c.archive.Devices()
	if err != nil {
		return err
	}
	for _, udid := range devices {
		if err := c.archive.Recover(udid); err != nil {
			return err
		}
	}
	if err := c.load(ctx); err != nil {
		return err
	}
	return c.reconcile(ctx, devices)
}

func (c *copies) load(ctx context.Context) error {
	rows, err := c.a.db.IPhoneBackupCopies(ctx)
	if err != nil {
		return err
	}
	c.rows = map[string][]ledger.IPhoneBackupCopy{}
	for _, r := range rows {
		c.rows[r.UDID] = append(c.rows[r.UDID], r)
	}
	return nil
}

// reconcile records copies on disk the ledger does not know (an interrupted
// run made them) and forgets those no longer on disk.
func (c *copies) reconcile(ctx context.Context, devices []string) error {
	now := c.a.now()
	known := map[string]bool{}
	changed := false
	for _, udid := range devices {
		snaps, err := c.archive.Snapshots(udid)
		if err != nil {
			return err
		}
		recorded := map[string]bool{}
		for _, r := range c.rows[udid] {
			recorded[r.Stamp] = true
		}
		for _, s := range snaps {
			known[udid+"/"+s.Stamp] = true
			if recorded[s.Stamp] {
				continue
			}
			files, size, err := c.archive.Size(s)
			if err != nil {
				return err
			}
			b := iphonebackup.Inspect(ctx, c.archive.Path(s.Dir()), udid)
			// What this copy wrote is not known: it was made by a run that was
			// interrupted before recording it. It is verified at the next check.
			if err := c.a.db.AddIPhoneBackupCopy(ctx, ledger.IPhoneBackupCopy{
				UDID: udid, Stamp: s.Stamp, DeviceName: b.DeviceName, Files: int64(files), Bytes: size, CreatedAt: now,
			}); err != nil {
				return err
			}
			changed = true
		}
	}
	for udid, rows := range c.rows {
		for _, r := range rows {
			if known[udid+"/"+r.Stamp] {
				continue
			}
			c.a.log.Warn("an iPhone backup copy is no longer in the archive", "udid", udid, "stamp", r.Stamp)
			if err := c.a.db.RemoveIPhoneBackupCopy(ctx, r, ledger.CopyRemovedMissing, now); err != nil {
				return err
			}
			changed = true
		}
	}
	if changed {
		return c.load(ctx)
	}
	return nil
}

// base is the copy the device's next copy links its unchanged files to: the
// newest one with no damaged files.
func (c *copies) base(udid string) (*iphonecopy.Snapshot, error) {
	damaged := map[string]bool{}
	for _, r := range c.rows[udid] {
		damaged[r.Stamp] = r.DamagedFiles > 0
	}
	snaps, err := c.archive.Snapshots(udid)
	if err != nil {
		return nil, err
	}
	for _, s := range snaps {
		if !damaged[s.Stamp] {
			return &s, nil
		}
	}
	return nil, nil //nolint:nilnil // no copy to build on
}

// added records a new copy. It was checked against its manifest as it was
// written, which counts as its first verification.
func (c *copies) added(ctx context.Context, s iphonecopy.Snapshot, st iphonecopy.Stats) error {
	now := c.a.now()
	b := iphonebackup.Inspect(ctx, c.archive.Path(s.Dir()), s.UDID)
	r := ledger.IPhoneBackupCopy{
		UDID: s.UDID, Stamp: s.Stamp, DeviceName: b.DeviceName, Files: int64(st.Files), Bytes: st.Bytes,
		CopiedBytes: st.CopiedBytes, CreatedAt: now, VerifiedAt: &now,
	}
	if err := c.a.db.AddIPhoneBackupCopy(ctx, r); err != nil {
		return err
	}
	c.a.log.Info("iPhone backup copied", "udid", s.UDID, "stamp", s.Stamp, "files", st.Files,
		"copied", st.Copied, "linked", st.Linked, "copied_bytes", st.CopiedBytes)
	c.rows[s.UDID] = append([]ledger.IPhoneBackupCopy{r}, c.rows[s.UDID]...)
	return nil
}

// prune removes each device's copies beyond iphone_backup.keep.
func (c *copies) prune(ctx context.Context) error {
	for udid, rows := range c.rows {
		removed, err := c.archive.Prune(udid, c.a.cfg.IPhoneBackup.Keep)
		if err != nil {
			return err
		}
		for _, s := range removed {
			for _, r := range rows {
				if r.Stamp == s.Stamp {
					if err := c.a.db.RemoveIPhoneBackupCopy(ctx, r, ledger.CopyRemovedRetention, c.a.now()); err != nil {
						return err
					}
				}
			}
		}
	}
	return c.load(ctx)
}

// verify re-checks every copy of each device with a copy not verified
// within iphone_backup.read_interval, all of a device's copies in one pass so
// that files they share are read once. Damage is alerted.
func (c *copies) verify(ctx context.Context) error {
	now := c.a.now()
	for udid, rows := range c.rows {
		due := false
		for _, r := range rows {
			if r.VerifiedAt == nil || now.Sub(*r.VerifiedAt) >= c.a.cfg.IPhoneBackup.ReadInterval.Std() {
				due = true
			}
		}
		if !due {
			continue
		}
		snaps, err := c.archive.Snapshots(udid)
		if err != nil {
			return err
		}
		damaged, err := c.archive.Verify(ctx, snaps)
		if err != nil {
			return err
		}
		bad := 0
		for _, r := range rows {
			n := int64(len(damaged[r.Stamp]))
			if err := c.a.db.SetIPhoneBackupCopyVerified(ctx, r, n, now); err != nil {
				return err
			}
			if n > 0 {
				bad++
				c.a.log.Error("iPhone backup copy damaged", "udid", udid, "stamp", r.Stamp, "files", damaged[r.Stamp])
			}
		}
		if bad > 0 {
			c.a.alertRepeated(ctx, alertIPhoneCopyDamaged+udid, notify.Msg{
				Title: "homecloud: an iPhone backup copy is damaged",
				Body: fmt.Sprintf("%d copy(ies) of an iPhone backup in the archive no longer match what was copied. "+
					"Check the archive drive's health; the next backup is copied whole.", bad),
				Priority: notify.PriorityUrgent,
				Tags:     []string{"rotating_light"},
			})
		}
	}
	return c.load(ctx)
}

// report lists a device's copies, newest first.
func (c *copies) report(udid string) []IPhoneBackupCopy {
	out := make([]IPhoneBackupCopy, 0, len(c.rows[udid]))
	for _, r := range c.rows[udid] {
		s := iphonecopy.Snapshot{UDID: udid, Stamp: r.Stamp}
		at, err := iphonecopy.ParseStamp(r.Stamp)
		if err != nil {
			continue // the ledger only records valid stamps
		}
		out = append(out, IPhoneBackupCopy{
			CompletedAt: at, Dir: c.archive.Path(s.Dir()), Files: r.Files, Bytes: r.Bytes,
			CopiedBytes: r.CopiedBytes, VerifiedAt: r.VerifiedAt, DamagedFiles: r.DamagedFiles,
		})
	}
	return out
}
