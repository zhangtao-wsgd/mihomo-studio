PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS subscriptions (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL COLLATE NOCASE UNIQUE,
  url TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  last_fetch_at TEXT,
  last_status TEXT NOT NULL DEFAULT 'never_fetched',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS terminals (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  identity_type TEXT NOT NULL CHECK(identity_type IN ('source_ip','cidr','mac','manual')),
  identity_value TEXT NOT NULL,
  proxy_group_id TEXT NOT NULL REFERENCES proxy_groups(id) ON DELETE RESTRICT,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  notes TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS proxy_groups (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL COLLATE NOCASE UNIQUE,
  mihomo_type TEXT NOT NULL CHECK(mihomo_type IN ('select','url-test','fallback','load-balance')),
  selection_mode TEXT NOT NULL CHECK(selection_mode IN ('manual','dynamic')),
  member_node_names_json TEXT NOT NULL DEFAULT '[]',
  member_filter_json TEXT NOT NULL DEFAULT '{}',
  health_url TEXT NOT NULL DEFAULT 'https://www.gstatic.com/generate_204',
  interval_seconds INTEGER NOT NULL DEFAULT 300,
  tolerance_ms INTEGER NOT NULL DEFAULT 50,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS rules (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  rule_type TEXT NOT NULL,
  rule_value TEXT NOT NULL,
  target TEXT NOT NULL,
  priority INTEGER NOT NULL,
  source TEXT NOT NULL CHECK(source IN ('built_in','custom','external')),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS config_candidates (
  id TEXT PRIMARY KEY,
  sha256 TEXT NOT NULL,
  snapshot_path TEXT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('built','validated','validation_failed','published','rejected')),
  validation_message TEXT NOT NULL DEFAULT '',
  source_fingerprint TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS config_candidate_inputs (
  candidate_id TEXT NOT NULL REFERENCES config_candidates(id) ON DELETE RESTRICT,
  subscription_id TEXT NOT NULL,
  content_sha256 TEXT NOT NULL,
  snapshot_path TEXT NOT NULL,
  PRIMARY KEY(candidate_id, subscription_id)
);

CREATE TABLE IF NOT EXISTS deployments (
  id TEXT PRIMARY KEY,
  operation TEXT NOT NULL DEFAULT 'publish' CHECK(operation IN ('publish','rollback')),
  candidate_id TEXT NOT NULL REFERENCES config_candidates(id),
  previous_candidate_id TEXT REFERENCES config_candidates(id),
  status TEXT NOT NULL CHECK(status IN ('prepared','publishing','published_unverified','verified','failed','unknown','rolled_back')),
  runtime_message TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  finished_at TEXT
);

CREATE TABLE IF NOT EXISTS app_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_events (
  id TEXT PRIMARY KEY,
  action TEXT NOT NULL,
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  outcome TEXT NOT NULL,
  detail_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_rules_priority ON rules(priority, created_at);
CREATE INDEX IF NOT EXISTS idx_deployments_status ON deployments(status, started_at);
