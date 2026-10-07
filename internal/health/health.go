// Package health judges the storage holding the archive from what its drives
// and RAID arrays report about themselves. The archive is the only copy of
// the user's library, so a drive that predicts its own failure, or an array
// that lost its redundancy, must be noticed at once and must block deletion
// from iCloud.
package health

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/platform"
	"github.com/vasilistotskas/homecloud/internal/smartctl"
)

// State is a verdict on a drive, an array, or all of the archive's storage.
type State string

// States. Unavailable means health could not be read; it is never treated as
// healthy.
const (
	StateOK          State = "ok"
	StateWarning     State = "warning"
	StateFailing     State = "failing"
	StateUnavailable State = "unavailable"
)

// severity orders states for the overall verdict: a problem a drive or
// array reports outranks one that could not be read, so a warning still
// alerts while another drive is unreadable.
func (s State) severity() int {
	switch s {
	case StateOK:
		return 0
	case StateUnavailable:
		return 1
	case StateWarning:
		return 2
	case StateFailing:
		return 3
	}
	return 3
}

// Report is one health check of the storage holding the archive.
type Report struct {
	// State is the worst state of any drive or array.
	State     State
	CheckedAt time.Time
	// Reasons explain a non-OK state, each naming its drive or array.
	Reasons []string
	Drives  []DriveReport
	Arrays  []ArrayReport
}

// DriveReport is the verdict on one drive.
type DriveReport struct {
	Name    string
	State   State
	Reasons []string
	// Health is what the drive reported; nil when unavailable.
	Health *platform.DriveHealth
}

// ArrayReport is the verdict on one md array.
type ArrayReport struct {
	platform.RaidArray
	State   State
	Reasons []string
}

// DriveReader reads one drive's health.
type DriveReader func(context.Context, platform.Drive) (platform.DriveHealth, error)

// ReaderFor returns the configured way of reading drive health: smartctl
// when drive_health.source says so (through the container's smartctl
// sidecar when smartctl.socket names one), otherwise what the operating
// system reports (Windows: NVMe health log; Linux: udisks).
func ReaderFor(cfg *config.Config) DriveReader {
	switch {
	case cfg.DriveHealth.Source != config.DriveHealthSmartctl:
		return platform.ReadDriveHealth
	case cfg.Smartctl.Socket != "":
		return func(ctx context.Context, d platform.Drive) (platform.DriveHealth, error) {
			return smartctl.RemoteRead(ctx, cfg.Smartctl, d)
		}
	default:
		return func(ctx context.Context, d platform.Drive) (platform.DriveHealth, error) {
			return smartctl.Read(ctx, cfg.Smartctl, d)
		}
	}
}

// Check finds the storage under the archive (normally platform.StorageOf on
// storage.root), reads every drive's health and judges drives and arrays.
func Check(ctx context.Context, storageOf func() (platform.Storage, error), read DriveReader, now time.Time) Report {
	s, err := storageOf()
	if err != nil {
		return Report{State: StateUnavailable, CheckedAt: now, Reasons: []string{err.Error()}}
	}
	if len(s.Drives) == 0 {
		return Report{State: StateUnavailable, CheckedAt: now, Reasons: []string{"no drive found under the archive"}}
	}
	r := Report{State: StateOK, CheckedAt: now}
	for _, d := range s.Drives {
		dr := DriveReport{Name: d.Name}
		if h, err := read(ctx, d); err != nil {
			dr.State, dr.Reasons = StateUnavailable, []string{err.Error()}
		} else {
			dr.State, dr.Reasons = EvaluateDrive(h)
			dr.Health = &h
		}
		r.add(d.Name, dr.State, dr.Reasons)
		r.Drives = append(r.Drives, dr)
	}
	for _, a := range s.Arrays {
		state, reasons := EvaluateArray(a)
		r.add(a.Name, state, reasons)
		r.Arrays = append(r.Arrays, ArrayReport{RaidArray: a, State: state, Reasons: reasons})
	}
	return r
}

func (r *Report) add(name string, s State, reasons []string) {
	if s.severity() > r.State.severity() {
		r.State = s
	}
	for _, reason := range reasons {
		r.Reasons = append(r.Reasons, name+": "+reason)
	}
}

// EvaluateDrive judges a drive using only thresholds its own standards define:
//   - any critical warning (NVMe critical-warning bits, ATA SMART failure
//     prediction) means the drive expects to fail;
//   - media and data-integrity errors mean data was already lost or remapped;
//   - NVMe "percentage used" reaching 100 means the vendor's rated endurance
//     is used up (NVMe Base Specification, SMART / Health log).
func EvaluateDrive(d platform.DriveHealth) (State, []string) {
	if len(d.Warnings) > 0 {
		reasons := make([]string, 0, len(d.Warnings))
		for _, w := range d.Warnings {
			reasons = append(reasons, "drive reports: "+w)
		}
		return StateFailing, reasons
	}
	var reasons []string
	if d.MediaErrors != nil && *d.MediaErrors > 0 {
		reasons = append(reasons, fmt.Sprintf("%d media errors", *d.MediaErrors))
	}
	if d.PercentUsed != nil && *d.PercentUsed >= 100 {
		reasons = append(reasons, "rated endurance used up")
	}
	if len(reasons) > 0 {
		return StateWarning, reasons
	}
	return StateOK, nil
}

// EvaluateArray judges an md array by the states the kernel reports
// (docs.kernel.org/admin-guide/md.html):
//   - an array that is not running, is broken, or has fewer working members
//     than it was built with is failing: one more failure loses data;
//   - an array rebuilding its redundancy, or one whose member had a write
//     error, corrected read errors, or is marked for replacement, is a
//     warning.
//
// The mismatch count is reported but not judged: on raid1 it can be non-zero
// without damage (md(4)), and homecloud's own scrub re-checks every file.
func EvaluateArray(a platform.RaidArray) (State, []string) {
	state := StateOK
	var reasons []string
	note := func(s State, format string, args ...any) {
		if s.severity() > state.severity() {
			state = s
		}
		reasons = append(reasons, fmt.Sprintf(format, args...))
	}

	switch a.ArrayState {
	case "clean", "active", "active-idle", "write-pending", "read-auto":
	case "clear", "inactive":
		note(StateFailing, "array is not running (%s)", a.ArrayState)
	case "broken":
		note(StateFailing, "array is broken: a member it cannot run without is missing")
	case "readonly", "suspended":
		note(StateWarning, "array is %s", a.ArrayState)
	default:
		note(StateWarning, "array reports an unknown state %q", a.ArrayState)
	}
	if a.Degraded > 0 {
		note(StateFailing, "lost redundancy: %d of %d drives missing or failed", a.Degraded, a.RaidDisks)
	}
	switch a.SyncAction {
	case "resync", "recover", "reshape":
		if a.SyncTotal > 0 {
			note(StateWarning, "rebuilding (%s, %d%% done)", a.SyncAction, a.SyncDone*100/a.SyncTotal)
		} else {
			note(StateWarning, "rebuilding (%s)", a.SyncAction)
		}
	}
	for _, m := range a.Members {
		switch {
		case slices.Contains(m.States, "faulty"):
			note(StateFailing, "%s has failed", m.Name)
		case slices.Contains(m.States, "blocked"):
			note(StateFailing, "%s failed and is blocked", m.Name)
		}
		if slices.Contains(m.States, "write_error") {
			note(StateWarning, "%s has had a write error", m.Name)
		}
		if slices.Contains(m.States, "want_replacement") {
			note(StateWarning, "%s is marked for replacement", m.Name)
		}
		if m.Errors > 0 {
			note(StateWarning, "%s: %d read errors corrected", m.Name, m.Errors)
		}
	}
	return state, reasons
}
