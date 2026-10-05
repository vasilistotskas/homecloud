// Package app wires homecloud's components into jobs.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
	"github.com/vasilistotskas/homecloud/internal/kei"
	"github.com/vasilistotskas/homecloud/internal/ledger"
	"github.com/vasilistotskas/homecloud/internal/verify"
)

// LedgerFile is the ledger's file name inside storage.root.
const LedgerFile = "homecloud.db"

// verifyWorkers bounds concurrent verification (disk- and hash-bound).
const verifyWorkers = 4

// LedgerPath returns the ledger location for cfg.
func LedgerPath(cfg *config.Config) string {
	return filepath.Join(cfg.Storage.Root, LedgerFile)
}

// Renditions maps kei manifest rows to ledger renditions.
func Renditions(rows []kei.ManifestRow) []ledger.Rendition {
	out := make([]ledger.Rendition, 0, len(rows))
	for _, r := range rows {
		rd := ledger.Rendition{
			Library:    r.Library,
			AssetID:    r.AssetID,
			Version:    r.Version,
			Filename:   r.Filename,
			Downloaded: r.Status == kei.StatusDownloaded,
			SizeBytes:  r.SizeBytes,
			MediaType:  string(r.MediaType),
			CreatedAt:  r.CreatedAt,
			AddedAt:    r.AddedAt,
			SeenAt:     r.LastSeenAt,
		}
		if rd.Downloaded {
			// Validate guarantees both are present for downloaded rows.
			rd.LocalPath = *r.LocalPath
			rd.SHA256 = *r.DownloadChecksum
		}
		out = append(out, rd)
	}
	return out
}

// Ingest reads kei's manifest and records it in the ledger.
func Ingest(ctx context.Context, cfg *config.Config, db *ledger.DB, now time.Time) (ledger.IngestStats, error) {
	rows, err := kei.Manifest(ctx, cfg.Kei)
	if err != nil {
		return ledger.IngestStats{}, fmt.Errorf("ingest: %w", err)
	}
	stats, err := db.Ingest(ctx, Renditions(rows), now)
	if err != nil {
		return stats, fmt.Errorf("ingest: %w", err)
	}
	return stats, nil
}

// VerifyStats summarises a verification run.
type VerifyStats struct {
	Checked, Passed, Mismatched, Errored int
}

// Verify runs every outstanding verification tier once. Mismatches quarantine
// the asset; transient errors are logged and retried on the next run.
func Verify(ctx context.Context, cfg *config.Config, db *ledger.DB, log *slog.Logger, now func() time.Time) (VerifyStats, error) {
	pending, err := db.PendingVerification(ctx, -1)
	if err != nil {
		return VerifyStats{}, fmt.Errorf("verify: %w", err)
	}

	var (
		mu    sync.Mutex
		stats VerifyStats
		wg    sync.WaitGroup
		work  = make(chan ledger.PendingResource)
	)
	record := func(f func(*VerifyStats)) { mu.Lock(); f(&stats); mu.Unlock() }

	for range verifyWorkers {
		wg.Go(func() {
			for p := range work {
				outcome := verifyOne(ctx, cfg, db, p, now)
				record(func(s *VerifyStats) {
					s.Checked++
					switch {
					case outcome == nil:
						s.Passed++
					case errors.Is(outcome, verify.ErrMismatch):
						s.Mismatched++
						log.Warn("verification mismatch; asset quarantined", "asset", p.AssetID, "version", p.Version, "err", outcome)
					default:
						s.Errored++
						log.Error("verification error; will retry", "asset", p.AssetID, "version", p.Version, "err", outcome)
					}
				})
			}
		})
	}
	for _, p := range pending {
		if ctx.Err() != nil {
			break
		}
		work <- p
	}
	close(work)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return stats, fmt.Errorf("verify: %w", err)
	}
	return stats, nil
}

// verifyOne runs the outstanding tiers for p in order. It returns nil when all
// passed, an ErrMismatch-wrapping error after quarantining, or a transient error.
func verifyOne(ctx context.Context, cfg *config.Config, db *ledger.DB, p ledger.PendingResource, now func() time.Time) error {
	type step struct {
		need bool
		tier ledger.Tier
		run  func() error
	}
	steps := []step{
		{p.NeedsContent, ledger.TierContent, func() error { return verify.Content(p.LocalPath, p.SizeBytes, p.SHA256) }},
		{p.NeedsStructure, ledger.TierStructure, func() error { return verify.Structure(p.LocalPath) }},
		{p.NeedsProbe, ledger.TierProbe, func() error { return verify.Probe(ctx, cfg.FFprobe, p.LocalPath) }},
	}
	for _, s := range steps {
		if !s.need {
			continue
		}
		if err := s.run(); err != nil {
			if errors.Is(err, verify.ErrMismatch) {
				if recErr := db.RecordMismatch(ctx, p.Key, s.tier, err.Error(), now()); recErr != nil {
					return errors.Join(err, recErr)
				}
			}
			return err
		}
		if err := db.RecordPass(ctx, p.Key, s.tier, now()); err != nil {
			return fmt.Errorf("record %s pass: %w", s.tier, err)
		}
	}
	return nil
}
