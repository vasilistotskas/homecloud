package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Snapshot writes a consistent, compacted copy of the ledger to dest. The copy
// is built beside dest, must pass an integrity check, and only then replaces
// dest, so dest is always either absent or a complete, checked ledger.
func (db *DB) Snapshot(ctx context.Context, dest string) (err error) {
	partial := dest + ".partial"
	if rmErr := os.Remove(partial); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		return fmt.Errorf("remove stale %s: %w", partial, rmErr)
	}
	defer func() {
		if err != nil {
			if rmErr := os.Remove(partial); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				err = errors.Join(err, rmErr)
			}
		}
	}()

	// VACUUM INTO reads one transaction-consistent view of the live ledger.
	if _, err := db.w.ExecContext(ctx, `VACUUM INTO ?`, partial); err != nil {
		return fmt.Errorf("snapshot ledger to %s: %w", partial, err)
	}
	if err := checkFile(ctx, partial); err != nil {
		return err
	}
	if err := os.Rename(partial, dest); err != nil {
		return fmt.Errorf("publish snapshot %s: %w", dest, err)
	}
	return nil
}

// checkFile runs an integrity check on the SQLite database at file, read-only.
func checkFile(ctx context.Context, file string) (err error) {
	conn, err := sql.Open("sqlite", fileURI(file, "mode=ro"))
	if err != nil {
		return fmt.Errorf("open %s: %w", file, err)
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	if err := integrityCheck(ctx, conn); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return nil
}
