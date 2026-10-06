package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PairedDevice is a device paired with the agent.
type PairedDevice struct {
	ID         string
	Name       string
	PairedAt   time.Time
	LastSeenAt time.Time
}

// AddPairedDevice records a newly paired device by the SHA-256 of its token.
func (db *DB) AddPairedDevice(ctx context.Context, id, name string, tokenSHA256 [32]byte, now time.Time) error {
	at := formatTime(now)
	if _, err := db.w.ExecContext(ctx, `INSERT INTO paired_devices (id, name, token_sha256, paired_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?)`, id, name, tokenSHA256[:], at, at); err != nil {
		return fmt.Errorf("add paired device: %w", err)
	}
	return nil
}

// PairedDeviceByToken returns the id of the device, not revoked, whose token
// has the given SHA-256, or ErrNotFound.
func (db *DB) PairedDeviceByToken(ctx context.Context, tokenSHA256 [32]byte) (string, error) {
	var id string
	err := db.r.QueryRowContext(ctx, `SELECT id FROM paired_devices WHERE token_sha256 = ? AND revoked_at IS NULL`,
		tokenSHA256[:]).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("paired device: %w", ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("paired device: %w", err)
	}
	return id, nil
}

// TouchPairedDevice records that a device was seen.
func (db *DB) TouchPairedDevice(ctx context.Context, id string, now time.Time) error {
	if _, err := db.w.ExecContext(ctx, `UPDATE paired_devices SET last_seen_at = ? WHERE id = ?`, formatTime(now), id); err != nil {
		return fmt.Errorf("touch paired device: %w", err)
	}
	return nil
}

// PairedDevices returns the devices that are not revoked, most recently
// paired first.
func (db *DB) PairedDevices(ctx context.Context) (_ []PairedDevice, err error) {
	rows, err := db.r.QueryContext(ctx, `SELECT id, name, paired_at, last_seen_at FROM paired_devices
		WHERE revoked_at IS NULL ORDER BY paired_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("query paired devices: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var out []PairedDevice
	for rows.Next() {
		var d PairedDevice
		var paired, seen string
		if scanErr := rows.Scan(&d.ID, &d.Name, &paired, &seen); scanErr != nil {
			return nil, fmt.Errorf("scan paired device: %w", scanErr)
		}
		if d.PairedAt, err = parseTime(paired); err != nil {
			return nil, err
		}
		if d.LastSeenAt, err = parseTime(seen); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate paired devices: %w", err)
	}
	return out, nil
}

// RevokePairedDevice ends a device's access, or returns ErrNotFound when no
// such device is paired.
func (db *DB) RevokePairedDevice(ctx context.Context, id string, now time.Time) error {
	res, err := db.w.ExecContext(ctx, `UPDATE paired_devices SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		formatTime(now), id)
	if err != nil {
		return fmt.Errorf("revoke paired device: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke paired device: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("revoke paired device %s: %w", id, ErrNotFound)
	}
	return nil
}
