package ledger

import (
	"encoding/json/v2"
	"testing"
	"time"
)

func TestIPhoneBackupCopies(t *testing.T) {
	db, _ := openTemp(t)
	ctx := t.Context()
	older := IPhoneBackupCopy{
		UDID: "00008110-001A2B3C4D5E801E", Stamp: "20261006T030000Z", DeviceName: "iPhone",
		Files: 7, Bytes: 700, CopiedBytes: 700, CreatedAt: t0,
	}
	newer := older
	newer.Stamp, newer.CopiedBytes, newer.CreatedAt = "20261007T030000Z", 30, t0.Add(24*time.Hour)
	for _, c := range []IPhoneBackupCopy{older, newer} {
		if err := db.AddIPhoneBackupCopy(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AddIPhoneBackupCopy(ctx, newer); err == nil {
		t.Fatal("the same copy was recorded twice")
	}
	copies, err := db.IPhoneBackupCopies(ctx)
	if err != nil || len(copies) != 2 || copies[0].Stamp != newer.Stamp || copies[1].VerifiedAt != nil {
		t.Fatalf("copies = %+v, %v", copies, err)
	}

	// A check that finds the same damage again records no new event.
	verified := t0.Add(48 * time.Hour)
	for range 2 {
		copies, err = db.IPhoneBackupCopies(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.SetIPhoneBackupCopyVerified(ctx, copies[1], 2, verified); err != nil {
			t.Fatal(err)
		}
	}
	copies, err = db.IPhoneBackupCopies(ctx)
	if err != nil || copies[1].DamagedFiles != 2 || copies[1].VerifiedAt == nil || !copies[1].VerifiedAt.Equal(verified) {
		t.Fatalf("verified copy = %+v, %v", copies[1], err)
	}

	if err = db.RemoveIPhoneBackupCopy(ctx, copies[1], CopyRemovedRetention, verified); err != nil {
		t.Fatal(err)
	}
	if err = db.RecordIPhoneBackupPushRefused(ctx, newer.UDID, "20261008T030000Z", "a file does not match", verified); err != nil {
		t.Fatal(err)
	}
	copies, err = db.IPhoneBackupCopies(ctx)
	if err != nil || len(copies) != 1 || copies[0].Stamp != newer.Stamp {
		t.Fatalf("copies after removal = %+v, %v", copies, err)
	}

	kinds := []string{EventIPhoneBackupCopied, EventIPhoneBackupCopyDamaged, EventIPhoneBackupCopyRemoved, EventIPhoneBackupPushRefused}
	events, err := db.Events(ctx, kinds, 0, 10)
	if err != nil || len(events) != 5 {
		t.Fatalf("events = %+v, %v", events, err)
	}
	var removed struct {
		Stamp  string `json:"stamp"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(events[1].Detail), &removed); err != nil || removed.Stamp != older.Stamp || removed.Reason != CopyRemovedRetention {
		t.Fatalf("removal event = %+v (%s), %v", removed, events[1].Detail, err)
	}
}
