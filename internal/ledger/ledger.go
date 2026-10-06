// Package ledger is homecloud's SQLite store of assets, verification state and audit events.
//
// It uses one writer connection (SQLite allows a single writer) and a separate
// read-only pool, both in WAL mode.
package ledger

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// readerConns bounds the read-only pool.
const readerConns = 4

// DB is an open ledger.
type DB struct {
	w *sql.DB // single writer connection
	r *sql.DB // read-only pool
}

// Open opens (creating if needed) the ledger at file and applies pending migrations.
func Open(ctx context.Context, file string) (*DB, error) {
	common := "_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"

	w, err := sql.Open("sqlite", fileURI(file, common+"&_txlock=immediate"))
	if err != nil {
		return nil, fmt.Errorf("open ledger writer: %w", err)
	}
	w.SetMaxOpenConns(1)

	db := &DB{w: w}
	if migrateErr := db.migrate(ctx); migrateErr != nil {
		return nil, errors.Join(migrateErr, w.Close())
	}

	r, err := sql.Open("sqlite", fileURI(file, common+"&_pragma=query_only(1)"))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open ledger reader: %w", err), w.Close())
	}
	r.SetMaxOpenConns(readerConns)
	db.r = r
	return db, nil
}

// fileURI is the SQLite URI (https://sqlite.org/uri.html) of the database at
// file, an absolute path, with query: the path is percent-encoded, so "?",
// "#" and "%" in a folder name stay part of it, and a Windows drive path is
// written file:///C:/... .
func fileURI(file, query string) string {
	p := filepath.ToSlash(file)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: query}).String()
}

// Close closes both pools.
func (db *DB) Close() error {
	var errs []error
	if db.r != nil {
		errs = append(errs, db.r.Close())
	}
	errs = append(errs, db.w.Close())
	return errors.Join(errs...)
}

// integrityCheck runs PRAGMA integrity_check on conn and returns an error
// unless it reports "ok".
func integrityCheck(ctx context.Context, conn *sql.DB) (err error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("integrity_check: %w", err)
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("integrity_check failed: %s", strings.Join(problems, "; "))
	}
	return nil
}

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations returns embedded migrations ordered by version. Files are named
// NNNN_description.sql and must be numbered contiguously from 1.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var ms []migration
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %q: name must be NNNN_description.sql", e.Name())
		}
		v, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration %q: bad version prefix: %w", e.Name(), err)
		}
		body, err := migrationFS.ReadFile(path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", e.Name(), err)
		}
		ms = append(ms, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %q: expected version %d", m.name, i+1)
		}
	}
	return ms, nil
}

func (db *DB) migrate(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	var current int
	if err := db.w.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if current > len(ms) {
		return fmt.Errorf("ledger schema version %d is newer than this binary supports (%d)", current, len(ms))
	}
	for _, m := range ms[current:] {
		if err := db.apply(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) apply(ctx context.Context, m migration) (err error) {
	tx, err := db.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration %s: begin: %w", m.name, err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if _, err = tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	// PRAGMA does not accept bound parameters; the version is an int we control.
	if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return fmt.Errorf("migration %s: set user_version: %w", m.name, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("migration %s: commit: %w", m.name, err)
	}
	return nil
}
