-- 0002: record which verification tiers each rendition requires, decided in Go
-- (verify.RequiredTiers) at ingest instead of inferred from media_type in SQL.
-- Backfill mirrors verify.RequiredTiers; TestMigration0002MatchesRequiredTiers
-- keeps the two in agreement.

ALTER TABLE resources ADD COLUMN requires_structure INTEGER NOT NULL DEFAULT 0 CHECK (requires_structure IN (0, 1));
ALTER TABLE resources ADD COLUMN requires_probe     INTEGER NOT NULL DEFAULT 0 CHECK (requires_probe IN (0, 1));

UPDATE resources SET
    requires_probe     = media_type IN ('video', 'live_photo_video'),
    requires_structure = media_type IN ('video', 'live_photo_video')
                         AND (lower(filename) LIKE '%.mov' OR lower(filename) LIKE '%.mp4' OR lower(filename) LIKE '%.m4v');
