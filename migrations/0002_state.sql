-- 0002_state.sql
--
-- Phase 1 ingest ledger.
-- Synced with PLAN.md §2.6. Drives incremental ingest: `source_state` holds
-- the per-provider run bookkeeping, `source_items` the per-item fingerprint
-- used to skip unchanged items without fetching them.

CREATE TABLE IF NOT EXISTS source_state (
  source_id    TEXT PRIMARY KEY,   -- config id
  type         TEXT NOT NULL,
  config_hash  TEXT NOT NULL,      -- hash of the resolved provider config
  last_run_at  TEXT,              -- ISO-8601 UTC; null before the first run
  last_full_at TEXT,              -- ISO-8601 UTC; null until a --full run
  item_count   INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS source_items (
  source_id     TEXT NOT NULL,     -- config id
  item_id       TEXT NOT NULL,     -- Ghost uuid / Postiz id
  url           TEXT,
  content_hash  TEXT,              -- hash of the normalized text
  etag          TEXT,              -- upstream entity tag, when offered
  last_modified TEXT,              -- upstream updated_at, ISO-8601 UTC
  first_seen_at TEXT NOT NULL,     -- ISO-8601 UTC
  last_seen_at  TEXT NOT NULL,     -- ISO-8601 UTC
  seen_run_id   TEXT NOT NULL,     -- run that last saw the item
  PRIMARY KEY (source_id, item_id)
);
