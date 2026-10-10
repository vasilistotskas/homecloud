package ledger

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestEachArchivedAsset(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	quarantineLivePhoto(t, db) // L, taken t0-72h
	if err := db.RequestRepair(ctx, "PrimarySync", "L", t0); err != nil {
		t.Fatal(err)
	}
	ingest(t, db,
		at(rendition("P", "original", "photo", true), time.Hour),
		at(rendition("P", "adjusted", "photo", true), time.Hour),
		at(rendition("V", "original", "video", true), 2*time.Hour),
		// Not downloaded yet: no rendition on disk, so not archived.
		at(rendition("N", "original", "photo", false), 3*time.Hour),
	)
	pass(t, db, "P", "original", TierContent)
	scrubbed := t0.Add(time.Hour)
	if err := db.RecordScrub(ctx, Key{"PrimarySync", "P", "original"}, scrubbed); err != nil {
		t.Fatal(err)
	}

	var got []ArchivedAsset
	if err := db.EachArchivedAsset(ctx, func(a ArchivedAsset) error {
		got = append(got, a)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, a := range got {
		order = append(order, a.AssetID+":"+string(a.Kind))
	}
	if want := []string{"V:video", "P:photo", "L:live"}; !slices.Equal(order, want) {
		t.Fatalf("assets = %v, want %v (newest first, only those on disk)", order, want)
	}

	v, p, l := got[0], got[1], got[2]
	if v.Renditions[0].Passed[TierContent] != (time.Time{}) || !v.Renditions[0].RequiresProbe || v.SizeBytes() != 100 {
		t.Errorf("video evidence = %+v", v.Renditions)
	}
	if p.Filename != "P_original.HEIC" || p.Display.Version != VersionAdjusted || p.SizeBytes() != 200 {
		t.Errorf("photo = %+v display %+v", p.LibraryAsset, p.Display)
	}
	if o, ok := p.Original(); !ok || o.Version != VersionOriginal || o.LocalPath != `D:\Archive\Photos\P_original` {
		t.Errorf("photo original = %+v, %v", o, ok)
	}
	orig := p.Renditions[slices.IndexFunc(p.Renditions, func(r RenditionEvidence) bool { return r.Version == VersionOriginal })]
	if !orig.Passed[TierContent].Equal(t0) || orig.ConfirmedAt == nil || !orig.ConfirmedAt.Equal(scrubbed) {
		t.Errorf("scrubbed original evidence = %+v", orig)
	}
	if p.AddedAt == nil || !p.AddedAt.Equal(t0.Add(-48*time.Hour)) || p.CreatedAt == nil || p.UnderRepair {
		t.Errorf("photo dates/repair = %v %v %v", p.AddedAt, p.CreatedAt, p.UnderRepair)
	}
	if l.State != StateQuarantined || !l.UnderRepair {
		t.Errorf("live photo state = %s under repair %v, want quarantined under repair", l.State, l.UnderRepair)
	}
	failed := map[string]bool{}
	for _, r := range l.Renditions {
		failed[r.Version] = r.Failed
	}
	if !failed["live"] || failed["original"] {
		t.Errorf("live photo failures = %v, want only the video", failed)
	}
}
