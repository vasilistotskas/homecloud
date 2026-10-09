package ledger

import (
	"context"
	"errors"
	"testing"
	"time"
)

// quarantineLivePhoto ingests a live photo whose video failed verification.
func quarantineLivePhoto(t *testing.T, db *DB) {
	t.Helper()
	ingest(t, db, rendition("L", "original", "live_photo_image", true), rendition("L", "live", "live_photo_video", true))
	pass(t, db, "L", "original", TierContent)
	if err := db.RecordMismatch(context.Background(), Key{"PrimarySync", "L", "live"}, TierContent, "checksum differs", t0); err != nil {
		t.Fatal(err)
	}
}

// downloadedAt is a rendition of L as kei reports it after a download at at.
func downloadedAt(version, media string, at time.Time) Rendition {
	r := rendition("L", version, media, true)
	r.DownloadedAt = &at
	return r
}

func TestRepairCompletesWhenEveryFileIsDownloadedAgain(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	quarantineLivePhoto(t, db)

	if err := db.RequestRepair(ctx, "PrimarySync", "L", t0); err != nil {
		t.Fatal(err)
	}
	repairs, listErr := db.Repairs(ctx)
	if listErr != nil || len(repairs) != 1 || repairs[0].Version != "live" || repairs[0].SetAsideAt != nil {
		t.Fatalf("Repairs = %+v, %v; want the failing version, not yet set aside", repairs, listErr)
	}
	if err := db.RequestRepair(ctx, "PrimarySync", "L", t0); !errors.Is(err, ErrRepairInProgress) {
		t.Fatalf("second request: %v, want ErrRepairInProgress", err)
	}
	if err := db.ReleaseQuarantine(ctx, "PrimarySync", "L", "fine", t0); !errors.Is(err, ErrRepairInProgress) {
		t.Fatalf("release during repair: %v, want ErrRepairInProgress", err)
	}

	// Before the file is set aside, a download kei reports does not count.
	later := t0.Add(time.Hour)
	if st := ingest(t, db, downloadedAt("original", "live_photo_image", later), downloadedAt("live", "live_photo_video", later)); st.Repaired != 0 {
		t.Fatal("repair completed before the file was set aside")
	}

	setAside := t0.Add(2*time.Hour + 500*time.Millisecond)
	if err := db.RecordSetAside(ctx, Key{"PrimarySync", "L", "live"}, "Damaged/20261005T140000Z/L_live", setAside); err != nil {
		t.Fatal(err)
	}
	q, err := db.Quarantined(ctx)
	if err != nil || len(q) != 1 || q[0].Repair == nil || q[0].Repair.KeptAs != "Damaged/20261005T140000Z/L_live" ||
		q[0].Repair.SetAsideAt == nil || !q[0].Repair.RequestedAt.Equal(t0) {
		t.Fatalf("Quarantined = %+v, %v; want the repair's progress", q, err)
	}

	// kei still reports the download from before the file left.
	if st := ingest(t, db, downloadedAt("original", "live_photo_image", later), downloadedAt("live", "live_photo_video", later)); st.Repaired != 0 {
		t.Fatal("an earlier download completed the repair")
	}
	// kei marked it failed (reconcile) and has not downloaded it yet.
	waiting := rendition("L", "live", "live_photo_video", false)
	if st := ingest(t, db, downloadedAt("original", "live_photo_image", later), waiting); st.Repaired != 0 {
		t.Fatal("a failed download completed the repair")
	}
	if got := state(t, db, "L"); got != StateQuarantined {
		t.Fatalf("state while waiting = %s, want quarantined", got)
	}

	// kei records whole seconds: a download in the same second counts.
	again := setAside.Truncate(time.Second)
	if st := ingest(t, db, downloadedAt("original", "live_photo_image", later), downloadedAt("live", "live_photo_video", again)); st.Repaired != 1 {
		t.Fatal("repair not completed by the new download")
	}
	if got := state(t, db, "L"); got != StateDownloaded {
		t.Fatalf("state after repair = %s, want downloaded", got)
	}
	pending := pendingFor(t, db, "L")
	if len(pending) != 1 || pending[0].Version != "live" || !pending[0].NeedsContent || !pending[0].NeedsStructure {
		t.Fatalf("pending = %+v; want every tier of the repaired version only", pending)
	}
	if repairs, err = db.Repairs(ctx); err != nil || len(repairs) != 0 {
		t.Fatalf("Repairs after completion = %+v, %v", repairs, err)
	}
	events, err := db.Events(ctx, EventQuery{Kinds: []string{EventRepairRequested, EventRenditionSetAside, EventRepaired}}, 0, 10)
	if err != nil || len(events) != 3 || events[0].Kind != EventRepaired || events[2].Kind != EventRepairRequested {
		t.Fatalf("events = %+v, %v", events, err)
	}
}

func TestRepairRefusals(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	ingest(t, db, rendition("P", "original", "photo", true))
	if err := db.RequestRepair(ctx, "PrimarySync", "P", t0); !errors.Is(err, ErrNotQuarantined) {
		t.Fatalf("repair of a healthy asset: %v, want ErrNotQuarantined", err)
	}
	if err := db.RequestRepair(ctx, "PrimarySync", "nope", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repair of an unknown asset: %v, want ErrNotFound", err)
	}
	if err := db.RecordSetAside(ctx, Key{"PrimarySync", "P", "original"}, "", t0); err == nil {
		t.Fatal("set aside without a repair")
	}
}

func TestAbandonedRepairKeepsTheQuarantine(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	quarantineLivePhoto(t, db)
	if err := db.RequestRepair(ctx, "PrimarySync", "L", t0); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordSetAside(ctx, Key{"PrimarySync", "L", "live"}, "", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.AbandonRepair(ctx, "PrimarySync", "L", "kei no longer lists the file", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := state(t, db, "L"); got != StateQuarantined {
		t.Fatalf("state = %s, want quarantined", got)
	}
	q, err := db.Quarantined(ctx)
	if err != nil || len(q) != 1 || q[0].Repair != nil || q[0].Failure.Reason != "checksum differs" {
		t.Fatalf("Quarantined = %+v, %v; want the failure without a repair", q, err)
	}
	if err := db.AbandonRepair(ctx, "PrimarySync", "L", "again", t0); err == nil {
		t.Fatal("abandoned a repair twice")
	}
	// A new repair can be requested.
	if err := db.RequestRepair(ctx, "PrimarySync", "L", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}
