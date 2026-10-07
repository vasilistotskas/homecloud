-- 0006: verified copies of iPhone backups kept in the archive
-- (<storage.root>/iPhone backups/<UDID>/<stamp>, internal/iphonecopy). One row
-- per copy on disk; retention removes the row with the copy, the events stay.
CREATE TABLE iphone_backup_copies (
    udid          TEXT    NOT NULL,
    -- When the copied backup finished, in UTC: the copy's folder name.
    stamp         TEXT    NOT NULL,
    device_name   TEXT    NOT NULL,
    files         INTEGER NOT NULL CHECK (files >= 0),
    bytes         INTEGER NOT NULL CHECK (bytes >= 0),
    -- Bytes written for this copy; the rest is shared with the copy before.
    copied_bytes  INTEGER NOT NULL CHECK (copied_bytes >= 0),
    created_at    TEXT    NOT NULL,
    -- When every file was last checked against the copy's manifest, and how
    -- many did not match or could not be read.
    verified_at   TEXT,
    damaged_files INTEGER NOT NULL DEFAULT 0 CHECK (damaged_files >= 0),
    PRIMARY KEY (udid, stamp)
) STRICT;
