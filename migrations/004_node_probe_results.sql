CREATE TABLE IF NOT EXISTS node_probe_results (
  node_id TEXT PRIMARY KEY,
  subscription_id TEXT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  node_name TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('available','unavailable','not_loaded','error')),
  latency_ms INTEGER,
  checked_at TEXT NOT NULL,
  error_message TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_node_probe_checked_at ON node_probe_results(checked_at);
