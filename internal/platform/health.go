package platform

import "errors"

// ErrHealthUnsupported is returned when a drive's health cannot be read
// without elevated rights, the drive reports no SMART data, or the path is
// not on a drive at all.
var ErrHealthUnsupported = errors.New("drive health is not available for this drive")

// DriveHealth is what a drive reports about itself (SMART). Absent values
// are nil: not every drive reports every attribute.
type DriveHealth struct {
	Model    string
	Serial   string
	Protocol string // "NVMe" or "ATA"
	// Warnings are the drive's own critical warnings: the NVMe critical-warning
	// bits by their udisks names (spare, temperature, degraded, readonly,
	// volatile_mem, pmr_readonly, and unknown for a bit the specification
	// reserves) or "failing" for an ATA SMART failure prediction. Any warning
	// means the drive may fail.
	Warnings              []string
	TemperatureCelsius    *int
	PercentUsed           *int // NVMe endurance estimate; 100 means the rated life is used up
	AvailableSparePercent *int
	// MediaErrors counts NVMe media and data-integrity errors, or ATA bad
	// (reallocated and pending) sectors.
	MediaErrors  *uint64
	PowerOnHours *uint64
}

func kelvinToCelsius(k int) int { return k - 273 }
