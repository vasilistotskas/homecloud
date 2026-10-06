package app

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"time"

	"github.com/vasilistotskas/homecloud/internal/iphonebackup"
	"github.com/vasilistotskas/homecloud/internal/notify"
)

// IPhoneBackupRunKind is the ledger run kind of an iPhone backup check.
const IPhoneBackupRunKind = "iphone_backup"

// iphoneBaselineRuns bounds how many past checks are searched for a device's
// last healthy backup size.
const iphoneBaselineRuns = 50

// IPhoneBackupReport is recorded as the detail of each iPhone backup check.
type IPhoneBackupReport struct {
	Devices []IPhoneBackupDevice `json:"devices"`
	// Archived counts frozen copies the user archived in Apple's tool.
	Archived int    `json:"archived"`
	Error    string `json:"error,omitempty"`
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
}

// ParseIPhoneBackupReport decodes the detail recorded for a finished check.
func ParseIPhoneBackupReport(detail string) (IPhoneBackupReport, error) {
	var rep IPhoneBackupReport
	if err := json.Unmarshal([]byte(detail), &rep); err != nil {
		return IPhoneBackupReport{}, fmt.Errorf("decode iphone backup report: %w", err)
	}
	return rep, nil
}

// checkIPhoneBackups inspects every configured backup directory, judges each
// device's current backup, records state changes and alerts on problems. It is
// healthy when at least one device has a backup and none has a problem.
func (a *Agent) checkIPhoneBackups(ctx context.Context) (any, bool) {
	var rep IPhoneBackupReport
	baseline, err := a.iphoneBaselines(ctx)
	if err != nil {
		rep.Error = err.Error()
		return rep, false
	}
	policy := iphonebackup.Policy{
		MaxAge:           a.cfg.IPhoneBackup.MaxAge.Std(),
		MaxShrinkPercent: a.cfg.IPhoneBackup.MaxShrinkPercent,
	}
	healthy := true
	for _, dir := range a.cfg.IPhoneBackup.Dirs {
		current, archived, err := iphonebackup.Scan(ctx, dir)
		if err != nil {
			a.log.Error("iphone backup check failed", "dir", dir, "err", err)
			rep.Error = err.Error()
			return rep, false
		}
		rep.Archived += len(archived)
		for _, b := range current {
			v := iphonebackup.Evaluate(b, baseline[b.UDID], policy, a.now())
			d := IPhoneBackupDevice{
				UDID: b.UDID, Name: b.DeviceName, ProductVersion: b.ProductVersion, Dir: b.Dir,
				Encrypted: b.Encrypted, Files: b.Files, Bytes: b.Bytes,
				State: v.State, Reasons: v.Reasons,
			}
			if !b.CompletedAt.IsZero() {
				d.CompletedAt = &b.CompletedAt
			}
			rep.Devices = append(rep.Devices, d)
			if _, _, err := a.db.RecordIPhoneBackupState(ctx, d.UDID, d.Name, string(d.State), d.Reasons, a.now()); err != nil {
				rep.Error = err.Error()
				return rep, false
			}
			if v.State != iphonebackup.StateOK && v.State != iphonebackup.StateInProgress {
				healthy = false
				a.alertIPhoneBackup(ctx, d.UDID, v)
			}
		}
	}
	if len(rep.Devices) == 0 {
		healthy = false
		a.alertRepeated(ctx, alertIPhoneNone, notify.Msg{
			Title:    "homecloud: no iPhone backup on this computer",
			Body:     "No local iPhone backup was found. Turn on encrypted local backups in Apple's Devices app and back up once.",
			Priority: notify.PriorityHigh,
			Tags:     []string{"iphone"},
		})
	}
	a.log.Info("iphone backups checked", "devices", len(rep.Devices), "archived", rep.Archived, "healthy", healthy)
	return rep, healthy
}

// alertIPhoneBackup alerts once a day per device and problem. The message
// names neither the device nor its identifiers: ntfy topics are public.
func (a *Agent) alertIPhoneBackup(ctx context.Context, udid string, v iphonebackup.Verdict) {
	body := map[iphonebackup.State]string{
		iphonebackup.StateInvalid:     "A local iPhone backup folder is not a readable backup.",
		iphonebackup.StateUnreadable:  "Some files of a local iPhone backup could not be read from disk.",
		iphonebackup.StateUnencrypted: "A local iPhone backup is not encrypted. Turn on \"Encrypt local backup\" in Apple's Devices app.",
		iphonebackup.StateIncomplete:  "The last local iPhone backup did not finish.",
		iphonebackup.StateStale:       fmt.Sprintf("A local iPhone backup is older than %s. Connect the iPhone to this computer or check Apple's Devices app.", iphonebackup.Span(a.cfg.IPhoneBackup.MaxAge.Std())),
		iphonebackup.StateShrunk:      "A local iPhone backup is much smaller than before. Check it in Apple's Devices app.",
	}[v.State]
	a.alertRepeated(ctx, alertIPhonePrefix+udid+"/"+string(v.State), notify.Msg{
		Title:    "homecloud: iPhone backup needs attention",
		Body:     body,
		Priority: notify.PriorityHigh,
		Tags:     []string{"iphone"},
	})
}

// iphoneBaselines returns, per UDID, the size of the device's most recent
// backup that was judged OK.
func (a *Agent) iphoneBaselines(ctx context.Context) (map[string]int64, error) {
	runs, err := a.db.Runs(ctx, IPhoneBackupRunKind, 0, iphoneBaselineRuns)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, r := range runs { // newest first
		if r.FinishedAt == nil {
			continue
		}
		rep, err := ParseIPhoneBackupReport(r.Detail)
		if err != nil {
			return nil, fmt.Errorf("run %d: %w", r.ID, err)
		}
		for _, d := range rep.Devices {
			if _, seen := out[d.UDID]; !seen && d.State == iphonebackup.StateOK {
				out[d.UDID] = d.Bytes
			}
		}
	}
	return out, nil
}
