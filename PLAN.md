# PLAN.md — elilap.dev-agent

Build plan for a local-first RAG agent over Eli's published writing.
This document is the source of truth for scope, sequencing, and exit criteria.
Update it as we learn.

**Last refined**: 2026-10-21, audited against the bootstrap scaffold (commit
`7338ecf`) and the actual dev host (§1.5). First pass corrected the host
assumptions, the CLI shape (one binary, not five), and a contentless-FTS5
bug in the original schema. Second pass (same day) made the runtime
**environment-independent**: a self-hosted LocalAI replaces ollama/codex as
the model backend behind one OpenAI-compatible connector, and the app ships
as a multi-stage Docker image with a volume-mapped SQLite index — see §2.5
and §3.

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
7. Environment-independent: the app plus a self-hosted LocalAI run from one
   `docker compose up` on any host with a container runtime — no host Go
   toolchain, no ollama, no codex required.

**Non-goals (deliberately not in scope)**

* Membership/auth/payments on the A2A surface — read-only.
* Re-writing or summarizing the corpus — retrieval only, generation is the
  caller's problem.
* A web UI. The CLI is the surface; a UI is a future concern.
* Mutating upstream content. This repo never writes to Ghost or Postiz.
* Real-time streaming indexes. Hourly polling is fine.
* Model serving at scale (multi-GPU, clustering) — one LocalAI container is
  the ceiling.

---

## 1. Decisions already locked in

| # | Decision | Choice | Rationale (one line) |
|---|---|---|---|
| 1 | Language | Go 1.27 (language floor) | Single binary, no runtime, easy ops |
| 2 | Index store | SQLite + `sqlite-vec` (one file) | Corpus ≤1,500 chunks — service overkill, file-portable, FTS5 free |
| 3 | Embedder + chat model | Self-hosted **LocalAI** behind one OpenAI-compatible connector (`/v1/embeddings`, `/v1/chat/completions`); default embed model `nomic-embed-text` (768d) | Self-hosted and environment-independent; swapping providers = changing `LOCALAI_BASE_URL` |
| 4 | Retrieval | Hybrid: FTS5 BM25 + vec cosine, RRF merge | Neither alone is good enough on mixed prose+social |
| 5 | Repos / sources | Ghost Admin API + Postiz HTTP API | Live, queryable, idempotent on `(source_kind, source_id)` |
| 2b | Go driver | CGO: `mattn/go-sqlite3` (`-tags sqlite_fts5`) + sqlite-vec Go bindings (asg017), pinned | FTS5 + vec0 in one DB handle; exact binding import path confirmed in the Phase 1 driver spike, not before |
| 6 | Repo location | `/Users/ilja/projects/elilap.dev-agent` (this host); remote `git@github.com:eli-l/elilap.dev-agent.git` — both verified 2026-10-21 | Previous plan revision assumed a different machine (`/home/openbot/...`, systemd); corrected |
| 7 | CLI surface | One binary `agent`, subcommands `ingest`, `embed`, `query`, `stats`, `serve` | Matches `./agent ingest` muscle memory; the five separate `cmd/*/main.go` stubs collapse into `cmd/agent/` in Phase 1 |
| 8 | Auth posture | env-loaded keys; never printed; gitignored index | Defense by default |
| 9 | Container runtime | Multi-stage Dockerfile (builder `golang:1.27` → minimal runtime) + `compose.yaml` with agent + localai services | Whole system runs on any Docker host; builder tag floats to the latest 1.27 patch ≥ go.mod's `1.27` |
| 10 | Model serving | LocalAI container, **versioned** image tag (CPU image default; GPU tags exist per platform) | One server covers embeddings + chat; `/readyz` healthcheck; OpenAI-compatible API |
| 11 | SQLite placement | `AGENT_DB_PATH` env: `/data/index.db` in container, `./index.db` on host; named volume `agent-data` by default | Keeps `index.db` + WAL/SHM siblings on one mount; clean, durable host mapping |

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
* **LocalAI** (facts from official docs, checked 2026-10-21): serves
  OpenAI-compatible `/v1/chat/completions`, `/v1/completions`,
  `/v1/embeddings`, `/v1/models`; official image `localai/localai` (CPU)
  with GPU variants (`-gpu-nvidia-cuda-12`, `-gpu-hipblas`,
  `-gpu-vulkan`); volumes `/models`, `/backends`, `/configuration`,
  `/data`; liveness via `GET /readyz`; current release line v4.11.x. Docs
  warn Docker on Apple Silicon runs a Linux VM without Metal → CPU-only
  there. Acceptable for embeddings at ≤1,500 chunks; chat latency targets
  in Phase 2 account for CPU.
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
├── .env.example                      # Phase 0: var names, no values
├── .gitignore                        # index.db / .env / model weights
├── Dockerfile                        # multi-stage: golang:1.27 → minimal runtime
├── .dockerignore
├── compose.yaml                      # agent + localai services, volumes, healthchecks
├── go.mod
├── cmd/
│   └── agent/
│       └── main.go                   # one binary; subcommand dispatch +
│                                     # one file per subcommand (ingest.go,
│                                     # embed.go, query.go, stats.go, serve.go)
├── internal/
│   ├── sources/
│   │   ├── ghost/                    # Ghost Admin client
│   │   ├── postiz/                   # Postiz HTTP client
│   │   └── shared/                   # common chunker + hash + types
│   ├── store/                        # SQLite, migrations, FTS5 glue, sqlite-vec
│   ├── embed/                        # batching/resume logic over the llm connector
│   ├── llm/                          # one OpenAI-compatible HTTP client (LocalAI default)
│   └── retrieval/                    # hybrid FTS + vec + RRF
├── localai/
│   └── models/                       # LocalAI model YAMLs (committed; GGUFs gitignored)
├── migrations/
│   └── 0001_init.sql
├── eval/                             # Phase 2: queries.yaml + ground truth
├── scripts/
│   ├── ask                           # thin wrapper over `agent ask` (Phase 2+)
│   ├── check-ddl.py                  # schema smoke check (runs under plain python3)
│   └── ingest-all.sh                 # cron-friendly
└── docs/
    ├── architecture.md               # Phase 2, kept thin
    ├── api-shapes.md                 # Phase 0 output
    ├── ghost.md                      # Phase 0 capture
    ├── postiz.md                     # Phase 0 capture
    └── runbook.md                    # Phase 3: ops + evaluation
```

Current state vs. target: the five `cmd/*/main.go` stubs exist and compile;
they are collapsed into `cmd/agent/` as the first Phase 1 task. Everything
under `internal/` is empty (`.gitkeep`). `migrations/0001_init.sql`,
`scripts/*`, and `docs/*.md` exist as Phase-0/1 placeholders.
The container files (`Dockerfile`, `.dockerignore`, `compose.yaml`,
`localai/`) do not exist yet — they are Phase 1 deliverables.

### 2.5 Container topology (target)

One `docker compose up` brings up the whole system on any Docker host:

```yaml
services:
  localai:
    image: localai/localai:<pinned-version>   # CPU image default; GPU tags per host
    volumes:
      - ./localai/models:/models              # YAML configs committed; GGUFs fetched once, gitignored
      - localai-data:/data
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8080/readyz"]
  agent:
    build: .
    environment:
      LOCALAI_BASE_URL: http://localai:8080/v1
      AGENT_DB_PATH: /data/index.db
    volumes:
      - agent-data:/data
    depends_on:
      localai:
        condition: service_healthy

volumes:
  localai-data:
  agent-data:
```

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
one-line swap. LocalAI is pinned to a **versioned tag, not `latest`**, so
model behavior doesn't drift under us.

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

- [ ] Create `.env.example` with the variable names (no values) and
      document load order (`.env` for humans, real env for cron/launchd):
      the four source credentials plus runtime config — `LOCALAI_BASE_URL`,
      `LOCALAI_API_KEY` (optional), `EMBED_MODEL`, `CHAT_MODEL`,
      `AGENT_DB_PATH`.
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
      `LOCALAI_BASE_URL` (default `http://localai:8080/v1`),
      `LOCALAI_API_KEY` (optional), `EMBED_MODEL`, `CHAT_MODEL`. LocalAI is
      the default; any OpenAI-compatible server (llama.cpp server, vLLM,
      OpenAI itself) works by changing the base URL.
- [ ] Containerize (§2.5): `Dockerfile`, `.dockerignore`, `compose.yaml`
      with agent + localai services, `/readyz` healthcheck, `agent-data`
      volume, `AGENT_DB_PATH` wiring. LocalAI model configs pinned under
      `localai/models/` — embed model `nomic-embed-text` (768d, matches the
      `vec0` schema); GGUF weights gitignored, fetched on first boot.
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
  source_id      TEXT NOT NULL,
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
  embedded_with  TEXT,                       -- e.g. 'localai:nomic-embed-text@<git-sha>'
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

(`chunk_seq` is the per-source ordinal of the chunk; it feeds `chunk_id`.)

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
- `agent embed` finishes in <5 min on this hardware against the LocalAI
  container (embed model loaded; first-boot model download excluded).
- `docker compose build` produces the image and `docker compose up`
  reaches a healthy LocalAI (`/readyz`) with `agent embed` working from a
  clean container — no host Go toolchain, no ollama, no codex.
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
      retries, no host `codex` dependency. `scripts/ask` stays as a thin
      wrapper around `./agent ask` for muscle memory.

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
  host, ≤60s on the CPU-only Docker setup (small chat model).
- A second human can read `README.md` + `docs/runbook.md` and run `ask`
  without help — including via `docker compose up` on a clean host.

**Estimated effort**: 1–2 days.

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
| Credentials missing entirely (verified 2026-10-21) | Certain today | Phase 0+ blocked | Keys are the one human blocker; Phase 1 store/chunker work can start in parallel against fixtures |
| Postiz response shape differs from docs | Medium | Phase 1 schema rework | Phase 0 captures real shape first |
| Ghost response shape differs from expectations | Low | Same as above | Phase 0 captures one real post |
| sqlite-vec CGO build trouble on darwin/arm64 | Medium | Build fragility | Phase 1 driver spike decides mattn vs ncruces before store code lands; pin versions |
| CGO/sqlite-vec misbehaves inside the container | Medium | Image build breaks | Driver spike must prove `docker compose build` + KNN round-trip in-container before store code lands |
| LocalAI first-boot model download is slow or fails | Medium | `embed`/`ask` blocked until models load | Pinned configs in `localai/models/`; `/readyz` gates the agent container; weights cached in the volume after first fetch |
| Apple-Silicon Docker is CPU-only (no Metal; per LocalAI docs) | Certain on this Mac | Slow chat | Small default chat model; CPU latency targets in Phase 2; native LocalAI binary as a Mac escape hatch |
| Docker daemon unreachable from the agent sandbox (verified) | Certain in-sandbox | Slow dev loop for container steps | Run container commands with explicit permission or outside the sandbox; never bake keys into the image |
| SQLite on a macOS bind mount (VirtioFS) is slow / has locking quirks | Low | Degraded perf when DB is host-visible | Named volume is the default; bind mount documented for export only |
| Contentless-FTS5 trap (caught in refinement) | Fixed | Would have broken query join-back | Self-contained FTS5 in `0001_init.sql` |
| Embedding quality too low | Low | Phase 2 eval falls short | Swap embed model in `localai/models/` or point `LOCALAI_BASE_URL` at a stronger server; `embedded_with` audit makes re-runs cheap |
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
4. Default LocalAI chat model: pick the smallest instruct model that passes
   the Phase 2 eval inside the CPU-only container (candidates from the
   LocalAI gallery). **Default**: decide at Phase 2 with the eval as the
   arbiter, not by preference.
5. Pin LocalAI by versioned tag (docs currently show v4.11.x) or by digest?
   **Default**: versioned tag; digest pinning only if tag drift bites.

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
