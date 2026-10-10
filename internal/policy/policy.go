// Package policy decides, without I/O, whether an iCloud Photos item's
// archived copy is proven well enough that the iCloud copy may go.
package policy

import (
	"time"

	"github.com/vasilistotskas/homecloud/internal/ledger"
)

// Verdict is the outcome for one asset: Eligible, or the first unmet
// condition in the order the conditions are checked.
type Verdict string

// Verdicts.
const (
	Eligible Verdict = "eligible"
	// NotVerified: not every file passed its required checks, or a check
	// failed (quarantined), or a repair is under way.
	NotVerified Verdict = "not_verified"
	// AwaitingScrub: a file has not been confirmed again by a scrub since it
	// was verified.
	AwaitingScrub Verdict = "awaiting_scrub"
	// TooRecent: added to iCloud Photos less than the minimum age ago, or
	// iCloud did not say when.
	TooRecent Verdict = "too_recent"
)

// Judge applies every condition to a. addedBefore is the latest time an
// asset may have been added to iCloud Photos (now minus the minimum age).
func Judge(a ledger.ArchivedAsset, addedBefore time.Time) Verdict {
	if !verified(a) {
		return NotVerified
	}
	for _, r := range a.Renditions {
		if !rescrubbed(r) {
			return AwaitingScrub
		}
	}
	if a.AddedAt == nil || a.AddedAt.After(addedBefore) {
		return TooRecent
	}
	return Eligible
}

// verified: the asset is verified, its original is archived, every file
// passed each tier it requires and none has a recorded failure or a pending
// repair.
func verified(a ledger.ArchivedAsset) bool {
	if a.State != ledger.StateVerified || a.UnderRepair {
		return false
	}
	if _, ok := a.Original(); !ok {
		return false
	}
	for _, r := range a.Renditions {
		if r.Failed {
			return false
		}
		for _, t := range requiredTiers(r) {
			if _, ok := r.Passed[t]; !ok {
				return false
			}
		}
	}
	return true
}

func requiredTiers(r ledger.RenditionEvidence) []ledger.Tier {
	tiers := []ledger.Tier{ledger.TierContent}
	if r.RequiresStructure {
		tiers = append(tiers, ledger.TierStructure)
	}
	if r.RequiresProbe {
		tiers = append(tiers, ledger.TierProbe)
	}
	return tiers
}

// rescrubbed: the file's checksum was confirmed after its last tier passed,
// i.e. by a scrub, which reads the file back from the disk again.
func rescrubbed(r ledger.RenditionEvidence) bool {
	if r.ConfirmedAt == nil {
		return false
	}
	for _, at := range r.Passed {
		if !r.ConfirmedAt.After(at) {
			return false
		}
	}
	return true
}
