package iphonebackup

import (
	"fmt"
	"time"
)

// State is the verdict on one device's current backup, most severe first.
type State string

// States.
const (
	// StateInvalid: the folder is not a readable backup (missing or malformed
	// plists, or a UDID that does not match the folder).
	StateInvalid State = "invalid"
	// StateUnreadable: some backed-up files could not be read from disk.
	StateUnreadable State = "unreadable"
	// StateUnencrypted: the backup is not encrypted, so it lacks passwords,
	// Health and other protected data, and anyone with the disk can read it.
	StateUnencrypted State = "unencrypted"
	// StateIncomplete: the last backup did not finish and none is running.
	StateIncomplete State = "incomplete"
	// StateStale: the last finished backup is older than the policy allows.
	StateStale State = "stale"
	// StateShrunk: the backup is much smaller than the last healthy one.
	StateShrunk State = "shrunk"
	// StateInProgress: a backup is being written; judged when it finishes.
	StateInProgress State = "in_progress"
	// StateOK: encrypted, finished, recent, readable.
	StateOK State = "ok"
)

// severity orders states for picking the verdict among several problems.
var severity = map[State]int{
	StateInvalid: 7, StateUnreadable: 6, StateUnencrypted: 5, StateIncomplete: 4,
	StateStale: 3, StateShrunk: 2, StateInProgress: 1, StateOK: 0,
}

// snapshotFinished is Status.plist's SnapshotState of a completed backup.
const snapshotFinished = "finished"

// Policy is what a healthy backup must satisfy.
type Policy struct {
	// MaxAge is the oldest acceptable finished backup.
	MaxAge time.Duration
	// MaxShrinkPercent is the largest acceptable size drop, in percent of
	// the last healthy backup.
	MaxShrinkPercent float64
}

// Verdict is the state of one device's backup and why.
type Verdict struct {
	State State
	// Reasons explain every problem found; the checks run in order of severity.
	Reasons []string
}

// Evaluate judges b at now. lastHealthyBytes is the size of the device's last
// backup judged OK (0 when there was none), used to notice a sharp shrink.
func Evaluate(b Backup, lastHealthyBytes int64, p Policy, now time.Time) Verdict {
	if b.Err != nil {
		return Verdict{State: StateInvalid, Reasons: []string{b.Err.Error()}}
	}
	v := Verdict{State: StateOK}
	add := func(s State, format string, args ...any) {
		v.Reasons = append(v.Reasons, fmt.Sprintf(format, args...))
		if severity[s] > severity[v.State] {
			v.State = s
		}
	}

	if n := len(b.Unreadable); n > 0 {
		add(StateUnreadable, "%d file(s) could not be read", n)
	}
	switch {
	case b.InProgress:
		add(StateInProgress, "a backup is in progress")
	case !b.Encrypted:
		add(StateUnencrypted, "the backup is not encrypted")
	}
	if !b.InProgress && b.SnapshotState != snapshotFinished {
		add(StateIncomplete, "the last backup did not finish (state %q)", b.SnapshotState)
	}
	if !b.CompletedAt.IsZero() {
		if age := now.Sub(b.CompletedAt); age > p.MaxAge {
			add(StateStale, "the last backup finished %s ago, more than %s", Span(age), Span(p.MaxAge))
		}
	} else if !b.InProgress {
		add(StateIncomplete, "the backup has no completion date")
	}
	if !b.InProgress && lastHealthyBytes > 0 {
		if drop := 100 * float64(lastHealthyBytes-b.Bytes) / float64(lastHealthyBytes); drop > p.MaxShrinkPercent {
			add(StateShrunk, "the backup is %.0f%% smaller than the last healthy one", drop)
		}
	}
	return v
}

// Span names a duration for people: whole hours below two days ("26 hours"),
// whole days from then on ("9 days").
func Span(d time.Duration) string {
	if hours := int(d / time.Hour); hours < 48 {
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	}
	return fmt.Sprintf("%d days", int(d/(24*time.Hour)))
}
