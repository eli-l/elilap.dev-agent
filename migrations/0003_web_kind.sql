-- 0003_web_kind.sql
--
-- The web provider writes chunks with source_kind 'web_page', so the CHECK
-- constraint from 0001_init.sql has to learn the new value. SQLite cannot
-- alter a CHECK constraint in place, so this migration rebuilds the chunks
-- table: create it with the widened constraint, copy every row, drop the old
-- table, rename the new one, and recreate the indexes.
--
-- chunks_fts and chunks_vec reference chunks by chunk_id only and are left
-- untouched; the store writes them in the same transaction as chunks, so they
-- are already consistent with the rows copied here.

CREATE TABLE chunks_0003 (
  chunk_id       TEXT PRIMARY KEY,
  source_kind    TEXT NOT NULL CHECK (source_kind IN ('ghost_post','postiz_post','web_page')),
  source_id      TEXT NOT NULL,
  source_url     TEXT,
  platform       TEXT,
  author         TEXT NOT NULL,
  published_at   TEXT NOT NULL,
  ingested_at    TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  content_hash   TEXT NOT NULL,
  title          TEXT,
  section        TEXT,
  body           TEXT NOT NULL,
  body_tokens    INTEGER NOT NULL,
  meta_json      TEXT NOT NULL DEFAULT '{}',
  embedded_with  TEXT,
  embedded_at    TEXT
);

INSERT INTO chunks_0003 (
  chunk_id, source_kind, source_id, source_url, platform, author,
  published_at, ingested_at, updated_at, content_hash, title, section,
  body, body_tokens, meta_json, embedded_with, embedded_at
)
SELECT
  chunk_id, source_kind, source_id, source_url, platform, author,
  published_at, ingested_at, updated_at, content_hash, title, section,
  body, body_tokens, meta_json, embedded_with, embedded_at
FROM chunks;

DROP TABLE chunks;
ALTER TABLE chunks_0003 RENAME TO chunks;

CREATE INDEX IF NOT EXISTS idx_chunks_source      ON chunks (source_kind, source_id);
CREATE INDEX IF NOT EXISTS idx_chunks_published   ON chunks (published_at);
CREATE INDEX IF NOT EXISTS idx_chunks_hash        ON chunks (content_hash);
