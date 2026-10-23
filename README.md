# elilap.dev-agent

Local-first retrieval-augmented agent over Eli's published writing.
Indexes Ghost posts (blog) + Postiz cross-posts (social), exposes retrieval
and a thin agent wrapper, and is built to be A2A-callable later.

This repo is **infrastructure**, not the writing itself. The corpus lives
upstream on Ghost and Postiz; the index is a derived artifact you can delete
and rebuild at any time.

## Status

Bootstrap only. No code yet. See [`PLAN.md`](./PLAN.md) for the
build plan and current phase.

## Concept

```
Ghost (blog)        ─┐
                     ├─►  ingest  ─►  SQLite + sqlite-vec  ─►  query  ─►  LLM
Postiz (social)     ─┘                                  ▲
                                                        │
                                          LocalAI (self-hosted)
```

* **One Go binary** with subcommands: `ingest`, `embed`, `query`, `stats`.
* **One SQLite file** (`index.db`) you can `cp` to back up.
* **Hybrid retrieval**: BM25 (FTS5) + vector (sqlite-vec) with RRF merging.
* **Model-agnostic**: self-hosted LocalAI by default
  (`http://192.168.78.243:8080`) or any OpenAI-compatible server — one
  config switch.
* **Models**: embeddings `embeddinggemma-300m` (768d), chat `spark-x2.5-4b`.
  Both self-hosted; no API keys, no cloud calls.
* **A2A-ready**: retrieval is exposed over JSON-RPC in a later phase; the CLI
  surface is the source of truth.

## Quick reference (after Phase 1 ships)

```bash
export POSTIZ_API_KEY=...
export POSTIZ_API_URL=https://postiz.mist.lv/api
export GHOST_API_URL=...
export GHOST_API_KEY=...

go build -o agent ./cmd/agent
./agent ingest
./agent embed
./agent query "when did I write about burnout"
```

## What is NOT in this repo

* The corpus itself — owned by Ghost and Postiz.
* Generated embeddings — those live in `index.db` (gitignored).
* Any LLM wrapper code — generation is intentionally out of band so the
  retrieval layer stays model-agnostic.

## See also

* [`PLAN.md`](./PLAN.md) — full step-by-step build plan with exit criteria.
