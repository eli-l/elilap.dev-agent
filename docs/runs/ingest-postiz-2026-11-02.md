---
type: Run
title: Postiz full-corpus ingest
description: The first live Postiz public-API ingest indexed 280 LinkedIn cross-posts into 298 chunks and FTS rows, and an immediate incremental rerun fetched nothing.
status: stable
generated:
  by: z-ai/glm-5.3-flashx
  at: 2026-11-02T20:12:00Z
run:
  type: agent-ingest
  cmd: ./agent ingest --source postiz
output: |
  postiz: indexed 280 item(s) since all: fetched 280, chunks +298/~0, removed 0
  $ ./agent ingest --source postiz
  postiz: indexed 2 item(s) since 2026-11-01T20:05:23Z: fetched 0, chunks +0/~0, removed 0
  $ ./agent stats
  chunks:    886
  fts rows:  886
  sources:   2
---

# Result

The first live Postiz ingest ran against the public API of
`postiz.mist.lv`. The listing returned 1,710 posts for the whole history;
`options.platforms: [linkedin]` and the state filter (published only, no
drafts, no failures, no scheduled posts) narrowed that to 280 items, which
produced 298 chunks and 298 FTS rows (a handful of long posts split at the
320-token cap). The chunk total includes the 588 Ghost chunks already in the
index.

An immediate incremental rerun listed the two posts inside the
`last_run_at - 24h` watermark and fetched zero, confirming that the
content-state ETag is idempotent even though Postiz exposes no modified time.

Two capture notes that shaped the implementation:

* The public listing endpoint is `/api/public/v1/posts` and **requires**
  `startDate`/`endDate`; the older `/api/posts` route answers `401` with an
  empty body, and `page`/`limit` are ignored.
* There is no read-by-id endpoint (`404`), so `Fetch` serves the listing made
  during the same run.

The ingest needs the `sqlite_fts5` build tag, as every run does:
`go run -tags sqlite_fts5 ./cmd/agent ingest --source postiz`.

Embedding is still a separate, unimplemented command, so `embedded` and
`vec rows` are zero.
