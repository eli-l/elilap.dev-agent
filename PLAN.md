# PLAN.md — elilap.dev-agent

Build plan for a local-first RAG agent over Eli's published writing.
This document is the source of truth for scope, sequencing, and exit criteria.
Update it as we learn.

**Last refined**: 2026-10-27. **Scope change (2026-10-27)**: this repo is
an **agentic A2A connector** — the agent loop lives here. A remote peer
sends a conversational task, and *our* service runs its own tool-using
loop (retrieve → ground → answer with citations) against the local index
over the OpenAI-compatible chat model. The A2A surface exposes the
**agent**, not a raw retriever; RAG is an internal tool of the service,
and `agent ask` is its CLI shadow. Same-day refinement (2026-10-27): a
minimal **HTTP dev surface** — `agent serve` in Phase 2, token-less and
loopback-only, with `POST /v1/retrieve` (the exact JSON `search_writing`
hands the model) and `POST /v1/ask` (the full loop) — makes retrieval and
the whole flow curl-testable before A2A exists; Phase 4 hardens the same
paths and adds the A2A binding. Earlier revisions: 2026-10-23 pinned
the model backend to the concrete LocalAI instance at
`http://192.168.78.243:8080` (embed `embeddinggemma-300m`, chat
`spark-x2.5-4b`) and made the runtime environment-independent (multi-stage
Docker image, volume-mapped SQLite index, §2.5); 2026-10-21 audited the
bootstrap scaffold (commit `7338ecf`) and the dev host (§1.5), collapsed
five stubs into one binary, and fixed a contentless-FTS5 bug in the
original schema.

---

## 0. Goals & non-goals

**Goals**

1. Retrieval over ~130 Ghost posts and ~4 months of Postiz cross-posts.
2. Hybrid retrieval (BM25 + vectors) with RRF merging.
3. Single-host, single-file SQLite index; no long-lived vector DB service.
4. Reproducible: `rm index.db && ./agent ingest && ./agent embed` rebuilds
   the whole thing from upstream sources.
5. A grounded, citation-first answer loop: one internal tool-calling
   agent (Decisions 15/16) turns a peer's conversational question into
   hybrid retrieval calls over the local index and returns an answer
   whose claims cite `chunk_id` + `source_url`; the OpenAI-compatible
   connector keeps the model swappable.
6. An A2A server surface that exposes *this* agent to remote peers: they
   bring a question, our loop retrieves, grounds, and answers.
7. Environment-independent: the app runs from one `docker compose up` on any
   host with a container runtime, talking to a self-hosted LocalAI over the
   LAN — no host Go toolchain, no ollama, no codex required.
8. Config-driven imports: every provider is declared in one file, so it can
   be enabled, disabled, and reconfigured without touching Go code.

**Non-goals (deliberately not in scope)**

* Membership/auth/payments on the A2A surface — token-gated, read-only.
* Corpus mutation: ingest stays strictly read-only against Ghost and
  Postiz; the loop reads the index, it never writes upstream.
* Free-form open-domain chat: the loop exists to answer *from the
  corpus*; off-corpus questions get the refusal string, not improvisation.
* Multi-agent orchestration inside this service: one loop, one internal
  tool (`search_writing`); peer-side agents are other people's systems.
* A web UI. The CLI (`ask`, `query`) and A2A are the surfaces; a UI is a
  future concern.
* Mutating upstream content. This repo never writes to Ghost or Postiz.
* Real-time streaming indexes. Hourly polling is fine.
* Model serving at scale (multi-GPU, clustering) — the single self-hosted
  LocalAI instance is the ceiling; this repo never operates a model server.
* Crawling Eli's website as a source — **deferred 2026-10-23**. The
  provider registry (§2.6) is the seam that makes adding it a config change
  later, so deferring costs nothing but one package.

---

## 1. Decisions already locked in

| # | Decision | Choice | Rationale (one line) |
|---|---|---|---|
| 1 | Language | Go 1.27 (language floor) | Single binary, no runtime, easy ops |
| 2 | Index store | SQLite + `sqlite-vec` (one file) | Corpus ≤1,500 chunks — service overkill, file-portable, FTS5 free |
| 3 | Embedder + chat model | Self-hosted **LocalAI** behind one OpenAI-compatible connector (`/v1/embeddings`, `/v1/chat/completions`); embed model **`embeddinggemma-300m`** (768d, verified 2026-10-23), chat model **`spark-x2.5-4b`** | Self-hosted and environment-independent; swapping providers = changing `LOCALAI_BASE_URL` |
| 4 | Retrieval | Hybrid: FTS5 BM25 + vec cosine, RRF merge | Neither alone is good enough on mixed prose+social |
| 5 | Repos / sources | Ghost Admin API + Postiz HTTP API | Live, queryable, idempotent on `(source_kind, source_id)` |
| 2b | Go driver | CGO: `mattn/go-sqlite3` (`-tags sqlite_fts5`) + sqlite-vec Go bindings (asg017), pinned | FTS5 + vec0 in one DB handle; exact binding import path confirmed in the Phase 1 driver spike, not before |
| 6 | Repo location | `/Users/ilja/projects/elilap.dev-agent` (this host); remote `git@github.com:eli-l/elilap.dev-agent.git` — both verified 2026-10-21 | Previous plan revision assumed a different machine (`/home/openbot/...`, systemd); corrected |
| 7 | CLI surface | One binary `agent`, subcommands `ingest`, `embed`, `query`, `stats`, `serve` | Matches `./agent ingest` muscle memory; the five separate `cmd/*/main.go` stubs collapse into `cmd/agent/` in Phase 1 |
| 8 | Auth posture | env-loaded keys; never printed; gitignored index | Defense by default |
| 9 | Container runtime | Multi-stage Dockerfile (builder `golang:1.27` → minimal runtime) + `compose.yaml` with the agent service only (LocalAI is external — Decision 10) | Whole system runs on any Docker host; builder tag floats to the latest 1.27 patch ≥ go.mod's `1.27` |
| 10 | Model serving | **Existing self-hosted LocalAI instance** at `http://192.168.78.243:8080` (LAN host `dev`, LocalAI `v4.12.0`) — models already loaded: `embeddinggemma-300m`, `spark-x2.5-4b`, `spark-x2.5-1.7b-q4`; `/readyz` → 200 (verified 2026-10-23) | One server covers embeddings + chat; nothing to pull on first boot; compose no longer runs LocalAI |
| 11 | SQLite placement | `AGENT_DB_PATH` env: `/data/index.db` in container, `./index.db` on host; named volume `agent-data` by default | Keeps `index.db` + WAL/SHM siblings on one mount; clean, durable host mapping |
| 12 | Import config file | One `sources.yaml` at the repo root (YAML, strict decode: `KnownFields(true)`, `version:` field) | Providers are declared in one place and can be enabled/disabled/reconfigured without a rebuild; TOML rejected — the repo already speaks YAML (`eval/queries.yaml`) and YAML carries comments in a hand-edited file (decided 2026-10-23) |
| 13 | Provider model | One `Provider` interface (`List` cheap → `Fetch` full) + a registry keyed by config `type`; unknown type or bad options = startup error | One code path for every source; the `List`/`Fetch` split is what makes incremental ingest cheap and fixtures-testable without credentials |
| 14 | Ingest modes | Default = incremental against a state ledger (`source_state` + `source_items`); `--full` = re-fetch + re-chunk + **re-embed** (decided 2026-10-23) | On a full reindex the index is rebuilt exactly as the sources stand today, and the corpus is small enough (≤1,500 chunks) that the CPU cost is acceptable |
| 15 | Agent locus | The agent loop lives **in this service** (decided 2026-10-27): one tool-calling loop over the OpenAI-compatible chat model, one internal tool `search_writing` bound to `internal/retrieval`, bounded iterations, citation-or-refusal | A2A peers ask; we answer grounded from the corpus — "generation is the caller's problem" is rescoped: the caller is remote, the loop is ours |
| 16 | A2A surface | A2A v0.3 JSON-RPC (`message/send`, agent card at `/.well-known/agent.json`) exposing the **agent**, plus a plain `POST /v1/retrieve` for deterministic search clients; bearer-token, read-only, local-bind default. Dev build (2026-10-27): Phase 2 ships the same two paths token-less on loopback (`agent serve`), before any A2A work | Peers get answers, not bare search results; streaming and task resumability stay out until a peer actually needs them |

### 1.5 Verified environment (audited 2026-10-21; Go floor re-verified 2026-10-23)

Facts checked on the actual dev machines, so the plan stops guessing:

* **Host**: macOS (darwin/arm64), repo clean at bootstrap commit `7338ecf`.
* **Toolchain**: Go 1.27 (the `go.mod` floor; verified on the AI Node with go1.27.3 — `go build ./...`, `go vet ./...`, and `gofmt -l .` all clean). The Mac-side version has not been re-verified since 2026-10-21.
* **Credentials**: **none present** — `POSTIZ_API_KEY`, `POSTIZ_API_URL`,
  `GHOST_API_URL`, `GHOST_API_KEY` are all unset and there is no `.env`
  file. The old plan's "current on-disk key 401s" note is stale; on this
  host there is no key to be stale. Both keys are the Phase 0 blocker.
* **ollama**: CLI installed (`/usr/local/bin/ollama`) but the server is not
  running and no models are pulled. **No longer load-bearing** — LocalAI
  (below) is the default embed + chat backend. ollama stays an optional dev
  shortcut; nothing in this repo depends on it.
* **`codex`**: present at `/Users/ilja/.local/bin/codex`. No longer
  load-bearing either — `agent ask` goes through the same OpenAI-compatible
  connector instead of shelling out to codex.
* **Docker**: CLI 28.0.1 + Compose v2.33.1 (Docker Desktop) installed. The
  agent sandbox cannot reach the Docker daemon socket — container builds
  and `compose up` run with explicit permission or outside the sandbox.
* **LocalAI** — **an instance is already running at
  `http://192.168.78.243:8080`** (LAN host `dev`; the AI Node is
  `192.168.78.242`). Verified from the node 2026-10-23: `GET /readyz` →
  200; `GET /version` → `v4.12.0 (15e37df9)`; `GET /v1/models` →
  `embeddinggemma-300m`, `spark-x2.5-4b`, `spark-x2.5-1.7b-q4`;
  `POST /v1/embeddings` with `embeddinggemma-300m` returns **768** dims
  (matches `chunks_vec.embedding float[768]`); `POST /v1/chat/completions`
  with `spark-x2.5-4b` answers. `spark-x2.5-4b` is a **reasoning** model —
  the response carries a `reasoning` field, so `max_tokens` must budget
  for reasoning or `content` comes back empty (observed at
  `max_tokens=10`). This is the model backend: we consume it, we do not
  deploy it. The container-image facts (CPU `localai/localai`, GPU
  variants, `/readyz` liveness) are kept only as a documented fallback if
  the LAN host goes away.
* **`postiz` CLI**: **not installed** here. Phase 0 capture therefore uses
  plain `curl` against `POSTIZ_API_URL` (or is run wherever the CLI exists);
  nothing in this repo may depend on a `postiz` binary.
* **No systemd** (darwin): Phase 3 scheduling is launchd or user cron.
  No `AGENTS.md` exists in this repo either — earlier references to
  openclaw/AGENTS.md constraints were from another machine and are dropped.

---

## 2. Repo layout (target)

```
elilap.dev-agent/
├── PLAN.md                           # this file
├── README.md
├── example.env                       # committed inventory: every env var,
│                                     # defaults listed / secrets empty
├── .gitignore                        # index.db / .env / model weights
├── Dockerfile                        # multi-stage: golang:1.27 → minimal runtime
├── .dockerignore
├── compose.yaml                      # agent service only; LocalAI is external
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
│   ├── store/                        # SQLite, migrations, FTS5 glue, sqlite-vec
│   ├── embed/                        # batching/resume logic over the llm connector
│   ├── config/                       # sources.yaml loader + strict validation
│   ├── llm/                          # one OpenAI-compatible HTTP client (LocalAI default)
│   ├── retrieval/                    # hybrid FTS + vec + RRF — the search_writing tool's engine
│   └── agentloop/                    # the in-service loop: tool schema, grounded prompt, budgets, A2A task runner
├── migrations/
│   ├── 0001_init.sql
│   └── 0002_state.sql                # source_state + source_items ledger
├── eval/                             # Phase 2: queries.yaml + ground truth
├── scripts/
│   ├── ask                           # thin wrapper over `agent ask` (Phase 2+)
│   ├── check-ddl.py                  # schema smoke check (runs under plain python3)
│   └── ingest-all.sh                 # cron-friendly
└── docs/
    ├── agent-architecture.md         # the loop + the HTTP dev / A2A surfaces (2026-10-27 scope change)
    ├── api-shapes.md                 # Phase 0 output
    ├── ghost.md                      # Phase 0 capture
    ├── postiz.md                     # Phase 0 capture
    └── runbook.md                    # Phase 3: ops + evaluation
```

Current state vs. target: the five `cmd/*/main.go` stubs exist and compile;
they are collapsed into `cmd/agent/` as the first Phase 1 task. Everything
under `internal/` is empty (`.gitkeep`). `migrations/0001_init.sql`,
`scripts/*`, and `docs/*.md` exist as Phase-0/1 placeholders.
The container files (`Dockerfile`, `.dockerignore`, `compose.yaml`) do not
exist yet — they are Phase 1 deliverables. `localai/` is no longer part of
this layout: the model server is external (Decision 10), so its model
configs and weights live on `192.168.78.243`, not in this repo.

### 2.5 Container topology (target)

One `docker compose up` brings up the agent on any Docker host; the model
server is external (Decision 10):

```yaml
services:
  agent:
    build: .
    environment:
      LOCALAI_BASE_URL: http://192.168.78.243:8080/v1   # self-hosted LocalAI on host `dev`
      EMBED_MODEL: embeddinggemma-300m
      CHAT_MODEL: spark-x2.5-4b
      AGENT_DB_PATH: /data/index.db
    volumes:
      - agent-data:/data

volumes:
  agent-data:
```

**LocalAI is external now.** The compose stack runs the agent only; the
model server is the pre-existing instance on `192.168.78.243` (Decision 10).
Every variable in that block is part of the committed inventory in
`example.env` (defaults listed, secrets empty); on a host run the binary
loads `.env` at startup with godotenv, and a variable already exported wins
over the file, so compose passes them directly and no `.env` ships in the
image.
Two consequences: (a) the container needs a route to that LAN address —
true on the node's default bridge, but it is now a hard dependency on
another host, so the agent must probe `/readyz` itself and fail fast with
a clear message; (b) there is no `depends_on` health gate any more, so the
readiness probe moves into the agent's own startup path. If `dev` ever goes
away, the fallback is to add a `localai` service back here (CPU image,
pinned to a versioned tag) and point `LOCALAI_BASE_URL` at it.

**SQLite mapping contract** (the clean way):

* The DB path is one env var, `AGENT_DB_PATH` — `/data/index.db` inside the
  container, `./index.db` in a host run. Same binary, same behavior.
* `/data` is a **named volume** (`agent-data`) by default: durable across
  container recreations, fast on every platform, and it keeps `index.db`,
  `index.db-wal`, `index.db-shm` together on one filesystem (required by
  SQLite WAL — never split them across mounts).
* A bind mount (`-v "$PWD/data:/data"`) is supported when the file must be
  host-visible (inspection, backup). On macOS bind mounts are slower
  (VirtioFS) — fine occasionally, not the default.
* Backup: `agent backup` (online `VACUUM INTO`, Phase 3) or stop + copy the
  volume. Documented in the runbook.

**Image contract**: multi-stage `Dockerfile` — builder `golang:1.27`
(Debian variant: CGO on, toolchain present, tag floats to the newest 1.27
patch) → runtime `debian:bookworm-slim` carrying only the binary plus CA
certs and tzdata. If the Phase 1 driver spike lands a static or pure-Go
build (ncruces), the runtime collapses to `distroless`/`scratch` as a
one-line swap. We do not ship LocalAI in the image — it runs on
`192.168.78.243` (Decision 10, currently `v4.12.0`), so model behavior
depends on that host's build, not ours. If the fallback service is ever
added here, pin it to a **versioned tag, not `latest`**.

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

- [ ] Create `example.env` - the committed inventory of every environment
      variable, defaults listed and secret values empty - and document load
      order (`.env` loaded at startup for humans, real env for cron/launchd
      and compose). The inventory: the four source credentials plus runtime
      config - `LOCALAI_BASE_URL`, `LOCALAI_API_KEY` (optional), `EMBED_MODEL`,
      `CHAT_MODEL`, `AGENT_DB_PATH`. Decode it with envconfig structs
      (`internal/envcfg`, `internal/logging`); empty means unset. Every new
      variable must be added to `example.env` in the same change
      (`docs/environment.md`).
- [ ] Author `sources.yaml` (§2.6) with one entry per provider (`ghost`,
      `postiz`), `enabled: true`, credentials as `{ env: VAR }` references
      and no values. `agent sources validate` must pass before any
      credential exists.
- [ ] Obtain **both** credentials (Postiz and Ghost) — neither exists on
      this host today. They enter via env / `.env` only; we will not echo,
      log, or commit them.
- [ ] Confirm `POSTIZ_API_KEY` works against `https://postiz.mist.lv/api`
      using `curl` (no `postiz` CLI on this host). Capture the real
      `/posts` (or equivalent) response shape: post id, content text,
      integrations (per-platform), status, publish date, media, release id.
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
SQLite index with `chunks` + `chunks_fts` + `chunks_vec`. Embedding is
intentionally a separate command.

**Tasks**

- [ ] Collapse `cmd/{ingest,embed,query,stats,serve}/main.go` stubs into
      `cmd/agent/` with subcommand dispatch (Decision 7). `go build`
      still green after.
- [ ] `internal/config`: load `sources.yaml` (YAML, `KnownFields(true)`,
      `version` check), validate per-provider options, resolve `{ env: VAR }`
      credentials without printing values; `sources validate` warns on missing
      variables by default and fails only with `--strict`.
- [ ] `internal/sources/provider.go` + `registry.go`: the `Provider`
      interface and the `type → constructor` registry; an unknown type is a
      startup error.
- [ ] `migrations/0002_state.sql`: the `source_state` + `source_items`
      ledger; retain the existing item-level `chunks.source_id` and
      `chunks.content_hash` from `0001_init.sql`.
- [ ] `agent sources list` / `agent sources validate`: show enabled
      providers and resolved options (never secrets); validate the file
      with no credentials present.
- [ ] `agent ingest` modes: incremental by default against the ledger;
      `--full` re-fetches, re-chunks and re-embeds; `--source <id>` scopes
      to one provider; `--dry-run` reports the plan without writing.
- [ ] Driver spike (small, first): pin `mattn/go-sqlite3` + sqlite-vec Go
      bindings in `go.mod`; prove FTS5 BM25 query + `vec0` insert/KNN
      round-trip in a scratch DB on this Mac **and inside
      `docker compose build`** — the container is where CGO toolchain
      surprises actually bite. If CGO misbehaves, decide the fallback
      (ncruces driver) at this spike, not mid-build. The spike also fixes
      the runtime base image (glibc `bookworm-slim` for a CGO build vs
      `distroless`/`scratch` for a static or pure-Go build).
- [ ] `internal/llm` connector: one small OpenAI-compatible HTTP client
      (plain `net/http`, no SDK) — `POST /v1/embeddings`,
      `POST /v1/chat/completions`, `GET /v1/models`. Config:
      `LOCALAI_BASE_URL` (default `http://192.168.78.243:8080/v1` — the
      verified self-hosted instance), `LOCALAI_API_KEY` (optional),
      `EMBED_MODEL` (default `embeddinggemma-300m`), `CHAT_MODEL`
      (default `spark-x2.5-4b`). LocalAI is the default; any
      OpenAI-compatible server (llama.cpp server, vLLM, OpenAI itself)
      works by changing the base URL. The tool-calling loop (Decision 15)
      rides on the same connector: chat requests carry the
      `search_writing` tool definition, and the client must round-trip
      the model's tool calls. Two behaviours the client must
      honour: embeddings are **768d** (the schema says `float[768]` —
      assert the dimension before writing, or refuse to write), and
      `spark-x2.5-4b` is a **reasoning** model whose response carries a
      `reasoning` field — `max_tokens` must budget for reasoning or
      `content` comes back empty.
- [ ] Containerize (§2.5): `Dockerfile`, `.dockerignore`, `compose.yaml`
      with the agent service, `agent-data` volume, `AGENT_DB_PATH` wiring,
      and `LOCALAI_BASE_URL` pointing at `http://192.168.78.243:8080/v1`.
      No LocalAI service and no `/readyz` healthcheck in compose — the
      agent probes `/readyz` on the model host itself at startup and fails
      fast with a clear error. Embed model `embeddinggemma-300m` (768d,
      matches the `vec0` schema).
- [ ] Store layer: migration runner that loads the sqlite-vec extension
      *before* applying `0001_init.sql` (the `vec0` DDL fails without it).
      `scripts/check-ddl.py` stays green as the schema evolves.
- [ ] Source clients (`internal/sources/ghost`, `postiz`) + shared chunker;
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
CREATE TABLE chunks (
  chunk_id       TEXT PRIMARY KEY,           -- sha256(source_kind|source_id|chunk_seq), hex
  source_kind    TEXT NOT NULL CHECK (source_kind IN ('ghost_post','postiz_post')),
  source_id      TEXT NOT NULL,              -- upstream item id (Ghost uuid / Postiz id)
  source_url     TEXT,
  platform       TEXT,                       -- null for ghost
  author         TEXT NOT NULL,
  published_at   TEXT NOT NULL,              -- ISO-8601 UTC
  ingested_at    TEXT NOT NULL,              -- local write time
  updated_at     TEXT NOT NULL,              -- upstream last-modified time
  content_hash   TEXT NOT NULL,              -- sha256 of normalized text
  title          TEXT,
  section        TEXT,                       -- heading path; null for social
  body           TEXT NOT NULL,
  body_tokens    INTEGER NOT NULL,
  meta_json      TEXT NOT NULL DEFAULT '{}', -- tags, status, release id, etc.
  embedded_with  TEXT,                       -- e.g. 'localai:embeddinggemma-300m@<git-sha>'
  embedded_at    TEXT                       -- ISO-8601
);

CREATE INDEX idx_chunks_source      ON chunks (source_kind, source_id);
CREATE INDEX idx_chunks_published   ON chunks (published_at);
CREATE INDEX idx_chunks_hash        ON chunks (content_hash);

-- Self-contained FTS5 (NOT contentless): stored columns are readable, so
-- matches join back to chunk_id directly. Duplicating body text is fine
-- at ≤1,500 chunks. `tags` values come from meta_json at write time.
-- The store keeps chunks and chunks_fts in sync inside one transaction.
CREATE VIRTUAL TABLE chunks_fts USING fts5(
  chunk_id UNINDEXED, title, body, tags,
  tokenize='porter unicode61'
);

CREATE VIRTUAL TABLE chunks_vec USING vec0(
  chunk_id TEXT PRIMARY KEY,
  embedding float[768]
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
- `agent embed` finishes in <5 min on this hardware against the
  self-hosted LocalAI at `192.168.78.243` (embed model already loaded
  there — no first-boot download).
- `docker compose build` produces the image and `docker compose up` yields
  an agent container that reaches the model host (`/readyz` on
  `192.168.78.243:8080`) with `agent embed` working from a clean
  container — no host Go toolchain, no ollama, no codex.
- A second incremental run is a near no-op (no re-fetch, no re-embed).
- `agent ingest --full` rebuilds every chunk and every embedding from the
  sources as they stand today.
- All keys stay in env; no secret leaks in `git log -p` or stdout.

**Estimated effort**: 3–4 focused days (containerization + connector added).

---

### Phase 2 — Retrieval & the in-service agent loop

**Goal**: prove retrieval quality on real queries, then wire the loop that
makes this service an agent: one tool (`search_writing`) bound to
`internal/retrieval`, a grounded prompt, bounded tool-calling iterations,
and a citation-checked answer. Phase 2 also ships a **minimal HTTP dev
surface** (`agent serve`, decided 2026-10-27): token-less, loopback-only,
so both halves are curl-testable — `POST /v1/retrieve` returns exactly
what `search_writing` hands the model (retrieval in isolation, no LLM),
and `POST /v1/ask` runs the full loop. Same paths and JSON as Phase 4;
only auth and hardening get added later.

**Tasks**

- [ ] `agent query` (no LLM in the loop — the raw tool, fully visible):
  - inputs: free-text, `--top N`, `--hybrid` (on by default),
    `--source ghost|postiz|all`, `--platform` (repeatable), `--after`,
    `--before`, `--exclude-scheduled`, `--recency-halflife-days`,
    `--format jsonl|text`.
  - outputs: per-chunk metadata + body + score.
- [ ] `eval/queries.yaml`: 20 hand-picked queries with ground-truth
  chunk_ids, so the eval is a reviewable artifact, not a one-off script.
  Target ≥15/20 with a clearly correct, verifiable answer. Once the loop
  lands, the same 20 run end-to-end: the loop's *answers* (with
  citations) are graded, not just raw retrieval.
- [ ] The **in-service agent loop** (Decisions 15/16) —
      `internal/agentloop`, consumed by both `agent ask` and, in Phase 4,
      the A2A server:
  - one internal tool, `search_writing`, wrapping `internal/retrieval`
    (params: `query`, `top_n`, `source`, `platform`, `after`, `before`,
    `recency_halflife_days`);
  - grounded prompt assembly (template below) and bounded tool-calling
    iterations over the `internal/llm` connector (chat model
    `spark-x2.5-4b`; `max_iterations` default 3, a total excerpt-token
    budget, a per-request wall-clock cap);
  - citation enforcement: every claim must cite `chunk_id` + `source_url`
    from retrieved chunks; on grounding failure the answer is the refusal
    string — the loop never answers off-corpus from parametric memory;
  - no retries across requests, no cross-request state, no host `codex`
    dependency.
- [ ] `agent ask`: CLI form of the loop — same prompt, same budget, same
      refusal behavior. `scripts/ask` stays as a thin wrapper around
      `./agent ask` for muscle memory.
- [ ] `agent serve` (dev form): plain `net/http`, binds `127.0.0.1:8080`
      (override with `--addr`), **no auth, loopback-only** — it exists to
      be curl-tested during development, never exposed:
  - `POST /v1/retrieve` — request body carries the `search_writing`
    params (`query`, `top_n`, `source`, `platform`, `after`, `before`,
    `recency_halflife_days`); the response is exactly the JSON array the
    model's tool call would receive — same shape as
    `agent query --format jsonl` (Decision 16 dev variant).
  - `POST /v1/ask` — `{"question": "..."}` in; the full loop out:
    `{"answer", "citations": [{"chunk_id", "source_url"}],
    "tool_iterations", "refused", "elapsed_ms"}` — the same struct
    `agent ask` prints; Phase 4 wraps it in an A2A `Message` without
    redefining it.

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

**Loop policy (shared by `agent ask` and the A2A task runner)**

* One internal tool: `search_writing` (params: `query`, `top_n`,
  `source`, `platform`, `after`, `before`, `recency_halflife_days`).
* Budget: max 3 tool iterations and ~4,000 total prompt tokens of
  excerpts per answer; stop on budget or when the model stops calling
  the tool.
* Every claim in the answer must cite a `chunk_id` + `source_url` from
  the retrieved set; if grounding fails, the answer is the refusal
  string. No tool other than `search_writing` exists in Phase 2 — the
  loop is intentionally minimal, and new tools are new decisions.

**Exit criteria**

- 20-query eval reaches ≥15/20 with verified chunk_ids.
- p50 retrieval latency <300ms; end-to-end `ask` p50 <8s on a GPU-capable
  host, ≤60s on the CPU-only Docker setup (small chat model, reasoning
  budget included).
- Loop behavior verified against `spark-x2.5-4b`: tool calls round-trip,
  the iteration/token budget holds, and unanswerable questions return
  the refusal string instead of hallucinated content.
- A second human can read `README.md` + `docs/runbook.md` and run `ask`
  without help — including via `docker compose up` on a clean host.
- `POST /v1/retrieve` and `agent query --format jsonl` return identical
  results for identical inputs; `POST /v1/ask` returns the same answer
  content as `agent ask` for the same question (model non-determinism
  aside).
- The dev server refuses to start with a non-loopback bind address —
  token-less dev convenience must not be exposable (auth is Phase 4).

**Estimated effort**: 1–2 days (about half a day of it is the dev
server).

---

### Phase 3 — MVP operations

**Goal**: the system runs unattended and you actually use it.

**Tasks**

- [ ] `scripts/ingest-all.sh`: cron/launchd-friendly wrapper (already a
      stub; make it call the built binary, not `go run`, once Phase 1 ships).
      Container mode: `docker compose run --rm agent agent ingest`.
- [ ] `agent ingest --watch`: poll upstream every N minutes during work
      hours, no-op when nothing changed. In container mode the `agent`
      service runs long-lived with `restart: unless-stopped`.
- [ ] `agent backup`: online `VACUUM INTO` snapshot of the SQLite file
      into `/data` (safe while the writer is live).
- [ ] Schedule on this Mac: **launchd** agent or user cron (no systemd on
      darwin) invoking the container command above. Hourly default.
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

### Phase 4 — A2A surface: our agent, on the wire (token-gated)

**Goal** (revised 2026-10-27): expose the **in-service agent loop** over
A2A for remote peers. A peer sends a conversational task (`message/send`);
our service runs the same loop `ask` runs — retrieve with
`search_writing`, ground, answer with citations — and returns the answer
as an A2A `Message` with citation metadata. Raw retrieval stays exposed
as a plain JSON-RPC endpoint for deterministic callers, but the
peer-facing product is the agent. No write surface; no public web
exposure by default. The Phase 2 dev endpoints graduate here rather than
being rebuilt: same paths, same JSON, plus bearer-token auth, public
bind behind an explicit flag, rate limits, and the audit log.

**Tasks**

- [ ] `agent serve`: HTTP server binding to `127.0.0.1` (default) or
  `0.0.0.0` (only with `--bind-public`); token auth via
  `Authorization: Bearer <token>`. (Container mode: publish as
  `ports: "127.0.0.1:8081:8080"` — a container's loopback is not the
  host's; public exposure stays opt-in.)
  `/v1/retrieve` and `/v1/ask` graduate unchanged from the Phase 2
  dev server — same request/response JSON, now token-gated.
- [ ] A2A endpoint: `message/send` JSON-RPC at `/a2a` (A2A v0.3), running
  the Phase 2 loop per task; stateless per request — a task is answered
  in one request or fails. No persisted task store and no task
  resumption until a peer actually needs it; `capabilities.streaming` is
  `false` in v1 (add `message/stream` over HTTP+SSE later if a peer
  asks).
- [ ] `POST /v1/retrieve`: plain JSON-RPC forward to `internal/retrieval`,
  same schema as `agent query --format jsonl` — the deterministic escape
  hatch for callers that want search, not conversation.
- [ ] Agent card at `GET /.well-known/agent.json` (A2A v0.3 `AgentCard`):
  `name`, `description`, `version`, `supportedInterfaces` (JSONRPC
  binding + `protocolVersion`), `capabilities`, `securitySchemes`
  (bearer), and one skill describing grounded Q&A over Eli's published
  writing.
- [ ] Audit log: per-task timestamp, token id (not token), query, tool
  iteration count, result count, answer/refusal. Bodies never logged.
- [ ] Token store: env-driven list of `id:token` pairs; `--add-token`,
  `--revoke-token` subcommands; rotation is config-only.
- [ ] Rate limit: 30 tasks/min per token (configurable) plus a per-task
  wall-clock budget (loop iterations + chat tokens) so one peer cannot
  pin the single LocalAI host.
- [ ] Operational documentation in `docs/runbook.md`.

**Security posture (documented and enforced)**

- API is read-only — no methods that mutate the index or upstream
  sources.
- The loop sees only the question plus index excerpts; peer-supplied
  text is data, never instructions — the grounded prompt must survive a
  prompt injection arriving through a peer's message (the refusal path
  is the backstop).
- Local bind by default; public bind requires an explicit flag AND a
  token.
- Audit log never contains chunk bodies, prompts, or user answers.
- Tokens are high-entropy random ≥32 bytes; the optional token file is
  `0600` and env-driven (see open question 3).

**Exit criteria**

- An A2A client (or `curl` with a hand-built JSON-RPC envelope) can send
  `message/send` and receives a grounded, citation-carrying answer — or
  the refusal string — identical in content to what `agent ask` produces
  for the same question.
- `/.well-known/agent.json` validates as an A2A AgentCard and its skill
  description matches the loop's actual behavior.
- `POST /v1/retrieve` returns results identical to `agent query` CLI
  output and to the Phase 2 dev endpoint for the same inputs.
- Audit log shows the task with token id, query, iterations, result
  count.
- Token issuance/revocation is config-only; no service restart needed.
- `docs/runbook.md` documents the rotation story and the per-task
  budget.

**Estimated effort**: 3–4 days (loop-over-A2A integration + card, auth,
rate limits).

---

## 4. Risks & mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Credentials missing entirely (verified 2026-10-21) | Certain today | Phase 0+ blocked | Keys are the one human blocker; Phase 1 store/chunker work can start in parallel against fixtures |
| Postiz response shape differs from docs | Medium | Phase 1 schema rework | Phase 0 captures real shape first |
| Ghost response shape differs from expectations | Low | Same as above | Phase 0 captures one real post |
| sqlite-vec CGO build trouble on darwin/arm64 | Medium | Build fragility | Phase 1 driver spike decides mattn vs ncruces before store code lands; pin versions |
| CGO/sqlite-vec misbehaves inside the container | Medium | Image build breaks | Driver spike must prove `docker compose build` + KNN round-trip in-container before store code lands |
| Model host `dev` (192.168.78.243) down or unreachable from the container | Medium | `embed`/`ask` blocked | Agent probes `/readyz` at startup and fails fast with a clear message; models are pre-loaded, so no first-boot wait; documented fallback is a local `localai` compose service |
| Model host is CPU-only (no GPU confirmed for `dev`) | Medium | Slow chat | `spark-x2.5-4b` is a 4B model — acceptable at this corpus size; Phase 2 latency targets are measured against the real host, not guessed |
| `spark-x2.5-4b` is a reasoning model — the token budget is eaten by `reasoning` | Medium | Empty `content` in `agent ask` | `max_tokens` must budget for reasoning; `agent ask` reads the `reasoning` field and errors loudly on empty content; `spark-x2.5-1.7b-q4` is the lighter fallback |
| Docker daemon unreachable from the agent sandbox (verified) | Certain in-sandbox | Slow dev loop for container steps | Run container commands with explicit permission or outside the sandbox; never bake keys into the image |
| SQLite on a macOS bind mount (VirtioFS) is slow / has locking quirks | Low | Degraded perf when DB is host-visible | Named volume is the default; bind mount documented for export only |
| Contentless-FTS5 trap (caught in refinement) | Fixed | Would have broken query join-back | Self-contained FTS5 in `0001_init.sql` |
| Embedding quality too low | Low | Phase 2 eval falls short | Point `LOCALAI_BASE_URL` at a stronger server or change the model id; the `embedded_with` audit makes re-runs cheap |
| Config typo silently disables a provider | Medium | A source goes dark without an error | Strict YAML decode (`KnownFields(true)`) + `agent sources validate`; the enabled set is printed before each run |
| Full reindex re-embeds the whole corpus | Medium | CPU time on the model host | Deliberate (2026-10-23); ≤1,500 chunks at 768d; `--source` to scope; incremental stays the default |
| Ledger and index disagree after a manual DB edit | Low | Silent wrong results | `source_state.config_hash` records the config that produced the index; a mismatch forces a full reindex |
| Posts scheduled into future not yet live get indexed | Medium | Stale context | Skip `status='scheduled'` by default |
| Index drift silent | Medium | Wrong answers | `agent stats` diffs count against source |
| A2A token leaked | Low | Read-only exposure of published corpus | Tokens are scoped read-only; rotated monthly |
| Phase 2 dev server (token-less by design) reaches a non-loopback interface | Low | Read-only exposure of the corpus without auth | Loopback bind is the default and a public address fails startup; tokens and rate limits land in Phase 4 |
| Loop grounds on weak retrieval (model calls the tool once, answers from thin context) | Medium | Confident but uncited or wrong answers on A2A | Prompt requires a citation for every claim and the refusal string otherwise; Phase 2 eval grades loop answers, not just raw retrieval; the iteration budget forces a second retrieval attempt |
| Prompt injection via a peer's task text | Low | Refusal bypass or fabricated citations | Peer text is data, never instructions (grounded prompt); citations must resolve to retrieved `chunk_id`s; the API is read-only, capping the blast radius |
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
4. ~~Default LocalAI chat model~~ — **decided 2026-10-23**: `spark-x2.5-4b`
   (self-hosted on `dev`). `spark-x2.5-1.7b-q4` is the fallback if the
   Phase 2 eval shows the 4B is too slow on that host.
5. ~~Pin LocalAI by versioned tag~~ — **moot 2026-10-23**: the instance we
   use is external (`192.168.78.243`, currently `v4.12.0`), so its version
   is that host's concern. We depend on the OpenAI-compatible surface, not
   on a specific build.
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
8. **Decided 2026-10-27 — the agent lives here.** A2A peers talk to *our*
   loop; RAG (`internal/retrieval`) is its internal tool. Consequences:
   Phase 2 builds the tool-calling loop (not a one-shot wrapper), Phase 4
   serves the agent card + `message/send` (raw `/v1/retrieve` remains for
   deterministic callers), and "generation is the caller's problem" is
   rescoped to "generation is ours — grounded in the index, read-only,
   citation-or-refusal". Multi-tool autonomy and peer-side loop hosting
   stay out of scope.
9. Should the Phase 2 dev `/v1/ask` stream partial tokens? **Default
   (2026-10-27)**: no — the dev surface exists to diff against
   `agent ask`, and streaming would change the answer contract; revisit
   only if a Phase 4 peer asks for `message/stream`, in which case it
   arrives as an A2A capability, not a dev-endpoint feature.

---

## 6. Done = …

The project is "done" when:

* `agent ingest` is idempotent against both sources.
* `agent query` returns verifiable, citable answers.
* The repo + one `index.db` file constitute the entire operational footprint
  (the index lives in the `agent-data` volume in container mode).
* The whole system runs from `docker compose up` on a clean host with only
  a container runtime — environment independence proven.
* A second agent can task our agent over A2A and receive a grounded,
  citation-carrying answer produced by the in-service loop.
* It runs unattended for a month without manual intervention.

We are not done at MVP. We are not done at Phase 2. We are done at the
end of Phase 4: an A2A-reachable agent, with the loop, the tool, and the
grounding guarantees living in this repo.
