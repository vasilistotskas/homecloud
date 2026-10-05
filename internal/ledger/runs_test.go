package ledger

import (
	"context"
	"testing"
	"time"
)

func TestRunsLifecycle(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	id, err := db.StartRun(ctx, "cycle", t0)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := db.RecentRuns(ctx, 5)
	if err != nil || len(runs) != 1 || runs[0].FinishedAt != nil || runs[0].OK != nil {
		t.Fatalf("unfinished run = %+v, %v", runs, err)
	}
	if finishErr := db.FinishRun(ctx, id, true, map[string]int{"verified": 3}, t0.Add(time.Minute)); finishErr != nil {
		t.Fatal(finishErr)
	}
	if againErr := db.FinishRun(ctx, id, false, nil, t0); againErr == nil {
		t.Fatal("finishing a run twice must fail")
	}
	runs, err = db.RecentRuns(ctx, 5)
	if err != nil || runs[0].OK == nil || !*runs[0].OK || runs[0].Detail != `{"verified":3}` {
		t.Fatalf("finished run = %+v, %v", runs, err)
	}
}

func TestRecentEventsFiltersByKind(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	ingest(t, db, rendition("Q", "original", "video", true))
	if err := db.RecordMismatch(ctx, Key{"PrimarySync", "Q", "original"}, TierStructure, "truncated", t0); err != nil {
		t.Fatal(err)
	}
	events, err := db.RecentEvents(ctx, []string{"quarantined"}, 10)
	if err != nil || len(events) != 1 || events[0].Asset != "Q" {
		t.Fatalf("events = %+v, %v", events, err)
	}
	none, err := db.RecentEvents(ctx, []string{"no_such_kind"}, 10)
	if err != nil || len(none) != 0 {
		t.Fatalf("unexpected events = %+v, %v", none, err)
	}
}
