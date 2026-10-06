package api

import (
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"path"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv2 "github.com/vasilistotskas/homecloud/gen/homecloud/agent/v2"
	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/health"
	"github.com/vasilistotskas/homecloud/internal/iphonebackup"
	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/ledger"
)

// pageSize validates a requested page size; 0 selects def.
func pageSize(size int32, def int) (int, error) {
	switch {
	case size < 0 || size > maxPageSize:
		return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("page_size must be 0..%d", maxPageSize))
	case size == 0:
		return def, nil
	}
	return int(size), nil
}

// page validates a page size and decodes a page token into a "before id" cursor.
func page(requested int32, token string) (int, int64, error) {
	size, err := pageSize(requested, defaultPageSize)
	if err != nil {
		return 0, 0, err
	}
	if token == "" {
		return size, 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, 0, connect.NewError(connect.CodeInvalidArgument, errors.New("malformed page_token"))
	}
	before, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || before <= 0 {
		return 0, 0, connect.NewError(connect.CodeInvalidArgument, errors.New("malformed page_token"))
	}
	return size, before, nil
}

func encodePageToken(lastID int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(lastID, 10)))
}

func clampInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

func optionalTime(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func optionalTimePtr(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

func phase(p app.Phase) agentv2.Phase {
	switch p {
	case app.PhaseIdle:
		return agentv2.Phase_PHASE_IDLE
	case app.PhaseDownloading:
		return agentv2.Phase_PHASE_DOWNLOADING
	case app.PhaseIngesting:
		return agentv2.Phase_PHASE_INGESTING
	case app.PhaseVerifying:
		return agentv2.Phase_PHASE_VERIFYING
	case app.PhasePreparingPreviews:
		return agentv2.Phase_PHASE_PREPARING_PREVIEWS
	case app.PhaseCheckingArchive:
		return agentv2.Phase_PHASE_CHECKING_ARCHIVE
	case app.PhaseBackingUpLedger:
		return agentv2.Phase_PHASE_BACKING_UP_LEDGER
	case app.PhaseCheckingIPhoneBackups:
		return agentv2.Phase_PHASE_CHECKING_IPHONE_BACKUPS
	case app.PhaseSyncingDrive:
		return agentv2.Phase_PHASE_SYNCING_DRIVE
	default:
		return agentv2.Phase_PHASE_UNSPECIFIED
	}
}

var downloadOutcomes = map[string]agentv2.DownloadOutcome{
	kei.OutcomeOK.String():           agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_OK,
	kei.OutcomePartial.String():      agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_PARTIAL,
	kei.OutcomeAuthRequired.String(): agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_SIGN_IN_REQUIRED,
	kei.OutcomeAuthTerminal.String(): agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_NEEDS_ATTENTION,
	app.KeiOutcomeError:              agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_ERROR,
	app.KeiOutcomeLowSpace:           agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_SKIPPED_LOW_SPACE,
	app.KeiOutcomeArchiveUnavailable: agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_ARCHIVE_UNAVAILABLE,
	app.KeiOutcomeInterrupted:        agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_INTERRUPTED,
}

func runToProto(r ledger.Run) (*agentv2.Run, error) {
	out := &agentv2.Run{
		Id:         r.ID,
		Kind:       r.Kind,
		StartedAt:  timestamppb.New(r.StartedAt),
		FinishedAt: optionalTimePtr(r.FinishedAt),
	}
	switch {
	case r.OK == nil:
		out.Result = agentv2.RunResult_RUN_RESULT_RUNNING
	case *r.OK:
		out.Result = agentv2.RunResult_RUN_RESULT_OK
	default:
		out.Result = agentv2.RunResult_RUN_RESULT_FAILED
	}
	if r.FinishedAt == nil {
		return out, nil
	}
	var err error
	switch r.Kind {
	case app.CycleRunKind:
		out.Cycle, err = cycleToProto(r.Detail)
	case app.ScrubRunKind:
		out.Scrub, err = scrubToProto(r.Detail)
	case app.LedgerBackupRunKind:
		out.LedgerBackup, err = ledgerBackupToProto(r.Detail)
	case app.IPhoneBackupRunKind:
		out.IphoneBackup, err = iphoneBackupToProto(r.Detail)
	case app.DriveSyncRunKind:
		out.DriveSync, err = driveSyncToProto(r.Detail)
	default:
		err = fmt.Errorf("unknown run kind %q", r.Kind)
	}
	if err != nil {
		return nil, fmt.Errorf("run %d: %w", r.ID, err)
	}
	return out, nil
}

// runKinds maps API run kinds to the ledger's run kind names.
var runKinds = map[agentv2.RunKind]string{
	agentv2.RunKind_RUN_KIND_CYCLE:         app.CycleRunKind,
	agentv2.RunKind_RUN_KIND_SCRUB:         app.ScrubRunKind,
	agentv2.RunKind_RUN_KIND_LEDGER_BACKUP: app.LedgerBackupRunKind,
	agentv2.RunKind_RUN_KIND_IPHONE_BACKUP: app.IPhoneBackupRunKind,
	agentv2.RunKind_RUN_KIND_DRIVE_SYNC:    app.DriveSyncRunKind,
}

func scrubToProto(detail string) (*agentv2.ScrubReport, error) {
	rep, err := app.ParseScrubReport(detail)
	if err != nil {
		return nil, err
	}
	return &agentv2.ScrubReport{
		Eligible:   int64(rep.Eligible),
		Checked:    int64(rep.Checked),
		Passed:     int64(rep.Passed),
		Mismatched: int64(rep.Mismatched),
		Errored:    int64(rep.Errored),
		Error:      rep.Error,
		Drive: &agentv2.DriveScrub{
			Eligible: int64(rep.Drive.Eligible), Checked: int64(rep.Drive.Checked), Passed: int64(rep.Drive.Passed),
			Damaged: int64(rep.Drive.Damaged), Errored: int64(rep.Drive.Errored),
		},
	}, nil
}

func ledgerBackupToProto(detail string) (*agentv2.LedgerBackupReport, error) {
	rep, err := app.ParseLedgerBackupReport(detail)
	if err != nil {
		return nil, err
	}
	return &agentv2.LedgerBackupReport{
		Path:      rep.File,
		SizeBytes: rep.SizeBytes,
		Pruned:    int64(rep.Pruned),
		Error:     rep.Error,
	}, nil
}

var iphoneBackupStates = map[iphonebackup.State]agentv2.IPhoneBackupState{
	iphonebackup.StateOK:          agentv2.IPhoneBackupState_I_PHONE_BACKUP_STATE_OK,
	iphonebackup.StateInProgress:  agentv2.IPhoneBackupState_I_PHONE_BACKUP_STATE_IN_PROGRESS,
	iphonebackup.StateShrunk:      agentv2.IPhoneBackupState_I_PHONE_BACKUP_STATE_SHRUNK,
	iphonebackup.StateStale:       agentv2.IPhoneBackupState_I_PHONE_BACKUP_STATE_STALE,
	iphonebackup.StateIncomplete:  agentv2.IPhoneBackupState_I_PHONE_BACKUP_STATE_INCOMPLETE,
	iphonebackup.StateUnencrypted: agentv2.IPhoneBackupState_I_PHONE_BACKUP_STATE_UNENCRYPTED,
	iphonebackup.StateUnreadable:  agentv2.IPhoneBackupState_I_PHONE_BACKUP_STATE_UNREADABLE,
	iphonebackup.StateInvalid:     agentv2.IPhoneBackupState_I_PHONE_BACKUP_STATE_INVALID,
}

// iphoneBackupState converts a recorded state; "" (no earlier state) is
// unspecified.
func iphoneBackupState(s iphonebackup.State) (agentv2.IPhoneBackupState, error) {
	if s == "" {
		return agentv2.IPhoneBackupState_I_PHONE_BACKUP_STATE_UNSPECIFIED, nil
	}
	return lookup(iphoneBackupStates, s, "iphone backup state")
}

func iphoneBackupToProto(detail string) (*agentv2.IPhoneBackupReport, error) {
	rep, err := app.ParseIPhoneBackupReport(detail)
	if err != nil {
		return nil, err
	}
	out := &agentv2.IPhoneBackupReport{Archived: int64(rep.Archived), Error: rep.Error, DirErrors: rep.DirErrors}
	for _, d := range rep.Devices {
		state, err := lookup(iphoneBackupStates, d.State, "iphone backup state")
		if err != nil {
			return nil, err
		}
		out.Devices = append(out.Devices, &agentv2.IPhoneBackup{
			Udid: d.UDID, Name: d.Name, ProductVersion: d.ProductVersion, Dir: d.Dir,
			Encrypted: d.Encrypted, CompletedAt: optionalTimePtr(d.CompletedAt),
			Files: d.Files, SizeBytes: d.Bytes, State: state, Reasons: d.Reasons,
			LastFullReadAt: optionalTimePtr(d.LastFullReadAt),
		})
	}
	return out, nil
}

var driveSyncOutcomes = map[string]agentv2.DriveSyncOutcome{
	app.DriveOutcomeOK:                 agentv2.DriveSyncOutcome_DRIVE_SYNC_OUTCOME_OK,
	app.DriveOutcomePartial:            agentv2.DriveSyncOutcome_DRIVE_SYNC_OUTCOME_PARTIAL,
	app.DriveOutcomeSignInRequired:     agentv2.DriveSyncOutcome_DRIVE_SYNC_OUTCOME_SIGN_IN_REQUIRED,
	app.DriveOutcomeSkippedLowSpace:    agentv2.DriveSyncOutcome_DRIVE_SYNC_OUTCOME_SKIPPED_LOW_SPACE,
	app.DriveOutcomeArchiveUnavailable: agentv2.DriveSyncOutcome_DRIVE_SYNC_OUTCOME_ARCHIVE_UNAVAILABLE,
	app.DriveOutcomeError:              agentv2.DriveSyncOutcome_DRIVE_SYNC_OUTCOME_ERROR,
}

func driveSyncToProto(detail string) (*agentv2.DriveSyncReport, error) {
	rep, err := app.ParseDriveSyncReport(detail)
	if err != nil {
		return nil, err
	}
	outcome, err := lookup(driveSyncOutcomes, rep.Outcome, "drive sync outcome")
	if err != nil {
		return nil, err
	}
	return &agentv2.DriveSyncReport{
		Outcome: outcome, Listed: int64(rep.Listed), Archived: int64(rep.Archived), Changed: int64(rep.Changed),
		Repaired: int64(rep.Repaired), Moved: int64(rep.Moved), Deleted: int64(rep.Deleted), Restored: int64(rep.Restored),
		DownloadedBytes: rep.Bytes, Failed: int64(rep.Failed), Failures: rep.Failures,
		ManifestRows: int64(rep.ManifestRows), Error: rep.Error, UnlistedApps: int64(rep.UnlistedApps),
	}, nil
}

func cycleToProto(detail string) (*agentv2.CycleReport, error) {
	rep, err := app.ParseCycleReport(detail)
	if err != nil {
		return nil, err
	}
	dl, ok := downloadOutcomes[rep.KeiOutcome]
	if !ok {
		return nil, fmt.Errorf("unknown download outcome %q", rep.KeiOutcome)
	}
	return &agentv2.CycleReport{
		Download:          dl,
		Assets:            int64(rep.Ingested.Assets),
		Renditions:        int64(rep.Ingested.Renditions),
		NewRenditions:     int64(rep.Ingested.NewRenditions),
		ChangedRenditions: int64(rep.Ingested.ChangedRenditions),
		MovedRenditions:   int64(rep.Ingested.MovedRenditions),
		Checked:           int64(rep.Verified.Checked),
		Passed:            int64(rep.Verified.Passed),
		Mismatched:        int64(rep.Verified.Mismatched),
		Errored:           int64(rep.Verified.Errored),
		Error:             rep.Error,
	}, nil
}

func appleState(d agentv2.DownloadOutcome) agentv2.AppleAccountState {
	switch d {
	case agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_OK, agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_PARTIAL:
		return agentv2.AppleAccountState_APPLE_ACCOUNT_STATE_SIGNED_IN
	case agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_SIGN_IN_REQUIRED:
		return agentv2.AppleAccountState_APPLE_ACCOUNT_STATE_SIGN_IN_REQUIRED
	case agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_NEEDS_ATTENTION:
		return agentv2.AppleAccountState_APPLE_ACCOUNT_STATE_NEEDS_ATTENTION
	case agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_UNSPECIFIED, agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_ERROR,
		agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_SKIPPED_LOW_SPACE, agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_ARCHIVE_UNAVAILABLE,
		agentv2.DownloadOutcome_DOWNLOAD_OUTCOME_INTERRUPTED:
		return agentv2.AppleAccountState_APPLE_ACCOUNT_STATE_UNSPECIFIED
	default:
		return agentv2.AppleAccountState_APPLE_ACCOUNT_STATE_UNSPECIFIED
	}
}

// eventKindNames maps API event kinds to the ledger's event kind names.
var eventKindNames = map[agentv2.EventKind]string{
	agentv2.EventKind_EVENT_KIND_RENDITION_CHANGED:     ledger.EventRenditionChanged,
	agentv2.EventKind_EVENT_KIND_STATE_CHANGED:         ledger.EventStateChanged,
	agentv2.EventKind_EVENT_KIND_QUARANTINED:           ledger.EventQuarantined,
	agentv2.EventKind_EVENT_KIND_QUARANTINE_RELEASED:   ledger.EventQuarantineReleased,
	agentv2.EventKind_EVENT_KIND_DRIVE_HEALTH_CHANGED:  ledger.EventDriveHealthChanged,
	agentv2.EventKind_EVENT_KIND_RENDITION_MOVED:       ledger.EventRenditionMoved,
	agentv2.EventKind_EVENT_KIND_IPHONE_BACKUP_CHANGED: ledger.EventIPhoneBackupChanged,
	agentv2.EventKind_EVENT_KIND_DRIVE_FILE_ARCHIVED:   ledger.EventDriveFileArchived,
	agentv2.EventKind_EVENT_KIND_DRIVE_FILE_CHANGED:    ledger.EventDriveFileChanged,
	agentv2.EventKind_EVENT_KIND_DRIVE_FILE_MOVED:      ledger.EventDriveFileMoved,
	agentv2.EventKind_EVENT_KIND_DRIVE_FILE_DELETED:    ledger.EventDriveFileDeleted,
	agentv2.EventKind_EVENT_KIND_DRIVE_FILE_RESTORED:   ledger.EventDriveFileRestored,
	agentv2.EventKind_EVENT_KIND_DRIVE_FILE_DAMAGED:    ledger.EventDriveFileDamaged,
	agentv2.EventKind_EVENT_KIND_DRIVE_FILE_REPAIRED:   ledger.EventDriveFileRepaired,
}

// driveFileChanges maps the ledger's iCloud Drive event kinds to the API's.
var driveFileChanges = map[string]agentv2.DriveFileChange{
	ledger.EventDriveFileArchived: agentv2.DriveFileChange_DRIVE_FILE_CHANGE_ARCHIVED,
	ledger.EventDriveFileChanged:  agentv2.DriveFileChange_DRIVE_FILE_CHANGE_CHANGED,
	ledger.EventDriveFileMoved:    agentv2.DriveFileChange_DRIVE_FILE_CHANGE_MOVED,
	ledger.EventDriveFileDeleted:  agentv2.DriveFileChange_DRIVE_FILE_CHANGE_DELETED,
	ledger.EventDriveFileRestored: agentv2.DriveFileChange_DRIVE_FILE_CHANGE_RESTORED,
	ledger.EventDriveFileDamaged:  agentv2.DriveFileChange_DRIVE_FILE_CHANGE_DAMAGED,
	ledger.EventDriveFileRepaired: agentv2.DriveFileChange_DRIVE_FILE_CHANGE_REPAIRED,
}

var assetStates = map[ledger.AssetState]agentv2.AssetState{
	ledger.StateDiscovered:  agentv2.AssetState_ASSET_STATE_DISCOVERED,
	ledger.StateDownloaded:  agentv2.AssetState_ASSET_STATE_DOWNLOADED,
	ledger.StateVerified:    agentv2.AssetState_ASSET_STATE_VERIFIED,
	ledger.StateQuarantined: agentv2.AssetState_ASSET_STATE_QUARANTINED,
}

var tiers = map[ledger.Tier]agentv2.Tier{
	ledger.TierContent:   agentv2.Tier_TIER_CONTENT,
	ledger.TierStructure: agentv2.Tier_TIER_STRUCTURE,
	ledger.TierProbe:     agentv2.Tier_TIER_PROBE,
}

var mediaTypes = map[string]agentv2.MediaType{
	"photo":            agentv2.MediaType_MEDIA_TYPE_PHOTO,
	"video":            agentv2.MediaType_MEDIA_TYPE_VIDEO,
	"live_photo_image": agentv2.MediaType_MEDIA_TYPE_LIVE_PHOTO_IMAGE,
	"live_photo_video": agentv2.MediaType_MEDIA_TYPE_LIVE_PHOTO_VIDEO,
}

func lookup[K comparable, V any](m map[K]V, k K, what string) (V, error) {
	v, ok := m[k]
	if !ok {
		var zero V
		return zero, fmt.Errorf("unknown %s %v", what, k)
	}
	return v, nil
}

func eventToProto(e ledger.Event) (*agentv2.Event, error) {
	out := &agentv2.Event{
		Id:    e.ID,
		At:    timestamppb.New(e.At),
		Asset: &agentv2.AssetRef{Library: e.Library, AssetId: e.Asset, Filename: e.Filename},
	}
	var err error
	switch e.Kind {
	case ledger.EventRenditionChanged:
		var d struct {
			Version string `json:"version"`
			Size    int64  `json:"size"`
			SHA256  string `json:"sha256"`
		}
		if err = json.Unmarshal([]byte(e.Detail), &d); err == nil {
			out.Detail = &agentv2.Event_RenditionChanged{RenditionChanged: &agentv2.RenditionChanged{
				Version: d.Version, SizeBytes: d.Size, Sha256: d.SHA256,
			}}
		}
	case ledger.EventStateChanged:
		var d struct {
			From ledger.AssetState `json:"from"`
			To   ledger.AssetState `json:"to"`
		}
		if err = json.Unmarshal([]byte(e.Detail), &d); err == nil {
			sc := &agentv2.StateChanged{}
			if sc.From, err = lookup(assetStates, d.From, "asset state"); err == nil {
				sc.To, err = lookup(assetStates, d.To, "asset state")
			}
			out.Detail = &agentv2.Event_StateChanged{StateChanged: sc}
		}
	case ledger.EventQuarantined:
		var d struct {
			Version string      `json:"version"`
			Tier    ledger.Tier `json:"tier"`
			Reason  string      `json:"reason"`
		}
		if err = json.Unmarshal([]byte(e.Detail), &d); err == nil {
			q := &agentv2.Quarantined{Version: d.Version, Reason: d.Reason}
			q.Tier, err = lookup(tiers, d.Tier, "tier")
			out.Detail = &agentv2.Event_Quarantined{Quarantined: q}
		}
	case ledger.EventQuarantineReleased:
		var d struct {
			Reason string `json:"reason"`
		}
		if err = json.Unmarshal([]byte(e.Detail), &d); err == nil {
			out.Detail = &agentv2.Event_QuarantineReleased{QuarantineReleased: &agentv2.QuarantineReleased{Reason: d.Reason}}
		}
	case ledger.EventRenditionMoved:
		var d struct {
			Version string `json:"version"`
			From    string `json:"from"`
			To      string `json:"to"`
		}
		if err = json.Unmarshal([]byte(e.Detail), &d); err == nil {
			out.Detail = &agentv2.Event_RenditionMoved{RenditionMoved: &agentv2.RenditionMoved{Version: d.Version, From: d.From, To: d.To}}
		}
	case ledger.EventDriveHealthChanged:
		var d struct {
			From    health.State `json:"from"`
			To      health.State `json:"to"`
			Reasons []string     `json:"reasons"`
		}
		if err = json.Unmarshal([]byte(e.Detail), &d); err == nil {
			c := &agentv2.DriveHealthChanged{Reasons: d.Reasons}
			if c.From, err = driveState(d.From); err == nil {
				c.To, err = driveState(d.To)
			}
			out.Detail = &agentv2.Event_DriveHealthChanged{DriveHealthChanged: c}
		}
	case ledger.EventIPhoneBackupChanged:
		var d struct {
			Folder     string             `json:"subject"`
			UDID       string             `json:"udid"`
			DeviceName string             `json:"device_name"`
			From       iphonebackup.State `json:"from"`
			To         iphonebackup.State `json:"to"`
			Problems   []string           `json:"problems"`
		}
		if err = json.Unmarshal([]byte(e.Detail), &d); err == nil {
			c := &agentv2.IPhoneBackupChanged{Udid: d.UDID, Dir: d.Folder, DeviceName: d.DeviceName, Reasons: d.Problems}
			if c.From, err = iphoneBackupState(d.From); err == nil {
				c.To, err = iphoneBackupState(d.To)
			}
			out.Detail = &agentv2.Event_IphoneBackupChanged{IphoneBackupChanged: c}
		}
	case ledger.EventDriveFileArchived, ledger.EventDriveFileChanged, ledger.EventDriveFileMoved,
		ledger.EventDriveFileDeleted, ledger.EventDriveFileRestored, ledger.EventDriveFileDamaged,
		ledger.EventDriveFileRepaired:
		var d struct {
			Path   string `json:"path"`
			From   string `json:"from"`
			To     string `json:"to"`
			KeptAs string `json:"kept_as"`
			Reason string `json:"reason"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		}
		if err = json.Unmarshal([]byte(e.Detail), &d); err == nil {
			ev := &agentv2.DriveFileEvent{
				Change: driveFileChanges[e.Kind], Path: d.Path, KeptAs: d.KeptAs, Reason: d.Reason,
				SizeBytes: d.Size, Sha256: d.SHA256,
			}
			if e.Kind == ledger.EventDriveFileMoved {
				ev.Path, ev.FromPath = d.To, d.From
			}
			out.Detail = &agentv2.Event_DriveFile{DriveFile: ev}
			// A Drive event names a file, not a Photos asset.
			out.Asset = &agentv2.AssetRef{Library: e.Library, AssetId: e.Asset, Filename: path.Base(ev.Path)}
		}
	default:
		err = fmt.Errorf("unknown event kind %q", e.Kind)
	}
	if err != nil {
		return nil, fmt.Errorf("event %d: %w", e.ID, err)
	}
	return out, nil
}

func failureToProto(version string, f ledger.Failure) *agentv2.Failure {
	return &agentv2.Failure{Version: version, Tier: tiers[f.Tier], Reason: f.Reason}
}

func assetToProto(d ledger.AssetDetail) (*agentv2.Asset, error) {
	state, err := lookup(assetStates, d.State, "asset state")
	if err != nil {
		return nil, err
	}
	out := &agentv2.Asset{
		Ref:            &agentv2.AssetRef{Library: d.Library, AssetId: d.AssetID},
		State:          state,
		StateChangedAt: timestamppb.New(d.StateChangedAt),
		CreatedAt:      optionalTimePtr(d.CreatedAt),
		AddedAt:        optionalTimePtr(d.AddedAt),
		LastSeenAt:     optionalTimePtr(d.LastSeenAt),
	}
	for _, r := range d.Renditions {
		if r.Version == ledger.VersionOriginal {
			out.Ref.Filename = r.Filename
		}
		mt, err := lookup(mediaTypes, r.MediaType, "media type")
		if err != nil {
			return nil, err
		}
		pr := &agentv2.Rendition{
			Version:       r.Version,
			Filename:      r.Filename,
			LocalPath:     r.LocalPath,
			MediaType:     mt,
			SizeBytes:     r.SizeBytes,
			Sha256:        r.SHA256,
			RequiredTiers: []agentv2.Tier{agentv2.Tier_TIER_CONTENT},
		}
		if r.RequiresStructure {
			pr.RequiredTiers = append(pr.RequiredTiers, agentv2.Tier_TIER_STRUCTURE)
		}
		if r.RequiresProbe {
			pr.RequiredTiers = append(pr.RequiredTiers, agentv2.Tier_TIER_PROBE)
		}
		for _, t := range []ledger.Tier{ledger.TierContent, ledger.TierStructure, ledger.TierProbe} {
			if at, ok := r.Passed[t]; ok {
				pr.Passes = append(pr.Passes, &agentv2.TierPass{Tier: tiers[t], At: timestamppb.New(at)})
			}
		}
		if r.Failure != nil {
			pr.Failure = failureToProto(r.Version, *r.Failure)
		}
		out.Renditions = append(out.Renditions, pr)
	}
	return out, nil
}

// driveState maps a recorded health state; "" means none was recorded before.
func driveState(s health.State) (agentv2.DriveHealthState, error) {
	switch s {
	case "":
		return agentv2.DriveHealthState_DRIVE_HEALTH_STATE_UNSPECIFIED, nil
	case health.StateOK:
		return agentv2.DriveHealthState_DRIVE_HEALTH_STATE_OK, nil
	case health.StateWarning:
		return agentv2.DriveHealthState_DRIVE_HEALTH_STATE_WARNING, nil
	case health.StateFailing:
		return agentv2.DriveHealthState_DRIVE_HEALTH_STATE_FAILING, nil
	case health.StateUnavailable:
		return agentv2.DriveHealthState_DRIVE_HEALTH_STATE_UNAVAILABLE, nil
	default:
		return agentv2.DriveHealthState_DRIVE_HEALTH_STATE_UNSPECIFIED, fmt.Errorf("unknown drive health state %q", s)
	}
}

func optionalInt32(v *int) *int32 {
	if v == nil {
		return nil
	}
	n := int32(*v) //nolint:gosec // SMART percentages and temperatures are small
	return &n
}

// driveToProto converts the latest drive check; nil before the first check.
func driveToProto(r health.Report) (*agentv2.DriveHealth, error) {
	if r.State == "" {
		return nil, nil //nolint:nilnil // no check has run yet
	}
	state, err := driveState(r.State)
	if err != nil {
		return nil, err
	}
	out := &agentv2.DriveHealth{State: state, CheckedAt: timestamppb.New(r.CheckedAt), Reasons: r.Reasons}
	if d := r.Drive; d != nil {
		out.Model, out.Protocol, out.Warnings = d.Model, d.Protocol, d.Warnings
		out.TemperatureCelsius = optionalInt32(d.TemperatureCelsius)
		out.PercentUsed = optionalInt32(d.PercentUsed)
		out.AvailableSparePercent = optionalInt32(d.AvailableSparePercent)
		out.MediaErrors, out.PowerOnHours = d.MediaErrors, d.PowerOnHours
	}
	return out, nil
}

var assetKinds = map[ledger.AssetKind]agentv2.AssetKind{
	ledger.KindPhoto:     agentv2.AssetKind_ASSET_KIND_PHOTO,
	ledger.KindLivePhoto: agentv2.AssetKind_ASSET_KIND_LIVE_PHOTO,
	ledger.KindVideo:     agentv2.AssetKind_ASSET_KIND_VIDEO,
}

// assetKindFilter maps a requested kind; unspecified means every kind.
func assetKindFilter(k agentv2.AssetKind) (ledger.AssetKind, error) {
	if k == agentv2.AssetKind_ASSET_KIND_UNSPECIFIED {
		return "", nil
	}
	for lk, pk := range assetKinds {
		if pk == k {
			return lk, nil
		}
	}
	return "", fmt.Errorf("unsupported asset kind %v", k)
}
