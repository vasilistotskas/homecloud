package ledger

import (
	"context"
	"testing"
	"time"
)

func TestRecordPhotosActivityOnlyOnChange(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	if last, err := db.LastPhotosActivity(ctx); err != nil || last != "" {
		t.Fatalf("before any check = %q, %v; want none", last, err)
	}
	newest := t0.Add(-30 * 24 * time.Hour)
	steps := []struct {
		state    PhotosActivity
		previous PhotosActivity
		changed  bool
	}{
		{PhotosQuiet, "", true},
		{PhotosQuiet, PhotosQuiet, false},
		{PhotosActive, PhotosQuiet, true},
		{PhotosActive, PhotosActive, false},
	}
	for i, s := range steps {
		prev, changed, err := db.RecordPhotosActivity(ctx, s.state, newest, 14*24*time.Hour, t0.Add(time.Duration(i)*time.Hour))
		if err != nil || prev != s.previous || changed != s.changed {
			t.Fatalf("step %d: %q, %v, %v; want %q, %v", i, prev, changed, err, s.previous, s.changed)
		}
	}
	if last, err := db.LastPhotosActivity(ctx); err != nil || last != PhotosActive {
		t.Fatalf("last = %q, %v; want active", last, err)
	}
	events, err := db.Events(ctx, EventQuery{Kinds: []string{EventPhotosActivityChanged}}, 0, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %v, %v; want one per change", events, err)
	}
}

func TestNewestAddition(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	if newest, err := db.NewestAddition(ctx); err != nil || newest != nil {
		t.Fatalf("empty ledger = %v, %v; want none", newest, err)
	}
	whole, half := t0, t0.Add(500*time.Millisecond)
	a, b := rendition("A", "original", "photo", true), rendition("B", "original", "photo", true)
	// "…00Z" sorts after "…00.5Z" as text; the newest is the later instant.
	a.AddedAt, b.AddedAt = &whole, &half
	ingest(t, db, a, b)
	newest, err := db.NewestAddition(ctx)
	if err != nil || newest == nil || !newest.Equal(half) {
		t.Fatalf("newest addition = %v, %v; want %v", newest, err, half)
	}
}
