-- 0004: per-folder state of local iPhone backups, the alert throttle, and an
-- index for "the last event of a kind" lookups.

-- One row per backup folder ever seen. The folder (its absolute path) is the
-- key: two folders can carry the same UDID (a copy, or one per backup tool).
CREATE TABLE iphone_backups (
    folder               TEXT    NOT NULL PRIMARY KEY,
    udid                 TEXT    NOT NULL,
    device_name          TEXT    NOT NULL,
    state                TEXT    NOT NULL,
    -- Size of the last backup judged to have a normal size.
    baseline_bytes       INTEGER NOT NULL DEFAULT 0 CHECK (baseline_bytes >= 0),
    -- A much smaller backup, accepted as the new normal once a later backup
    -- confirms the size.
    pending_shrink_bytes INTEGER NOT NULL DEFAULT 0 CHECK (pending_shrink_bytes >= 0),
    -- Completion time of the backup last judged.
    judged_completed_at  TEXT,
    -- When every file of the folder was last read in full, and how many
    -- files that read could not read.
    last_full_read_at    TEXT,
    unreadable_files     INTEGER NOT NULL DEFAULT 0 CHECK (unreadable_files >= 0),
    checked_at           TEXT    NOT NULL
) STRICT;

-- When each throttled alert was last sent, so restarts do not resend them.
CREATE TABLE alert_throttle (
    key     TEXT NOT NULL PRIMARY KEY,
    sent_at TEXT NOT NULL
) STRICT;

CREATE INDEX events_kind ON events (kind, id);
