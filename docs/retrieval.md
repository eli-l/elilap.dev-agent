---
type: Reference
title: Hybrid retrieval and the /v1/retrieve + agent query surfaces
description: The hybrid retrieval engine behind search_writing - FTS5 BM25 plus vec0 KNN merged with RRF k=60, the source/platform/date/recency filters, the JSON hit shape, and the CLI plus loopback HTTP surfaces that expose it.
status: draft
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-03T15:45:00Z
tags: [retrieval, hybrid, rrf, bm25, vectors, http, cli, sqlite-vec]
sources: ["../PLAN.md", "../AGENTS.md"]
---

# Hybrid retrieval and the `/v1/retrieve` + `agent query` surfaces

Phase 2's retrieval engine, completed 2026-11-02 in two slices: the
vector-only path first, then FTS5 BM25 + vec0 KNN merged with reciprocal
rank fusion and the shared filter set. `internal/retrieval.Searcher` is the
one implementation behind `agent query` and `POST /v1/retrieve`, so the
CLI and the HTTP surface cannot drift; in Phase 2's loop the same call
becomes the `search_writing` tool.

## Layers

* `internal/store` — `SearchVectors` runs the vec0 KNN; `SearchText` runs
  FTS5 `bm25`; both hydrate the same readable chunk fields and take the
  same `Filter`. `FTSQuery` turns free text into a safe quoted OR query, so
  user punctuation or FTS operators cannot change the query's meaning.
* `internal/retrieval` — embeds the query once with `EMBED_MODEL`, fetches
  both candidate lists, merges them with RRF, applies recency weighting,
  and returns the JSON `Hit` shape.
* `cmd/agent/query.go` — `agent query`: the raw tool, no LLM, text/jsonl
  output.
* `cmd/agent/serve.go` — `agent serve`: `POST /v1/retrieve`, loopback-only
  and token-less, read-only index, plus the model-tag startup guard in
  `cmd/agent/indexcheck.go` (also used by `agent query`).

The corpus is embedded with `title`, `section`, and `body` joined by blank
lines (see [embeddings.md](/embeddings.md)); the query is embedded as the
trimmed request text. Both use the same model, and both surfaces refuse an
index whose `embedded_with` stamp does not match `EMBED_MODEL`.

## Ranking policy

Pinned in `PLAN.md` Phase 2:

* Both paths are fetched at `clamp(top_n * 4, 20, 200)` candidates.
* RRF contribution for a 1-based rank is `1/(60 + rank)`; a chunk found by
  both paths sums both contributions.
* `recency_halflife_days > 0` multiplies the merged score by
  `0.5^(age_days / halflife)`; `0` (the default) disables weighting. Future
  `published_at` values count as age 0.
* `score` is the RRF value in hybrid mode and the cosine similarity when
  the CLI runs `--hybrid=false`; higher is better in both. Ties break on
  `chunk_id`, so the CLI and HTTP order agree.

Each hit also reports the path-native numbers that produced it:

| Field | Meaning |
|---|---|
| `distance` | vec0 L2 distance (present on vector candidates) |
| `bm25` | FTS5 bm25 rank, negative and lower-is-better (text candidates) |
| `cosine` | cosine similarity of query and chunk vectors (vector candidates) |

## Request and filters

`POST /v1/retrieve` and the CLI accept the same parameters:

| HTTP (JSON) | CLI flag | Meaning |
|---|---|---|
| `query` | positional text | required free text |
| `top_n` | `--top` | result count, default 5, max 50 |
| `source` | `--source` | `ghost`, `postiz`, or `all` (default) |
| `platform` | `--platform` (repeatable) | Postiz platform, e.g. `linkedin`; case-insensitive |
| `after` | `--after` | inclusive lower bound, `YYYY-MM-DD` or RFC3339 |
| `before` | `--before` | exclusive upper bound; a date-only value covers that day |
| `recency_halflife_days` | `--recency-halflife-days` | decay half-life in days, default 0 = off |
| — | `--exclude-scheduled` | CLI-only: drop Postiz chunks whose state is `QUEUE` |
| — | `--hybrid=false` | CLI-only: vector-only debug mode |
| — | `--format text\|jsonl` | CLI output, default `text` |

Unknown HTTP fields are rejected, so a typo cannot silently widen the
search. Body cap 64 KiB, request budget 30 s. Empty results are `[]`, never
`null`.

Two indexing details matter for filters:

* **vec0 filters after the KNN.** Asking for only `top_n` rows first would
  drop matching chunks that rank behind non-matching ones. A filtered
  vector search therefore asks for the whole corpus and applies the filter
  in the outer query, which keeps results exact. Above vec0's 4096-row cap
  the search returns an explicit error rather than silently truncating the
  ranked set.
* **`exclude_scheduled` is state-aware.** Postiz scheduled posts are only
  imported when they were due and the config opted in; the flag drops the
  remaining `state = QUEUE` chunks. The SQL uses `COALESCE` so rows without
  a state (Ghost, older Postiz metadata) are not lost to three-valued logic.

## Examples

```bash
# hybrid search, human view
./agent query "harness vendor lock-in"

# tool view: one JSON object per chunk, same objects as the HTTP array
./agent query --format jsonl --top 3 --source ghost "harness vendor lock-in"

# filters + recency weighting
./agent query --top 5 --source postiz --platform linkedin \
  --after 2026-01-01 --before 2026-11-01 --recency-halflife-days 90 "agents"

# vector-only debug view (score becomes cosine)
./agent query --hybrid=false --format jsonl --top 2 "harness"
```

```bash
./agent serve --addr 127.0.0.1:8080

curl -sS -X POST http://127.0.0.1:8080/v1/retrieve \
  -H 'Content-Type: application/json' \
  -d '{"query":"harness vendor lock-in","top_n":3,"source":"ghost",
       "after":"2026-01-01","recency_halflife_days":90}'
```

`agent query --format jsonl` and the HTTP array are rendered from the same
`[]retrieval.Hit`; a live parity check on the 886-chunk index returned
byte-identical objects for identical parameters (see
[runs/retrieval-hybrid-2026-11-02.md](/runs/retrieval-hybrid-2026-11-02.md)).

## Debugging retrieval

`--log-level debug` makes the engine narrate one query on the `db` module:
`retrieval: query` (normalized text, resolved filters, `embed_model`, candidate
depth), `retrieval: candidates` (the generated FTS5 query and how many rows the
vector and text paths each returned), `retrieval: hit` per ranked chunk (rank,
`chunk_id`, title, merged `score`, path-native `distance`/`bm25`/`cosine`, body
word count, and a 200-character preview), and `retrieval: done` (result count,
duration). Nothing is logged at `info`, and the trace works for `agent query`,
`POST /v1/retrieve`, and the loop's `search_writing` tool alike. See
[logging.md](/logging.md) for the payload policy.

## Safety posture

The dev server is token-less, so it refuses to start unless `--addr` names a
loopback host: `:8080`, `0.0.0.0`, and LAN addresses are startup errors.
Default is `127.0.0.1:8080`. Auth, rate limits, and the A2A binding are
Phase 4 work (see [agent-architecture.md](/agent-architecture.md)); the path
and JSON stay the same when they land.

## How it is tested

Retrieval has no eval runner of its own. Quality is graded end to end: the
Ragas suite in `evals/` scores the answers retrieval feeds (faithfulness to the
cited excerpts, context utilization, correctness, refusal discipline), and the
promptfoo suite in `tests-ai/` covers adversarial behaviour. A ranking problem
shows up as a faithfulness or correctness miss on the question that depended
on the chunk. To inspect a single ranking by hand, use
`agent query --format jsonl` and read the `chunk_id`s against the post text.

The grounded loop that consumes this engine (`internal/agentloop`, `agent
ask`, `POST /v1/ask`) is implemented; see
[agent-architecture.md](/agent-architecture.md).
