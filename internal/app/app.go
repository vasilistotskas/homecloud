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
	"github.com/vasilistotskas/homecloud/internal/parallel"
	"github.com/vasilistotskas/homecloud/internal/verify"
)

// LedgerWriterLock names the single-instance lock held by whichever process
// writes the ledger: the agent for its whole life, or a CLI command that
// verifies or releases quarantine while the agent is stopped.
const LedgerWriterLock = "homecloud-ledger-writer"

// LedgerFile is the ledger's file name inside storage.root.
const LedgerFile = "homecloud.db"

// LedgerPath returns the ledger location for cfg.
func LedgerPath(cfg *config.Config) string {
	return filepath.Join(cfg.Storage.Root, LedgerFile)
}

// Renditions maps kei manifest rows to ledger renditions.
func Renditions(rows []kei.ManifestRow) []ledger.Rendition {
	out := make([]ledger.Rendition, 0, len(rows))
	for _, r := range rows {
		out = append(out, rendition(r))
	}
	return out
}

func rendition(r kei.ManifestRow) ledger.Rendition {
	structure, probe := verify.RequiredTiers(string(r.MediaType), r.Filename)
	rd := ledger.Rendition{
		Library:           r.Library,
		AssetID:           r.AssetID,
		Version:           r.Version,
		Filename:          r.Filename,
		Downloaded:        r.Status == kei.StatusDownloaded,
		SizeBytes:         r.SizeBytes,
		MediaType:         string(r.MediaType),
		RequiresStructure: structure,
		RequiresProbe:     probe,
		CreatedAt:         r.CreatedAt,
		AddedAt:           r.AddedAt,
		SeenAt:            r.LastSeenAt,
	}
	if rd.Downloaded {
		// Validate guarantees both are present for downloaded rows.
		rd.LocalPath = *r.LocalPath
		rd.SHA256 = *r.DownloadChecksum
		rd.DownloadedAt = r.DownloadedAt
	}
	return rd
}

// Ingest reads kei's manifest and records it in the ledger.
func Ingest(ctx context.Context, cfg *config.Config, db *ledger.DB, now time.Time) (ledger.IngestStats, error) {
	rows, err := kei.Manifest(ctx, cfg.Kei, kei.DownloadDir(cfg.Storage.Root))
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
// progress is called after each rendition is recorded.
func Verify(ctx context.Context, cfg *config.Config, db *ledger.DB, log *slog.Logger, now func() time.Time,
	progress func(),
) (VerifyStats, error) {
	pending, err := db.PendingVerification(ctx)
	if err != nil {
		return VerifyStats{}, fmt.Errorf("verify: %w", err)
	}

	var (
		mu    sync.Mutex
		stats VerifyStats
	)
	record := func(f func(*VerifyStats)) { mu.Lock(); f(&stats); mu.Unlock() }
	parallel.ForEach(ctx, pending, cfg.Verify.Workers, func(p ledger.PendingResource) {
		outcome := verifyOne(ctx, cfg, db, p, now)
		record(func(s *VerifyStats) {
			s.Checked++
			switch {
			case outcome == nil:
				s.Passed++
			case errors.Is(outcome, verify.ErrMismatch):
				s.Mismatched++
				log.WarnContext(ctx, "verification mismatch; asset quarantined", "library", p.Library, "asset", p.AssetID, "version", p.Version, "err", outcome)
			default:
				s.Errored++
				log.ErrorContext(ctx, "file not verified; checked again next cycle", "library", p.Library, "asset", p.AssetID, "version", p.Version, "err", outcome)
			}
		})
		progress()
	})
	if err := ctx.Err(); err != nil {
		return stats, fmt.Errorf("verify: %w", err)
	}
	return stats, nil
}

// verifyOne runs the outstanding tiers for p in order. It returns nil when all
// passed, an ErrMismatch-wrapping error once the asset is quarantined, or a
// transient error: a mismatch whose quarantine could not be recorded is one, so
// it is retried rather than reported as quarantined.
func verifyOne(ctx context.Context, cfg *config.Config, db *ledger.DB, p ledger.PendingResource, now func() time.Time) error {
	type step struct {
		need bool
		tier ledger.Tier
		run  func() error
	}
	steps := []step{
		{p.NeedsContent, ledger.TierContent, func() error { return verify.Content(p.LocalPath, p.SizeBytes, p.SHA256) }},
		{p.NeedsStructure, ledger.TierStructure, func() error { return verify.Structure(p.LocalPath) }},
		{p.NeedsProbe, ledger.TierProbe, func() error { return verify.Probe(ctx, cfg.FFmpeg, p.LocalPath) }},
	}
	for _, s := range steps {
		if !s.need {
			continue
		}
		if err := s.run(); err != nil {
			if errors.Is(err, verify.ErrMismatch) {
				if recErr := db.RecordMismatch(ctx, p.Key, s.tier, err.Error(), now()); recErr != nil {
					return fmt.Errorf("record %s mismatch (%v): %w", s.tier, err, recErr) //nolint:errorlint // the mismatch must not match ErrMismatch: nothing was quarantined
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
