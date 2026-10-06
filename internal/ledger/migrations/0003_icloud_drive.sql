-- 0003: the iCloud Drive archive (P2). One row per iCloud Drive file ever
-- archived; rows are never removed. A file deleted in iCloud keeps its row and
-- its archived copy, with deleted_in_icloud_at set.

CREATE TABLE icloud_drive_files (
    drivewsid            TEXT    NOT NULL PRIMARY KEY,
    -- The file's path in iCloud, '/'-separated, as last seen.
    icloud_path          TEXT    NOT NULL,
    -- The archived copy, relative to the archive's Drive folder.
    local_path           TEXT    NOT NULL UNIQUE,
    etag                 TEXT    NOT NULL,
    size_bytes           INTEGER NOT NULL CHECK (size_bytes >= 0),
    modified_at          TEXT,
    -- Packages (iWork documents and other bundles) are archived as the ZIP
    -- iCloud serves.
    package              INTEGER NOT NULL CHECK (package IN (0, 1)),
    -- Apple's content signature of a plain file, hex; checked at download.
    apple_signature      TEXT,
    sha256               TEXT    NOT NULL CHECK (length(sha256) = 64),
    archived_at          TEXT    NOT NULL,
    last_scrub_at        TEXT,
    -- Set when the archived copy no longer matches sha256: the next sync
    -- downloads the file again.
    damaged_at           TEXT,
    deleted_in_icloud_at TEXT
) STRICT;

CREATE INDEX icloud_drive_files_scrub ON icloud_drive_files (last_scrub_at);
