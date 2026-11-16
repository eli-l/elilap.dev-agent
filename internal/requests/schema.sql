-- Deferred knowledge requests: questions a peer submitted because the corpus
-- did not answer them. This is original data, not derived data: it lives in
-- its own SQLite file so `rm data/index.db*` and a full re-ingest cannot erase
-- it (PRODUCT.md, PLAN.md Decision 26).
--
-- The question text is stored here on purpose. It never reaches the audit
-- stream or an ordinary log line, and it is readable only through the local
-- owner CLI (`agent requests`) or by the peer that submitted it, through its
-- own A2A task id.

CREATE TABLE IF NOT EXISTS knowledge_requests (
  id                TEXT PRIMARY KEY,           -- 'req_<32 hex chars>'
  submitted_at      TEXT NOT NULL,              -- ISO-8601 UTC
  updated_at        TEXT NOT NULL,              -- ISO-8601 UTC
  question          TEXT NOT NULL,
  reason            TEXT NOT NULL DEFAULT '',   -- requester-supplied, short
  context_text      TEXT NOT NULL DEFAULT '',   -- requester-supplied, bounded
  principal         TEXT NOT NULL,              -- agent:<id>/user:<id>/token:<id>/ip:<addr>
  token_id          TEXT NOT NULL DEFAULT '',   -- authenticated token id, if any
  idempotency_key   TEXT NOT NULL DEFAULT '',
  origin            TEXT NOT NULL DEFAULT 'peer_request'
                    CHECK (origin IN ('insufficient_evidence','partial','peer_request')),
  answer_outcome    TEXT NOT NULL DEFAULT '',   -- the outcome that led to the submission
  corpus_revision   TEXT NOT NULL DEFAULT '',   -- reserved: index revision at submission
  context_id        TEXT NOT NULL DEFAULT '',   -- the A2A context the peer sent
  status            TEXT NOT NULL DEFAULT 'new'
                    CHECK (status IN ('new','reviewed','resolved','declined','canceled')),
  note              TEXT NOT NULL DEFAULT '',   -- owner triage note (never corpus evidence)
  source_url        TEXT NOT NULL DEFAULT '',   -- what Eli published to answer it
  answer            TEXT NOT NULL DEFAULT '',   -- the grounded answer, on resolve
  citations         TEXT NOT NULL DEFAULT '[]', -- JSON array, as returned by the loop
  sources           TEXT NOT NULL DEFAULT '[]', -- JSON array, as returned by the loop
  reviewed_at       TEXT,
  resolved_at       TEXT,
  push_url          TEXT NOT NULL DEFAULT '',   -- peer-registered A2A push target
  push_token        TEXT NOT NULL DEFAULT '',   -- never serialized to JSON
  push_attempts     INTEGER NOT NULL DEFAULT 0,
  push_last_error   TEXT NOT NULL DEFAULT '',
  push_delivered_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_requests_status
  ON knowledge_requests (status, submitted_at);

-- One submission per idempotency key per requester: a retried submission
-- returns the same request instead of recording the question twice.
CREATE UNIQUE INDEX IF NOT EXISTS idx_requests_idempotency
  ON knowledge_requests (principal, idempotency_key)
  WHERE idempotency_key <> '';
