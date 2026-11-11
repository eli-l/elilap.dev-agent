# elilap.dev-agent

An agent representing Eli Lap's published technical knowledge, engineering
experience, experiments, and opinions. It answers questions using published
sources and provides verifiable citations.

Local-first **agentic A2A connector** over that writing.
Indexes Ghost posts (blog) + Postiz cross-posts (social) + the public
website's HTML pages, answers questions from them with its own grounded
tool-calling loop — retrieval is the loop's
internal `search_writing` tool — and exposes that agent to remote peers
over A2A v0.3.

This repo is **infrastructure**, not the writing itself. The corpus lives
upstream on Ghost and Postiz plus the public website; the index is a derived
artifact you can delete
and rebuild at any time.

## Status

Phases 1, 2, and the A2A half of Phase 4 are implemented and runnable on a
Go host: ingestion (Ghost, Postiz, and the generic HTML `web` provider with
per-site extraction strategies), resumable embeddings, hybrid retrieval
(BM25 + vectors, RRF), the grounded tool-calling loop (`agent ask`), the dev JSON paths
(`/v1/retrieve`, `/v1/ask`), and the A2A surface — agent card discovery,
`message/send`, bearer tokens, a per-token rate limit, and the audit stream.
Phase 3 operations are still open (the container path, `ingest --watch`,
and the first full-size runs of the two test suites). See
[`PLAN.md`](./PLAN.md) for the build plan and
[`docs/runbook.md`](./docs/runbook.md) for the operator path.

## Concept

```
Ghost (blog)      ─┐
Postiz (social)   ─┼─►  ingest  ─►  SQLite + sqlite-vec ─►  search_writing
Website (HTML)    ─┘        ▲                                (hybrid RRF)
                            │                                     ▲
                     chat + embeddings                            │ tool call
                            │                                     │
                 LocalAI (self-hosted)  ◄── agent loop ──  question
              (`spark-x2.5-4b`, `embeddinggemma-300m`)   (`ask`, `/v1/ask`,
                                                          A2A message/send)
```

* **One Go binary** with subcommands: `ingest`, `embed`, `query`, `ask`,
  `stats`, `serve` (A2A card + `message/send` + `/v1/retrieve` +
  `/v1/ask`), and `tokens` (A2A bearer issuance/revocation).
* **One SQLite file** (`index.db`) you can `cp` to back up.
* **Hybrid retrieval**: BM25 (FTS5) + vector (sqlite-vec) with RRF merging.
* **Model-agnostic**: self-hosted LocalAI by default
  (`http://192.168.78.243:8080`) or any OpenAI-compatible server — one
  config switch.
* **Models**: embeddings `embeddinggemma-300m` (768d), chat `spark-x2.5-4b`.
  Both self-hosted; no API keys, no cloud calls.
* **Agentic**: the service runs the loop — one internal tool
  (`search_writing`), bounded iterations, citation-or-refusal. `agent ask`
  is the CLI form; `agent serve` exposes the same loop and the raw
  retriever over HTTP (`/v1/retrieve`, `/v1/ask`) and as an **A2A v0.3
  agent** (`message/send` at `/a2a`, card at
  `/.well-known/agent-card.json`). Loopback is token-less for development;
  a configured bearer token gates every surface, and a public bind requires
  one.

## Quick reference

```bash
cp example.env .env       # then fill in the credentials
go mod vendor             # vendor/ is gitignored

go build -tags sqlite_fts5 -o agent ./cmd/agent
./agent ingest
./agent embed
./agent query "when did I write about burnout"
./agent ask "what did I write about harnesses?"
./agent backup            # online VACUUM INTO snapshot next to the index
./agent serve             # 127.0.0.1:8080, token-less loopback
./agent tokens add --id alice   # then re-run serve; prints id:token once
./agent serve --bind-public --addr 0.0.0.0:8080   # requires a token
```

An A2A client discovers the agent at
`http://127.0.0.1:8080/.well-known/agent-card.json` and sends `message/send`
to `http://127.0.0.1:8080/a2a`. See
[`docs/a2a.md`](./docs/a2a.md) for the wire contract, token rotation, rate
limit, and audit record.

`example.env` lists every environment variable the agent reads, with defaults
for settings and empty values for secrets. The binary loads `.env` at startup
(godotenv); variables already exported in the shell win over the file, and
containers/cron/launchd set them directly. Add any new variable to
`example.env` (empty for secrets, default otherwise) and to
[`docs/environment.md`](./docs/environment.md) in the same change.

Logs go to stderr; raise verbosity with `--log-level debug` (or
`AGENT_LOG_LEVEL`) and switch to one JSON record per line with `--log-format
json`. See [`docs/logging.md`](./docs/logging.md).

## Testing

Two end-to-end tools, no third harness:

* **Ragas** — [`evals/`](./evals/README.md) grades answer quality with an LLM
  judge: faithfulness to the cited excerpts, context utilization, correctness
  against recorded facts, and refusal discipline.
* **promptfoo** — [`tests-ai/`](./tests-ai/) grades adversarial behaviour over
  `POST /v1/ask`: bias, harmful content, PII, excessive agency, hijacking,
  hallucination, and fabricated citations.

Retrieval is not scored on its own; a ranking problem surfaces as a
faithfulness or correctness miss in the Ragas suite. Unit tests stay in Go:
`go test -tags sqlite_fts5 ./...`.

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
* [`docs/runbook.md`](./docs/runbook.md) — build, ingest, query, ask, serve, the A2A surface.
* [`docs/a2a.md`](./docs/a2a.md) — A2A v0.3 wire contract: agent card, `message/send`, tokens, rate limit, audit.
* [`docs/red-team.md`](./docs/red-team.md) — promptfoo red-team suite for `/v1/ask`.
* [`evals/`](./evals/README.md) — Ragas answer-quality harness: one tool (`evaluate.py`) scoring answers and refusals.
