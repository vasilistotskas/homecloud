// Package api implements homecloud.agent.v3.AgentService, the agent's local
// Connect API. Transport security (Host/Origin checks, authentication) is
// applied around it by package web.
package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv3 "github.com/vasilistotskas/homecloud/gen/homecloud/agent/v3"
	"github.com/vasilistotskas/homecloud/gen/homecloud/agent/v3/agentv3connect"
	"github.com/vasilistotskas/homecloud/internal/app"
	"github.com/vasilistotskas/homecloud/internal/auth"
	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/drive"
	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/media"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/thumbs"
)

// Version is this API's "MAJOR.MINOR" version, reported by GetInfo. MAJOR is
// the proto package's version (homecloud.agent.v3); MINOR counts compatible
// additions to it.
const Version = "3.0"

const (
	defaultPageSize = 50
	maxPageSize     = 200
	// defaultLibraryPage fills a screen of thumbnails.
	defaultLibraryPage = 60
)

// Ledger is the part of the ledger the API reads and writes.
type Ledger interface {
	PairedDevices(ctx context.Context) ([]ledger.PairedDevice, error)
	RevokePairedDevice(ctx context.Context, id string, now time.Time) error
	StateCounts(ctx context.Context) (map[ledger.AssetState]int, error)
	ArchiveTotals(ctx context.Context) (files, bytes int64, err error)
	DriveTotals(ctx context.Context) (files, bytes, deleted int64, err error)
	LastFinishedRun(ctx context.Context, kind string) (ledger.Run, error)
	LastCompletedRun(ctx context.Context, kind string) (ledger.Run, error)
	Runs(ctx context.Context, kind string, beforeID int64, limit int) ([]ledger.Run, error)
	Events(ctx context.Context, kinds []string, beforeID int64, limit int) ([]ledger.Event, error)
	Quarantined(ctx context.Context) ([]ledger.QuarantinedRendition, error)
	Asset(ctx context.Context, library, assetID string) (ledger.AssetDetail, error)
	ReleaseQuarantine(ctx context.Context, library, assetID, reason string, now time.Time) error
	LibraryPage(ctx context.Context, q ledger.LibraryQuery) ([]ledger.LibraryAsset, error)
}

// Agent is the running agent's control surface.
type Agent interface {
	Hub() *app.Hub
	RequestCycle() bool
	// CheckArchive reports whether the configured archive volume is mounted.
	CheckArchive() error
	// ICloudSignedInAt is when the owner last signed homecloud in to iCloud
	// (zero if never).
	ICloudSignedInAt() (time.Time, error)
}

// Service implements AgentService.
type Service struct {
	cfg          *config.Config
	configPath   string
	cli          string
	agentVersion string
	db           Ledger
	agent        Agent
	notify       app.Notifier
	auth         *auth.Authenticator
	media        *media.Signer
	hiddenDir    string
	now          func() time.Time
}

var _ agentv3connect.AgentServiceHandler = (*Service)(nil)

// Deps are the Service's collaborators.
type Deps struct {
	Config     *config.Config
	ConfigPath string
	// CLI is the homecloud command-line tool, for commands the owner runs.
	CLI          string
	AgentVersion string
	Ledger       Ledger
	Agent        Agent
	Notifier     app.Notifier
	Auth         *auth.Authenticator
	Media        *media.Signer
	Now          func() time.Time
}

// New returns a Service.
func New(d Deps) *Service {
	return &Service{
		cfg: d.Config, configPath: d.ConfigPath, cli: d.CLI, agentVersion: d.AgentVersion,
		db: d.Ledger, agent: d.Agent, notify: d.Notifier, auth: d.Auth, media: d.Media, now: d.Now,
		hiddenDir: kei.HiddenPath(d.Config.Storage.Root) + string(filepath.Separator),
	}
}

// GetInfo implements AgentService.
func (s *Service) GetInfo(context.Context, *connect.Request[agentv3.GetInfoRequest]) (*connect.Response[agentv3.GetInfoResponse], error) {
	return connect.NewResponse(&agentv3.GetInfoResponse{
		AgentVersion: s.agentVersion,
		ApiVersion:   Version,
		Os:           runtime.GOOS,
	}), nil
}

// GetOverview implements AgentService.
func (s *Service) GetOverview(ctx context.Context, _ *connect.Request[agentv3.GetOverviewRequest]) (*connect.Response[agentv3.GetOverviewResponse], error) {
	st, _ := s.agent.Hub().Snapshot()
	ov, err := s.overview(ctx, st)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv3.GetOverviewResponse{Overview: ov}), nil
}

// overviewInterval is the minimum time between updates on one overview
// stream. Verification wakes the hub after every file and each update re-reads
// the ledger, so changes within the interval fold into the next update.
const overviewInterval = time.Second

// WatchOverview implements AgentService.
func (s *Service) WatchOverview(ctx context.Context, _ *connect.Request[agentv3.WatchOverviewRequest], stream *connect.ServerStream[agentv3.WatchOverviewResponse]) error {
	for {
		st, changed := s.agent.Hub().Snapshot()
		ov, err := s.overview(ctx, st)
		if err != nil {
			return err
		}
		if err := stream.Send(&agentv3.WatchOverviewResponse{Overview: ov}); err != nil {
			return fmt.Errorf("send overview: %w", err)
		}
		sent := time.Now()
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		}
		if wait := overviewInterval - time.Since(sent); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
	}
}

// icloudDrive reports the iCloud Drive archive's totals and session.
func (s *Service) icloudDrive(ctx context.Context) (*agentv3.ICloudDrive, error) {
	files, bytes, deleted, err := s.db.DriveTotals(ctx)
	if err != nil {
		return nil, internal(err)
	}
	out := &agentv3.ICloudDrive{Files: files, Bytes: bytes, DeletedInIcloud: deleted}
	// A session that cannot be read is a state of the iCloud Drive card, not
	// a failure of the whole overview.
	switch signedIn, err := s.agent.ICloudSignedInAt(); {
	case err != nil:
		out.SessionProblem = err.Error()
	case !signedIn.IsZero():
		out.SignedInAt = timestamppb.New(signedIn)
	}
	return out, nil
}

func (s *Service) overview(ctx context.Context, st app.Status) (*agentv3.Overview, error) {
	// The ledger lives on the archive volume, so without it there is no
	// overview; say so precisely instead of failing as an internal error.
	space, err := s.archiveSpace()
	if err != nil {
		return nil, err
	}
	counts, err := s.db.StateCounts(ctx)
	if err != nil {
		return nil, internal(err)
	}
	files, bytes, err := s.db.ArchiveTotals(ctx)
	if err != nil {
		return nil, internal(err)
	}
	ov := &agentv3.Overview{
		Assets: &agentv3.AssetCounts{
			Discovered:  int64(counts[ledger.StateDiscovered]),
			Downloaded:  int64(counts[ledger.StateDownloaded]),
			Verified:    int64(counts[ledger.StateVerified]),
			Quarantined: int64(counts[ledger.StateQuarantined]),
		},
		Archive: &agentv3.ArchiveTotals{Files: files, Bytes: bytes},
		Storage: &agentv3.Storage{
			Root:           s.cfg.Storage.Root,
			TotalBytes:     clampInt64(space.Total),
			FreeBytes:      clampInt64(space.Free),
			MinFreePercent: s.cfg.Storage.MinFreePercent,
		},
		AppleAccount: &agentv3.AppleAccount{},
		Activity: &agentv3.Activity{
			Phase:          phase(st.Phase),
			PhaseStartedAt: optionalTime(st.PhaseStartedAt),
			NextCycleAt:    optionalTime(st.NextCycleAt),
		},
	}
	if ov.StorageHealth, err = storageHealthToProto(st.Storage); err != nil {
		return nil, internal(err)
	}
	if ov.LastCycle, err = s.lastRun(ctx, app.CycleRunKind); err != nil {
		return nil, err
	}
	if ov.LastScrub, err = s.lastRun(ctx, app.ScrubRunKind); err != nil {
		return nil, err
	}
	if ov.LastLedgerBackup, err = s.lastRun(ctx, app.LedgerBackupRunKind); err != nil {
		return nil, err
	}
	if ov.LastIphoneBackup, err = s.lastRun(ctx, app.IPhoneBackupRunKind); err != nil {
		return nil, err
	}
	if ov.LastDriveSync, err = s.lastRun(ctx, app.DriveSyncRunKind); err != nil {
		return nil, err
	}
	if ov.IcloudDrive, err = s.icloudDrive(ctx); err != nil {
		return nil, err
	}
	every := app.MaintenanceEvery(s.cfg)
	for kind, field := range map[string]**timestamppb.Timestamp{
		app.ScrubRunKind:        &ov.Activity.NextScrubAt,
		app.LedgerBackupRunKind: &ov.Activity.NextLedgerBackupAt,
		app.IPhoneBackupRunKind: &ov.Activity.NextIphoneBackupCheckAt,
		app.DriveSyncRunKind:    &ov.Activity.NextDriveSyncAt,
	} {
		if *field, err = s.nextRun(ctx, kind, every[kind], st.NextCycleAt); err != nil {
			return nil, err
		}
	}
	if ov.LastCycle != nil {
		ov.AppleAccount.State = appleState(ov.LastCycle.GetCycle().GetDownload())
	}
	return ov, nil
}

// nextRun is when the maintenance job kind runs next, by the agent's own rule.
func (s *Service) nextRun(ctx context.Context, kind string, every time.Duration, nextCycle time.Time) (*timestamppb.Timestamp, error) {
	var completed time.Time
	switch last, err := s.db.LastCompletedRun(ctx, kind); {
	case err == nil:
		completed = *last.FinishedAt // completed runs are finished
	case !errors.Is(err, ledger.ErrNotFound):
		return nil, internal(err)
	}
	return optionalTime(app.NextRunAt(completed, every, nextCycle, s.cfg.Schedule.SyncInterval.Std())), nil
}

// lastRun returns the most recent finished run of kind, or nil before the first.
func (s *Service) lastRun(ctx context.Context, kind string) (*agentv3.Run, error) {
	last, err := s.db.LastFinishedRun(ctx, kind)
	switch {
	case errors.Is(err, ledger.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, internal(err)
	}
	run, err := runToProto(last)
	if err != nil {
		return nil, internal(err)
	}
	return run, nil
}

// archiveSpace returns the archive volume's space, or FailedPrecondition when
// the configured volume is missing or another volume is mounted in its place.
func (s *Service) archiveSpace() (platform.Space, error) {
	if err := s.agent.CheckArchive(); err != nil {
		return platform.Space{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("archive drive not available: %w", err))
	}
	space, err := platform.DiskSpace(s.cfg.Storage.Root)
	if err != nil {
		return platform.Space{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("archive drive not available: %w", err))
	}
	return space, nil
}

// ListRuns implements AgentService.
func (s *Service) ListRuns(ctx context.Context, req *connect.Request[agentv3.ListRunsRequest]) (*connect.Response[agentv3.ListRunsResponse], error) {
	size, before, err := page(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	kind, ok := runKinds[req.Msg.GetKind()]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown run kind %v", req.Msg.GetKind()))
	}
	runs, err := s.db.Runs(ctx, kind, before, size)
	if err != nil {
		return nil, internal(err)
	}
	res := &agentv3.ListRunsResponse{}
	for _, r := range runs {
		pr, convErr := runToProto(r)
		if convErr != nil {
			return nil, internal(convErr)
		}
		res.Runs = append(res.Runs, pr)
	}
	if len(runs) == size {
		res.NextPageToken = encodePageToken(runs[len(runs)-1].ID)
	}
	return connect.NewResponse(res), nil
}

// ListEvents implements AgentService.
func (s *Service) ListEvents(ctx context.Context, req *connect.Request[agentv3.ListEventsRequest]) (*connect.Response[agentv3.ListEventsResponse], error) {
	if len(req.Msg.GetKinds()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("at least one event kind is required"))
	}
	kinds := make([]string, 0, len(req.Msg.GetKinds()))
	for _, k := range req.Msg.GetKinds() {
		name, ok := eventKindNames[k]
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported event kind %v", k))
		}
		kinds = append(kinds, name)
	}
	size, before, err := page(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	events, err := s.db.Events(ctx, kinds, before, size)
	if err != nil {
		return nil, internal(err)
	}
	res := &agentv3.ListEventsResponse{}
	for _, e := range events {
		pe, convErr := eventToProto(e)
		if convErr != nil {
			return nil, internal(convErr)
		}
		res.Events = append(res.Events, pe)
	}
	if len(events) == size {
		res.NextPageToken = encodePageToken(events[len(events)-1].ID)
	}
	return connect.NewResponse(res), nil
}

// ListQuarantine implements AgentService.
func (s *Service) ListQuarantine(ctx context.Context, _ *connect.Request[agentv3.ListQuarantineRequest]) (*connect.Response[agentv3.ListQuarantineResponse], error) {
	items, err := s.db.Quarantined(ctx)
	if err != nil {
		return nil, internal(err)
	}
	res := &agentv3.ListQuarantineResponse{}
	byAsset := map[[2]string]*agentv3.QuarantinedAsset{}
	for _, q := range items {
		key := [2]string{q.Library, q.AssetID}
		qa, ok := byAsset[key]
		if !ok {
			qa = &agentv3.QuarantinedAsset{
				Asset:         &agentv3.AssetRef{Library: q.Library, AssetId: q.AssetID},
				QuarantinedAt: timestamppb.New(q.QuarantinedAt),
			}
			byAsset[key] = qa
			res.Assets = append(res.Assets, qa)
		}
		if q.Version == ledger.VersionOriginal || qa.GetAsset().GetFilename() == "" {
			qa.Asset.Filename = q.Filename
		}
		qa.Failures = append(qa.Failures, failureToProto(q.Version, q.Failure))
	}
	return connect.NewResponse(res), nil
}

// GetAsset implements AgentService.
func (s *Service) GetAsset(ctx context.Context, req *connect.Request[agentv3.GetAssetRequest]) (*connect.Response[agentv3.GetAssetResponse], error) {
	d, err := s.db.Asset(ctx, req.Msg.GetLibrary(), req.Msg.GetAssetId())
	if errors.Is(err, ledger.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if err != nil {
		return nil, internal(err)
	}
	a, err := assetToProto(d)
	if err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&agentv3.GetAssetResponse{Asset: a}), nil
}

// ReleaseQuarantine implements AgentService.
func (s *Service) ReleaseQuarantine(ctx context.Context, req *connect.Request[agentv3.ReleaseQuarantineRequest]) (*connect.Response[agentv3.ReleaseQuarantineResponse], error) {
	m := req.Msg
	err := s.db.ReleaseQuarantine(ctx, m.GetLibrary(), m.GetAssetId(), strings.TrimSpace(m.GetReason()), s.now())
	switch {
	case errors.Is(err, ledger.ErrReasonRequired):
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, ledger.ErrNotQuarantined):
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	case err != nil:
		return nil, internal(err)
	}
	s.agent.Hub().Changed()
	return connect.NewResponse(&agentv3.ReleaseQuarantineResponse{}), nil
}

// StartSync implements AgentService.
func (s *Service) StartSync(context.Context, *connect.Request[agentv3.StartSyncRequest]) (*connect.Response[agentv3.StartSyncResponse], error) {
	return connect.NewResponse(&agentv3.StartSyncResponse{Started: s.agent.RequestCycle()}), nil
}

// SendTestNotification implements AgentService.
func (s *Service) SendTestNotification(ctx context.Context, _ *connect.Request[agentv3.SendTestNotificationRequest]) (*connect.Response[agentv3.SendTestNotificationResponse], error) {
	if err := s.notify.Send(ctx, notify.TestMsg()); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("send test notification: %w", err))
	}
	return connect.NewResponse(&agentv3.SendTestNotificationResponse{}), nil
}

// GetSettings implements AgentService.
func (s *Service) GetSettings(context.Context, *connect.Request[agentv3.GetSettingsRequest]) (*connect.Response[agentv3.GetSettingsResponse], error) {
	c := s.cfg
	return connect.NewResponse(&agentv3.GetSettingsResponse{Settings: &agentv3.Settings{
		ConfigPath:                       s.configPath,
		StorageRoot:                      c.Storage.Root,
		StorageArchiveId:                 c.Storage.ArchiveID,
		MinFreePercent:                   c.Storage.MinFreePercent,
		Downloader:                       &agentv3.ToolPin{Binary: c.Kei.Binary, Version: c.Kei.Version},
		DownloaderConfig:                 c.Kei.Config,
		VideoProbe:                       &agentv3.ToolPin{Binary: c.FFmpeg.FFprobe, Version: c.FFmpeg.Version},
		Thumbnailer:                      &agentv3.ToolPin{Binary: c.FFmpeg.FFmpeg, Version: c.FFmpeg.Version},
		Listen:                           c.Web.Listen,
		NotifyServer:                     c.Notify.Server,
		NotifyTopic:                      c.Notify.Topic,
		SyncIntervalSeconds:              int64(c.Schedule.SyncInterval.Std() / time.Second),
		ScrubIntervalSeconds:             int64(c.Schedule.ScrubInterval.Std() / time.Second),
		ScrubFraction:                    c.Schedule.ScrubFraction,
		BackupDir:                        c.Backup.Dir,
		BackupIntervalSeconds:            int64(c.Backup.Interval.Std() / time.Second),
		BackupKeep:                       int32(c.Backup.Keep), //nolint:gosec // validated small positive count
		IphoneBackupDirs:                 c.IPhoneBackup.Dirs,
		IphoneBackupMaxAgeSeconds:        int64(c.IPhoneBackup.MaxAge.Std() / time.Second),
		IphoneBackupCheckIntervalSeconds: int64(c.IPhoneBackup.CheckInterval.Std() / time.Second),
		IphoneBackupMaxShrinkPercent:     c.IPhoneBackup.MaxShrinkPercent,
		DriveDir:                         filepath.Join(c.Storage.Root, drive.Dir),
		DriveSyncIntervalSeconds:         int64(c.Drive.SyncInterval.Std() / time.Second),
		DriveWorkers:                     int32(c.Drive.Workers), //nolint:gosec // validated 1..16
		Cli:                              s.cli,
		AppleId:                          c.ICloud.AppleID,
		IncludeHidden:                    *c.ICloud.IncludeHidden,
	}}), nil
}

// CreateLoginCode implements AgentService. Package web only lets bearer-token
// callers reach it.
func (s *Service) CreateLoginCode(context.Context, *connect.Request[agentv3.CreateLoginCodeRequest]) (*connect.Response[agentv3.CreateLoginCodeResponse], error) {
	code, exp, err := s.auth.NewLoginCode()
	if err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&agentv3.CreateLoginCodeResponse{Code: code, ExpiresAt: timestamppb.New(exp)}), nil
}

// ExchangeLoginCode implements AgentService.
func (s *Service) ExchangeLoginCode(_ context.Context, req *connect.Request[agentv3.ExchangeLoginCodeRequest]) (*connect.Response[agentv3.ExchangeLoginCodeResponse], error) {
	cookie, err := s.auth.Exchange(req.Msg.GetCode())
	if errors.Is(err, auth.ErrInvalidCode) {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}
	if err != nil {
		return nil, internal(err)
	}
	res := connect.NewResponse(&agentv3.ExchangeLoginCodeResponse{})
	res.Header().Set("Set-Cookie", cookie.String())
	return res, nil
}

// maxDeviceName bounds a paired device's name, in characters.
const maxDeviceName = 64

// PairDevice implements AgentService.
func (s *Service) PairDevice(ctx context.Context, req *connect.Request[agentv3.PairDeviceRequest]) (*connect.Response[agentv3.PairDeviceResponse], error) {
	name := strings.TrimSpace(req.Msg.GetName())
	if n := utf8.RuneCountInString(name); n == 0 || n > maxDeviceName || !utf8.ValidString(name) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name must be 1-%d characters", maxDeviceName))
	}
	id, token, err := s.auth.Pair(ctx, req.Msg.GetCode(), name)
	if errors.Is(err, auth.ErrInvalidCode) {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}
	if err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&agentv3.PairDeviceResponse{DeviceId: id, Token: token}), nil
}

// ListDevices implements AgentService.
func (s *Service) ListDevices(ctx context.Context, _ *connect.Request[agentv3.ListDevicesRequest]) (*connect.Response[agentv3.ListDevicesResponse], error) {
	devices, err := s.db.PairedDevices(ctx)
	if err != nil {
		return nil, internal(err)
	}
	res := &agentv3.ListDevicesResponse{}
	for _, d := range devices {
		res.Devices = append(res.Devices, &agentv3.PairedDevice{
			Id: d.ID, Name: d.Name, PairedAt: timestamppb.New(d.PairedAt), LastSeenAt: timestamppb.New(d.LastSeenAt),
		})
	}
	return connect.NewResponse(res), nil
}

// RevokeDevice implements AgentService.
func (s *Service) RevokeDevice(ctx context.Context, req *connect.Request[agentv3.RevokeDeviceRequest]) (*connect.Response[agentv3.RevokeDeviceResponse], error) {
	err := s.db.RevokePairedDevice(ctx, req.Msg.GetDeviceId(), s.now())
	if errors.Is(err, ledger.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&agentv3.RevokeDeviceResponse{}), nil
}

func internal(err error) error {
	return connect.NewError(connect.CodeInternal, err)
}

// ListAssets implements AgentService.
func (s *Service) ListAssets(ctx context.Context, req *connect.Request[agentv3.ListAssetsRequest]) (*connect.Response[agentv3.ListAssetsResponse], error) {
	m := req.Msg
	size, err := pageSize(m.GetPageSize(), defaultLibraryPage)
	if err != nil {
		return nil, err
	}
	q := ledger.LibraryQuery{HiddenDir: s.hiddenDir, Hidden: m.GetHidden(), Limit: size}
	if q.Kind, err = assetKindFilter(m.GetKind()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if m.GetPageToken() != "" {
		raw, decodeErr := base64.RawURLEncoding.DecodeString(m.GetPageToken())
		if decodeErr != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("malformed page_token"))
		}
		c, decodeErr := ledger.DecodeCursor(raw)
		if decodeErr != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("malformed page_token"))
		}
		q.After = &c
	}
	assets, err := s.db.LibraryPage(ctx, q)
	if err != nil {
		return nil, internal(err)
	}
	res := &agentv3.ListAssetsResponse{}
	for _, a := range assets {
		item, convErr := s.libraryItem(a)
		if convErr != nil {
			return nil, internal(convErr)
		}
		res.Items = append(res.Items, item)
	}
	if len(assets) == size {
		raw, encErr := ledger.EncodeCursor(assets[len(assets)-1].Cursor)
		if encErr != nil {
			return nil, internal(encErr)
		}
		res.NextPageToken = base64.RawURLEncoding.EncodeToString(raw)
	}
	return connect.NewResponse(res), nil
}

func (s *Service) libraryItem(a ledger.LibraryAsset) (*agentv3.LibraryItem, error) {
	state, err := lookup(assetStates, a.State, "asset state")
	if err != nil {
		return nil, err
	}
	kind, err := lookup(assetKinds, a.Kind, "asset kind")
	if err != nil {
		return nil, err
	}
	item := &agentv3.LibraryItem{
		Asset:     &agentv3.AssetRef{Library: a.Library, AssetId: a.AssetID, Filename: a.Filename},
		Kind:      kind,
		CreatedAt: optionalTimePtr(a.CreatedAt),
		State:     state,
	}
	if a.Display != nil {
		v := a.Display.Version
		item.ThumbnailUrl = s.media.URL(thumbs.Thumb, a.Library, a.AssetID, v)
		item.PreviewUrl = s.media.URL(thumbs.Preview, a.Library, a.AssetID, v)
		item.DisplayVersion = v
	}
	return item, nil
}
