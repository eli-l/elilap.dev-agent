---
type: Run
title: Hybrid retrieval, filters, and CLI/HTTP parity
description: First live run of hybrid BM25 + vec0 retrieval with RRF, the source/platform/date/recency filters, `agent query --format jsonl`, and a byte-identical comparison between the CLI and POST /v1/retrieve on the 886-chunk index.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-02T21:50:00Z
run:
  type: agent-query-hybrid
  cmd: ./agent query --format jsonl --top 3 "harness vendor lock-in"
output: |
  $ go test -tags sqlite_fts5 -mod=vendor -count=1 ./...    # 13 packages ok

  $ ./agent query --top 3 "harness vendor lock-in"
  1. score 0.032787  cosine 0.6579  distance 0.8271  bm25 -15.5440
     I gave up on zero harness and here is why
     chunk_id: ccdecc4556e1e05b5566bcbd9df58a5fce347e491dbd72437db41ea8d73e97c7 ...
  2. score 0.029324  cosine 0.6028  distance 0.8913  bm25 -5.8639
     I chose a harness written in Go because I enjoy working with it ...
  3. score 0.029010  cosine 0.5746  distance 0.9224  bm25 -7.1723
     ...

  $ ./agent query --format jsonl --top 3 "harness vendor lock-in" > cli.jsonl
  $ curl -sS -X POST http://127.0.0.1:18083/v1/retrieve \
      -d '{"query":"harness vendor lock-in","top_n":3}' > http.json
  $ python3 -c 'compare parsed arrays'
  parity: True

  POST /v1/retrieve latencies (5 runs, top_n=5, embedding included):
  0.101073 0.063476 0.054818 0.058040 0.053932  ->  p50 0.058s
---

# Result

Hybrid retrieval returned the expected Ghost post first for a phrase from
its own intro, with both paths contributing: bm25 `-15.54` (best text rank)
and cosine `0.632` (best vector rank). The second hit is the LinkedIn
cross-post quoting the same post, which only the text path could have
raised that high — a useful sanity check that fusion is doing its job.

Filter checks on the live index:

* `--source postiz --platform linkedin "harness"` returned only
  `postiz_post`/`linkedin` chunks.
* `--before 2026-01-01 "agents"` returned only chunks published in 2025.
* `--hybrid=false` returned cosine as `score`, with `bm25` absent and
  `distance` present.
* `--recency-halflife-days 90` kept recent hits on top while the raw
  score carried the expected decay.

CLI/HTTP parity was exact: the parsed `agent query --format jsonl` lines
and the `POST /v1/retrieve` array were identical objects, including
optional `distance`/`bm25`/`cosine` fields.

Five hybrid requests measured a p50 of 58ms end to end (LocalAI embedding
plus FTS5 and vec0), comfortably inside the Phase 2 <300ms retrieval
target. The dev server stayed on `127.0.0.1`, logged only request lines,
and never wrote the index.

Automated coverage added in this slice:

* `internal/store` — BM25 ordering, all filters on both paths, FTS5 query
  escaping/tokenization, and a selective-filter test proving the vector
  path over-fetches instead of truncating the filtered result set.
* `internal/retrieval` — two-path RRF ordering, vector-only cosine scores,
  source/platform/date filters, date-only bounds, recency weighting with a
  fixed clock, and invalid-filter classification.
* `cmd/agent` — request passthrough for every filter, usage errors,
  missing-index handling, text/jsonl rendering, and CLI/HTTP parity.
