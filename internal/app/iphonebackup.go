package app

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/vasilistotskas/homecloud/internal/iphonebackup"
	"github.com/vasilistotskas/homecloud/internal/iphonecopy"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// IPhoneBackupRunKind is the ledger run kind of an iPhone backup check.
const IPhoneBackupRunKind = "iphone_backup"

// IPhoneBackupReport is recorded as the detail of each iPhone backup check.
type IPhoneBackupReport struct {
	Devices []IPhoneBackupDevice `json:"devices"`
	// Archived counts frozen copies the user archived in Apple's tool.
	Archived int `json:"archived"`
	// DirErrors lists backup directories that could not be checked.
	DirErrors []string `json:"dir_errors,omitempty"`
	// PushErrors lists backups pushed from another computer that were
	// refused, or why iphone_backup.incoming could not be read.
	PushErrors []string `json:"push_errors,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// IPhoneBackupDevice is one device's current backup and its verdict.
type IPhoneBackupDevice struct {
	UDID           string             `json:"udid"`
	Name           string             `json:"name"`
	ProductVersion string             `json:"product_version"`
	Dir            string             `json:"dir"`
	Encrypted      bool               `json:"encrypted"`
	CompletedAt    *time.Time         `json:"completed_at,omitempty"`
	Files          int64              `json:"files"`
	Bytes          int64              `json:"bytes"`
	State          iphonebackup.State `json:"state"`
	Reasons        []string           `json:"reasons,omitempty"`
	// LastFullReadAt is when every file was last read in full.
	LastFullReadAt *time.Time `json:"last_full_read_at,omitempty"`
	// FromCopy is set for a device whose backups arrive from another
	// computer (iphone_backup.incoming): the verdict is on its newest copy.
	FromCopy bool `json:"from_copy,omitempty"`
	// Copies are the device's copies in the archive, newest first.
	Copies []IPhoneBackupCopy `json:"copies,omitempty"`
	// CopyError says why the current backup could not be copied.
	CopyError string `json:"copy_error,omitempty"`
}

// ParseIPhoneBackupReport decodes the detail recorded for a finished check.
func ParseIPhoneBackupReport(detail string) (IPhoneBackupReport, error) {
	var rep IPhoneBackupReport
	if err := json.Unmarshal([]byte(detail), &rep); err != nil {
		return IPhoneBackupReport{}, fmt.Errorf("decode iphone backup report: %w", err)
	}
	return rep, nil
}

// checkIPhoneBackups verifies the archive's copies when that is due, takes in
// backups pushed from another computer, judges each device's current backup
// in the configured directories against what the ledger remembers of its
// folder (every iphone_backup.read_interval also reading every file), copies
// healthy backups into the archive, prunes the copies, and judges devices
// known only from copies by their newest one. State changes are recorded and problems alerted. The check is
// healthy when at least one device has a backup and nothing failed.
func (a *Agent) checkIPhoneBackups(ctx context.Context) (any, bool) {
	var rep IPhoneBackupReport
	records, err := a.db.IPhoneBackupRecords(ctx)
	if err != nil {
		rep.Error = err.Error()
		return rep, false
	}
	var cp *copies
	if a.cfg.IPhoneBackup.Keep > 0 {
		if cp, err = a.openCopies(ctx); err != nil {
			rep.Error = err.Error()
			return rep, false
		}
		defer func() {
			if err := cp.close(); err != nil {
				a.log.ErrorContext(ctx, "iPhone backup copies not closed", "err", err)
			}
		}()
		// Verify first: a copy found damaged is never the base new copies
		// link their unchanged files to.
		if err := cp.verify(ctx); err != nil {
			rep.Error = err.Error()
			return rep, false
		}
	}
	healthy := true
	if incoming := a.cfg.IPhoneBackup.Incoming; incoming != "" {
		if err := a.ingestPushes(ctx, cp, incoming, &rep); err != nil {
			rep.Error = err.Error()
			return rep, false
		}
		if len(rep.PushErrors) > 0 {
			healthy = false
		}
	}

	local := map[string]bool{}
	for _, dir := range a.cfg.IPhoneBackup.Dirs {
		current, archived, err := iphonebackup.Scan(ctx, dir)
		if ctx.Err() != nil {
			rep.Error = ctx.Err().Error()
			return rep, false
		}
		if err != nil {
			a.log.ErrorContext(ctx, "iPhone backup folder not checked; checked again next time", "dir", dir, "err", err)
			rep.DirErrors = append(rep.DirErrors, err.Error())
			healthy = false
			continue
		}
		rep.Archived += archived
		for _, b := range current {
			d, err := a.judgeIPhoneBackup(ctx, b, records[b.Dir])
			if err != nil {
				rep.Error = err.Error()
				return rep, false
			}
			if b.UDID != "" {
				local[b.UDID] = true
			}
			if cp != nil && d.State == iphonebackup.StateOK {
				if err := a.copyIPhoneBackup(ctx, cp, b); err != nil {
					if ctx.Err() != nil {
						rep.Error = ctx.Err().Error()
						return rep, false
					}
					a.log.ErrorContext(ctx, "iPhone backup not copied; tried again next check", "dir", b.Dir, "err", err)
					d.CopyError = err.Error()
					a.alertRepeated(ctx, alertIPhoneCopy+b.UDID, notify.Msg{
						Title:    "homecloud: an iPhone backup could not be copied",
						Body:     "A healthy iPhone backup could not be copied into the archive. The app shows why; the copy is tried again at the next check.",
						Priority: notify.PriorityHigh,
						Tags:     []string{"iphone"},
					})
				}
			}
			rep.Devices = append(rep.Devices, d)
		}
	}

	if cp != nil {
		if err := cp.prune(ctx); err != nil {
			rep.Error = err.Error()
			return rep, false
		}
		for udid, rows := range cp.rows {
			if local[udid] || len(rows) == 0 {
				continue
			}
			d, err := a.judgeIPhoneCopy(ctx, cp, udid, rows[0], records)
			if err != nil {
				rep.Error = err.Error()
				return rep, false
			}
			rep.Devices = append(rep.Devices, d)
		}
		for i := range rep.Devices {
			rep.Devices[i].Copies = cp.report(rep.Devices[i].UDID)
		}
	}
	for _, d := range rep.Devices {
		if !d.State.Healthy() {
			a.alertIPhoneBackup(ctx, d.Dir, d.State)
		}
		if !d.State.Healthy() || d.CopyError != "" || slices.ContainsFunc(d.Copies, func(c IPhoneBackupCopy) bool { return c.DamagedFiles > 0 }) {
			healthy = false
		}
	}
	switch {
	case len(rep.DirErrors) > 0:
		a.alertRepeated(ctx, alertIPhoneCheck, notify.Msg{
			Title:    "homecloud: iPhone backups could not be checked",
			Body:     "A configured iPhone backup folder could not be read. Check that it exists and that this account can read it.",
			Priority: notify.PriorityHigh,
			Tags:     []string{"iphone"},
		})
	case len(rep.Devices) == 0:
		healthy = false
		body := "No local iPhone backup was found. Turn on encrypted local backups in Apple's Devices app and back up once."
		if len(a.cfg.IPhoneBackup.Dirs) == 0 {
			body = "No iPhone backup has arrived yet. Run \"homecloud iphone push\" on the computer Apple's Devices app backs the iPhone up to."
		}
		a.alertRepeated(ctx, alertIPhoneNone, notify.Msg{
			Title:    "homecloud: no iPhone backup yet",
			Body:     body,
			Priority: notify.PriorityHigh,
			Tags:     []string{"iphone"},
		})
	}
	a.log.InfoContext(ctx, "iPhone backups checked", "devices", len(rep.Devices), "archived", rep.Archived,
		"dir_errors", len(rep.DirErrors), "push_errors", len(rep.PushErrors), "healthy", healthy)
	return rep, healthy
}

// ingestPushes takes the backups pushed to iphone_backup.incoming into the
// archive. A refused push is recorded and alerted; the next push from the
// other computer sends it again. Without this computer's push key, or below
// storage.min_free_percent, pushes wait where they are. The returned error is
// the ledger's.
func (a *Agent) ingestPushes(ctx context.Context, cp *copies, incoming string, rep *IPhoneBackupReport) error {
	defer func() {
		if len(rep.PushErrors) > 0 {
			a.alertRepeated(ctx, alertIPhonePush, notify.Msg{
				Title:    "homecloud: a pushed iPhone backup was not taken in",
				Body:     "A backup pushed from another computer could not be taken into the archive. The app shows why; refused backups are sent again by the next push.",
				Priority: notify.PriorityHigh,
				Tags:     []string{"iphone"},
			})
		}
	}()
	key, err := a.loadPushKey()
	if err != nil {
		a.log.ErrorContext(ctx, "pushed iPhone backups wait for a push key", "err", err)
		rep.PushErrors = append(rep.PushErrors, err.Error())
		return nil
	}
	if _, err := a.checkSpace(); err != nil {
		a.log.WarnContext(ctx, "pushed iPhone backups wait for free space", "err", err)
		rep.PushErrors = append(rep.PushErrors, err.Error())
		return nil
	}
	ingested, ingestErr := cp.archive.Ingest(ctx, incoming, key, a.now(), cp.base)
	for _, in := range ingested {
		if in.Err != nil {
			a.log.ErrorContext(ctx, "pushed iPhone backup refused", "udid", in.UDID, "stamp", in.Stamp, "err", in.Err)
			rep.PushErrors = append(rep.PushErrors, fmt.Sprintf("backup of %s: %v", in.Stamp, in.Err))
			if err := a.db.RecordIPhoneBackupPushRefused(ctx, in.UDID, in.Stamp, in.Err.Error(), a.now()); err != nil {
				return err
			}
			continue
		}
		if err := cp.added(ctx, in.Snapshot, in.Stats); err != nil {
			return err
		}
	}
	if ingestErr != nil {
		if ctx.Err() != nil {
			return ctx.Err() //nolint:wrapcheck // cancellation
		}
		a.log.ErrorContext(ctx, "pushed iPhone backups could not be taken in", "incoming", incoming, "err", ingestErr)
		rep.PushErrors = append(rep.PushErrors, ingestErr.Error())
	}
	return nil
}

// loadPushKey reads the key pushes must be signed with.
func (a *Agent) loadPushKey() (iphonecopy.PushKey, error) {
	path, err := a.pushKey()
	if err != nil {
		return iphonecopy.PushKey{}, err
	}
	key, err := iphonecopy.LoadPushKey(path)
	if errors.Is(err, iphonecopy.ErrNoPushKey) {
		return iphonecopy.PushKey{}, fmt.Errorf("%w: run \"homecloud iphone push-key\" here and give the key it prints to the computer that pushes", err)
	}
	return key, err
}

// copyIPhoneBackup copies a healthy backup on this computer into the archive
// unless it is there already. A backup that changes meanwhile is copied at a
// later check.
func (a *Agent) copyIPhoneBackup(ctx context.Context, cp *copies, b iphonebackup.Backup) error {
	if has, err := cp.archive.Has(b.UDID, b.CompletedAt); err != nil || has {
		return err
	}
	// A first copy takes the whole backup; it must leave the archive volume
	// above storage.min_free_percent.
	space, err := platform.DiskSpace(a.cfg.Storage.Root)
	if err != nil {
		return err
	}
	after := space
	after.Free = max(space.Free, uint64(b.Bytes)) - uint64(b.Bytes) //nolint:gosec // a backup's size is never negative
	if after.FreePercent() < a.cfg.Storage.MinFreePercent {
		return fmt.Errorf("copying %d MB would leave less than %.0f%% of the archive volume free", b.Bytes>>20, a.cfg.Storage.MinFreePercent)
	}
	base, err := cp.base(b.UDID)
	if err != nil {
		return err
	}
	snap, st, err := cp.archive.CopyLocal(ctx, b, base)
	if errors.Is(err, iphonecopy.ErrChanged) {
		a.log.InfoContext(ctx, "iPhone backup changed while it was copied; copied at the next check", "dir", b.Dir)
		return nil
	}
	if err != nil {
		return err
	}
	return cp.added(ctx, snap, st)
}

// judgeIPhoneBackup reads a backup folder on this computer in full when that
// is due, judges it, and saves what the ledger remembers of it.
func (a *Agent) judgeIPhoneBackup(ctx context.Context, b iphonebackup.Backup, rec ledger.IPhoneBackupRecord) (IPhoneBackupDevice, error) {
	now := a.now()
	if b.Err == nil && !b.InProgress &&
		(rec.LastFullReadAt == nil || now.Sub(*rec.LastFullReadAt) >= a.cfg.IPhoneBackup.ReadInterval.Std()) {
		unreadable, err := iphonebackup.ReadAll(ctx, b.Dir)
		if err != nil {
			return IPhoneBackupDevice{}, err
		}
		rec.LastFullReadAt, rec.UnreadableFiles = &now, len(unreadable)
	}
	return a.judgeAndSave(ctx, b, rec, b.Dir)
}

// judgeIPhoneCopy judges a device whose backups arrive from another computer
// by its newest copy; damaged files of that copy count as unreadable. The
// ledger remembers the device by its folder of copies.
func (a *Agent) judgeIPhoneCopy(ctx context.Context, cp *copies, udid string, newest ledger.IPhoneBackupCopy,
	records map[string]ledger.IPhoneBackupRecord,
) (IPhoneBackupDevice, error) {
	folder := cp.archive.Path(udid)
	b := iphonebackup.Inspect(ctx, cp.archive.Path(udid+"/"+newest.Stamp), udid)
	rec := records[folder]
	rec.LastFullReadAt, rec.UnreadableFiles = newest.VerifiedAt, int(newest.DamagedFiles)
	d, err := a.judgeAndSave(ctx, b, rec, folder)
	d.FromCopy = true
	return d, err
}

// judgeAndSave judges b and saves what the ledger remembers of folder.
func (a *Agent) judgeAndSave(ctx context.Context, b iphonebackup.Backup, rec ledger.IPhoneBackupRecord, folder string) (IPhoneBackupDevice, error) {
	now := a.now()
	prev := iphonebackup.Record{Baseline: rec.BaselineBytes, PendingShrink: rec.PendingShrinkBytes}
	if rec.JudgedCompletedAt != nil {
		prev.Judged = *rec.JudgedCompletedAt
	}
	v, next := iphonebackup.Judge(b, rec.UnreadableFiles, prev, iphonebackup.Policy{
		MaxAge:           a.cfg.IPhoneBackup.MaxAge.Std(),
		StalledAfter:     a.cfg.IPhoneBackup.StalledAfter.Std(),
		MaxShrinkPercent: a.cfg.IPhoneBackup.MaxShrinkPercent,
	}, now)

	rec.Folder, rec.UDID, rec.DeviceName, rec.State = folder, b.UDID, b.DeviceName, string(v.State)
	rec.BaselineBytes, rec.PendingShrinkBytes = next.Baseline, next.PendingShrink
	if !next.Judged.IsZero() {
		rec.JudgedCompletedAt = &next.Judged
	}
	if err := a.db.SaveIPhoneBackup(ctx, rec, v.Reasons, now); err != nil {
		return IPhoneBackupDevice{}, err
	}
	d := IPhoneBackupDevice{
		UDID: b.UDID, Name: b.DeviceName, ProductVersion: b.ProductVersion, Dir: b.Dir,
		Encrypted: b.Encrypted, Files: b.Files, Bytes: b.Bytes,
		State: v.State, Reasons: v.Reasons, LastFullReadAt: rec.LastFullReadAt,
	}
	if !b.CompletedAt.IsZero() {
		d.CompletedAt = &b.CompletedAt
	}
	return d, nil
}

// alertIPhoneBackup alerts once per notify.repeat_every per backup folder and
// problem. The message names neither the device nor its identifiers: ntfy
// topics are public.
func (a *Agent) alertIPhoneBackup(ctx context.Context, folder string, s iphonebackup.State) {
	body := map[iphonebackup.State]string{
		iphonebackup.StateInvalid:     "An iPhone backup folder is not a readable backup.",
		iphonebackup.StateUnreadable:  "Some files of an iPhone backup could not be read from disk.",
		iphonebackup.StateUnencrypted: "An iPhone backup is not encrypted. Turn on \"Encrypt local backup\" in Apple's Devices app.",
		iphonebackup.StateIncomplete:  "An iPhone backup did not finish.",
		iphonebackup.StateStale: fmt.Sprintf("The newest iPhone backup is older than %s. Back the iPhone up in Apple's Devices app "+
			"(and, if it backs up to another computer, check that \"homecloud iphone push\" runs there).",
			iphonebackup.Span(a.cfg.IPhoneBackup.MaxAge.Std())),
		iphonebackup.StateShrunk: "An iPhone backup is much smaller than before. Check it in Apple's Devices app.",
	}[s]
	a.alertRepeated(ctx, alertIPhonePrefix+folder+"/"+string(s), notify.Msg{
		Title:    "homecloud: iPhone backup needs attention",
		Body:     body,
		Priority: notify.PriorityHigh,
		Tags:     []string{"iphone"},
	})
}
