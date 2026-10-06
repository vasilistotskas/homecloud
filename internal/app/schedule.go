package app

import (
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
)

// MaintenanceEvery is how often each maintenance job is due, by run kind.
func MaintenanceEvery(cfg *config.Config) map[string]time.Duration {
	return map[string]time.Duration{
		DriveSyncRunKind:    cfg.Drive.SyncInterval.Std(),
		ScrubRunKind:        cfg.Schedule.ScrubInterval.Std(),
		LedgerBackupRunKind: cfg.Backup.Interval.Std(),
		IPhoneBackupRunKind: cfg.IPhoneBackup.CheckInterval.Std(),
	}
}

// dueAt is when a maintenance job whose last completed run finished at
// lastCompleted is due again; a job that never completed is due at once
// (zero).
func dueAt(lastCompleted time.Time, every time.Duration) time.Time {
	if lastCompleted.IsZero() {
		return time.Time{}
	}
	return lastCompleted.Add(every)
}

// NextRunAt is when a maintenance job runs next: maintenance runs after sync
// cycles, so at the first cycle at or after the job is due. Cycles start at
// nextCycle and every cycle after it; zero nextCycle (a cycle is running)
// gives zero.
func NextRunAt(lastCompleted time.Time, every time.Duration, nextCycle time.Time, cycle time.Duration) time.Time {
	if nextCycle.IsZero() {
		return time.Time{}
	}
	due := dueAt(lastCompleted, every)
	if !due.After(nextCycle) {
		return nextCycle
	}
	cycles := (due.Sub(nextCycle) + cycle - 1) / cycle
	return nextCycle.Add(cycles * cycle)
}
