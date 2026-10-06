package iphonebackup

import (
	"fmt"
	"time"
)

// State is the verdict on one device's current backup.
type State string

// States, from most to least severe (Severity orders them).
const (
	// StateInvalid: the folder is not a readable backup (missing or malformed
	// plists, a UDID that does not match the folder, a date in the future).
	StateInvalid State = "invalid"
	// StateUnreadable: some backed-up files could not be read from disk.
	StateUnreadable State = "unreadable"
	// StateUnencrypted: the backup is not encrypted, so it lacks passwords,
	// Health and other protected data, and anyone with the disk can read it.
	StateUnencrypted State = "unencrypted"
	// StateIncomplete: the last backup did not finish, or one stopped part way.
	StateIncomplete State = "incomplete"
	// StateStale: the last finished backup is older than the policy allows.
	StateStale State = "stale"
	// StateShrunk: the backup is much smaller than the last normal one.
	StateShrunk State = "shrunk"
	// StateInProgress: a backup is being written; judged when it finishes.
	StateInProgress State = "in_progress"
	// StateOK: encrypted, finished, recent, readable, of a normal size.
	StateOK State = "ok"
)

var severity = map[State]int{
	StateInvalid: 7, StateUnreadable: 6, StateUnencrypted: 5, StateIncomplete: 4,
	StateStale: 3, StateShrunk: 2, StateInProgress: 1, StateOK: 0,
}

// Severity orders states: higher is more severe.
func Severity(s State) int { return severity[s] }

// Healthy reports whether a state needs nothing from the owner.
func (s State) Healthy() bool { return s == StateOK || s == StateInProgress }

// snapshotFinished is Status.plist's SnapshotState of a completed backup.
const snapshotFinished = "finished"

// futureTolerance is how far ahead of this computer's clock a backup's
// completion date may lie (clocks drift) before the date is not believed.
const futureTolerance = time.Hour

// Policy is what a healthy backup must satisfy.
type Policy struct {
	// MaxAge is the oldest acceptable finished backup.
	MaxAge time.Duration
	// StalledAfter is how long a backup in progress may go without writing
	// before it counts as stopped.
	StalledAfter time.Duration
	// MaxShrinkPercent is the largest acceptable size drop, in percent of
	// the last normal backup.
	MaxShrinkPercent float64
}

// Record is what is remembered about a backup folder between checks.
type Record struct {
	// Baseline is the size of the last backup with a normal size (0: none).
	Baseline int64
	// PendingShrink is a much smaller backup's size, awaiting a later backup
	// that confirms it as the new normal (0: none).
	PendingShrink int64
	// Judged is the completion time of the backup last judged.
	Judged time.Time
}

// Verdict is the state of one device's backup and why.
type Verdict struct {
	State State
	// Reasons explain every problem found.
	Reasons []string
}

// Judge rates b at now, given what was remembered about its folder and how
// many files the latest full read could not read, and returns the verdict and
// what to remember next.
//
// A backup much smaller than the last normal one is "shrunk" until a later
// backup of about the same smaller size confirms it (apps or data removed on
// purpose); then that size becomes the new normal.
func Judge(b Backup, unreadable int, prev Record, p Policy, now time.Time) (Verdict, Record) {
	if b.Err != nil {
		return Verdict{State: StateInvalid, Reasons: []string{b.Err.Error()}}, prev
	}
	v := Verdict{State: StateOK}
	add := func(s State, format string, args ...any) {
		v.Reasons = append(v.Reasons, fmt.Sprintf(format, args...))
		if severity[s] > severity[v.State] {
			v.State = s
		}
	}
	if b.InProgress {
		if idle := now.Sub(b.SnapshotActivity); idle > p.StalledAfter {
			add(StateIncomplete, "a backup stopped %s ago without finishing", Span(idle))
		} else {
			add(StateInProgress, "a backup is in progress")
		}
		return v, prev
	}

	if n := unreadable + len(b.Unlisted); n > 0 {
		add(StateUnreadable, "%d file(s) could not be read", n)
	}
	if !b.Encrypted {
		add(StateUnencrypted, "the backup is not encrypted")
	}
	if b.SnapshotState != snapshotFinished {
		add(StateIncomplete, "the last backup did not finish (state %q)", b.SnapshotState)
	}
	switch {
	case b.CompletedAt.IsZero():
		add(StateIncomplete, "the backup has no completion date")
		return v, prev
	case b.CompletedAt.After(now.Add(futureTolerance)):
		add(StateInvalid, "the backup's completion date %s is in the future; check this computer's clock",
			b.CompletedAt.Format(time.RFC3339))
		return v, prev
	}
	if age := now.Sub(b.CompletedAt); age > p.MaxAge {
		add(StateStale, "the last backup finished %s ago, more than %s", Span(age), Span(p.MaxAge))
	}

	next := prev
	if b.CompletedAt.After(prev.Judged) {
		// A backup not judged before: its size is news.
		next.Judged = b.CompletedAt
		switch {
		case prev.Baseline == 0 || !shrunk(prev.Baseline, b.Bytes, p):
			next.Baseline, next.PendingShrink = b.Bytes, 0
		case prev.PendingShrink > 0 && !shrunk(prev.PendingShrink, b.Bytes, p):
			// A second smaller backup confirms the new size.
			next.Baseline, next.PendingShrink = b.Bytes, 0
		default:
			next.PendingShrink = b.Bytes
		}
	}
	if next.PendingShrink > 0 {
		add(StateShrunk, "the backup is %.0f%% smaller than the last normal one; a later backup of that size confirms it",
			drop(next.Baseline, next.PendingShrink))
	}
	return v, next
}

// shrunk reports whether size is more than the policy's percentage below base.
func shrunk(base, size int64, p Policy) bool {
	return drop(base, size) > p.MaxShrinkPercent
}

func drop(base, size int64) float64 {
	return 100 * float64(base-size) / float64(base)
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
