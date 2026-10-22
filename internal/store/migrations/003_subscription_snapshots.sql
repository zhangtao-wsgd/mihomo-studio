CREATE TABLE IF NOT EXISTS subscription_snapshots (
  id TEXT PRIMARY KEY,
  subscription_id TEXT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  content_sha256 TEXT NOT NULL,
  snapshot_path TEXT NOT NULL,
  fetched_at TEXT NOT NULL,
  is_current INTEGER NOT NULL DEFAULT 0 CHECK (is_current IN (0,1)),
  proxy_count INTEGER NOT NULL DEFAULT 0,
  UNIQUE(subscription_id, content_sha256)
);
CREATE INDEX IF NOT EXISTS idx_subscription_snapshots_current ON subscription_snapshots(subscription_id, is_current, fetched_at);
