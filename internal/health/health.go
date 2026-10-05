// Package health judges the archive drive from what it reports about itself.
// The archive is the only copy of the user's library, so a drive that predicts
// its own failure must be noticed at once and must block deletion from iCloud.
package health

import (
	"fmt"
	"time"

	"github.com/vasilistotskas/homecloud/internal/platform"
)

// State is the verdict on the archive drive.
type State string

// States, from best to worst. Unavailable means the drive's health could not be
// read; it is never treated as healthy.
const (
	StateOK          State = "ok"
	StateWarning     State = "warning"
	StateFailing     State = "failing"
	StateUnavailable State = "unavailable"
)

// Report is one health check of the archive drive.
type Report struct {
	State     State
	CheckedAt time.Time
	// Drive is what the drive reported; nil when unavailable.
	Drive *platform.DriveHealth
	// Reasons explain a non-OK state.
	Reasons []string
}

// Evaluate judges a drive using only thresholds its own standards define:
//   - any critical warning (NVMe critical-warning bits, ATA SMART failure
//     prediction) means the drive expects to fail;
//   - media and data-integrity errors mean data was already lost or remapped;
//   - NVMe "percentage used" reaching 100 means the vendor's rated endurance
//     is used up (NVMe Base Specification, SMART / Health log).
func Evaluate(d platform.DriveHealth) (State, []string) {
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

// Check runs read (normally platform.DriveHealthOf for the archive root) and judges the result.
func Check(read func() (platform.DriveHealth, error), now time.Time) Report {
	d, err := read()
	if err != nil {
		return Report{State: StateUnavailable, CheckedAt: now, Reasons: []string{err.Error()}}
	}
	state, reasons := Evaluate(d)
	return Report{State: state, CheckedAt: now, Drive: &d, Reasons: reasons}
}
