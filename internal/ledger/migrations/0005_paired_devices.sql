-- 0005: devices paired with the agent (the desktop app on another computer).
-- Only a SHA-256 of each device's token is kept. Rows are never deleted: a
-- revoked device keeps its row, with revoked_at, as a record.
CREATE TABLE paired_devices (
    id           TEXT NOT NULL PRIMARY KEY,
    name         TEXT NOT NULL,
    token_sha256 BLOB NOT NULL UNIQUE CHECK (length(token_sha256) = 32),
    paired_at    TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    revoked_at   TEXT
) STRICT;
