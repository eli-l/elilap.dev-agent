---
type: Run
title: Ghost windowed ingest (last week)
description: A windowed Ghost ingest indexed two posts from the previous seven days into the SQLite index with FTS rows, then reported index counts.
status: stable
generated:
  by: command-code/deepseek/deepseek-v4.1-flash
  at: 2026-11-02T18:49:18Z
run:
  type: agent-ingest
  cmd: ./agent ingest --source ghost --since 7d
output: |
  ghost: indexed 2 item(s) since 2026-10-26T18:49:18Z: fetched 2, chunks +17/~0, removed 0
  $ ./agent stats
  db:        ./index.db
  chunks:    17
  fts rows:  17
  vec rows:  0
  embedded:  0
  sources:   1
  last run:  2026-11-02T18:49:18Z
---

# Result

The first live Ghost ingest succeeded against the Content API on
`read.elilap.dev`. Two posts were published or edited inside the seven-day
window, producing 17 markdown-aware chunks (heading sections preserved) and
17 FTS5 rows. A follow-up incremental run fetched 0 items, confirming the
ledger fingerprint is idempotent. That run also showed why a bounded
incremental list must not delete unseen items: the older post fell outside
the watermark window and was initially misclassified as drift. Drift deletion
now requires an unbounded (full) list.

Embedding is intentionally a separate command and is still a placeholder, so
`vec rows` and `embedded` are zero: FTS search works, hybrid retrieval does
not yet.
