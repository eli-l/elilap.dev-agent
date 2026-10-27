# elilap.dev-agent

Local-first **agentic A2A connector** over Eli's published writing.
Indexes Ghost posts (blog) + Postiz cross-posts (social), answers questions
from them with its own grounded tool-calling loop — retrieval is the loop's
internal `search_writing` tool — and exposes that agent to remote peers
over A2A (Phase 4).

This repo is **infrastructure**, not the writing itself. The corpus lives
upstream on Ghost and Postiz; the index is a derived artifact you can delete
and rebuild at any time.

## Status

Bootstrap only. No code yet. See [`PLAN.md`](./PLAN.md) for the
build plan and current phase.

## Concept

```
Ghost (blog)      ─┐
                   ├─►  ingest  ─►  SQLite + sqlite-vec ─►  search_writing
Postiz (social)   ─┘        ▲                                (hybrid RRF)
                            │                                     ▲
                     chat + embeddings                            │ tool call
                            │                                     │
                 LocalAI (self-hosted)  ◄── agent loop ──  question
              (`spark-x2.5-4b`, `embeddinggemma-300m`)   (`ask` today,
                                                          A2A in Phase 4)
```

* **One Go binary** with subcommands: `ingest`, `embed`, `query`, `ask`
  (Phase 2), `stats`, `serve` (Phase 2 dev form: token-less, loopback;
  token-gated A2A in Phase 4).
* **One SQLite file** (`index.db`) you can `cp` to back up.
* **Hybrid retrieval**: BM25 (FTS5) + vector (sqlite-vec) with RRF merging.
* **Model-agnostic**: self-hosted LocalAI by default
  (`http://192.168.78.243:8080`) or any OpenAI-compatible server — one
  config switch.
* **Models**: embeddings `embeddinggemma-300m` (768d), chat `spark-x2.5-4b`.
  Both self-hosted; no API keys, no cloud calls.
* **Agentic**: the service runs the loop — one internal tool
  (`search_writing`), bounded iterations, citation-or-refusal. `agent ask`
  is the CLI form; the Phase 2 dev server exposes the same loop and the
  raw retriever over loopback HTTP (`/v1/retrieve`, `/v1/ask`); A2A
  `message/send` + agent card land in Phase 4 on the same paths.

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
* Free-form generation beyond the grounded loop — answers come only from
  the corpus (citation-or-refusal), never from open-domain chat; the loop
  itself stays model-agnostic via the OpenAI-compatible connector.
* Multi-agent orchestration — one loop, one internal tool; peer-side
  agents are the callers' systems.

## See also

* [`PLAN.md`](./PLAN.md) — full step-by-step build plan with exit criteria.
