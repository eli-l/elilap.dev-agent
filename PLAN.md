# PLAN.md — elilap.dev-agent

Build plan for a local-first RAG agent over Eli's published writing.
This document is the source of truth for scope, sequencing, and exit criteria.
Update it as we learn.

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

**Non-goals (deliberately not in scope)**

* Membership/auth/payments on the A2A surface — read-only.
* Re-writing or summarizing the corpus — retrieval only, generation is the
  caller's problem.
* A web UI. The CLI is the surface; a UI is a future concern.
* Mutating upstream content. This repo never writes to Ghost or Postiz.
* Real-time streaming indexes. Hourly polling is fine.

---

## 1. Decisions already locked in

| # | Decision | Choice | Rationale (one line) |
|---|---|---|---|
| 1 | Language | Go 1.26 | Single binary, no runtime, easy ops |
| 2 | Index store | SQLite + `sqlite-vec` (one file) | Corpus ≤1,500 chunks — service overkill, file-portable, FTS5 free |
| 3 | Embedder | ollama `nomic-embed-text` (768d), OpenAI as escape hatch | Free re-runs, offline, swappable in one config switch |
| 4 | Retrieval | Hybrid: FTS5 BM25 + vec cosine, RRF merge | Neither alone is good enough on mixed prose+social |
| 5 | Repos / sources | Ghost Admin API + Postiz HTTP API | Live, queryable, idempotent on `(source_kind, source_id)` |
| 6 | Repo location | `/home/openbot/projects/elilap.dev-agent` | New repo per your instruction; remote `eli-l/elilap.dev-agent` |
| 7 | CLI surface | `ingest`, `embed`, `query`, `stats`, `serve` (Phase 4) | One binary, subcommands; matches muscle memory |
| 8 | Auth posture | env-loaded keys; never printed; gitignored index | Defense by default |

---

## 2. Repo layout (target)

```
elilap.dev-agent/
├── PLAN.md                           # this file
├── README.md
├── .gitignore                        # index.db / .env / build artifacts
├── go.mod
├── cmd/
│   ├── ingest/main.go                # source → chunks table
│   ├── embed/main.go                 # un-embedded chunks → vec table
│   ├── query/main.go                 # retrieval + format
│   ├── stats/main.go                 # chunk counts, drift, last run
│   └── serve/main.go                 # Phase 4: JSON-RPC + A2A agent card
├── internal/
│   ├── sources/
│   │   ├── ghost/                    # Ghost Admin client
│   │   ├── postiz/                   # Postiz HTTP client
│   │   └── shared/                   # common chunker + hash + types
│   ├── store/                        # SQLite, migrations, FTS5 glue, sqlite-vec
│   ├── embed/                        # Embedder interface + ollama + openai
│   └── retrieval/                    # hybrid FTS + vec + RRF
├── migrations/
│   └── 0001_init.sql
├── scripts/
│   ├── ask                           # query → LLM wrapper (Phase 2+)
│   └── ingest-all.sh                 # cron-friendly
└── docs/
    ├── architecture.md
    ├── api-shapes.md                 # Phase 0 output
    └── runbook.md                    # ops + evaluation
```

---

## 3. Phased build plan

Each phase has explicit prerequisites, deliverables, and exit criteria.
We commit at the end of each phase. We do not start the next phase until
the exit criteria pass.

### Phase 0 — Discovery & credentials

**Goal**: confirm both upstream APIs work end-to-end and capture their
actual response shapes before writing any schema.

**Tasks**

- [ ] Confirm `POSTIZ_API_KEY` works against `https://postiz.mist.lv/api`
      (current on-disk key 401s — either stale, wrong instance, or wrong URL).
- [ ] Capture one-page response shape of `POSTIZ /posts` (or whatever
      `postiz posts:list` proxies to). Fields needed: post id, content
      text, integrations (per-platform), status, publish date, media,
      release id.
- [ ] Confirm `GHOST_API_URL` + `GHOST_API_KEY` work; one full post JSON
      captured for schema reference.
- [ ] Write `docs/api-shapes.md` with both surfaces, as we actually see them.

**Exit criteria**

- A real `postiz posts:list` returns ≥1 record and the JSON is documented.
- A real Ghost `/posts/?limit=1` returns a usable post JSON.

**Blocker (resolved by you)**: working `POSTIZ_API_KEY` value. We will not
echo, log, or commit this key. It enters the process via env only.

---

### Phase 1 — Ingestion

**Goal**: a working ingestor that pulls from Ghost and Postiz into a
SQLite index with `chunks` + `chunks_fts` + `chunks_vec`. Embedding is
intentionally a separate command.

**Tasks**

- [ ] Project skeleton (Go module, dirs per §2).
- [ ] Migration `0001_init.sql` (schema below).
- [ ] `cmd/ingest`: walks both sources, normalizes, chunks, writes.
- [ ] `cmd/stats`: chunk counts, last-ingest timestamp, drift report.
- [ ] Source-aware chunker rules (table below).
- [ ] Idempotency on `(source_kind, source_id, chunk_id)` + content hash.
- [ ] `cmd/embed`: separate command, ollama default, OpenAI opt-in.
- [ ] `docs/runbook.md` section: "verifying ingestion is correct."
- [ ] End-of-phase commit + push.

**Schema** (`migrations/0001_init.sql`):

```sql
CREATE TABLE chunks (
  chunk_id       TEXT PRIMARY KEY,
  source_kind    TEXT NOT NULL CHECK (source_kind IN ('ghost_post','postiz_post')),
  source_id      TEXT NOT NULL,
  source_url     TEXT,
  platform       TEXT,                       -- null for ghost
  author         TEXT NOT NULL,
  published_at   TEXT NOT NULL,              -- ISO-8601 UTC
  ingested_at    TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  content_hash   TEXT NOT NULL,              -- sha256 of normalized text
  title          TEXT,
  section        TEXT,                       -- heading path; null for social
  body           TEXT NOT NULL,
  body_tokens    INTEGER NOT NULL,
  meta_json      TEXT NOT NULL DEFAULT '{}', -- tags, status, release id, etc.
  embedded_with  TEXT,                       -- e.g. 'ollama:nomic-embed-text@<git-sha>'
  embedded_at    TEXT,                       -- ISO-8601
  UNIQUE (source_kind, source_id, chunk_id)
);

CREATE INDEX idx_chunks_source      ON chunks (source_kind, source_id);
CREATE INDEX idx_chunks_published   ON chunks (published_at);
CREATE INDEX idx_chunks_hash        ON chunks (content_hash);

CREATE VIRTUAL TABLE chunks_fts USING fts5(
  chunk_id UNINDEXED, title, body, tags,
  content='', tokenize='porter unicode61'
);

CREATE VIRTUAL TABLE chunks_vec USING vec0(
  chunk_id TEXT PRIMARY KEY,
  embedding float[768]
);
```

**Chunker rules**

| Source | Unit | Cap (tokens) | Overlap | Notes |
|---|---|---|---|---|
| `ghost_post` (≥800 tok) | H2 sections, fallback H3 | 400 | 60 | HTML→MD via turndown; preserve code blocks |
| `ghost_post` (<800 tok) | Whole post | — | — | Don't fragment |
| `postiz_post` | One chunk per `(postiz_post_id, platform)` | 320 | 0 | Cross-post linkage via `thread_root_id` |
| `postiz_post` (multi-para > cap) | Split on `\n\n` | 320 | 0 | One Postiz post = one source_id; multiple chunks share it |

**Status filter (postiz)**: skip `draft`, skip `failed`. Skip `scheduled`
until `publish_at <= now` (configurable; default skip).

**Exit criteria**

- `cmd/ingest` from clean state succeeds against live Ghost + live Postiz.
- `cmd/stats` reports chunk counts matching a manual text inventory within ±10%.
- Re-running `cmd/ingest` is a near-no-op (insert/update counts near zero
  with no source changes).
- Deleting an upstream post and re-running reduces the corresponding chunks
  (drift detection works).
- `cmd/embed` finishes in <5 min on this hardware.
- All keys stay in env; no secret leaks in `git log -p` or stdout.

**Estimated effort**: 2–3 focused days.

---

### Phase 2 — Retrieval & agent wrapper

**Goal**: prove retrieval quality on real queries, then put a thin LLM
wrapper on top.

**Tasks**

- [ ] `cmd/query` (no LLM in the loop yet):
  - inputs: free-text, `--top N`, `--hybrid` (on), `--source ghost|postiz|all`,
    `--platform` (repeatable), `--after`, `--before`, `--exclude-scheduled`,
    `--recency-halflife-days`, `--format jsonl|text`.
  - outputs: per-chunk metadata + body + score.
- [ ] `scripts/ask`: one-shot wrapper. Builds a strict prompt with the
  retrieved chunks, shells out to `codex exec -q` (or a configurable
  model command). No state, no retries.
- [ ] Eval harness: 20 hand-picked queries with ground-truth chunk_ids.
  Target ≥15/20 with a clearly correct, verifiable answer.

**Prompt template (for `scripts/ask`)**:

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
- p50 latency <300ms retrieval, <8s end-to-end with LLM.
- A second human can read `README.md` + `runbook.md` and run `ask` without help.

**Estimated effort**: 1–2 days.

---

### Phase 3 — MVP operations

**Goal**: the system runs unattended and you actually use it.

**Tasks**

- [ ] `scripts/ingest-all.sh`: cron-friendly wrapper.
- [ ] `cmd/ingest --watch` mode: poll upstream every N minutes during
  work hours, no-op when nothing changed.
- [ ] systemd timer (or `openclaw` automation) drives ingest hourly.
- [ ] Add a `MEMORY.md` entry: "for questions about my positions, history,
  or thinking, use `ask` from this repo."
- [ ] One week of shadow usage: agent answers go through `ask` once per day
  on real questions; no operational surprises.

**Exit criteria**

- 1 week with no manual re-ingest interventions.
- Memory footprint ≤50 MB (index + binary).
- New contributor can `git clone && go build && ./agent ingest && ./agent embed && ./agent query "..."` in <30 min.

**Estimated effort**: 1 day.

---

### Phase 4 — A2A surface (read-only, token-gated)

**Goal**: expose `cmd/query` over HTTP for other agents. No write surface.
No public web exposure by default.

**Tasks**

- [ ] `cmd/serve`: HTTP server binding to `127.0.0.1` (default) or `0.0.0.0`
  (`--bind-public`); token auth via `Authorization: Bearer ***`.
- [ ] JSON-RPC-style `POST /v1/retrieve`: forwards to `internal/retrieval`.
- [ ] Agent card at `GET /.well-known/agent.json` (A2A format).
- [ ] Audit log: per-query timestamp, token id (not token), query, result
  count. Bodies never logged.
- [ ] Token store: env-driven list of `id:***` pairs; `--add-token`,
  `--revoke-token` subcommands; rotation is config-only.
- [ ] Rate limit: 30 q/min per token (configurable).
- [ ] Operational documentation in `docs/runbook.md`.

**Security posture (documented and enforced)**

- API is read-only — no write methods.
- Local bind by default; public bind requires an explicit flag AND a token.
- Audit log never contains chunk bodies or user answers.
- Token store is plain text but file-scoped (`0600`); tokens are
  high-entropy random ≥32 bytes.

**Exit criteria**

- A second OpenClaw agent (or local `curl`) can call `/v1/retrieve` and
  gets results identical to `cmd/query` CLI output.
- Audit log shows the call with token id and query.
- Token issuance/revocation is config-only; no service restart needed.
- `runbook.md` documents the rotation story.

**Estimated effort**: 2 days.

---

## 4. Risks & mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Postiz key rejected / stale | Confirmed | Phase 0 blocker | Phase 0 surface; you re-issue |
| Postiz response shape differs from docs | Medium | Phase 1 schema rework | Phase 0 captures real shape first |
| Ghost response shape differs from `publishing-to-ghost` skill | Low | Same as above | Phase 0 captures one real post |
| Embedding quality too low | Low | Phase 2 eval falls short | Swap to OpenAI; `embedded_with` audit makes it cheap |
| Posts scheduled into future not yet live get indexed | Medium | Stale context | Skip `status='scheduled'` by default |
| Index drift silent | Medium | Wrong answers | `cmd/stats` diffs count against source |
| A2A token leaked | Low | Read-only exposure of published corpus | Tokens are scoped read-only; rotated monthly |
| Worker `zero` sandbox lacks network for embeddings | High (per AGENTS.md) | Slow dev loop | Run `cmd/embed` outside the sandbox |
| `sqlite-vec` extension versioning | Low | Build fragility | Pin version; CI smoke test |

---

## 5. Open questions

1. Should `postiz:status='scheduled'` posts that are *past* their publish
   time be indexed, or only those the API now reports `published`?
   **Default**: skip until status flips to `published`.
2. Should `cmd/embed` also embed `meta_json`-only fields (tags, titles
   without body)? **Default**: no; tags appear in FTS5 path.
3. For Phase 4 token storage: env-only (simplest) vs. encrypted file on
   disk (more ops). **Default**: plain text env, file mode `0600`.

---

## 6. Done = …

The project is "done" when:

* `cmd/ingest` is idempotent against both sources.
* `cmd/query` returns verifiable, citable answers.
* The repo + one `index.db` file constitute the entire operational footprint.
* A second agent can call our retrieval over A2A.
* It runs unattended for a month without manual intervention.

We are not done at MVP. We are not done at Phase 2. We are done at the
end of Phase 4.
