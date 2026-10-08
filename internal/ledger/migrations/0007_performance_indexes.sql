-- 0007: performance indexes for verification scans and timeline ordering.
CREATE INDEX IF NOT EXISTS assets_state ON assets (state, state_changed_at);
CREATE INDEX IF NOT EXISTS assets_created_at ON assets (created_at DESC, asset_id DESC);
