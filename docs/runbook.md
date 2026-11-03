---
type: Guide
title: Runbook - build, ingest, embed, query, ask, serve
description: The operator path from a clean checkout to grounded answers - building the binary, rebuilding the index, running hybrid queries and the agent loop, the loopback dev server, the retrieval eval, and the failures you are likely to hit.
status: draft
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-02T22:00:00Z
tags: [runbook, operations, cli, http, eval, troubleshooting]
sources: ["../PLAN.md", "../AGENTS.md"]
---

# Runbook

## Prerequisites

* Go 1.27 and a CGO toolchain (the SQLite driver needs both).
* A populated `.env` (start from `example.env`; credentials live only there).
* The model host in `LOCALAI_BASE_URL` reachable:
  `curl "${LOCALAI_BASE_URL%/v1}/readyz"` (the default is
  `http://192.168.78.243:8080`). It serves both embeddings (`EMBED_MODEL`)
  and chat (`CHAT_MODEL`).

## Build

```bash
go mod vendor                  # after any go.mod/go.sum change; vendor/ is gitignored
go build -tags sqlite_fts5 -o agent ./cmd/agent
```

Every command below is read-only over the index except `ingest` and `embed`.

## Rebuild the index

```bash
./agent ingest                 # Ghost + Postiz, incremental by default
./agent ingest --full          # re-fetch and re-chunk everything
./agent embed --progress       # vectorize pending chunks; resumable
./agent stats                  # chunks / fts rows / vec rows / embedded / sources
```

`chunks`, `chunks_fts`, and `chunks_vec` should report the same count, and
`embedded` should equal `vec rows`. A second `./agent ingest` is a near no-op.

## Query (no LLM)

```bash
./agent query "harness vendor lock-in"                       # text, top 5
./agent query --format jsonl --top 10 "agents and memory"     # one JSON per chunk
./agent query --source ghost --after 2026-01-01 "burnout"
./agent query --source postiz --platform linkedin "harness"
./agent query --recency-halflife-days 90 "what changed lately"
./agent query --hybrid=false --format jsonl "vector only"
```

`score` is RRF in hybrid mode and cosine with `--hybrid=false`; `distance`,
`bm25`, and `cosine` show the path-native numbers.

## Ask (grounded loop)

```bash
./agent ask "What does Eli say about harnesses and vendor lock-in?"
./agent ask --format json --timeout 120s "What did he build at the hackathon?"
./agent ask "What is the capital of France?"     # -> "Not in my published writing"
```

Every claim must cite a retrieved `chunk_id`; a reply without a resolvable
citation, or with one the loop never retrieved, is replaced by the refusal
string. `tool_iterations` counts tool-executing rounds (max 3 by default).
On this CPU host an answer takes roughly 10-25 seconds.

## Dev server

```bash
./agent serve                          # 127.0.0.1:8080, loopback only
./agent serve --addr 127.0.0.1:18080 --db ./index.db
```

```bash
curl -sS -X POST http://127.0.0.1:8080/v1/retrieve \
  -H 'Content-Type: application/json' \
  -d '{"query":"harness vendor lock-in","top_n":3,"source":"ghost"}'

curl -sS -X POST http://127.0.0.1:8080/v1/ask \
  -H 'Content-Type: application/json' \
  -d '{"question":"What does Eli say about harnesses?"}'
```

The dev server is token-less; a non-loopback `--addr` is a startup error.

## Evaluate retrieval

```bash
go run -mod=vendor ./scripts/eval
```

This runs the 20 human-verified queries in `eval/queries.yaml` through
`agent query --format jsonl` and fails below the threshold (15/20). The
current baseline is 19/20; the known miss is `local-pipeline-state`, where
the "state became more important than intelligence" section does not surface
in the top five.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `no index at ...; run agent ingest and agent embed first` | The index is missing or empty. Run ingest, then embed. |
| `index has no embeddings` | `./agent embed` has not run or wrote no rows; run it, then check `agent stats`. |
| `index vectors are stamped X, but EMBED_MODEL=Y expects Z` | The index was embedded by another model/text template. Run `./agent embed` (the tag makes every chunk pending) or point `EMBED_MODEL` at the model that built it. |
| `--addr ... is not loopback` | The dev server refuses to bind a public interface by design. Use `127.0.0.1:<port>`. |
| `llm: embeddings returned ...` / `chat returned 503` | The model host is down or the model is not loaded. Check `/readyz` and `curl $LOCALAI_BASE_URL/models`. |
| Answers are refusals for answerable questions | Model non-determinism on a small chat model; retry once, then inspect `./agent query` for the same wording. If retrieval misses, improve the query or the index. |
| `agent query` says `provider is not enabled`-style config errors | `sources.yaml` scope: run `./agent sources validate`. |

Logs go to stderr. Use `--log-level debug` or `AGENT_LOG_LEVEL=debug`; the
HTTP server logs one line per request on the `http` module logger and never
logs prompts, excerpts, or bodies.
