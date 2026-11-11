---
type: Run
title: Local state rebuild on main
description: Rebuilt the binary, ingested all three sources incrementally, embedded the pending chunks, and verified table counts, an idempotent rerun, and a web-source retrieval hit on 2026-11-11.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-11T07:10:21Z
tags: [validation, ingest, embed, index, web]
run:
  type: local-state-rebuild
  cmd: |
    go build -tags sqlite_fts5 -o agent ./cmd/agent
    ./agent sources validate
    ./agent ingest --timeout 20m
    ./agent stats
    ./agent embed --dry-run
    ./agent embed --timeout 15m
    ./agent stats
    ./agent query --source web --top 3 "OKF knowledge bundle"
    ./agent ingest --timeout 10m
output: |
  sources.yaml: ok (version 1, 3 source(s), 3 enabled, 3 provider type(s) validated)

  # ingest: one summary row per source
  {"source_id":"ghost","source_kind":"ghost_post","since":"2026-11-01T20:21:22Z","listed":3,"indexed":2,"unchanged":1,"chunks_added":5,"chunks_updated":6,"removed":0,"dry_run":false}
  {"source_id":"postiz","source_kind":"postiz_post","since":"2026-11-01T20:29:16Z","listed":7,"indexed":6,"unchanged":1,"chunks_added":7,"chunks_updated":0,"removed":0,"dry_run":false}
  {"source_id":"website","source_kind":"web_page","since":null,"listed":12,"indexed":12,"unchanged":0,"chunks_added":56,"chunks_updated":0,"removed":0,"dry_run":false}

  db:        ./index.db
  chunks:    949
  fts rows:  949
  vec rows:  875
  embedded:  875
  sources:   3

  {"model":"embeddinggemma-300m","tag":"localai:embeddinggemma-300m/text-v1","full":false,"dry_run":true,"pending":74,"embedded":0,"remaining":0,"batches":0}
  {"model":"embeddinggemma-300m","tag":"localai:embeddinggemma-300m/text-v1","full":false,"pending":74,"embedded":74,"remaining":0,"batches":3}

  db:        ./index.db
  chunks:    949
  fts rows:  949
  vec rows:  949
  embedded:  949
  sources:   3
  last run:  2026-11-11T07:09:20Z

  1. score 0.032258  cosine 0.6364  distance 0.8527  bm25 -16.8379
     Knowledge that survives the session. / Questions
     https://elilap.dev/ai/okf-builder
     chunk_id: 117b9ec8046836378a0bb52c704ed0164610715d123b2a8522d82af86c0abbc5  source: web_page  published: 2026-10-23T05:41:34Z

  # second ingest: no changes
  {"source_id":"ghost","source_kind":"ghost_post","since":"2026-11-10T07:09:18Z","listed":0,"indexed":0,"unchanged":0,"chunks_added":0,"chunks_updated":0,"removed":0,"dry_run":false}
  {"source_id":"postiz","source_kind":"postiz_post","since":"2026-11-10T07:09:18Z","listed":1,"indexed":0,"unchanged":1,"chunks_added":0,"chunks_updated":0,"removed":0,"dry_run":false}
  {"source_id":"website","source_kind":"web_page","since":"2026-11-10T07:09:20Z","listed":0,"indexed":0,"unchanged":0,"chunks_added":0,"chunks_updated":0,"removed":0,"dry_run":false}
  exit 0
---

# Result

The checkout's `./agent` binary and `./index.db` predated the website provider
(they were built/last ingested on 2026-11-06 and 2026-11-02), so the old binary
rejected `type: web` and the index held only Ghost and Postiz chunks. This run
brought the derived local state up to the code on `main`.

- **Binary and config**: a fresh build accepts the committed `sources.yaml`
  with all three provider types (`ghost`, `postiz`, `web`); no credentials are
  echoed by `sources validate`.
- **Migration**: the index predated the `web_page` kind, so `agent ingest`
  applied `migrations/0003_web_kind.sql` before writing the first web chunks.
- **Ingest**: the incremental pass updated Ghost (2 posts) and Postiz (6
  cross-posts), then added the 12 discovered website pages as 56 chunks. The
  content-free `/cases` route was skipped with one warning, as documented in
  [HTML website provider and per-site extraction strategies](/web.md).
- **Embed**: 74 chunks were pending after ingest; the resumable pass embedded
  them in 3 batches and left `chunks`, `chunks_fts`, and `chunks_vec` in
  agreement at 949 rows.
- **Idempotency**: a second `agent ingest` listed zero changed items and wrote
  nothing.
- **Retrieval**: a `--source web` query returned a real `web_page` hit
  (`elilap.dev/ai/okf-builder`) with hybrid `score`/`cosine`/`distance`/`bm25`
  fields, proving the site's chunks participate in the same RRF path as the
  other sources.

This is a local derived-state check, not a quality baseline. The chat-model
drift that blocks a shipped-model Ragas run is recorded in
[Ragas answer evaluation (evals)](/ragas-eval.md) and
[Self-hosted LocalAI endpoint and model catalog](/localai.md).
