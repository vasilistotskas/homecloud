package ledger

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunsLifecycle(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	if _, err := db.LastFinishedRun(ctx, "cycle"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LastFinishedRun on empty ledger = %v, want ErrNotFound", err)
	}
	id, err := db.StartRun(ctx, "cycle", t0)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := db.Runs(ctx, "cycle", 0, 5)
	if err != nil || len(runs) != 1 || runs[0].FinishedAt != nil || runs[0].OK != nil {
		t.Fatalf("unfinished run = %+v, %v", runs, err)
	}
	if _, lastErr := db.LastFinishedRun(ctx, "cycle"); !errors.Is(lastErr, ErrNotFound) {
		t.Fatalf("LastFinishedRun with only a running run = %v, want ErrNotFound", lastErr)
	}
	if finishErr := db.FinishRun(ctx, id, true, map[string]int{"verified": 3}, t0.Add(time.Minute)); finishErr != nil {
		t.Fatal(finishErr)
	}
	if againErr := db.FinishRun(ctx, id, false, nil, t0); againErr == nil {
		t.Fatal("finishing a run twice must fail")
	}
	last, err := db.LastFinishedRun(ctx, "cycle")
	if err != nil || last.ID != id || last.OK == nil || !*last.OK || last.Detail != `{"verified":3}` {
		t.Fatalf("finished run = %+v, %v", last, err)
	}
}

func TestRunsPaging(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	for i := range 5 {
		if _, err := db.StartRun(ctx, "cycle", t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.StartRun(ctx, "scrub", t0); err != nil { // id 6, another kind
		t.Fatal(err)
	}
	first, err := db.Runs(ctx, "cycle", 0, 2)
	if err != nil || len(first) != 2 || first[0].ID != 5 || first[1].ID != 4 {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	rest, err := db.Runs(ctx, "cycle", first[1].ID, 10)
	if err != nil || len(rest) != 3 || rest[0].ID != 3 || rest[2].ID != 1 {
		t.Fatalf("second page = %+v, %v", rest, err)
	}
}

func TestEventsFilterAndPage(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	ingest(t, db, rendition("Q", "original", "video", true))
	if err := db.RecordMismatch(ctx, Key{"PrimarySync", "Q", "original"}, TierStructure, "truncated", t0); err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseQuarantine(ctx, "PrimarySync", "Q", "reviewed", t0); err != nil {
		t.Fatal(err)
	}
	events, err := db.Events(ctx, []string{"quarantined", "quarantine_released"}, 0, 10)
	if err != nil || len(events) != 2 || events[0].Kind != "quarantine_released" || events[1].Asset != "Q" {
		t.Fatalf("events = %+v, %v", events, err)
	}
	if events[1].Filename == "" {
		t.Error("event filename should come from the original rendition")
	}
	older, err := db.Events(ctx, []string{"quarantined", "quarantine_released"}, events[0].ID, 10)
	if err != nil || len(older) != 1 || older[0].Kind != "quarantined" {
		t.Fatalf("older events = %+v, %v", older, err)
	}
	none, err := db.Events(ctx, []string{"no_such_kind"}, 0, 10)
	if err != nil || len(none) != 0 {
		t.Fatalf("unexpected events = %+v, %v", none, err)
	}
}

func TestAssetDetail(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	if _, err := db.Asset(ctx, "PrimarySync", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing asset = %v, want ErrNotFound", err)
	}
	ingest(t, db, rendition("V", "original", "video", true))
	pass(t, db, "V", "original", TierContent)
	if err := db.RecordMismatch(ctx, Key{"PrimarySync", "V", "original"}, TierProbe, "no streams", t0); err != nil {
		t.Fatal(err)
	}
	d, err := db.Asset(ctx, "PrimarySync", "V")
	if err != nil {
		t.Fatal(err)
	}
	if d.State != StateQuarantined || len(d.Renditions) != 1 {
		t.Fatalf("asset = %+v", d)
	}
	r := d.Renditions[0]
	if _, ok := r.Passed[TierContent]; !ok || len(r.Passed) != 1 {
		t.Errorf("passed = %v, want content only", r.Passed)
	}
	if r.Failure == nil || r.Failure.Tier != TierProbe || r.Failure.Reason != "no streams" {
		t.Errorf("failure = %+v", r.Failure)
	}
	files, bytes, err := db.ArchiveTotals(ctx)
	if err != nil || files != 1 || bytes != r.SizeBytes {
		t.Errorf("totals = %d files, %d bytes, %v", files, bytes, err)
	}
}

func TestRecordDriveHealthOnlyOnChange(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	steps := []struct {
		state, wantPrev string
		wantChanged     bool
	}{
		{"ok", "", true},
		{"ok", "ok", false},
		{"failing", "ok", true},
		{"failing", "failing", false},
		{"ok", "failing", true},
	}
	for i, s := range steps {
		prev, changed, err := db.RecordDriveHealth(ctx, s.state, nil, t0.Add(time.Duration(i)*time.Hour))
		if err != nil || prev != s.wantPrev || changed != s.wantChanged {
			t.Fatalf("step %d (%s): prev=%q changed=%v err=%v", i, s.state, prev, changed, err)
		}
	}
	events, err := db.Events(ctx, []string{EventDriveHealthChanged}, 0, 10)
	if err != nil || len(events) != 3 || events[0].Asset != "" || events[0].Filename != "" {
		t.Fatalf("events = %+v, %v", events, err)
	}
}

func TestEachManifestRow(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	ingest(t, db, rendition("M", "original", "photo", true))
	pass(t, db, "M", "original", TierContent)
	var rows []ManifestRow
	if err := db.EachManifestRow(ctx, func(m ManifestRow) error { rows = append(rows, m); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].AssetID != "M" || rows[0].State != StateVerified || rows[0].VerifiedAt == nil || rows[0].CreatedAt == nil {
		t.Fatalf("rows = %+v", rows)
	}
	stop := errors.New("stop")
	if err := db.EachManifestRow(ctx, func(ManifestRow) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("callback error = %v, want it returned", err)
	}
}

func TestLastCompletedRunSkipsRunsThatCouldNotWork(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	finish := func(ok bool, detail map[string]any) int64 {
		t.Helper()
		id, err := db.StartRun(ctx, "scrub", t0)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.FinishRun(ctx, id, ok, detail, t0); err != nil {
			t.Fatal(err)
		}
		return id
	}
	findings := finish(false, map[string]any{"mismatched": 1}) // did its work, found a problem
	finish(false, map[string]any{"error": "volume not mounted"})
	got, err := db.LastCompletedRun(ctx, "scrub")
	if err != nil || got.ID != findings {
		t.Fatalf("LastCompletedRun = %d, %v; want run %d", got.ID, err, findings)
	}
	if _, err := db.LastCompletedRun(ctx, "ledger_backup"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no runs of kind: %v, want ErrNotFound", err)
	}
}
