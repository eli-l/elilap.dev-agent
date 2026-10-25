-- 0003_source_kind_file_post.sql
--
-- The local markdown provider (internal/sources/files) stores its items
-- under the source kind 'file_post'. The CHECK constraint on
-- chunks.source_kind only allowed 'ghost_post' and 'postiz_post', so an
-- index created before this change rejects those rows. SQLite cannot add a
-- constraint to an existing table, so the table is rebuilt: the rows are
-- copied into a table with the widened CHECK, the vectors are carried over
-- by hand, and the indexes are recreated.
--
-- The rebuild is unconditional. On an index that already accepts
-- 'file_post' it copies the same rows into the same shape, so the result
-- is the schema this migration wants either way.

CREATE TABLE chunks_v2 (
  chunk_id       TEXT PRIMARY KEY,           -- sha256(source_kind|source_id|chunk_seq), hex
  source_kind    TEXT NOT NULL CHECK (source_kind IN ('ghost_post','postiz_post','file_post')),
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

INSERT INTO chunks_v2 (
  chunk_id, source_kind, source_id, source_url, platform, author,
  published_at, ingested_at, updated_at, content_hash, title, section,
  body, body_tokens, meta_json, embedded_with, embedded_at
)
SELECT
  chunk_id, source_kind, source_id, source_url, platform, author,
  published_at, ingested_at, updated_at, content_hash, title, section,
  body, body_tokens, meta_json, embedded_with, embedded_at
FROM chunks;

-- The vectors are carried over by hand: dropping the old chunks table
-- would cascade them away, and the new vector table is created with its
-- foreign key after the swap.
CREATE TABLE chunk_vectors_v2 (
  chunk_id TEXT PRIMARY KEY,
  dim      INTEGER NOT NULL,
  vec      BLOB NOT NULL
);

INSERT INTO chunk_vectors_v2 (chunk_id, dim, vec)
SELECT chunk_id, dim, vec FROM chunk_vectors;

DROP TABLE chunk_vectors;
DROP TABLE chunks;

ALTER TABLE chunks_v2 RENAME TO chunks;

CREATE TABLE chunk_vectors (
  chunk_id TEXT PRIMARY KEY REFERENCES chunks(chunk_id) ON DELETE CASCADE,
  dim       INTEGER NOT NULL,
  vec       BLOB NOT NULL
);

INSERT INTO chunk_vectors (chunk_id, dim, vec)
SELECT chunk_id, dim, vec FROM chunk_vectors_v2;

DROP TABLE chunk_vectors_v2;

CREATE INDEX IF NOT EXISTS idx_chunks_source      ON chunks (source_kind, source_id);
CREATE INDEX IF NOT EXISTS idx_chunks_published   ON chunks (published_at);
CREATE INDEX IF NOT EXISTS idx_chunks_hash        ON chunks (content_hash);
