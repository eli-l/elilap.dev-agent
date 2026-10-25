# elilap.dev-agent

Local-first retrieval-augmented agent over Eli's published writing.
Indexes Ghost posts (blog) + Postiz cross-posts (social), exposes retrieval
and a thin agent wrapper, and is built to be A2A-callable later.

This repo is **infrastructure**, not the writing itself. The corpus lives
upstream on Ghost and Postiz; the index is a derived artifact you can delete
and rebuild at any time.

## Status

Phase 0/1 bootstrap. What exists today:

| Piece | State |
|---|---|
| `sources.yaml` config layer | done - strict loader, validation, provider registry |
| `agent sources list` / `validate` | done |
| `agent doctor` | done - preflight for config, credentials, model server, index path |
| Ghost / Postiz clients | pending - waiting on the Phase 0 API captures |
| SQLite store, embedding, retrieval | pending - Phase 1 |
| Container files (Dockerfile, compose.yaml) | pending - Phase 1 |

The plan and its exit criteria live in [`PLAN.md`](./PLAN.md); the bring-up
review is [`docs/mvp-review.md`](./docs/mvp-review.md).

## Concept

```
Ghost (blog)        ─┐
                     ├─►  ingest  ─►  SQLite + sqlite-vec  ─►  query  ─►  LLM
Postiz (social)     ─┘                                  ▲
                                                        │
                                          model server (OpenAI-compatible)
```

* **One Go binary** with subcommands: `ingest`, `embed`, `query`, `stats`,
  `serve`.
* **One SQLite file** (`index.db`) you can `cp` to back up.
* **Hybrid retrieval**: BM25 (FTS5) + vector (sqlite-vec) with RRF merging.
* **Model-agnostic**: any OpenAI-compatible server - one base URL, no SDK.
* **A2A-ready**: retrieval is exposed over JSON-RPC in a later phase; the CLI
  surface is the source of truth.

## Quick start

```bash
make setup     # toolchain check, .env from the example, build bin/agent
make doctor    # can this machine run the agent?
make test      # unit + fixture-based end-to-end tests
```

`make doctor` is the fastest way to see where you stand: it validates
`sources.yaml`, names the credential variables that are not set, probes the
model server and checks that the configured models are served, and verifies
that the index directory is writable. Every failure comes with the variable
or file that fixes it.

### Configuration

Two layers, no overlap:

* `sources.yaml` - which providers exist and how they are configured. It
  never holds a secret; every credential is an `{ env: VAR }` reference.
* environment variables - secrets and runtime values. Copy `.env.example`
  to `.env` and fill it in; `.env` is gitignored.

| Variable | Default | Purpose |
|---|---|---|
| `LOCALAI_BASE_URL` | `http://localhost:8080/v1` | OpenAI-compatible endpoint for embeddings + chat |
| `EMBED_MODEL` | `embeddinggemma-300m` | embedding model id (768 dimensions) |
| `CHAT_MODEL` | `spark-x2.5-4b` | chat model id |
| `AGENT_DB_PATH` | `./index.db` | SQLite index path |
| `GHOST_API_URL` / `GHOST_API_KEY` | - | Ghost Admin API credentials |
| `POSTIZ_API_URL` / `POSTIZ_API_KEY` | - | Postiz API credentials |

The reference environment - the machine and model server this was built
against - is recorded in [`PLAN.md`](./PLAN.md) section 7 and
[`docs/localai.md`](./docs/localai.md). Nothing in the code depends on it:
point the variables at your own endpoint and it runs there.

### Commands

```bash
./bin/agent sources list               # providers, options, credential names (never values)
./bin/agent sources validate --strict  # fail if a credential variable is unset
./bin/agent doctor                     # preflight for this machine
```

The remaining subcommands (`ingest`, `embed`, `query`, `stats`, `serve`)
land with the store layer in Phase 1 and currently report that they are not
implemented.

## Development

```bash
make fmt-check   # gofmt must be clean
make vet         # go vet
make test        # go test ./...
make e2e         # the fixture-based end-to-end test only
make verify      # all of the above plus a build
```

Dependencies are vendored, so every target works offline - always build
with `-mod=vendor` (the Makefile does). `make clean` removes `bin/`.

## What is NOT in this repo

* The corpus itself - owned by Ghost and Postiz.
* Generated embeddings - those live in `index.db` (gitignored).
* Any LLM wrapper code - generation is intentionally out of band so the
  retrieval layer stays model-agnostic.
* Credentials - they live in your environment, never in a file here.

## See also

* [`PLAN.md`](./PLAN.md) - full step-by-step build plan with exit criteria.
* [`docs/mvp-review.md`](./docs/mvp-review.md) - the MVP bring-up review.
* [`AGENTS.md`](./AGENTS.md) - conventions for working in this repo.
