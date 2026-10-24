package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// EditPair is an asset edited in Apple's Photos: the edit and the original
// it was made from, by their files in the archive.
type EditPair struct {
	Edit, Original string
}

// EditPairs returns every asset outside hiddenDir (as LibraryFilter.HiddenDir)
// that has both an edit and its original, in no order. A Live Photo's pair is
// its stills; its videos go with them.
func (db *DB) EditPairs(ctx context.Context, hiddenDir string) (pairs []EditPair, err error) {
	rows, err := db.r.QueryContext(ctx, `SELECT e.local_path, o.local_path FROM resources e
		JOIN resources o ON o.library = e.library AND o.asset_id = e.asset_id AND o.version = 'original'
		WHERE e.version = 'adjusted' AND NOT EXISTS (SELECT 1 FROM resources h
			WHERE h.library = e.library AND h.asset_id = e.asset_id
			AND substr(h.local_path, 1, length(:hidden_dir)) = :hidden_dir)`,
		sql.Named("hidden_dir", hiddenDir))
	if err != nil {
		return nil, fmt.Errorf("query edit pairs: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var p EditPair
		if err := rows.Scan(&p.Edit, &p.Original); err != nil {
			return nil, fmt.Errorf("scan edit pair: %w", err)
		}
		pairs = append(pairs, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate edit pairs: %w", err)
	}
	return pairs, nil
}
