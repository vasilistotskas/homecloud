-- 0001: core ledger for ingest and verification (P1).
-- Timestamps are RFC 3339 UTC text. Tables are STRICT.

CREATE TABLE assets (
    library          TEXT NOT NULL,
    asset_id         TEXT NOT NULL,
    state            TEXT NOT NULL CHECK (state IN ('discovered', 'downloaded', 'verified', 'quarantined')),
    state_changed_at TEXT NOT NULL,
    created_at       TEXT,
    added_at         TEXT,
    last_seen_at     TEXT,
    PRIMARY KEY (library, asset_id)
) STRICT;

-- One row per downloaded rendition (kei manifest grain: asset_id + version).
CREATE TABLE resources (
    library               TEXT    NOT NULL,
    asset_id              TEXT    NOT NULL,
    version               TEXT    NOT NULL,
    filename              TEXT    NOT NULL,
    local_path            TEXT    NOT NULL UNIQUE,
    media_type            TEXT    NOT NULL CHECK (media_type IN ('photo', 'video', 'live_photo_image', 'live_photo_video')),
    size_bytes            INTEGER NOT NULL CHECK (size_bytes >= 0),
    kei_sha256            TEXT    NOT NULL CHECK (length(kei_sha256) = 64),
    verified_content_at   TEXT,
    verified_structure_at TEXT,
    verified_probe_at     TEXT,
    last_scrub_at         TEXT,
    failure               TEXT,
    PRIMARY KEY (library, asset_id, version),
    FOREIGN KEY (library, asset_id) REFERENCES assets (library, asset_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX resources_scrub ON resources (last_scrub_at);

CREATE TABLE runs (
    id          INTEGER PRIMARY KEY,
    kind        TEXT    NOT NULL,
    started_at  TEXT    NOT NULL,
    finished_at TEXT,
    ok          INTEGER CHECK (ok IN (0, 1)),
    detail      TEXT    NOT NULL DEFAULT '{}'
) STRICT;

-- Append-only audit trail.
CREATE TABLE events (
    id       INTEGER PRIMARY KEY,
    at       TEXT NOT NULL,
    library  TEXT,
    asset_id TEXT,
    kind     TEXT NOT NULL,
    detail   TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE INDEX events_asset ON events (library, asset_id);

CREATE TRIGGER events_no_update BEFORE UPDATE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are append-only');
END;

CREATE TRIGGER events_no_delete BEFORE DELETE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are append-only');
END;
