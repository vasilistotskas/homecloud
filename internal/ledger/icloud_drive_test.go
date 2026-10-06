package ledger

import (
	"context"
	"strings"
	"testing"
	"time"
)

func driveFile(id, path string) DriveFile {
	return DriveFile{
		DriveWSID: id, ICloudPath: path, LocalPath: "iCloud Drive/" + path, Etag: "e1", Size: 3,
		AppleSignature: "01ab", SHA256: strings.Repeat("a", 64),
	}
}

func driveEventKinds(t *testing.T, db *DB) []string {
	t.Helper()
	events, err := db.Events(context.Background(), []string{
		EventDriveFileArchived, EventDriveFileChanged, EventDriveFileMoved,
		EventDriveFileDeleted, EventDriveFileRestored, EventDriveFileDamaged,
	}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for i := len(events) - 1; i >= 0; i-- { // oldest first
		if events[i].Library != DriveLibrary {
			t.Fatalf("event %d library %q", events[i].ID, events[i].Library)
		}
		kinds = append(kinds, events[i].Kind)
	}
	return kinds
}

func TestDriveFileLifecycle(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	f := driveFile("FILE::z::1", "Docs/a.pdf")
	if err := db.RecordDriveFile(ctx, f, "", t0); err != nil {
		t.Fatal(err)
	}

	// Changed in iCloud: new bytes, the old copy kept aside.
	f.Etag, f.SHA256 = "e2", strings.Repeat("b", 64)
	if err := db.RecordDriveFile(ctx, f, ".versions/x/Docs/a.pdf", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Renamed in iCloud: metadata only.
	f.ICloudPath, f.LocalPath, f.Etag = "Docs/b.pdf", "iCloud Drive/Docs/b.pdf", "e3"
	if err := db.UpdateDriveFile(ctx, f, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Deleted in iCloud, twice (the second is a no-op), then back again.
	for range 2 {
		if err := db.MarkDriveFilesDeleted(ctx, []string{f.DriveWSID}, t0.Add(3*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	files, err := db.DriveFiles(ctx)
	if err != nil || files[f.DriveWSID].DeletedAt == nil || files[f.DriveWSID].LocalPath != "iCloud Drive/Docs/b.pdf" {
		t.Fatalf("after deletion: %+v, %v", files[f.DriveWSID], err)
	}
	if err = db.UpdateDriveFile(ctx, f, t0.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}

	want := []string{EventDriveFileArchived, EventDriveFileChanged, EventDriveFileMoved, EventDriveFileDeleted, EventDriveFileRestored}
	if got := driveEventKinds(t, db); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v; want %v", got, want)
	}
	files, err = db.DriveFiles(ctx)
	if err != nil || files[f.DriveWSID].DeletedAt != nil || files[f.DriveWSID].SHA256 != strings.Repeat("b", 64) {
		t.Fatalf("after restore: %+v, %v", files[f.DriveWSID], err)
	}
	n, size, deleted, err := db.DriveTotals(ctx)
	if err != nil || n != 1 || size != 3 || deleted != 0 {
		t.Fatalf("totals = %d files, %d bytes, %d deleted, %v", n, size, deleted, err)
	}
}

func TestDriveScrubAndDamage(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	for _, id := range []string{"FILE::z::1", "FILE::z::2", "FILE::z::3"} {
		if err := db.RecordDriveFile(ctx, driveFile(id, id), "", t0); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.RecordDriveScrub(ctx, "FILE::z::1", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkDriveFileDamaged(ctx, "FILE::z::2", "sha256 mismatch", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	batch, eligible, err := db.DriveScrubBatch(ctx, 0.5)
	// Damaged files wait for their re-download; never-checked files go first.
	if err != nil || eligible != 2 || len(batch) != 1 || batch[0].DriveWSID != "FILE::z::3" {
		t.Fatalf("batch = %+v, eligible %d, %v", batch, eligible, err)
	}
	if _, _, zeroErr := db.DriveScrubBatch(ctx, 0); zeroErr == nil {
		t.Fatal("a zero fraction was accepted")
	}

	// A fresh download clears the damage.
	f := driveFile("FILE::z::2", "FILE::z::2")
	if err = db.RecordDriveFile(ctx, f, ".versions/damaged", t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	files, err := db.DriveFiles(ctx)
	if err != nil || files["FILE::z::2"].DamagedAt != nil {
		t.Fatalf("after re-download: %+v, %v", files["FILE::z::2"], err)
	}
}

func TestDriveLocalPathsAreUnique(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	if err := db.RecordDriveFile(ctx, driveFile("FILE::z::1", "a"), "", t0); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordDriveFile(ctx, driveFile("FILE::z::2", "a"), "", t0); err == nil {
		t.Fatal("two files were recorded at one local path")
	}
}
