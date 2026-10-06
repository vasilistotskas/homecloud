package app

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/vasilistotskas/homecloud/internal/drive"
	"github.com/vasilistotskas/homecloud/internal/export"
	"github.com/vasilistotskas/homecloud/internal/icloud"
	"github.com/vasilistotskas/homecloud/internal/notify"
	"github.com/vasilistotskas/homecloud/internal/platform"
)

// DriveSyncRunKind is the ledger run kind of an iCloud Drive sync.
const DriveSyncRunKind = "drive_sync"

// DriveSyncReport.Outcome values.
const (
	// DriveOutcomeOK: the archive matches iCloud Drive.
	DriveOutcomeOK = "ok"
	// DriveOutcomePartial: some files could not be archived; they are retried.
	DriveOutcomePartial = "partial"
	// DriveOutcomeSignInRequired: Apple wants the owner to sign in again.
	DriveOutcomeSignInRequired = "sign_in_required"
	// DriveOutcomeSkippedLowSpace: the archive volume is below its free-space minimum.
	DriveOutcomeSkippedLowSpace = "skipped_low_space"
	// DriveOutcomeArchiveUnavailable: the archive volume is missing or replaced.
	DriveOutcomeArchiveUnavailable = "archive_unavailable"
	// DriveOutcomeError: the sync could not run.
	DriveOutcomeError = "error"
)

// alertICloudSignIn throttles the alert that homecloud's iCloud session expired.
const alertICloudSignIn = "icloud-sign-in"

// DriveSyncReport is recorded as the detail of each iCloud Drive sync.
type DriveSyncReport struct {
	Outcome string `json:"outcome"`
	drive.Report
	// ManifestRows is how many files Drive/manifest.csv lists.
	ManifestRows int    `json:"manifest_rows"`
	Error        string `json:"error,omitempty"`
}

// ParseDriveSyncReport decodes the detail recorded for a finished Drive sync.
func ParseDriveSyncReport(detail string) (DriveSyncReport, error) {
	var rep DriveSyncReport
	if err := json.Unmarshal([]byte(detail), &rep); err != nil {
		return DriveSyncReport{}, fmt.Errorf("decode drive sync report: %w", err)
	}
	return rep, nil
}

// ICloudSignedInAt is when the owner last signed homecloud in to iCloud, or
// zero when the session was never established.
func (a *Agent) ICloudSignedInAt() (time.Time, error) {
	path, err := a.icloudSession()
	if err != nil {
		return time.Time{}, err
	}
	s, err := icloud.LoadSession(path)
	if err != nil {
		return time.Time{}, err
	}
	return s.SignedInAt, nil
}

// syncDrive archives iCloud Drive under <storage.root>/Drive with homecloud's
// own iCloud session. It never deletes an archived file.
func (a *Agent) syncDrive(ctx context.Context) (any, bool) {
	rep := DriveSyncReport{Outcome: DriveOutcomeError}
	fail := func(outcome string, err error) (any, bool) {
		rep.Outcome, rep.Error = outcome, err.Error()
		a.log.Error("iCloud Drive sync failed", "outcome", outcome, "err", err)
		return rep, false
	}
	if err := a.checkArchive(); err != nil {
		return fail(DriveOutcomeArchiveUnavailable, fmt.Errorf("archive volume: %w", err))
	}
	if _, err := a.checkSpace(); errors.Is(err, platform.ErrLowSpace) {
		return fail(DriveOutcomeSkippedLowSpace, err)
	} else if err != nil {
		return fail(DriveOutcomeError, err)
	}

	path, err := a.icloudSession()
	if err != nil {
		return fail(DriveOutcomeError, err)
	}
	file, session, err := icloud.OpenSessionFile(path)
	if err != nil {
		return fail(DriveOutcomeError, err)
	}
	client, err := icloud.New(a.icloudEndpoints, session, a.now)
	if err != nil {
		return fail(DriveOutcomeError, err)
	}
	root := filepath.Join(a.cfg.Storage.Root, drive.Dir)
	archive := &drive.Archive{Root: root, DB: a.db, Client: client, Now: a.now, Log: a.log, Workers: a.cfg.Drive.Workers}
	rep.Report, err = archive.Sync(ctx)
	// Keep what the session learned (renewed cookies), unless the owner
	// signed in again meanwhile.
	if saved, saveErr := file.Save(client.Session()); saveErr != nil {
		err = errors.Join(err, saveErr)
	} else if !saved {
		a.log.Info("iCloud session changed during the sync; keeping the newer sign-in")
	}
	a.hub.Changed()
	if errors.Is(err, icloud.ErrSignInRequired) {
		a.alertRepeated(ctx, alertICloudSignIn, notify.Msg{
			Title:    "homecloud: iCloud Drive sign-in needed",
			Body:     "iCloud Drive is not archived until you sign in again: run \"homecloud icloud login\" on this computer. Everything already archived is kept.",
			Priority: notify.PriorityHigh,
			Tags:     []string{"key"},
		})
		return fail(DriveOutcomeSignInRequired, err)
	}
	if err != nil {
		return fail(DriveOutcomeError, err)
	}
	if rep.ManifestRows, err = export.WriteDrive(ctx, a.db, root); err != nil {
		return fail(DriveOutcomeError, err)
	}
	rep.Outcome = DriveOutcomeOK
	if rep.Failed > 0 {
		rep.Outcome = DriveOutcomePartial
	}
	a.log.Info("iCloud Drive synced", "listed", rep.Listed, "archived", rep.Archived, "changed", rep.Changed,
		"moved", rep.Moved, "deleted", rep.Deleted, "failed", rep.Failed, "bytes", rep.Bytes)
	return rep, rep.Failed == 0
}
