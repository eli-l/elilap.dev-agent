# PLAN.md — elilap.dev-agent

Build plan for a local-first RAG agent over Eli's published writing.
This document is the source of truth for scope, sequencing, and exit criteria.
Update it as we learn.

**How to read this plan**: everything normative is stated as **requirements
and capabilities**, not as facts about one machine. Hosts, addresses, model
endpoints, and the database location are configuration points (environment
variables or config keys), never literals. Facts that were verified against a
specific environment are kept, but the machine-specific detail lives in the
non-normative appendix (§7).

**Revision history**: first pass (2026-10-21) corrected wrong host
assumptions, the CLI shape (one binary, not five), and a contentless-FTS5
bug in the original schema. Second pass (same day) made the runtime
environment-independent: one OpenAI-compatible connector in front of a
self-hosted model server, and a multi-stage Docker image with a
volume-mapped SQLite index — see §2.5 and §3. Third pass (2026-10-23)
pinned the model backend to an already running self-hosted LocalAI instance
(appendix §7) and raised the Go floor to 1.27. Fourth pass (2026-10-24)
made the plan platform-agnostic: every host-specific fact moved out of the
normative text into §7, and environment mechanics (scheduling, volumes, DB
placement) are stated as requirements with portable implementations.

Fifth pass (2026-10-25) wrote the §7 reference-environment appendix: the
facts the platform-agnostic pass removed are still on the record, clearly
marked non-normative, so nobody has to re-derive them and nobody can
mistake them for requirements.

Sixth pass (2026-10-25) settled the persistence stack after the driver spike:
the driver is the pure-Go `github.com/ncruces/go-sqlite3` with its `ext/fts5`
extension, and the sqlite-vec `vec0` virtual table is replaced by the plain
`chunk_vectors` table (a float32 BLOB scored in Go). Decision 2b, the §3
schema, and the risk table now say so. It also shipped the container files
and reconciled §2.5 with them: the multi-stage `Dockerfile` (static binary,
distroless runtime, non-root user, writable `/data`), `.dockerignore`,
`compose.yaml` with a **required** `LOCALAI_BASE_URL`, and
`scripts/deploy-check.sh` as the one-command proof that the deployment
works.
---

## 0. Goals & non-goals

**Goals**

1. Retrieval over ~130 Ghost posts and ~4 months of Postiz cross-posts.
2. Hybrid retrieval (BM25 + vectors) with RRF merging.
3. Single-host, single-file SQLite index; no long-lived vector DB service.
4. Reproducible: `rm index.db && ./agent ingest && ./agent embed` rebuilds
   the whole thing from upstream sources.
5. Model-agnostic generation step that the user (or any other agent) can
   invoke; does not pin a single LLM.
6. Built so that an A2A surface can be added later without rewriting the
   retrieval layer.
7. Environment-independent: the app runs from one `docker compose up` on any
   host with a container runtime, talking to any OpenAI-compatible model
   server over HTTP — no host Go toolchain required in container mode.
8. Config-driven imports: every provider is declared in one file, so it can
   be enabled, disabled, and reconfigured without touching Go code.

**Non-goals (deliberately not in scope)**

* Membership/auth/payments on the A2A surface — read-only.
* Re-writing or summarizing the corpus — retrieval only, generation is the
  caller's problem.
* A web UI. The CLI is the surface; a UI is a future concern.
* Mutating upstream content. This repo never writes to Ghost or Postiz.
* Real-time streaming indexes. Hourly polling is fine.
* Operating a model server. The model backend is an external dependency this
  repo consumes over HTTP; multi-GPU, clustering, and model hosting are out
  of scope.
* Crawling Eli's website as a source — **deferred 2026-10-23**. The
  provider registry (§2.6) is the seam that makes adding it a config change
  later, so deferring costs nothing but one package.

---

## 1. Decisions already locked in

| # | Decision | Choice | Rationale (one line) |
|---|---|---|---|
| 1 | Language | Go 1.27 (language floor) | Single binary, no runtime, easy ops |
| 2 | Index store | SQLite + FTS5 + in-Go cosine scoring (one file) | Corpus ≤1,500 chunks — service overkill, file-portable, FTS5 free; a native vector extension would have to ship beside every binary |
| 3 | Embedder + chat model | Self-hosted, OpenAI-compatible model server behind one connector (`/v1/embeddings`, `/v1/chat/completions`); embed model `embeddinggemma-300m` (768d), chat model `spark-x2.5-4b` — both overridable via env | Swapping providers = changing one base URL; no LLM SDK |
| 4 | Retrieval | Hybrid: FTS5 BM25 + vec cosine, RRF merge | Neither alone is good enough on mixed prose+social |
| 5 | Repos / sources | Ghost Admin API + Postiz HTTP API | Live, queryable, idempotent on `(source_kind, source_id)` |
| 2b | Go driver | Pure Go: `github.com/ncruces/go-sqlite3` (SQLite compiled to WebAssembly) + its `ext/fts5` extension | No C toolchain, so the binary cross-compiles for linux/amd64, linux/arm64 and darwin/arm64 with `CGO_ENABLED=0`; FTS5 comes from the driver's own extension; embeddings are a float32 BLOB scored in Go, so no native extension has to ship |
| 6 | Repo location | Any checkout of the git remote (`git@github.com:eli-l/elilap.dev-agent.git`); nothing in the repo may assume an absolute path | Previous plan revisions hardcoded a developer machine's path and later corrected it; paths are not plan material |
| 7 | CLI surface | One binary `agent`, subcommands `ingest`, `embed`, `query`, `stats`, `serve` | Matches `./agent ingest` muscle memory; the five separate `cmd/*/main.go` stubs collapse into `cmd/agent/` in Phase 1 |
| 8 | Auth posture | env-loaded keys; never printed; gitignored index | Defense by default |
| 9 | Container runtime | Multi-stage Dockerfile (builder `golang:1.27` → minimal runtime) + `compose.yaml` with the agent service only (model server is external — Decision 10) | Whole system runs on any Docker host; builder tag floats to the latest 1.27 patch ≥ go.mod's `1.27` |
| 10 | Model serving | An **external, already running** OpenAI-compatible model server (LocalAI in the reference environment, §7), reached via `LOCALAI_BASE_URL`; required models: `embeddinggemma-300m` (768d embeddings) and a chat model (`spark-x2.5-4b` default) | One server covers embeddings + chat; nothing to pull on first boot; compose never runs the model server |
| 11 | SQLite placement | `AGENT_DB_PATH` env (default `./index.db`); `/data/index.db` in the shipped compose file with a named volume | Keeps `index.db` + WAL/SHM siblings on one mount; same binary, same behavior on host and in container |
| 12 | Import config file | One `sources.yaml` at the repo root (YAML, strict decode: `KnownFields(true)`, `version:` field) | Providers are declared in one place and can be enabled/disabled/reconfigured without a rebuild; TOML rejected — the repo already speaks YAML (`eval/queries.yaml`) and YAML carries comments in a hand-edited file (decided 2026-10-23) |
| 13 | Provider model | One `Provider` interface (`List` cheap → `Fetch` full) + a registry keyed by config `type`; unknown type or bad options = startup error | One code path for every source; the `List`/`Fetch` split is what makes incremental ingest cheap and fixtures-testable without credentials |
| 14 | Ingest modes | Default = incremental against a state ledger (`source_state` + `source_items`); `--full` = re-fetch + re-chunk + **re-embed** (decided 2026-10-23) | On a full reindex the index is rebuilt exactly as the sources stand today, and the corpus is small enough (≤1,500 chunks) that the CPU cost is acceptable |

### 1.5 Runtime requirements

The plan makes no assumptions about a specific machine. Any environment that
runs this project must provide:

* **Toolchain (host mode only)**: Go ≥ 1.27 (the `go.mod` floor). The SQLite
  driver is pure Go, so no C toolchain is needed and `CGO_ENABLED=0` builds
  are the default. Container mode needs no host toolchain.
* **Container runtime (container mode only)**: Docker with Compose v2 (or an
  equivalent OCI runtime); the daemon socket must be reachable from wherever
  builds run.
* **Model server**: an OpenAI-compatible HTTP endpoint reachable from the
  agent (host or container), serving one 768-dimension embedding model and
  one chat model; LocalAI is the reference implementation (§7). The agent
  verifies the endpoint by listing its models (`GET {base}/models`) and fails
  fast with an actionable message when the endpoint is down or does not
  serve the configured models. A server may additionally expose `/readyz`;
  that is optional and not part of the contract.
* **Credentials**: Ghost and Postiz API credentials in the environment. On
  a fresh machine none exist; obtaining them is the Phase 0 blocker. They
  are never written into this repo.
* **Scheduler (Phase 3 only)**: any job scheduler — cron, systemd timers,
  launchd, or a container orchestrator. The requirement is "run ingest
  periodically"; the mechanism is the operator's choice (§3, Phase 3).
* **Not required**: ollama, a `codex` CLI, a `postiz` CLI, systemd, or any
  particular OS. Nothing in this repo may depend on those.

---

## 2. Repo layout (target)

```
elilap.dev-agent/
├── PLAN.md                           # this file
├── README.md
├── Makefile                          # build / test / check / setup / doctor entry points
├── .env.example                      # var names, no values
├── .gitignore                        # index.db / .env / model weights
├── Dockerfile                        # multi-stage: golang:1.27 → minimal runtime
├── .dockerignore
├── compose.yaml                      # agent service only; the model server is external
├── sources.yaml                      # static import config: providers, enable/disable, options
├── go.mod
├── cmd/
│   └── agent/
│       └── main.go                   # one binary; subcommand dispatch +
│                                     # one file per subcommand (ingest.go,
│                                     # embed.go, query.go, stats.go, serve.go)
├── internal/
│   ├── sources/
│   │   ├── provider.go               # Provider / Item / Document interfaces
│   │   ├── registry.go               # type → constructor, option binding
│   │   ├── ghost/                    # Ghost Admin client
│   │   ├── postiz/                   # Postiz HTTP client
│   │   └── shared/                   # common chunker + hash + types
│   ├── store/                        # SQLite, migrations, FTS5 + vector glue
│   ├── embed/                        # batching/resume logic over the llm connector
│   ├── config/                       # sources.yaml loader + strict validation
│   ├── llm/                          # one OpenAI-compatible HTTP client
│   └── retrieval/                    # hybrid FTS + vec + RRF
├── migrations/
│   ├── 0001_init.sql
│   └── 0002_state.sql                # source_state + source_items ledger
├── eval/                             # Phase 2: queries.yaml + ground truth
├── scripts/
│   ├── ask                           # thin wrapper over `agent ask` (Phase 2+)
│   ├── check-ddl.py                  # schema smoke check (runs under plain python3)
│   └── ingest-all.sh                 # scheduler-friendly
└── docs/
    ├── architecture.md               # Phase 2, kept thin
    ├── api-shapes.md                 # Phase 0 output
    ├── ghost.md                      # Phase 0 capture
    ├── postiz.md                     # Phase 0 capture
    ├── mvp-review.md                 # bring-up review (2026-10-24)
    └── runbook.md                    # Phase 3: ops + evaluation
```

Current state vs. target: `cmd/agent/` exists with subcommand dispatch, the
`sources` subcommands, the config loader, the provider registry, and a
`doctor` preflight, with tests. `internal/store` is implemented (embedded
migrations, the chunk write path, and the BM25 + cosine read paths);
`internal/{embed,llm,retrieval}` and the network clients are still empty
(`.gitkeep`). `migrations/`,
`scripts/` (including the DDL smoke check), and `docs/` exist in Phase-0/1
form. The bring-up entry points exist: `Makefile` (`setup`, `check`,
`doctor`, `build`, `test`), `.env.example`, and `agent doctor`. The
container files exist now and are verified: `Dockerfile` (multi-stage,
`CGO_ENABLED=0`, distroless runtime), `.dockerignore`, `compose.yaml` (the
agent service only, `LOCALAI_BASE_URL` required), and
`scripts/deploy-check.sh`, which proves the story on a host — the image
builds, `agent doctor` passes inside it, and `docker compose config`
renders. What is still missing is the store layer, so the ingestion
commands keep reporting that they are not implemented. No model configs or
weights live in this repo: the model server is external (Decision 10).

### 2.5 Container topology

`docker compose up` runs the agent on any Docker host; the model server is
external (Decision 10). The agent is a CLI, so the service has no
long-running mode: its default command is `doctor`, the one-shot preflight,
and real work is a `docker compose run --rm agent <command>`.

```yaml
services:
  agent:
    build: .
    environment:
      LOCALAI_BASE_URL: ${LOCALAI_BASE_URL:?set LOCALAI_BASE_URL to the OpenAI-compatible endpoint (must end in /v1)}
      LOCALAI_API_KEY: ${LOCALAI_API_KEY:-}
      EMBED_MODEL: ${EMBED_MODEL:-embeddinggemma-300m}
      CHAT_MODEL: ${CHAT_MODEL:-spark-x2.5-4b}
      AGENT_DB_PATH: /data/index.db
      GHOST_API_URL: ${GHOST_API_URL:-}
      GHOST_API_KEY: ${GHOST_API_KEY:-}
      POSTIZ_API_URL: ${POSTIZ_API_URL:-}
      POSTIZ_API_KEY: ${POSTIZ_API_KEY:-}
    volumes:
      - agent-data:/data
    command: ["doctor"]

volumes:
  agent-data:
```

**The model server is external.** The compose stack runs the agent only.
Two consequences: (a) the container needs a network route to
`LOCALAI_BASE_URL` — an address that works from the host does not
automatically work from inside a container (`localhost` inside a container
is the container itself; a LAN address must be routable from the container
network), so the agent must verify the endpoint itself and fail fast with a
clear message; (b) there is no `depends_on` health gate, so the check lives
in the agent's own startup path. If no external model server is
available, the documented fallback is to add a model-server service to this
compose file (CPU image, pinned to a versioned tag) and point
`LOCALAI_BASE_URL` at it.

**SQLite mapping contract** (the clean way):

* The DB path is one env var, `AGENT_DB_PATH` — `/data/index.db` inside the
  container, `./index.db` by default in a host run. Same binary, same
  behavior.
* `/data` is a **named volume** (`agent-data`) by default: durable across
  container recreations, fast on every platform, and it keeps `index.db`,
  `index.db-wal`, `index.db-shm` together on one filesystem (required by
  SQLite WAL — never split them across mounts).
* A bind mount (`-v "$PWD/data:/data"`) is supported when the file must be
  host-visible (inspection, backup). Bind mounts can be markedly slower on
  some platforms (e.g. macOS VirtioFS) — fine occasionally, not the default.
* Backup: `agent backup` (online `VACUUM INTO`, Phase 3) or stop + copy the
  volume. Documented in the runbook.

**Image contract**: multi-stage `Dockerfile` — builder `golang:1.27`
(tag floats to the newest 1.27 patch) building with `CGO_ENABLED=0` and
`-mod=vendor`, so the build needs no network and yields a static binary →
runtime `gcr.io/distroless/static-debian12:nonroot` carrying only the
binary, the CA bundle, the static import configuration (`sources.yaml`,
which holds no secret) and a `/data` directory owned by the non-root user
(uid 65532). No `EXPOSE` — the agent is a CLI, not a server — and no
`HEALTHCHECK`: the binary has no health mode, and a healthcheck that always
passes is worse than none. If the Phase 1 driver spike lands a CGO driver
instead, the runtime base must become glibc-based (`debian:bookworm-slim`)
and the build must set `CGO_ENABLED=1` — a two-line change. We do not ship
a model server in the image (Decision 10), so model behavior depends on the
external server build, not ours. If the fallback service is ever added
here, pin it to a **versioned tag, not `latest`**.

### 2.6 Import configuration, provider registry, and ingest modes

**The config file** — `sources.yaml` at the repo root: one static file
that declares every import provider. Secrets are never in it; each
credential names an env var to read.

```yaml
version: 1

defaults:
  chunking: { target_tokens: 400, overlap_tokens: 60 }
  user_agent: "elilap-dev-agent/0.1 (+https://elilap.dev)"

sources:
  - id: ghost
    type: ghost
    enabled: true
    credentials:
      api_url: { env: GHOST_API_URL }
      api_key:  { env: GHOST_API_KEY }
    options: { include_drafts: false, limit: 500 }

  - id: postiz
    type: postiz
    enabled: true
    credentials:
      api_url: { env: POSTIZ_API_URL }
      api_key:  { env: POSTIZ_API_KEY }
    options: { platforms: [instagram, linkedin, x], include_scheduled: false }
```

Rules: `version:` is checked (unknown version = error); the YAML is decoded
strictly (`KnownFields(true)`) so a typo is a hard error, never a silently
ignored key; `enabled: false` disables a provider without deleting it;
credentials are `{ env: VAR }` references.
`sources validate` warns about unset credential variables and exits 0 by
default; `sources validate --strict` treats them as errors and exits 1.
`sources list` reports set/unset state and exits 0 for missing credentials;
`--strict` is a validate-only flag. Credential values are never printed.
`--config` overrides the path (default `./sources.yaml`).

`defaults.chunking.target_tokens` is required and must be positive.
`overlap_tokens` defaults to zero and must be non-negative and less than
`target_tokens`. Invalid chunking defaults are configuration errors.

**The provider interface** — one interface, one registry:

```go
type Provider interface {
    Type() string
    List(ctx context.Context, since time.Time) ([]Item, error) // cheap candidates
    Fetch(ctx context.Context, it Item) (Document, error)      // full content
}
```

`List` is cheap (an Admin API list); `Fetch` is expensive (full body).
Providers are constructed from the registry by config `type`; an unknown
`type` or invalid options is a startup error, never a runtime surprise.

**The ledger** — `migrations/0002_state.sql`:

```sql
CREATE TABLE source_state (
  source_id    TEXT PRIMARY KEY,   -- config id
  type         TEXT NOT NULL,
  config_hash  TEXT NOT NULL,      -- hash of the resolved provider config
  last_run_at  TEXT,
  last_full_at TEXT,
  item_count   INTEGER DEFAULT 0
);

CREATE TABLE source_items (
  source_id     TEXT NOT NULL,
  item_id       TEXT NOT NULL,     -- Ghost uuid / Postiz id
  url           TEXT,
  content_hash  TEXT,
  etag          TEXT,
  last_modified TEXT,
  first_seen_at TEXT NOT NULL,
  last_seen_at  TEXT NOT NULL,
  seen_run_id   TEXT NOT NULL,     -- run that last saw the item
  PRIMARY KEY (source_id, item_id)
);
```

`chunks.source_id` remains the upstream **item id** (Ghost uuid / Postiz
id), not the config id. `chunk_id = sha256(source_kind|source_id|chunk_seq)`
therefore distinguishes different items; `chunk_seq` is an ordinal within
that item, including all of its platform chunks. The ledger's `source_id`
is the config id, and `source_items.item_id` corresponds to `chunks.source_id`.
`chunks.content_hash` already stores the hash of normalized chunk text for
change detection; no additional chunk hash column is needed.

Phase 1 must preserve this distinction when joining the ledger to chunks
and deleting items. The current index shares an item across configurations
with the same source kind and upstream item id; independent upstreams with
colliding item ids require a namespace/schema decision before support is added.

**Ingest modes**

* **Incremental (default)** — `List(since = last_run_at − overlap)` gives
  candidates; each is cheap-compared against the ledger (ETag,
  `updated_at`, or a content hint). Unchanged items are skipped: no fetch,
  no chunk, no embed. Changed or new items are fetched, normalized,
  chunked, and upserted. Items the ledger knows but this run did not see
  were deleted upstream — their chunks go with them (drift detection).
* **Full reindex (`--full`)** — re-fetch, re-chunk, and **re-embed**
  everything (decided 2026-10-23). `--source <id>` scopes either mode to
  one provider.

```
agent ingest                      # incremental, all enabled providers
agent ingest --source ghost       # one provider
agent ingest --full               # full reindex: re-fetch + re-chunk + re-embed
agent ingest --full --source postiz
agent ingest --dry-run            # what would change, no writes
agent sources list                # enabled/disabled + resolved options (no secrets)
agent sources validate            # validate config/options; missing credentials warn
agent sources validate --strict   # missing credentials fail (exit 1)
```

The website (HTML) is **not** a provider yet — deferred 2026-10-23. When
it comes back it is one `sources.yaml` entry plus one package, because the
registry is the seam.

---

## 3. Phased build plan

Each phase has explicit prerequisites, deliverables, and exit criteria.
We commit at the end of each phase. We do not start the next phase until
the exit criteria pass. One deliberate exception: Phase 1's
credential-free work (driver spike, store, chunker with fixture posts) may
start while Phase 0 is blocked on keys — live-source exit criteria still
wait for keys.

### Phase 0 — Discovery & credentials

**Goal**: confirm both upstream APIs work end-to-end and capture their
actual response shapes before writing any schema.

**Tasks**

- [x] Keep `.env.example` in sync with the real variable surface (no
      values) and document load order (`.env` for humans, real env for
      schedulers/containers): the four source credentials plus runtime
      config — `LOCALAI_BASE_URL`, `LOCALAI_API_KEY` (optional),
      `EMBED_MODEL`, `CHAT_MODEL`, `AGENT_DB_PATH`.
- [x] Author `sources.yaml` (§2.6) with one entry per provider (`ghost`,
      `postiz`), `enabled: true`, credentials as `{ env: VAR }` references
      and no values. `agent sources validate` must pass before any
      credential exists.
- [ ] Obtain **both** credentials (Postiz and Ghost). They enter via env /
      `.env` only; we will not echo, log, or commit them.
- [ ] Confirm `POSTIZ_API_KEY` works against the Postiz API base URL using
      `curl` (no dependency on a `postiz` CLI). Capture the real `/posts`
      (or equivalent) response shape: post id, content text, integrations
      (per-platform), status, publish date, media, release id.
- [ ] Write the captured Postiz response shape into `docs/postiz.md`.
- [ ] Confirm `GHOST_API_URL` + `GHOST_API_KEY` work; one full post JSON
      captured for schema reference (with `html`, `plaintext`,
      `published_at`, `updated_at`, tags) into `docs/ghost.md`.
- [ ] Update `docs/api-shapes.md` to link both captures as the honest source.

**Exit criteria**

- A real Postiz posts request returns ≥1 record and the JSON is documented
  in `docs/postiz.md`.
- A real Ghost `/posts/?limit=1` returns a usable post JSON, documented in
  `docs/ghost.md`.

**Blocker (yours)**: both API keys. Everything downstream that touches a
live source waits on this.

---

### Phase 1 — Ingestion

**Goal**: a working ingestor that pulls from Ghost and Postiz into a
SQLite index with `chunks` + `chunks_fts` + `chunk_vectors`. Embedding is
intentionally a separate command.

**Tasks**

- [x] Collapse `cmd/{ingest,embed,query,stats,serve}/main.go` stubs into
      `cmd/agent/` with subcommand dispatch (Decision 7). `go build`
      still green after.
- [x] `internal/config`: load `sources.yaml` (YAML, `KnownFields(true)`,
      `version` check), validate per-provider options, resolve `{ env: VAR }`
      credentials without printing values; `sources validate` warns on missing
      variables by default and fails only with `--strict`.
- [x] `internal/sources/provider.go` + `registry.go`: the `Provider`
      interface and the `type → constructor` registry; an unknown type is a
      startup error.
- [x] `migrations/0002_state.sql`: the `source_state` + `source_items`
      ledger; retain the existing item-level `chunks.source_id` and
      `chunks.content_hash` from `0001_init.sql`.
- [x] `agent sources list` / `agent sources validate`: show enabled
      providers and resolved options (never secrets); validate the file
      with no credentials present.
- [x] Bring-up entry points: a `Makefile` with `setup` / `check` /
      `doctor` / `build` / `test`, a `.env.example` carrying the whole
      variable surface with no values, and `agent doctor` — one command
      that says whether this machine can run the agent.
- [x] `scripts/check-ddl.py`: applies the migrations to a scratch database,
      proves the FTS5 stored-column join-back, and keeps `0001_init.sql`
      in sync with the schema block in §3.
- [ ] `agent ingest` modes: incremental by default against the ledger;
      `--full` re-fetches, re-chunks and re-embeds; `--source <id>` scopes
      to one provider; `--dry-run` reports the plan without writing.
- [x] Driver spike (small, first): the driver is pinned to the pure-Go
      `github.com/ncruces/go-sqlite3` (SQLite compiled to WebAssembly) with
      its `ext/fts5` extension. FTS5 BM25 and the vector round-trip are
      covered by the `internal/store` tests. There is no C toolchain in the
      picture, so the runtime image can be `distroless`/`scratch` and the
      binary cross-compiles with `CGO_ENABLED=0`.
- [ ] `internal/llm` connector: one small OpenAI-compatible HTTP client
      (plain `net/http`, no SDK) — `POST /v1/embeddings`,
      `POST /v1/chat/completions`, `GET /v1/models`. Config:
      `LOCALAI_BASE_URL` (default `http://localhost:8080/v1`),
      `LOCALAI_API_KEY` (optional), `EMBED_MODEL` (default
      `embeddinggemma-300m`), `CHAT_MODEL` (default `spark-x2.5-4b`).
      Any OpenAI-compatible server (LocalAI, llama.cpp server, vLLM,
      OpenAI itself) works by changing the base URL. Two behaviours the
      client must honour: embeddings are **768d** (the model contract in
      Decision 10 — assert the dimension before writing, or refuse to
      write; `chunk_vectors.dim` records the dimension of every stored
      row), and the default chat model is a **reasoning** model whose
      response can carry a `reasoning` field — `max_tokens` must budget
      for reasoning or `content` comes back empty.
- [x] Containerize (§2.5): `Dockerfile`, `.dockerignore`, `compose.yaml`
      with the agent service, `agent-data` volume, `AGENT_DB_PATH` wiring,
      and `LOCALAI_BASE_URL` **required** rather than defaulted — the
      default would point at the localhost of the container itself. No
      model-server service and no healthcheck in compose.
      `scripts/deploy-check.sh` proves it on a host: the image builds,
      `agent doctor` passes inside it, and `docker compose config`
      renders. The preflight verifies the model endpoint by listing its
      models and fails fast with a clear error; the ingest path inherits
      that check once the connector lands. Embed model
      `embeddinggemma-300m` (768d, matches the embedding contract in
      Decision 10).
- [x] Store layer: `internal/store` opens the index, applies the embedded
      migrations inside one transaction and records them in
      `schema_migrations`, writes `chunks` + `chunks_fts` (+ `chunk_vectors`
      when an embedding is present) in one transaction, and serves the
      BM25 and cosine read paths. `scripts/check-ddl.py` stays green as the
      schema evolves.- [ ] Source clients (`internal/sources/ghost`, `postiz`) + shared chunker;
      unit tests against captured fixture JSON from Phase 0, so chunker
      logic is testable without live credentials.
- [ ] `agent ingest`: walks both sources, normalizes, chunks, writes
      chunks + FTS rows in one transaction.
- [ ] `agent stats`: chunk counts, last-ingest timestamp, drift report.
- [ ] Source-aware chunker rules (table below).
- [ ] Idempotency on `chunk_id` + content hash.
- [ ] `agent embed`: separate command, LocalAI default via the connector
      (any OpenAI-compatible base URL works); batch + resume (skip rows
      where `embedded_with` is current).
- [ ] `docs/runbook.md` starter: "verifying ingestion is correct."
- [ ] End-of-phase commit + push.

**Schema** (`migrations/0001_init.sql`, synced with this file):

```sql
-- 0001_init.sql
--
-- Phase 1 initial schema.
-- Synced with PLAN.md §3 (Phase 1). Final field list lands after
-- Phase 0 captures real upstream response shapes.

CREATE TABLE IF NOT EXISTS chunks (
  chunk_id       TEXT PRIMARY KEY,           -- sha256(source_kind|source_id|chunk_seq), hex
  source_kind    TEXT NOT NULL CHECK (source_kind IN ('ghost_post','postiz_post')),
  source_id      TEXT NOT NULL,              -- upstream item id (Ghost uuid / Postiz id)
  source_url     TEXT,
  platform       TEXT,
  author         TEXT NOT NULL,
  published_at   TEXT NOT NULL,
  ingested_at    TEXT NOT NULL,              -- local write time
  updated_at     TEXT NOT NULL,              -- upstream last-modified time
  content_hash   TEXT NOT NULL,
  title          TEXT,
  section        TEXT,
  body           TEXT NOT NULL,
  body_tokens    INTEGER NOT NULL,
  meta_json      TEXT NOT NULL DEFAULT '{}',
  embedded_with  TEXT,
  embedded_at    TEXT
);

CREATE INDEX IF NOT EXISTS idx_chunks_source      ON chunks (source_kind, source_id);
CREATE INDEX IF NOT EXISTS idx_chunks_published   ON chunks (published_at);
CREATE INDEX IF NOT EXISTS idx_chunks_hash        ON chunks (content_hash);

-- Self-contained FTS5 (NOT contentless): stored columns are readable, so
-- matches join back to chunk_id directly. `tags` values come from
-- meta_json at write time; the store keeps chunks and chunks_fts in sync
-- inside one transaction.
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
  chunk_id UNINDEXED, title, body, tags,
  tokenize='porter unicode61'
);

-- Embeddings are stored as raw float32 little-endian bytes and scored in Go
-- (cosine similarity). A sqlite-vec "vec0" table would need a platform-specific
-- loadable extension next to every deployed binary; the corpus is small enough
-- that the difference is unmeasurable.
CREATE TABLE IF NOT EXISTS chunk_vectors (
  chunk_id TEXT PRIMARY KEY REFERENCES chunks(chunk_id) ON DELETE CASCADE,
  dim       INTEGER NOT NULL,
  vec       BLOB NOT NULL
);
```

**Schema notes (changed in this refinement)**

* **Dropped `content=''` from `chunks_fts`.** A contentless FTS5 table
  stores *nothing* — every stored column reads back NULL, so the Phase 2
  join-back on `chunk_id` would have failed at query time. Self-contained
  FTS5 is simpler and costs only duplicate text at this corpus size.
* **Dropped `UNIQUE (source_kind, source_id, chunk_id)`** — redundant once
  `chunk_id` is globally unique by construction (hash of
  source_kind|source_id|chunk_seq). Idempotency keys on `chunk_id` +
  `content_hash`.
* `updated_at` is the *upstream* modification time (drift detection);
  `ingested_at` is our local write time. The old schema conflated them.

**Chunker rules**

| Source | Unit | Cap (tokens) | Overlap | Notes |
|---|---|---|---|---|
| `ghost_post` (≥800 tok) | H2 sections, fallback H3 | 400 | 60 | HTML→Markdown in pure Go (htmd or equivalent); preserve code blocks |
| `ghost_post` (<800 tok) | Whole post | — | — | Don't fragment |
| `postiz_post` | One chunk per `(postiz_post_id, platform)` | 320 | 0 | Cross-post linkage via `thread_root_id` |
| `postiz_post` (multi-para > cap) | Split on `\n\n` | 320 | 0 | One Postiz post = one source_id; multiple chunks share it |

(`chunk_seq` is the per-item ordinal of the chunk; it feeds `chunk_id`.)

**Status filter (postiz)**: skip `draft`, skip `failed`. Skip `scheduled`
until `publish_at <= now` (configurable; default skip).

**Exit criteria**

- `agent ingest` from clean state succeeds against live Ghost + live Postiz.
- `agent stats` reports chunk counts matching a manual text inventory
  within ±10%.
- Re-running `agent ingest` is a near-no-op (insert/update counts near zero
  with no source changes).
- Deleting an upstream post and re-running reduces the corresponding chunks
  (drift detection works).
- `agent embed` finishes in <5 min against the configured model server
  (models already loaded there — no first-boot download).
- `docker compose build` produces the image and `docker compose up` yields
  an agent container that reaches the model endpoint (`LOCALAI_BASE_URL`)
  with `agent embed` working from a clean container — no host Go toolchain.
- A second incremental run is a near no-op (no re-fetch, no re-embed).
- `agent ingest --full` rebuilds every chunk and every embedding from the
  sources as they stand today.
- All keys stay in env; no secret leaks in `git log -p` or stdout.

**Estimated effort**: 3–4 focused days (containerization + connector added).

---

### Phase 2 — Retrieval & agent wrapper

**Goal**: prove retrieval quality on real queries, then put a thin LLM
wrapper on top.

**Tasks**

- [ ] `agent query` (no LLM in the loop yet):
  - inputs: free-text, `--top N`, `--hybrid` (on by default),
    `--source ghost|postiz|all`, `--platform` (repeatable), `--after`,
    `--before`, `--exclude-scheduled`, `--recency-halflife-days`,
    `--format jsonl|text`.
  - outputs: per-chunk metadata + body + score.
- [ ] `eval/queries.yaml`: 20 hand-picked queries with ground-truth
  chunk_ids, so the eval is a reviewable artifact, not a one-off script.
  Target ≥15/20 with a clearly correct, verifiable answer.
- [ ] `agent ask`: one-shot subcommand through the `internal/llm`
      connector — builds the strict prompt below with the retrieved chunks
      and POSTs to `/v1/chat/completions` on the configured base URL
      (LocalAI default; any OpenAI-compatible server). No state, no
      retries. `scripts/ask` stays as a thin wrapper around `./agent ask`
      for muscle memory.

**Prompt template (for `agent ask`)**:

```
Use only the excerpts below from Eli Lap's published writing to answer.
If the answer is not present in the excerpts, say "Not in my published
writing" and stop.
Cite each claim with the chunk_id and source_url from the excerpt metadata.

[Excerpts]
[chunk_id] source_url
body

[Question]
{question}
```

**Exit criteria**

- 20-query eval reaches ≥15/20 with verified chunk_ids.
- p50 retrieval latency <300ms; end-to-end `ask` p50 <8s on a GPU-capable
  model host, ≤60s against a CPU-only model server (small chat model).
- A second human can read `README.md` + `docs/runbook.md` and run `ask`
  without help — including via `docker compose up` on a clean host.

**Estimated effort**: 1–2 days.

---

### Phase 3 — MVP operations

**Goal**: the system runs unattended and you actually use it.

**Tasks**

- [ ] `scripts/ingest-all.sh`: scheduler-friendly wrapper (already a stub;
      it calls the built binary and fails clearly while Phase 1 is
      unimplemented). Container mode: `docker compose run --rm agent agent ingest`.
- [ ] `agent ingest --watch`: poll upstream every N minutes during work
      hours, no-op when nothing changed. In container mode the `agent`
      service runs long-lived with `restart: unless-stopped`.
- [ ] `agent backup`: online `VACUUM INTO` snapshot of the SQLite file
      into the data volume (safe while the writer is live).
- [ ] Scheduled ingest: the requirement is "run `agent ingest` (host mode)
      or `docker compose run --rm agent agent ingest` (container mode) on
      a schedule" — hourly by default. Portable option that works
      everywhere: a user crontab entry
      (`0 * * * * /path/to/repo/scripts/ingest-all.sh`). Platform-native
      alternatives: a systemd user timer on Linux (`systemd --user`), a
      launchd agent on macOS (`~/Library/LaunchAgents`), or a
      `restart: unless-stopped` long-running container doing
      `agent ingest --watch`. Pick one; the repo ships the wrapper script,
      not a scheduler unit.
- [ ] Wherever your agent memory lives, note: "for questions about my
      positions, history, or thinking, use `ask` from this repo."
- [ ] One week of shadow usage: run `ask` on real questions at least once
      per day; no operational surprises.

**Exit criteria**

- 1 week with no manual re-ingest interventions.
- Memory footprint ≤50 MB (index + binary).
- New contributor can either `go build && ./agent ingest && ./agent embed &&
  ./agent query "..."` on a Go host, or `docker compose up` then
  `docker compose run --rm agent agent ingest` on any Docker host — in <30 min
  either way.

**Estimated effort**: 1 day.

---

### Phase 4 — A2A surface (read-only, token-gated)

**Goal**: expose `agent query` over HTTP for other agents. No write surface.
No public web exposure by default.

**Tasks**

- [ ] `agent serve`: HTTP server binding to `127.0.0.1` (default) or
  `0.0.0.0` (only with `--bind-public`); token auth via
  `Authorization: Bearer <token>`.
      (Container mode: publish as `ports: "127.0.0.1:8081:8080"` — a
      container's loopback is not the host's; public exposure stays
      opt-in.)
- [ ] JSON-RPC-style `POST /v1/retrieve`: forwards to `internal/retrieval`.
- [ ] Agent card at `GET /.well-known/agent.json` (A2A format).
- [ ] Audit log: per-query timestamp, token id (not token), query, result
  count. Bodies never logged.
- [ ] Token store: env-driven list of `id:token` pairs; `--add-token`,
  `--revoke-token` subcommands; rotation is config-only.
- [ ] Rate limit: 30 q/min per token (configurable).
- [ ] Operational documentation in `docs/runbook.md`.

**Security posture (documented and enforced)**

- API is read-only — no write methods.
- Local bind by default; public bind requires an explicit flag AND a token.
- Audit log never contains chunk bodies or user answers.
- Tokens are high-entropy random ≥32 bytes; the optional token file is
  `0600` and env-driven (see open question 3).

**Exit criteria**

- A second agent (or local `curl`) can call `/v1/retrieve` and gets
  results identical to `agent query` CLI output.
- Audit log shows the call with token id and query.
- Token issuance/revocation is config-only; no service restart needed.
- `docs/runbook.md` documents the rotation story.

**Estimated effort**: 2 days.

---

## 4. Risks & mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Credentials missing entirely | Certain on a fresh machine | Phase 0+ blocked | Keys are the one human blocker; Phase 1 store/chunker work can start in parallel against fixtures |
| Postiz response shape differs from docs | Medium | Phase 1 schema rework | Phase 0 captures real shape first |
| Ghost response shape differs from expectations | Low | Same as above | Phase 0 captures one real post |
| A native vector extension would have to ship beside the binary | Avoided | Deployment fragility | The driver and the vector storage are pure Go: `chunk_vectors.vec` is a float32 BLOB scored in Go, so no `.so` and no C toolchain |
| The pure-Go driver misbehaves inside the container | Low | Image build breaks | The driver has no CGO and no platform-specific parts; `docker compose build` still has to prove the FTS5 + cosine round-trip in-container before the image is trusted |
| Model endpoint down or unreachable from the container | Medium | `embed`/`ask` blocked | Agent verifies the endpoint by listing its models and fails fast with a clear message; `LOCALAI_BASE_URL` must be reachable from inside the container network (`localhost` in compose means the container itself); documented fallback is a local model-server compose service |
| Model host is CPU-only | Medium | Slow chat | `spark-x2.5-4b` is a 4B model — acceptable at this corpus size; Phase 2 latency targets are measured against the real endpoint, not guessed |
| The chat model is a reasoning model — the token budget is eaten by `reasoning` | Medium | Empty `content` in `agent ask` | `max_tokens` must budget for reasoning; `agent ask` reads the `reasoning` field and errors loudly on empty content; a smaller instruct model is the documented fallback |
| Docker daemon unreachable from an agent sandbox | Certain in-sandbox | Slow dev loop for container steps | Run container commands with explicit permission or outside the sandbox; never bake keys into the image |
| SQLite on a host bind mount is slow / has locking quirks on some platforms | Low | Degraded perf when DB is host-visible | Named volume is the default; bind mount documented for export only |
| Contentless-FTS5 trap (caught in refinement) | Fixed | Would have broken query join-back | Self-contained FTS5 in `0001_init.sql` |
| Embedding quality too low | Low | Phase 2 eval falls short | Point `LOCALAI_BASE_URL` at a stronger server or change the model id; the `embedded_with` audit makes re-runs cheap |
| Config typo silently disables a provider | Medium | A source goes dark without an error | Strict YAML decode (`KnownFields(true)`) + `agent sources validate`; the enabled set is printed before each run |
| Full reindex re-embeds the whole corpus | Medium | CPU time on the model host | Deliberate (2026-10-23); ≤1,500 chunks at 768d; `--source` to scope; incremental stays the default |
| Ledger and index disagree after a manual DB edit | Low | Silent wrong results | `source_state.config_hash` records the config that produced the index; a mismatch forces a full reindex |
| Posts scheduled into future not yet live get indexed | Medium | Stale context | Skip `status='scheduled'` by default |
| Index drift silent | Medium | Wrong answers | `agent stats` diffs count against source |
| A2A token leaked | Low | Read-only exposure of published corpus | Tokens are scoped read-only; rotated monthly |
| Agent sandbox lacks network for embeddings/API calls | Medium | Slow dev loop | Run networked steps with explicit permission or outside the sandbox; never bake keys into the repo |

---

## 5. Open questions

1. Should `postiz:status='scheduled'` posts that are *past* their publish
   time be indexed, or only those the API now reports `published`?
   **Default**: skip until status flips to `published`.
2. Should `agent embed` also embed `meta_json`-only fields (tags, titles
   without body)? **Default**: no; tags appear in FTS5 path.
3. Phase 4 token storage: env-only (simplest) vs. a `0600` token file.
   **Default**: env-driven pairs; decide when Phase 4 starts, not before.
4. ~~Default chat model~~ — **decided 2026-10-23**: `spark-x2.5-4b` on the
   reference model server (§7). `spark-x2.5-1.7b-q4` is the fallback if the
   Phase 2 eval shows the 4B is too slow there. Both are config values
   (`CHAT_MODEL`), never build-time constants.
5. ~~Pin the model server to a versioned tag~~ — **moot 2026-10-23**: the
   model server is external, so its version is the operator's concern. The
   agent depends on the OpenAI-compatible surface (`/v1/embeddings`,
   `/v1/chat/completions`), not on a specific build or vendor.
6. The website (HTML) as a source — **deferred 2026-10-23**. Facts already
   captured: `elilap.dev` is an Astro static site with a single `<main>`
   landmark per page; `robots.txt` allows all crawlers and explicitly
   allows AI bots; `sitemap.xml` is an index → `sitemap-0.xml` (26 URLs on
   `elilap.dev`) + `sitemap-posts.xml` (34 post URLs on `read.elilap.dev` —
   the same posts Ghost serves). If revived: restrict it to non-blog
   `elilap.dev` pages, exclude `/tag/*`, and let Ghost stay canonical for
   posts.
7. Config file location — **decided 2026-10-23**: `sources.yaml` at the
   repo root, with `--config` overriding the path so it is never
   hardcoded.

---

## 6. Done = …

The project is "done" when:

* `agent ingest` is idempotent against both sources.
* `agent query` returns verifiable, citable answers.
* The repo + one `index.db` file constitute the entire operational footprint
  (the index lives in the `agent-data` volume in container mode).
* The whole system runs from `docker compose up` on a clean host with only
  a container runtime — environment independence proven.
* A second agent can call our retrieval over A2A.
* It runs unattended for a month without manual intervention.

We are not done at MVP. We are not done at Phase 2. We are done at the
end of Phase 4.

---

## 7. Reference environment (non-normative)

**This appendix is not a requirement.** Everything above it is normative and
holds on any host. This section records what was actually observed on **one**
reference environment, so the numbers and caveats behind the plan are
auditable and so a future reader can tell "the plan says this" from "this
happened to be true here". Nothing here may be read as a requirement, a
default, or a constraint: a different host, a different model server, or a
different credential set are all equally valid.

### 7.1 The reference host

| property | observed value |
|---|---|
| role | build + verification host for this repo (the "AI node") |
| OS | Ubuntu 26.04.1 LTS, kernel 7.0.0-38-generic, x86_64 |
| Go | 1.27.3 (gvm-managed), matching the `go.mod` floor of 1.27 |
| container runtime | Docker CLI 29.9.2, Compose v5.6.1 |
| other | `python3`, `curl`, `make`, `git` present; no `sqlite3` CLI |

The repository was also developed on a macOS (darwin/arm64) workstation.
That host is not required, not referenced by any script, and nothing in the
build depends on it.

### 7.2 The reference model server

An already-running LocalAI instance on the LAN, reached over plain HTTP:

| property | observed value |
|---|---|
| base URL | `http://192.168.78.243:8080` (LAN host named `dev`; the reference build host is `192.168.78.242`) |
| OpenAI-compatible base | `http://192.168.78.243:8080/v1` → `LOCALAI_BASE_URL` |
| liveness | `GET /readyz` → `200` |
| version | `GET /version` → `v4.12.0 (15e37df9a468c798a5a511624f054057da4380a1)` |
| auth | none required; `LOCALAI_API_KEY` stayed unset |

**Loaded models** (`GET /v1/models`):

| id | role |
|---|---|
| `embeddinggemma-300m` | embeddings — **768 dimensions** |
| `spark-x2.5-4b` | chat — the default chat model |
| `spark-x2.5-1.7b-q4` | chat — lighter fallback |

**Verified 2026-10-23, from the reference host:**

* `POST /v1/embeddings` with `embeddinggemma-300m` and input `"hello world"`
  returned 768 floats — the dimension the plan requires; every stored
  vector records its own dimension in `chunk_vectors.dim`.
* `POST /v1/chat/completions` with `spark-x2.5-4b` returned HTTP 200 with a
  completion.
* **Caveat — reasoning models:** `spark-x2.5-4b` is a *reasoning* model.
  Its response carries a `reasoning` field alongside `content`; at
  `max_tokens=10` the budget was consumed by reasoning and `content` came
  back empty. The client must budget `max_tokens` for reasoning and fail
  loudly on empty `content` rather than silently returning nothing. This
  caveat is a property of reasoning models in general, not of that one
  instance.

### 7.3 Observed credential state

On the reference host, all four source credential variables were **unset**
and no `.env` file existed:

```
POSTIZ_API_KEY   unset
POSTIZ_API_URL   unset
GHOST_API_URL    unset
GHOST_API_KEY    unset
```

The runtime variables (`LOCALAI_BASE_URL`, `LOCALAI_API_KEY`, `EMBED_MODEL`,
`CHAT_MODEL`, `AGENT_DB_PATH`) were unset as well — the defaults in §3 apply.
Obtaining the two source credentials is the Phase 0 blocker, and it is the
same blocker on any fresh host.

### 7.4 Tooling verified against

| tool | version / state |
|---|---|
| Go | 1.27.3 — `go build ./...`, `go vet ./...`, `gofmt -l .` all clean |
| Docker CLI | 29.9.2 |
| Docker Compose | v5.6.1 |
| python3 | present (used by `scripts/check-ddl.py`) |
| `sqlite3` CLI | **absent** — which is why the DDL smoke check goes through the Go driver, not the shell |

### 7.5 Known-to-be-unavailable tooling

These were checked and are **not** load-bearing. Nothing in this repo may
depend on them:

| tool | state on the reference host | note |
|---|---|---|
| `ollama` | CLI absent | never load-bearing — the OpenAI-compatible connector replaced it |
| `codex` CLI | present on the workstation, not on the reference host | not load-bearing — `agent ask` goes through the same connector |
| `postiz` CLI | absent | Phase 0 capture uses plain `curl` against the Postiz API |
| `sqlite3` CLI | absent | the DDL smoke check does not shell out to `sqlite3` |

### 7.6 What the reference environment does *not* prove

* It does not prove the pure-Go driver behaves identically on every target
  platform — the container image build, and a cross-compile for linux/arm64
  and darwin/arm64, still have to run on the target platform.
* It does not measure the Phase 2 latency targets; those must be measured
  against the model server actually in use.
* It does not verify any live Ghost or Postiz response shape: no credential
  existed, so `docs/ghost.md` and `docs/postiz.md` remain empty placeholders
  and the schema in `migrations/0001_init.sql` is still best-effort
  inference.
