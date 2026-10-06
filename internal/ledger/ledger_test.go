package ledger

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func openTemp(t *testing.T) (*DB, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "ledger.db")
	db, err := Open(context.Background(), file)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return db, file
}

func TestOpenMigratesToLatest(t *testing.T) {
	db, _ := openTemp(t)
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if got := schemaVersion(t, db); got != len(ms) {
		t.Fatalf("schema version = %d, want %d", got, len(ms))
	}
	if err := integrityCheck(context.Background(), db.r); err != nil {
		t.Fatal(err)
	}
}

// schemaVersion reads PRAGMA user_version, which migrate maintains.
func schemaVersion(t *testing.T, db *DB) int {
	t.Helper()
	var v int
	if err := db.r.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestReopenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	_, file := openTemp(t)
	db2, err := Open(ctx, file)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db2.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	})
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if v := schemaVersion(t, db2); v != len(ms) {
		t.Fatalf("schema version after reopen = %d; want %d", v, len(ms))
	}
}

func TestPragmasApplied(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	var mode string
	if err := db.w.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	var fk int
	if err := db.w.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v", fk, err)
	}
}

func TestReaderIsReadOnly(t *testing.T) {
	db, _ := openTemp(t)
	_, err := db.r.ExecContext(context.Background(), "INSERT INTO runs (kind, started_at) VALUES ('x', 'y')")
	if err == nil {
		t.Fatal("reader pool accepted a write")
	}
}

func TestEventsAppendOnly(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	if _, err := db.w.ExecContext(ctx, "INSERT INTO events (at, kind) VALUES ('2026-10-05T00:00:00Z', 'test')"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{"UPDATE events SET kind = 'changed'", "DELETE FROM events"} {
		_, err := db.w.ExecContext(ctx, stmt)
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: want append-only error, got %v", stmt, err)
		}
	}
}

func TestResourceConstraints(t *testing.T) {
	db, _ := openTemp(t)
	ctx := context.Background()
	exec := func(q string) error { _, err := db.w.ExecContext(ctx, q); return err }

	if err := exec(`INSERT INTO resources (library, asset_id, version, filename, local_path, media_type, size_bytes, kei_sha256)
		VALUES ('L', 'orphan', 'original', 'a.heic', 'p', 'photo', 1, '` + strings.Repeat("a", 64) + `')`); err == nil {
		t.Error("resource without asset accepted (foreign key not enforced)")
	}
	if err := exec(`INSERT INTO assets (library, asset_id, state, state_changed_at) VALUES ('L', 'A', 'downloaded', 't')`); err != nil {
		t.Fatal(err)
	}
	if err := exec(`INSERT INTO assets (library, asset_id, state, state_changed_at) VALUES ('L', 'B', 'nonsense', 't')`); err == nil {
		t.Error("invalid state accepted")
	}
	if err := exec(`INSERT INTO resources (library, asset_id, version, filename, local_path, media_type, size_bytes, kei_sha256)
		VALUES ('L', 'A', 'original', 'a.heic', 'p', 'photo', 1, 'short')`); err == nil {
		t.Error("non-64-char sha256 accepted")
	}
}
