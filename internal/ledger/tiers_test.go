package ledger

import (
	"context"
	"strings"
	"testing"

	"github.com/vasilistotskas/homecloud/internal/verify"
)

func TestGIFVideoNeedsProbeButNotStructure(t *testing.T) {
	db, _ := openTemp(t)
	gif := rendition("G", "original", "video", true)
	gif.Filename = "IMG_0272_Q0JFNjE.GIF"
	gif.RequiresStructure, gif.RequiresProbe = verify.RequiredTiers(gif.MediaType, gif.Filename)
	ingest(t, db, gif)

	p := pendingFor(t, db, "G")
	if len(p) != 1 || !p[0].NeedsContent || p[0].NeedsStructure || !p[0].NeedsProbe {
		t.Fatalf("pending = %+v, want content+probe only", p)
	}
	pass(t, db, "G", "original", TierContent, TierProbe)
	if got := state(t, db, "G"); got != StateVerified {
		t.Fatalf("state = %s, want verified without a structure tier", got)
	}
}

// TestMigration0002MatchesRequiredTiers re-runs migration 0002's backfill and
// checks it agrees with verify.RequiredTiers, the rule used at ingest.
func TestMigration0002MatchesRequiredTiers(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	body, err := migrationFS.ReadFile("migrations/0002_required_tiers.sql")
	if err != nil {
		t.Fatal(err)
	}
	backfill := string(body[strings.Index(string(body), "UPDATE resources SET"):])

	cases := []struct{ media, filename string }{
		{"photo", "a.HEIC"},
		{"photo", "a.jpg"},
		{"live_photo_image", "a.HEIC"},
		{"video", "a.MOV"},
		{"video", "a.mp4"},
		{"video", "a.M4V"},
		{"video", "a.GIF"},
		{"live_photo_video", "a_HEVC.MOV"},
		{"live_photo_video", "a.mov.gif"},
	}
	for i, c := range cases {
		r := rendition("M"+string(rune('a'+i)), "original", c.media, true)
		r.Filename = c.filename
		r.LocalPath += c.filename
		ingest(t, db, r)
	}
	if _, err := db.w.ExecContext(ctx, `UPDATE resources SET requires_structure = 0, requires_probe = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.w.ExecContext(ctx, backfill); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	for _, c := range cases {
		var gotStructure, gotProbe bool
		if err := db.r.QueryRowContext(ctx, `SELECT requires_structure, requires_probe FROM resources WHERE filename = ?`,
			c.filename).Scan(&gotStructure, &gotProbe); err != nil {
			t.Fatal(err)
		}
		wantStructure, wantProbe := verify.RequiredTiers(c.media, c.filename)
		if gotStructure != wantStructure || gotProbe != wantProbe {
			t.Errorf("%s %s: migration (structure=%v probe=%v) != RequiredTiers (%v %v)",
				c.media, c.filename, gotStructure, gotProbe, wantStructure, wantProbe)
		}
	}
}

func TestReleaseQuarantine(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	ingest(t, db, rendition("R", "original", "video", true))
	if err := db.ReleaseQuarantine(ctx, "PrimarySync", "R", "reviewed", t0); err == nil {
		t.Fatal("releasing a non-quarantined asset must fail")
	}
	if err := db.RecordMismatch(ctx, Key{"PrimarySync", "R", "original"}, TierStructure, "bad", t0); err != nil {
		t.Fatal(err)
	}
	if q, err := db.Quarantined(ctx); err != nil || len(q) != 1 || q[0].AssetID != "R" || !strings.Contains(q[0].Failure, "structure: bad") {
		t.Fatalf("Quarantined = %+v, %v", q, err)
	}
	if err := db.ReleaseQuarantine(ctx, "PrimarySync", "R", "", t0); err == nil {
		t.Fatal("release without a reason must fail")
	}
	if err := db.ReleaseQuarantine(ctx, "PrimarySync", "R", "false positive: rule fixed", t0); err != nil {
		t.Fatal(err)
	}
	if got := state(t, db, "R"); got != StateDownloaded {
		t.Fatalf("state = %s, want downloaded", got)
	}
	if p := pendingFor(t, db, "R"); len(p) != 1 {
		t.Fatalf("released asset must be re-verified; pending = %+v", p)
	}
	events, err := db.RecentEvents(ctx, []string{"quarantine_released"}, 5)
	if err != nil || len(events) != 1 || !strings.Contains(events[0].Detail, "rule fixed") {
		t.Fatalf("events = %+v, %v", events, err)
	}
}
