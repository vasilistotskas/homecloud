package ledger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vasilistotskas/homecloud/internal/verify"
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func rendition(asset, version, media string, downloaded bool) Rendition {
	added := t0.Add(-48 * time.Hour)
	ext := ".HEIC"
	if media == "video" || media == "live_photo_video" {
		ext = ".MOV"
	}
	filename := asset + "_" + version + ext
	structure, probe := verify.RequiredTiers(media, filename)
	r := Rendition{
		Library: "PrimarySync", AssetID: asset, Version: version, Filename: filename,
		Downloaded: downloaded, MediaType: media, RequiresStructure: structure, RequiresProbe: probe,
		CreatedAt: t0.Add(-72 * time.Hour), AddedAt: &added, SeenAt: t0,
	}
	if downloaded {
		r.LocalPath = `E:\HomeCloud\Photos\` + asset + "_" + version
		r.SizeBytes = 100
		r.SHA256 = strings.Repeat("a", 64)
	}
	return r
}

func state(t *testing.T, db *DB, asset string) AssetState {
	t.Helper()
	var s AssetState
	if err := db.r.QueryRowContext(context.Background(),
		`SELECT state FROM assets WHERE library = 'PrimarySync' AND asset_id = ?`, asset).Scan(&s); err != nil {
		t.Fatalf("read state of %s: %v", asset, err)
	}
	return s
}

func pendingFor(t *testing.T, db *DB, asset string) []PendingResource {
	t.Helper()
	all, err := db.PendingVerification(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []PendingResource
	for _, p := range all {
		if p.AssetID == asset {
			out = append(out, p)
		}
	}
	return out
}

func pass(t *testing.T, db *DB, asset, version string, tiers ...Tier) {
	t.Helper()
	for _, tier := range tiers {
		if err := db.RecordPass(context.Background(), Key{"PrimarySync", asset, version}, tier, t0); err != nil {
			t.Fatalf("RecordPass(%s/%s/%s): %v", asset, version, tier, err)
		}
	}
}

func ingest(t *testing.T, db *DB, rs ...Rendition) IngestStats {
	t.Helper()
	stats, err := db.Ingest(context.Background(), rs, t0)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return stats
}

func TestPhotoLifecycle(t *testing.T) {
	db, _ := openTemp(t)
	stats := ingest(t, db, rendition("P", "original", "photo", true))
	if stats.Assets != 1 || stats.NewRenditions != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if got := state(t, db, "P"); got != StateDownloaded {
		t.Fatalf("state = %s, want downloaded", got)
	}
	p := pendingFor(t, db, "P")
	if len(p) != 1 || !p[0].NeedsContent || p[0].NeedsStructure || p[0].NeedsProbe {
		t.Fatalf("pending = %+v, want content only", p)
	}
	pass(t, db, "P", "original", TierContent)
	if got := state(t, db, "P"); got != StateVerified {
		t.Fatalf("state = %s, want verified", got)
	}
	if len(pendingFor(t, db, "P")) != 0 {
		t.Fatal("verified asset still pending")
	}
}

func TestVideoNeedsAllTiers(t *testing.T) {
	db, _ := openTemp(t)
	ingest(t, db, rendition("V", "original", "video", true))
	pass(t, db, "V", "original", TierContent, TierStructure)
	if got := state(t, db, "V"); got != StateDownloaded {
		t.Fatalf("after content+structure: state = %s, want downloaded", got)
	}
	pass(t, db, "V", "original", TierProbe)
	if got := state(t, db, "V"); got != StateVerified {
		t.Fatalf("after all tiers: state = %s, want verified", got)
	}
}

func TestLivePhotoRequiresBothHalves(t *testing.T) {
	db, _ := openTemp(t)
	ingest(t, db,
		rendition("L", "original", "live_photo_image", true),
		rendition("L", "live_original", "live_photo_video", true))
	pass(t, db, "L", "original", TierContent)
	if got := state(t, db, "L"); got != StateDownloaded {
		t.Fatalf("still only: state = %s, want downloaded", got)
	}
	pass(t, db, "L", "live_original", TierContent, TierStructure, TierProbe)
	if got := state(t, db, "L"); got != StateVerified {
		t.Fatalf("both halves: state = %s, want verified", got)
	}
}

func TestPendingRenditionKeepsDiscovered(t *testing.T) {
	db, _ := openTemp(t)
	ingest(t, db,
		rendition("D", "original", "photo", true),
		rendition("D", "adjusted", "photo", false))
	if got := state(t, db, "D"); got != StateDiscovered {
		t.Fatalf("state = %s, want discovered", got)
	}
	if len(pendingFor(t, db, "D")) != 0 {
		t.Fatal("discovered asset must not be offered for verification")
	}
}

func TestChangedRenditionLosesEvidence(t *testing.T) {
	db, _ := openTemp(t)
	ingest(t, db, rendition("C", "original", "photo", true))
	pass(t, db, "C", "original", TierContent)

	changed := rendition("C", "original", "photo", true)
	changed.SHA256 = strings.Repeat("b", 64)
	stats := ingest(t, db, changed)
	if stats.ChangedRenditions != 1 {
		t.Fatalf("stats = %+v, want 1 changed", stats)
	}
	if got := state(t, db, "C"); got != StateDownloaded {
		t.Fatalf("state = %s, want downloaded after change", got)
	}
	if p := pendingFor(t, db, "C"); len(p) != 1 || !p[0].NeedsContent {
		t.Fatalf("pending = %+v, want content re-verification", p)
	}
	var n int
	if err := db.r.QueryRowContext(context.Background(),
		`SELECT count(*) FROM events WHERE asset_id = 'C' AND kind = 'rendition_changed'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rendition_changed events = %d, %v", n, err)
	}
}

func TestMovedRenditionIsVerifiedAgain(t *testing.T) {
	db, _ := openTemp(t)
	ingest(t, db, rendition("V", "original", "photo", true))
	pass(t, db, "V", "original", TierContent)
	if got := state(t, db, "V"); got != StateVerified {
		t.Fatalf("state = %s, want verified", got)
	}

	moved := rendition("V", "original", "photo", true)
	moved.LocalPath += ".album-copy"
	stats := ingest(t, db, moved)
	if stats.MovedRenditions != 1 || stats.ChangedRenditions != 0 {
		t.Fatalf("stats = %+v, want 1 moved", stats)
	}
	if got := state(t, db, "V"); got != StateDownloaded {
		t.Fatalf("state = %s, want downloaded until the new file is verified", got)
	}
	if p := pendingFor(t, db, "V"); len(p) != 1 || !p[0].NeedsContent || p[0].LocalPath != moved.LocalPath {
		t.Fatalf("pending = %+v, want content check of the new path", p)
	}
	events, err := db.Events(context.Background(), []string{RenditionMovedEvent}, 0, 5)
	if err != nil || len(events) != 1 || !strings.Contains(events[0].Detail, "album-copy") {
		t.Fatalf("events = %+v, %v", events, err)
	}
}

func TestQuarantineIsSticky(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	ingest(t, db, rendition("Q", "original", "video", true))
	if err := db.RecordMismatch(ctx, Key{"PrimarySync", "Q", "original"}, TierStructure, "truncated", t0); err != nil {
		t.Fatal(err)
	}
	if got := state(t, db, "Q"); got != StateQuarantined {
		t.Fatalf("state = %s, want quarantined", got)
	}
	ingest(t, db, rendition("Q", "original", "video", true))
	if got := state(t, db, "Q"); got != StateQuarantined {
		t.Fatalf("re-ingest cleared quarantine: state = %s", got)
	}
	if len(pendingFor(t, db, "Q")) != 0 {
		t.Fatal("quarantined asset offered for verification")
	}
	counts, err := db.StateCounts(ctx)
	if err != nil || counts[StateQuarantined] != 1 {
		t.Fatalf("StateCounts = %v, %v", counts, err)
	}
}

func TestRecordPassUnknownRendition(t *testing.T) {
	db, _ := openTemp(t)
	if err := db.RecordPass(context.Background(), Key{"PrimarySync", "nope", "original"}, TierContent, t0); err == nil {
		t.Fatal("want error for unknown rendition")
	}
	if err := db.RecordPass(context.Background(), Key{"PrimarySync", "nope", "original"}, Tier("bogus"), t0); err == nil {
		t.Fatal("want error for unknown tier")
	}
}
