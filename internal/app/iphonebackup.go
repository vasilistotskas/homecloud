package app

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"time"

	"github.com/vasilistotskas/homecloud/internal/iphonebackup"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/notify"
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
	Error     string   `json:"error,omitempty"`
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
}

// ParseIPhoneBackupReport decodes the detail recorded for a finished check.
func ParseIPhoneBackupReport(detail string) (IPhoneBackupReport, error) {
	var rep IPhoneBackupReport
	if err := json.Unmarshal([]byte(detail), &rep); err != nil {
		return IPhoneBackupReport{}, fmt.Errorf("decode iphone backup report: %w", err)
	}
	return rep, nil
}

// checkIPhoneBackups inspects every configured backup directory and judges
// each device's current backup against what the ledger remembers of its
// folder; every iphone_backup.read_interval it also reads every file. State
// changes are recorded and problems alerted. The check is healthy when at
// least one device has a backup, every directory could be checked, and no
// backup has a problem.
func (a *Agent) checkIPhoneBackups(ctx context.Context) (any, bool) {
	var rep IPhoneBackupReport
	records, err := a.db.IPhoneBackupRecords(ctx)
	if err != nil {
		rep.Error = err.Error()
		return rep, false
	}
	healthy := true
	for _, dir := range a.cfg.IPhoneBackup.Dirs {
		current, archived, err := iphonebackup.Scan(ctx, dir)
		if ctx.Err() != nil {
			rep.Error = ctx.Err().Error()
			return rep, false
		}
		if err != nil {
			a.log.Error("iphone backup directory could not be checked", "dir", dir, "err", err)
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
			rep.Devices = append(rep.Devices, d)
			if !d.State.Healthy() {
				healthy = false
				a.alertIPhoneBackup(ctx, d.Dir, d.State)
			}
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
		a.alertRepeated(ctx, alertIPhoneNone, notify.Msg{
			Title:    "homecloud: no iPhone backup on this computer",
			Body:     "No local iPhone backup was found. Turn on encrypted local backups in Apple's Devices app and back up once.",
			Priority: notify.PriorityHigh,
			Tags:     []string{"iphone"},
		})
	}
	a.log.Info("iphone backups checked", "devices", len(rep.Devices), "archived", rep.Archived,
		"dir_errors", len(rep.DirErrors), "healthy", healthy)
	return rep, healthy
}

// judgeIPhoneBackup reads the folder in full when that is due, judges it, and
// saves what the ledger remembers of it.
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
	prev := iphonebackup.Record{Baseline: rec.BaselineBytes, PendingShrink: rec.PendingShrinkBytes}
	if rec.JudgedCompletedAt != nil {
		prev.Judged = *rec.JudgedCompletedAt
	}
	v, next := iphonebackup.Judge(b, rec.UnreadableFiles, prev, iphonebackup.Policy{
		MaxAge:           a.cfg.IPhoneBackup.MaxAge.Std(),
		StalledAfter:     a.cfg.IPhoneBackup.StalledAfter.Std(),
		MaxShrinkPercent: a.cfg.IPhoneBackup.MaxShrinkPercent,
	}, now)

	rec.Folder, rec.UDID, rec.DeviceName, rec.State = b.Dir, b.UDID, b.DeviceName, string(v.State)
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
		iphonebackup.StateInvalid:     "A local iPhone backup folder is not a readable backup.",
		iphonebackup.StateUnreadable:  "Some files of a local iPhone backup could not be read from disk.",
		iphonebackup.StateUnencrypted: "A local iPhone backup is not encrypted. Turn on \"Encrypt local backup\" in Apple's Devices app.",
		iphonebackup.StateIncomplete:  "A local iPhone backup did not finish.",
		iphonebackup.StateStale:       fmt.Sprintf("A local iPhone backup is older than %s. Connect the iPhone to this computer or check Apple's Devices app.", iphonebackup.Span(a.cfg.IPhoneBackup.MaxAge.Std())),
		iphonebackup.StateShrunk:      "A local iPhone backup is much smaller than before. Check it in Apple's Devices app.",
	}[s]
	a.alertRepeated(ctx, alertIPhonePrefix+folder+"/"+string(s), notify.Msg{
		Title:    "homecloud: iPhone backup needs attention",
		Body:     body,
		Priority: notify.PriorityHigh,
		Tags:     []string{"iphone"},
	})
}
