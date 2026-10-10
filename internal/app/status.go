package app

import (
	"sync"
	"time"

	"github.com/vasilistotskas/homecloud/internal/health"
)

// Phase is what the agent is doing.
type Phase int

// Agent phases, in cycle order.
const (
	PhaseIdle Phase = iota
	PhaseDownloading
	PhaseIngesting
	PhaseVerifying
	PhasePreparingPreviews
	// PhaseCheckingArchive re-hashes a share of archived files (scrub).
	PhaseCheckingArchive
	PhaseBackingUpLedger
	// PhaseCheckingIPhoneBackups inspects the local iPhone backups.
	PhaseCheckingIPhoneBackups
	// PhaseSyncingDrive archives new and changed iCloud Drive files.
	PhaseSyncingDrive
)

// InCycle reports whether p is a phase of a sync cycle, as opposed to idle
// time or a maintenance job (scrub, ledger backup).
func (p Phase) InCycle() bool {
	return p >= PhaseDownloading && p <= PhasePreparingPreviews
}

// Status is the agent's live, in-memory state. Everything durable lives in
// the ledger.
type Status struct {
	Phase Phase
	// PhaseStartedAt is zero while idle.
	PhaseStartedAt time.Time
	// NextCycleAt is zero until the first cycle has finished.
	NextCycleAt time.Time
	// Storage is the latest health check of the drives holding the archive;
	// zero until the first cycle has checked them.
	Storage health.Report
}

// Hub holds the live Status and wakes watchers whenever it, or anything they
// derive from the ledger, changes. Watchers re-read state after each wake-up,
// so bursts of changes coalesce.
type Hub struct {
	mu      sync.Mutex
	status  Status
	changed chan struct{}
}

// NewHub returns an idle hub.
func NewHub() *Hub {
	return &Hub{changed: make(chan struct{})}
}

// Snapshot returns the current status and a channel that is closed at the next change.
func (h *Hub) Snapshot() (Status, <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status, h.changed
}

// Changed wakes watchers without altering the status, for example after a
// ledger write made outside a cycle.
func (h *Hub) Changed() {
	h.update(func(*Status) {})
}

// SetStorage publishes the latest health check of the archive's drives.
func (h *Hub) SetStorage(r health.Report) {
	h.update(func(s *Status) { s.Storage = r })
}

func (h *Hub) update(f func(*Status)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f(&h.status)
	close(h.changed)
	h.changed = make(chan struct{})
}
