package ledger

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func scrubIDs(ts []ScrubTarget) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.AssetID)
	}
	return out
}

func TestScrubBatchRotates(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	for _, id := range []string{"A", "B", "C", "D"} {
		ingest(t, db, rendition(id, "original", "photo", true))
		pass(t, db, id, "original", TierContent, TierStructure)
	}
	ingest(t, db, rendition("U", "original", "photo", true)) // downloaded, not verified: not eligible

	// Verification set last_scrub_at = t0 for all four; a newer scrub of A and B
	// puts C and D first.
	for _, id := range []string{"A", "B"} {
		if err := db.RecordScrub(ctx, Key{"PrimarySync", id, "original"}, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	batch, eligible, err := db.ScrubBatch(ctx, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if eligible != 4 || !slices.Equal(scrubIDs(batch), []string{"C", "D"}) {
		t.Fatalf("batch = %v of %d, want [C D] of 4", scrubIDs(batch), eligible)
	}
	if batch[0].SHA256 == "" || batch[0].LocalPath == "" || batch[0].SizeBytes != 100 {
		t.Fatalf("target = %+v, want path, size and checksum", batch[0])
	}

	// A tiny fraction still checks one file per run.
	small, _, err := db.ScrubBatch(ctx, 0.01)
	if err != nil || len(small) != 1 {
		t.Fatalf("small batch = %v, %v; want one target", scrubIDs(small), err)
	}
	if _, _, err := db.ScrubBatch(ctx, 0); err == nil {
		t.Fatal("fraction 0 accepted")
	}
	if err := db.RecordScrub(ctx, Key{"PrimarySync", "nope", "original"}, t0); err == nil {
		t.Fatal("scrub of an unknown rendition recorded")
	}
}

func TestScrubMismatchRequiresReverification(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	ingest(t, db, rendition("S", "original", "photo", true))
	pass(t, db, "S", "original", TierContent, TierStructure)

	k := Key{"PrimarySync", "S", "original"}
	if err := db.RecordMismatch(ctx, k, TierContent, "checksum changed", t0); err != nil {
		t.Fatal(err)
	}
	if batch, _, err := db.ScrubBatch(ctx, 1); err != nil || len(batch) != 0 {
		t.Fatalf("quarantined rendition scrubbed: %v, %v", scrubIDs(batch), err)
	}
	if err := db.ReleaseQuarantine(ctx, "PrimarySync", "S", "restored from iCloud", t0); err != nil {
		t.Fatal(err)
	}
	pending := pendingFor(t, db, "S")
	if len(pending) != 1 || !pending[0].NeedsContent || pending[0].NeedsStructure {
		t.Fatalf("pending after release = %+v, want the content tier only", pending)
	}
}

func TestSnapshot(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	ingest(t, db, rendition("A", "original", "photo", true))

	dest := filepath.Join(t.TempDir(), "snap.db")
	for range 2 { // a second snapshot replaces the first
		if err := db.Snapshot(ctx, dest); err != nil {
			t.Fatal(err)
		}
	}
	copyDB, err := Open(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = copyDB.Close() })
	if got := state(t, copyDB, "A"); got != StateDownloaded {
		t.Fatalf("snapshot state = %s, want downloaded", got)
	}
}
