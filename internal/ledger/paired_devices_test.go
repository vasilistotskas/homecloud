package ledger

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestPairedDevices(t *testing.T) {
	ctx := context.Background()
	db, openErr := Open(ctx, filepath.Join(t.TempDir(), "ledger.db"))
	if openErr != nil {
		t.Fatal(openErr)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	laptop, desk := sha256.Sum256([]byte("laptop token")), sha256.Sum256([]byte("desk token"))
	if err := db.AddPairedDevice(ctx, "a", "Laptop", laptop, t0); err != nil {
		t.Fatal(err)
	}
	if err := db.AddPairedDevice(ctx, "b", "Desk", desk, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.AddPairedDevice(ctx, "c", "Clone", laptop, t0); err == nil {
		t.Fatal("two devices with one token")
	}

	if id, err := db.PairedDeviceByToken(ctx, laptop); err != nil || id != "a" {
		t.Fatalf("by token = %q, %v", id, err)
	}
	if _, err := db.PairedDeviceByToken(ctx, sha256.Sum256([]byte("guess"))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token = %v, want ErrNotFound", err)
	}
	if err := db.TouchPairedDevice(ctx, "a", t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	devices, err := db.PairedDevices(ctx)
	if err != nil || len(devices) != 2 || devices[0].ID != "b" || !devices[1].LastSeenAt.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("devices = %+v, %v", devices, err)
	}

	if err := db.RevokePairedDevice(ctx, "a", t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PairedDeviceByToken(ctx, laptop); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token = %v, want ErrNotFound", err)
	}
	if err := db.RevokePairedDevice(ctx, "a", t0.Add(4*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking twice = %v, want ErrNotFound", err)
	}
	if devices, err := db.PairedDevices(ctx); err != nil || len(devices) != 1 || devices[0].ID != "b" {
		t.Fatalf("after revoke = %+v, %v", devices, err)
	}
}
