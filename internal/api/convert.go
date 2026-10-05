package api

import (
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/vasilistotskas/homecloud/gen/homecloud/agent/v1"
	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/health"
	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/ledger"
)

// page validates a page size and decodes a page token into a "before id" cursor.
func page(size int32, token string) (int, int64, error) {
	switch {
	case size < 0 || size > maxPageSize:
		return 0, 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("page_size must be 0..%d", maxPageSize))
	case size == 0:
		size = defaultPageSize
	}
	if token == "" {
		return int(size), 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, 0, connect.NewError(connect.CodeInvalidArgument, errors.New("malformed page_token"))
	}
	before, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || before <= 0 {
		return 0, 0, connect.NewError(connect.CodeInvalidArgument, errors.New("malformed page_token"))
	}
	return int(size), before, nil
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

func phase(p app.Phase) agentv1.Phase {
	switch p {
	case app.PhaseIdle:
		return agentv1.Phase_PHASE_IDLE
	case app.PhaseDownloading:
		return agentv1.Phase_PHASE_DOWNLOADING
	case app.PhaseIngesting:
		return agentv1.Phase_PHASE_INGESTING
	case app.PhaseVerifying:
		return agentv1.Phase_PHASE_VERIFYING
	case app.PhasePreparingPreviews:
		return agentv1.Phase_PHASE_PREPARING_PREVIEWS
	default:
		return agentv1.Phase_PHASE_UNSPECIFIED
	}
}

var downloadOutcomes = map[string]agentv1.DownloadOutcome{
	kei.OutcomeOK.String():           agentv1.DownloadOutcome_DOWNLOAD_OUTCOME_OK,
	kei.OutcomePartial.String():      agentv1.DownloadOutcome_DOWNLOAD_OUTCOME_PARTIAL,
	kei.OutcomeAuthRequired.String(): agentv1.DownloadOutcome_DOWNLOAD_OUTCOME_SIGN_IN_REQUIRED,
	kei.OutcomeAuthTerminal.String(): agentv1.DownloadOutcome_DOWNLOAD_OUTCOME_NEEDS_ATTENTION,
	app.KeiOutcomeError:              agentv1.DownloadOutcome_DOWNLOAD_OUTCOME_ERROR,
}

func runToProto(r ledger.Run) (*agentv1.Run, error) {
	out := &agentv1.Run{
		Id:         r.ID,
		Kind:       r.Kind,
		StartedAt:  timestamppb.New(r.StartedAt),
		FinishedAt: optionalTimePtr(r.FinishedAt),
	}
	switch {
	case r.OK == nil:
		out.Result = agentv1.RunResult_RUN_RESULT_RUNNING
	case *r.OK:
		out.Result = agentv1.RunResult_RUN_RESULT_OK
	default:
		out.Result = agentv1.RunResult_RUN_RESULT_FAILED
	}
	if r.Kind != app.CycleRunKind || r.FinishedAt == nil {
		return out, nil
	}
	rep, err := app.ParseCycleReport(r.Detail)
	if err != nil {
		return nil, fmt.Errorf("run %d: %w", r.ID, err)
	}
	dl, ok := downloadOutcomes[rep.KeiOutcome]
	if !ok {
		return nil, fmt.Errorf("run %d: unknown download outcome %q", r.ID, rep.KeiOutcome)
	}
	out.Cycle = &agentv1.CycleReport{
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
	}
	return out, nil
}

func appleState(d agentv1.DownloadOutcome) agentv1.AppleAccountState {
	switch d {
	case agentv1.DownloadOutcome_DOWNLOAD_OUTCOME_OK, agentv1.DownloadOutcome_DOWNLOAD_OUTCOME_PARTIAL:
		return agentv1.AppleAccountState_APPLE_ACCOUNT_STATE_SIGNED_IN
	case agentv1.DownloadOutcome_DOWNLOAD_OUTCOME_SIGN_IN_REQUIRED:
		return agentv1.AppleAccountState_APPLE_ACCOUNT_STATE_SIGN_IN_REQUIRED
	case agentv1.DownloadOutcome_DOWNLOAD_OUTCOME_NEEDS_ATTENTION:
		return agentv1.AppleAccountState_APPLE_ACCOUNT_STATE_NEEDS_ATTENTION
	case agentv1.DownloadOutcome_DOWNLOAD_OUTCOME_UNSPECIFIED, agentv1.DownloadOutcome_DOWNLOAD_OUTCOME_ERROR:
		return agentv1.AppleAccountState_APPLE_ACCOUNT_STATE_UNSPECIFIED
	default:
		return agentv1.AppleAccountState_APPLE_ACCOUNT_STATE_UNSPECIFIED
	}
}

// eventKindNames maps API event kinds to the ledger's event kind names.
var eventKindNames = map[agentv1.EventKind]string{
	agentv1.EventKind_EVENT_KIND_RENDITION_CHANGED:    "rendition_changed",
	agentv1.EventKind_EVENT_KIND_STATE_CHANGED:        "state_changed",
	agentv1.EventKind_EVENT_KIND_QUARANTINED:          "quarantined",
	agentv1.EventKind_EVENT_KIND_QUARANTINE_RELEASED:  "quarantine_released",
	agentv1.EventKind_EVENT_KIND_DRIVE_HEALTH_CHANGED: ledger.DriveHealthEvent,
	agentv1.EventKind_EVENT_KIND_RENDITION_MOVED:      ledger.RenditionMovedEvent,
}

var assetStates = map[ledger.AssetState]agentv1.AssetState{
	ledger.StateDiscovered:  agentv1.AssetState_ASSET_STATE_DISCOVERED,
	ledger.StateDownloaded:  agentv1.AssetState_ASSET_STATE_DOWNLOADED,
	ledger.StateVerified:    agentv1.AssetState_ASSET_STATE_VERIFIED,
	ledger.StateQuarantined: agentv1.AssetState_ASSET_STATE_QUARANTINED,
}

var tiers = map[ledger.Tier]agentv1.Tier{
	ledger.TierContent:   agentv1.Tier_TIER_CONTENT,
	ledger.TierStructure: agentv1.Tier_TIER_STRUCTURE,
	ledger.TierProbe:     agentv1.Tier_TIER_PROBE,
}

var mediaTypes = map[string]agentv1.MediaType{
	"photo":            agentv1.MediaType_MEDIA_TYPE_PHOTO,
	"video":            agentv1.MediaType_MEDIA_TYPE_VIDEO,
	"live_photo_image": agentv1.MediaType_MEDIA_TYPE_LIVE_PHOTO_IMAGE,
	"live_photo_video": agentv1.MediaType_MEDIA_TYPE_LIVE_PHOTO_VIDEO,
}

func lookup[K comparable, V any](m map[K]V, k K, what string) (V, error) {
	v, ok := m[k]
	if !ok {
		var zero V
		return zero, fmt.Errorf("unknown %s %v", what, k)
	}
	return v, nil
}

func eventToProto(e ledger.Event) (*agentv1.Event, error) {
	out := &agentv1.Event{
		Id:    e.ID,
		At:    timestamppb.New(e.At),
		Asset: &agentv1.AssetRef{Library: e.Library, AssetId: e.Asset, Filename: e.Filename},
	}
	var err error
	switch e.Kind {
	case "rendition_changed":
		var d struct {
			Version string `json:"version"`
			Size    int64  `json:"size"`
			SHA256  string `json:"sha256"`
		}
		if err = json.Unmarshal([]byte(e.Detail), &d); err == nil {
			out.Detail = &agentv1.Event_RenditionChanged{RenditionChanged: &agentv1.RenditionChanged{
				Version: d.Version, SizeBytes: d.Size, Sha256: d.SHA256,
			}}
		}
	case "state_changed":
		var d struct {
			From ledger.AssetState `json:"from"`
			To   ledger.AssetState `json:"to"`
		}
		if err = json.Unmarshal([]byte(e.Detail), &d); err == nil {
			sc := &agentv1.StateChanged{}
			if sc.From, err = lookup(assetStates, d.From, "asset state"); err == nil {
				sc.To, err = lookup(assetStates, d.To, "asset state")
			}
			out.Detail = &agentv1.Event_StateChanged{StateChanged: sc}
		}
	case "quarantined":
		var d struct {
			Version string      `json:"version"`
			Tier    ledger.Tier `json:"tier"`
			Reason  string      `json:"reason"`
		}
		if err = json.Unmarshal([]byte(e.Detail), &d); err == nil {
			q := &agentv1.Quarantined{Version: d.Version, Reason: d.Reason}
			q.Tier, err = lookup(tiers, d.Tier, "tier")
			out.Detail = &agentv1.Event_Quarantined{Quarantined: q}
		}
	case "quarantine_released":
		var d struct {
			Reason string `json:"reason"`
		}
		if err = json.Unmarshal([]byte(e.Detail), &d); err == nil {
			out.Detail = &agentv1.Event_QuarantineReleased{QuarantineReleased: &agentv1.QuarantineReleased{Reason: d.Reason}}
		}
	case ledger.RenditionMovedEvent:
		var d struct {
			Version string `json:"version"`
			From    string `json:"from"`
			To      string `json:"to"`
		}
		if err = json.Unmarshal([]byte(e.Detail), &d); err == nil {
			out.Detail = &agentv1.Event_RenditionMoved{RenditionMoved: &agentv1.RenditionMoved{Version: d.Version, From: d.From, To: d.To}}
		}
	case ledger.DriveHealthEvent:
		var d struct {
			From    health.State `json:"from"`
			To      health.State `json:"to"`
			Reasons []string     `json:"reasons"`
		}
		if err = json.Unmarshal([]byte(e.Detail), &d); err == nil {
			c := &agentv1.DriveHealthChanged{Reasons: d.Reasons}
			if c.From, err = driveState(d.From); err == nil {
				c.To, err = driveState(d.To)
			}
			out.Detail = &agentv1.Event_DriveHealthChanged{DriveHealthChanged: c}
		}
	default:
		err = fmt.Errorf("unknown event kind %q", e.Kind)
	}
	if err != nil {
		return nil, fmt.Errorf("event %d: %w", e.ID, err)
	}
	return out, nil
}

func failureToProto(version string, f ledger.Failure) *agentv1.Failure {
	return &agentv1.Failure{Version: version, Tier: tiers[f.Tier], Reason: f.Reason}
}

func assetToProto(d ledger.AssetDetail) (*agentv1.Asset, error) {
	state, err := lookup(assetStates, d.State, "asset state")
	if err != nil {
		return nil, err
	}
	out := &agentv1.Asset{
		Ref:            &agentv1.AssetRef{Library: d.Library, AssetId: d.AssetID},
		State:          state,
		StateChangedAt: timestamppb.New(d.StateChangedAt),
		CreatedAt:      optionalTimePtr(d.CreatedAt),
		AddedAt:        optionalTimePtr(d.AddedAt),
		LastSeenAt:     optionalTimePtr(d.LastSeenAt),
	}
	for _, r := range d.Renditions {
		if r.Version == "original" {
			out.Ref.Filename = r.Filename
		}
		mt, err := lookup(mediaTypes, r.MediaType, "media type")
		if err != nil {
			return nil, err
		}
		pr := &agentv1.Rendition{
			Version:       r.Version,
			Filename:      r.Filename,
			LocalPath:     r.LocalPath,
			MediaType:     mt,
			SizeBytes:     r.SizeBytes,
			Sha256:        r.SHA256,
			RequiredTiers: []agentv1.Tier{agentv1.Tier_TIER_CONTENT},
		}
		if r.RequiresStructure {
			pr.RequiredTiers = append(pr.RequiredTiers, agentv1.Tier_TIER_STRUCTURE)
		}
		if r.RequiresProbe {
			pr.RequiredTiers = append(pr.RequiredTiers, agentv1.Tier_TIER_PROBE)
		}
		for _, t := range []ledger.Tier{ledger.TierContent, ledger.TierStructure, ledger.TierProbe} {
			if at, ok := r.Passed[t]; ok {
				pr.Passes = append(pr.Passes, &agentv1.TierPass{Tier: tiers[t], At: timestamppb.New(at)})
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
func driveState(s health.State) (agentv1.DriveHealthState, error) {
	switch s {
	case "":
		return agentv1.DriveHealthState_DRIVE_HEALTH_STATE_UNSPECIFIED, nil
	case health.StateOK:
		return agentv1.DriveHealthState_DRIVE_HEALTH_STATE_OK, nil
	case health.StateWarning:
		return agentv1.DriveHealthState_DRIVE_HEALTH_STATE_WARNING, nil
	case health.StateFailing:
		return agentv1.DriveHealthState_DRIVE_HEALTH_STATE_FAILING, nil
	case health.StateUnavailable:
		return agentv1.DriveHealthState_DRIVE_HEALTH_STATE_UNAVAILABLE, nil
	default:
		return agentv1.DriveHealthState_DRIVE_HEALTH_STATE_UNSPECIFIED, fmt.Errorf("unknown drive health state %q", s)
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
func driveToProto(r health.Report) (*agentv1.DriveHealth, error) {
	if r.State == "" {
		return nil, nil //nolint:nilnil // no check has run yet
	}
	state, err := driveState(r.State)
	if err != nil {
		return nil, err
	}
	out := &agentv1.DriveHealth{State: state, CheckedAt: timestamppb.New(r.CheckedAt), Reasons: r.Reasons}
	if d := r.Drive; d != nil {
		out.Model, out.Protocol, out.Warnings = d.Model, d.Protocol, d.Warnings
		out.TemperatureCelsius = optionalInt32(d.TemperatureCelsius)
		out.PercentUsed = optionalInt32(d.PercentUsed)
		out.AvailableSparePercent = optionalInt32(d.AvailableSparePercent)
		out.MediaErrors, out.PowerOnHours = d.MediaErrors, d.PowerOnHours
	}
	return out, nil
}

var assetKinds = map[ledger.AssetKind]agentv1.AssetKind{
	ledger.KindPhoto:     agentv1.AssetKind_ASSET_KIND_PHOTO,
	ledger.KindLivePhoto: agentv1.AssetKind_ASSET_KIND_LIVE_PHOTO,
	ledger.KindVideo:     agentv1.AssetKind_ASSET_KIND_VIDEO,
}

// assetKindFilter maps a requested kind; unspecified means every kind.
func assetKindFilter(k agentv1.AssetKind) (ledger.AssetKind, error) {
	if k == agentv1.AssetKind_ASSET_KIND_UNSPECIFIED {
		return "", nil
	}
	for lk, pk := range assetKinds {
		if pk == k {
			return lk, nil
		}
	}
	return "", fmt.Errorf("unsupported asset kind %v", k)
}
