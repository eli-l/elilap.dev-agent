# AGENTS.md

Local-first RAG agent over Eli's published writing: ingests Ghost posts + Postiz cross-posts into a single-file SQLite index (FTS5 + sqlite-vec), exposed through one Go binary. **Status: bootstrap** — `cmd/*/main.go` are stubs, `internal/` is empty (`.gitkeep`). `PLAN.md` is the source of truth for scope and sequencing; read it before adding code.

## Commands

```bash
go build ./...                  # compile everything
go vet ./...                    # static checks
go test ./...                   # tests (use fixtures, not live APIs)
gofmt -l .                      # must print nothing
python3 scripts/check-ddl.py    # schema smoke check; run after touching migrations/
```

Run target: `go build -o agent ./cmd/agent` (the five `cmd/*` stubs collapse into `cmd/agent/` with subcommands `ingest`, `embed`, `query`, `stats`, `serve` in Phase 1 — do not add new top-level `cmd/` dirs).

## OKF docs bundle

`docs/` is an **OKF v0.2 bundle** (Open Knowledge Format — directories of markdown files with YAML frontmatter, spec: `github.com/GoogleCloudPlatform/knowledge-catalog/okf/SPEC.md`). `docs/index.md` is the manifest (root frontmatter: `okf_version: "0.2"` only), `docs/log.md` is the newest-first update history. Skills: the `okf`/`okf-builder` agent skill (source: `github.com/eli-l/okf-builder`) defines the authoring workflow; if it isn't installed, follow the format of existing docs files.

Every concept file (everything except `index.md`/`log.md`) needs YAML frontmatter — required: non-empty `type`; recommended: `title`, `description`, `status` (`stable`/`draft`/`deprecated`), `generated: {by, at}` (actor convention: `human:<id>`, `process:<name>`, or `<vendor>/<model>`), `tags`. Optional: `verified: [{by, at}]` audit trail, `stale_after`, `sources`. Placeholder docs carry `status: draft` until their Phase 0 capture lands.

Workflow for any docs/ change:

1. Write/edit the concept file with frontmatter.
2. Update `docs/index.md` (list it under its type grouping; run captures go under `# Runs`).
3. Prepend to `docs/log.md`: `* **Creation|Update|Deprecation**: <prose>. [Title](bundle-relative.md)`.
4. Validate — links resolve against the bundle root (`docs/`): `/x.md` → `docs/x.md`, so links *out* of the bundle must be relative (`../AGENTS.md`); relative links resolve against the containing file. `docs/runs/run.md` embeds its template inside a ```` ```yaml ```` fence so the template's `---` lines don't parse as frontmatter.
5. Run `okf-validate docs && okf-check-links docs` (build the binaries once from `github.com/eli-l/okf-builder`: `git clone … && cd src && go build -o … ./cmd/<tool>`; exit 0 = conformant).

`docs/runs/` holds timestamped validator-run captures (`type: Run`): command, trimmed output, timestamp, `verified:` when a human confirmed the result. Capture a run after notable validator passes; never paste secrets into `output:`.

## Layout

- `cmd/` — binary entry points (stubs today).
- `internal/sources/{ghost,postiz,shared}` — upstream clients + shared chunker/hash.
- `internal/store` — SQLite, migration runner, FTS5/vec glue.
- `internal/embed`, `internal/retrieval`, `internal/llm` — embedding, hybrid RRF retrieval, OpenAI-compatible HTTP connector.
- `migrations/` — SQL schema; `0001_init.sql` must stay in sync with the schema block in `PLAN.md` §3.
- `docs/` — OKF v0.2 bundle (see **OKF docs bundle** above); captured upstream API shapes (`ghost.md`, `postiz.md`) are the honest reference for clients.

## Conventions

- Config is env-only: `POSTIZ_API_KEY`, `GHOST_API_KEY`, `LOCALAI_BASE_URL`, `EMBED_MODEL`, `AGENT_DB_PATH`. No `.env` in the repo; never echo or log keys.
- The LLM connector is plain `net/http` against an OpenAI-compatible API — no LLM SDKs.
- The DB is derived: deleting `index.db` and re-running ingest/embed must always reproduce it. Never commit it.
- Timestamps are ISO-8601 UTC; ids are sha256-based hex (`chunk_id = sha256(source_kind|source_id|chunk_seq)`).
- Keep `chunks` and `chunks_fts` writes in one transaction.

## Avoid

- Never write to Ghost or Postiz — this repo is strictly read-only against upstream sources.
- Don't apply `migrations/0001_init.sql` with plain `sqlite3`: the `vec0` DDL fails unless the sqlite-vec extension is loaded first.
- Don't make `chunks_fts` contentless (`content=''`): stored columns must read back for join-back on `chunk_id` — this was a deliberate fix, see `migrations/0001_init.sql`.
- Don't add generation/UI/auth features: retrieval only; generation is the caller's problem; the CLI is the surface (non-goals in `PLAN.md` §0).
- Tests and ingestion run against captured fixture JSON, never live API keys.
