package ledger

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestIPhoneBackupRecordsAndStateEvents(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	judged := t0.Add(-time.Hour)
	rec := IPhoneBackupRecord{
		Folder: `C:\Backups\UDID-A`, UDID: "UDID-A", DeviceName: "iPhone", State: "ok",
		BaselineBytes: 1000, JudgedCompletedAt: &judged, LastFullReadAt: &t0, UnreadableFiles: 2,
	}
	for i, state := range []string{"ok", "ok", "stale"} {
		rec.State = state
		if err := db.SaveIPhoneBackup(ctx, rec, []string{"why"}, t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	// A second folder with the same UDID is a separate record.
	other := rec
	other.Folder = `D:\Copy\UDID-A`
	if err := db.SaveIPhoneBackup(ctx, other, nil, t0); err != nil {
		t.Fatal(err)
	}
	recs, err := db.IPhoneBackupRecords(ctx)
	got := recs[rec.Folder]
	if err != nil || len(recs) != 2 || got.State != "stale" || got.BaselineBytes != 1000 || got.UnreadableFiles != 2 ||
		!got.JudgedCompletedAt.Equal(judged) || !got.LastFullReadAt.Equal(t0) {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	events, err := db.Events(ctx, EventQuery{Kinds: []string{EventIPhoneBackupChanged}}, 0, 10)
	if err != nil || len(events) != 3 { // ok, stale, and the other folder's first state
		t.Fatalf("events = %d, %v; want 3", len(events), err)
	}
}

func TestAlertThrottleSurvivesReopening(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ledger.db")
	ctx := context.Background()
	open := func() *DB {
		t.Helper()
		db, err := Open(ctx, file)
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open()
	if due, err := db.AlertDue(ctx, "k", 24*time.Hour, t0); err != nil || !due {
		t.Fatalf("first alert: %v, %v", due, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// After a restart the alert is still throttled, then due a day later.
	db = open()
	t.Cleanup(func() { _ = db.Close() })
	if due, err := db.AlertDue(ctx, "k", 24*time.Hour, t0.Add(time.Hour)); err != nil || due {
		t.Fatalf("after reopening: %v, %v", due, err)
	}
	if due, err := db.AlertDue(ctx, "k", 24*time.Hour, t0.Add(25*time.Hour)); err != nil || !due {
		t.Fatalf("a day later: %v, %v", due, err)
	}
}
