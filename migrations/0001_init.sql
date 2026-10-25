-- 0001_init.sql
--
-- Phase 1 initial schema.
-- Synced with PLAN.md §3 (Phase 1). Final field list lands after
-- Phase 0 captures real upstream response shapes.

CREATE TABLE IF NOT EXISTS chunks (
  chunk_id       TEXT PRIMARY KEY,           -- sha256(source_kind|source_id|chunk_seq), hex
  source_kind    TEXT NOT NULL CHECK (source_kind IN ('ghost_post','postiz_post')),
  source_id      TEXT NOT NULL,              -- upstream item id (Ghost uuid / Postiz id)
  source_url     TEXT,
  platform       TEXT,
  author         TEXT NOT NULL,
  published_at   TEXT NOT NULL,
  ingested_at    TEXT NOT NULL,              -- local write time
  updated_at     TEXT NOT NULL,              -- upstream last-modified time
  content_hash   TEXT NOT NULL,
  title          TEXT,
  section        TEXT,
  body           TEXT NOT NULL,
  body_tokens    INTEGER NOT NULL,
  meta_json      TEXT NOT NULL DEFAULT '{}',
  embedded_with  TEXT,
  embedded_at    TEXT
);

CREATE INDEX IF NOT EXISTS idx_chunks_source      ON chunks (source_kind, source_id);
CREATE INDEX IF NOT EXISTS idx_chunks_published   ON chunks (published_at);
CREATE INDEX IF NOT EXISTS idx_chunks_hash        ON chunks (content_hash);

-- Self-contained FTS5 (NOT contentless): stored columns are readable, so
-- matches join back to chunk_id directly. `tags` values come from
-- meta_json at write time; the store keeps chunks and chunks_fts in sync
-- inside one transaction.
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
  chunk_id UNINDEXED, title, body, tags,
  tokenize='porter unicode61'
);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_vec USING vec0(
  chunk_id TEXT PRIMARY KEY,
  embedding float[768]
);
