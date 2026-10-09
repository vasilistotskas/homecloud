-- 0007: repairs of quarantined renditions. An operator asks for a repair; the
-- agent then moves the damaged file aside (kept_as, archive-relative) so the
-- downloader sees it missing and downloads it again. The asset stays
-- quarantined until every row of the repair is downloaded again; the rows are
-- then deleted (the events keep the history).

CREATE TABLE rendition_repairs (
    library      TEXT NOT NULL,
    asset_id     TEXT NOT NULL,
    version      TEXT NOT NULL,
    requested_at TEXT NOT NULL,
    -- When the file left its recorded path, or was found already missing
    -- there. NULL until the agent's next cycle does it.
    set_aside_at TEXT,
    -- Where the damaged file was moved, relative to storage.root with '/'.
    -- NULL when the file was already missing.
    kept_as      TEXT,
    PRIMARY KEY (library, asset_id, version),
    FOREIGN KEY (library, asset_id, version) REFERENCES resources (library, asset_id, version) ON DELETE RESTRICT,
    CHECK (kept_as IS NULL OR set_aside_at IS NOT NULL)
) STRICT;
