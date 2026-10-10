package policy

import (
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/ledger"
)

var (
	verifiedAt  = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	addedBefore = time.Date(2025, 10, 10, 0, 0, 0, 0, time.UTC)
)

func ptr(t time.Time) *time.Time { return &t }

// eligible is a Live Photo whose picture and video were verified and then
// scrubbed, added to iCloud before addedBefore.
func eligible() ledger.ArchivedAsset {
	scrubbed := verifiedAt.Add(7 * 24 * time.Hour)
	return ledger.ArchivedAsset{
		LibraryAsset: ledger.LibraryAsset{Library: "PrimarySync", AssetID: "A", Kind: ledger.KindLivePhoto, State: ledger.StateVerified},
		AddedAt:      ptr(addedBefore.Add(-time.Hour)),
		Renditions: []ledger.RenditionEvidence{
			{
				Version: ledger.VersionOriginal, MediaType: "live_photo_image",
				Passed: map[ledger.Tier]time.Time{ledger.TierContent: verifiedAt}, ConfirmedAt: ptr(scrubbed),
			},
			{
				Version: "live", MediaType: "live_photo_video", RequiresStructure: true, RequiresProbe: true,
				Passed: map[ledger.Tier]time.Time{
					ledger.TierContent: verifiedAt, ledger.TierStructure: verifiedAt, ledger.TierProbe: verifiedAt.Add(time.Second),
				},
				ConfirmedAt: ptr(scrubbed),
			},
		},
	}
}

func TestJudge(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ledger.ArchivedAsset)
		want   Verdict
	}{
		{"eligible", func(*ledger.ArchivedAsset) {}, Eligible},
		{"added exactly at the limit", func(a *ledger.ArchivedAsset) { a.AddedAt = ptr(addedBefore) }, Eligible},
		{"quarantined", func(a *ledger.ArchivedAsset) { a.State = ledger.StateQuarantined }, NotVerified},
		{"still downloading", func(a *ledger.ArchivedAsset) { a.State = ledger.StateDownloaded }, NotVerified},
		{"under repair", func(a *ledger.ArchivedAsset) { a.UnderRepair = true }, NotVerified},
		{"a failed file", func(a *ledger.ArchivedAsset) { a.Renditions[1].Failed = true }, NotVerified},
		{"a required tier missing", func(a *ledger.ArchivedAsset) { delete(a.Renditions[1].Passed, ledger.TierProbe) }, NotVerified},
		{"no original on disk", func(a *ledger.ArchivedAsset) { a.Renditions[0].Version = "adjusted" }, NotVerified},
		{"never scrubbed", func(a *ledger.ArchivedAsset) {
			// Each tier pass records the content as confirmed at that time.
			a.Renditions[1].ConfirmedAt = ptr(verifiedAt.Add(time.Second))
		}, AwaitingScrub},
		{"no confirmation", func(a *ledger.ArchivedAsset) { a.Renditions[0].ConfirmedAt = nil }, AwaitingScrub},
		{"verified again after the scrub", func(a *ledger.ArchivedAsset) {
			a.Renditions[0].Passed[ledger.TierContent] = a.Renditions[0].ConfirmedAt.Add(time.Hour)
		}, AwaitingScrub},
		{"added too recently", func(a *ledger.ArchivedAsset) { a.AddedAt = ptr(addedBefore.Add(time.Nanosecond)) }, TooRecent},
		{"added date unknown", func(a *ledger.ArchivedAsset) { a.AddedAt = nil }, TooRecent},
		{"first unmet condition wins", func(a *ledger.ArchivedAsset) {
			a.State, a.AddedAt, a.Renditions[0].ConfirmedAt = ledger.StateQuarantined, nil, nil
		}, NotVerified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := eligible()
			tc.change(&a)
			if got := Judge(a, addedBefore); got != tc.want {
				t.Fatalf("Judge = %s, want %s", got, tc.want)
			}
		})
	}
}
