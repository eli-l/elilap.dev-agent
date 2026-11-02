---
type: Run
title: Ghost full-corpus ingest (clean rebuild)
description: A clean-database full Ghost ingest fetched all 126 published posts through the Content API and yielded 588 chunks and FTS rows in under ten seconds; the follow-up incremental run fetched nothing.
status: stable
generated:
  by: command-code/deepseek/deepseek-v4.1-flash
  at: 2026-11-02T19:05:00Z
run:
  type: agent-ingest
  cmd: ./agent ingest --source ghost --log-level warn
output: |
  ghost: indexed 126 item(s) since all: fetched 126, chunks +588/~0, removed 0
  $ ./agent stats
  db:        ./index.db
  chunks:    588
  fts rows:  588
  vec rows:  0
  embedded:  0
  sources:   1
  last run:  2026-11-02T18:58:33Z
  $ ./agent ingest --source ghost --log-level warn
  ghost: indexed 1 item(s) since 2026-11-01T18:58:33Z: fetched 0, chunks +0/~0, removed 0
---

# Result

With `index.db` deleted, one unbounded Ghost run reproduced the whole corpus
from the Content API: **126 published posts**, **588 chunks**, and **588 FTS5
rows** in roughly eight seconds. The 126-item list spans two pages (the
Content API caps `limit` at 100, so the configured 500 is clamped and the
provider follows `meta.pagination.next`).

The immediate incremental rerun listed one candidate inside its
`last_run_at - overlap` watermark and fetched **0** items: the ledger
fingerprint (`updated_at`) already matched, so re-running ingest is a
near-no-op. `vec rows` and `embedded` are zero because `agent embed` is still
a placeholder; FTS search works, hybrid retrieval does not yet.

Verification queries on the rebuilt index:

```
sqlite3 index.db "select count(*), sum(length(body)) from chunks;"
588|550124
sqlite3 index.db "select title from chunks_fts where chunks_fts match 'sqlite' order by bm25(chunks_fts) limit 3;"
From idea to implementation in 24 hours. Served on localhost.
```

# Changes this run verified

* The shared provider HTTP client now retries transient failures
  (`429`, `408`, `5xx`, network errors) with exponential backoff and honors
  `Retry-After`; a single upstream hiccup no longer aborts a 126-request
  run. Non-replayable requests (a `POST` without `GetBody`) are never
  retried, and a canceled/timed-out run context stops retrying immediately.
* The Admin API's list limit is clamped to 100 as well, so the configured
  `limit: 500` cannot be forwarded as an over-limit request.
* `include_drafts: true` with a Content API key now logs a warning: the
  Content API only returns published posts, so the option cannot take
  effect.
