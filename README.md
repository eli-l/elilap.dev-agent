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
| `agent doctor` | done - preflight for config, credentials, model server, index path and store |
| `agent ingest` / `embed` / `query` / `stats` | done - the vertical slice: corpus in, answer out |
| Local markdown provider | done - a directory of markdown files, no credentials |
| Ghost / Postiz clients | pending - waiting on the Phase 0 API captures |
| SQLite store | done - embedded migrations, chunk writes, BM25 + cosine reads |
| Embedding, retrieval | done - the OpenAI-compatible embeddings client and the hybrid RRF retrieval |
| Container files (Dockerfile, compose.yaml) | done - static image, external model server, `scripts/deploy-check.sh` |
The plan and its exit criteria live in [`PLAN.md`](./PLAN.md); the bring-up
review is [`docs/mvp-review.md`](./docs/mvp-review.md).

## Concept

```
Ghost (blog)        ─┐
                     ├─►  ingest  ─►  SQLite + FTS5 + vectors  ─►  query  ─►  LLM
Postiz (social)     ─┘                                             ▲
                                                                   │
                                          model server (OpenAI-compatible)
```

* **One Go binary** with subcommands: `ingest`, `embed`, `query`, `stats`,
  `serve`.
* **One SQLite file** (`index.db`) you can `cp` to back up.
* **Hybrid retrieval**: BM25 (FTS5) + vector (cosine similarity, scored in Go) with RRF merging.
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
./bin/agent ingest --source files      # import a corpus into the index
./bin/agent embed                      # embed the chunks that have no vector
./bin/agent query "what did I write about X"  # the answer, fused from BM25 and vectors
./bin/agent stats                      # what the index holds
```

The corpus comes from the providers in `sources.yaml`. The local markdown
provider needs no credentials, so the whole pipeline can be run before the
upstream clients land:

```yaml
  - id: files
    type: files
    enabled: true
    options: { dir: ./docs, pattern: "*.md" }
```

`agent serve` - the read-only A2A surface - is still a placeholder.

## Deploy

Two ways to run the same binary, one behaviour (the container is the binary
plus a volume). Both need a reachable OpenAI-compatible model server: the
model server is **external** (`PLAN.md` Decision 10), this repo never runs
one.

### Host mode

```bash
make setup                        # toolchain check, .env from the example, build
set -a; . ./.env; set +a          # load the credentials into the environment
./agent doctor                    # preflight: config, credentials, model server, index path
```

### Container mode

```bash
export LOCALAI_BASE_URL=http://model-server:8080/v1   # required
docker compose build
docker compose run --rm agent doctor                  # preflight inside the container
docker compose run --rm agent ingest                  # real work
```

`compose.yaml` runs the agent service and nothing else. `LOCALAI_BASE_URL`
is **required**: compose fails loudly instead of silently pointing the agent
at `localhost` inside the container, where nothing is listening. The index
lives at `/data/index.db` on the named volume `agent-data`, so `index.db`,
`index.db-wal` and `index.db-shm` always share one filesystem - which SQLite
WAL requires.

| variable | container mode | default | purpose |
|---|---|---|---|
| `LOCALAI_BASE_URL` | required | - | OpenAI-compatible endpoint, must end in `/v1` |
| `LOCALAI_API_KEY` | optional | empty | bearer token, when the server wants one |
| `EMBED_MODEL` | optional | `embeddinggemma-300m` | embedding model id (768 dimensions) |
| `CHAT_MODEL` | optional | `spark-x2.5-4b` | chat model id |
| `GHOST_API_URL` / `GHOST_API_KEY` | optional | empty | Ghost Admin API credentials |
| `POSTIZ_API_URL` / `POSTIZ_API_KEY` | optional | empty | Postiz API credentials |

No secret is ever written into the repo: compose reads a `.env` file for
interpolation when you provide one, and every credential is passed through
from the environment.

The image is a static binary (`CGO_ENABLED=0`) on a distroless base, running
as a non-root user (uid 65532) that owns a writable `/data`. There is no
port to expose and no healthcheck to fake: the agent is a CLI, and
`agent doctor` is the preflight - it verifies the model endpoint by listing
its models and fails fast with the variable that fixes it.

### Check the deployment

```bash
./scripts/deploy-check.sh              # docker, compose, model server, build, doctor, compose config
./scripts/deploy-check.sh --skip-build # re-check without rebuilding the image
```

Each check fails with one specific, actionable message.

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
* [`docs/deployment.md`](./docs/deployment.md) - the container deployment facts: image contract, compose topology, and the deploy check.
* [`AGENTS.md`](./AGENTS.md) - conventions for working in this repo.
