package ledger

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// Timestamps are stored as RFC 3339 UTC text with nanoseconds.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t, nil
}

func parseNullTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil //nolint:nilnil // absent timestamp is not an error
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Errors returned by operator actions.
var (
	// ErrNotQuarantined: the asset is not in quarantine.
	ErrNotQuarantined = errors.New("asset is not quarantined")
	// ErrReasonRequired: an audited action was requested without a reason.
	ErrReasonRequired = errors.New("a reason is required")
	// ErrRepairInProgress: the asset's repair has not finished.
	ErrRepairInProgress = errors.New("a repair of this asset is in progress")
)
