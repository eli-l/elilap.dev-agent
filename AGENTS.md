# AGENTS.md

Local-first RAG agent over Eli's published writing: ingests Ghost posts + Postiz cross-posts into a single-file SQLite index (FTS5 + sqlite-vec), exposed through one Go binary. **Status: Phase 1 in progress** — `cmd/agent/` exists with subcommand dispatch and the `sources` subcommands; `internal/config` (the `sources.yaml` loader) and `internal/sources` (the `Provider` interface, the registry, and the Ghost/Postiz option parsers) are implemented and tested; `internal/{store,embed,llm,retrieval}` and the upstream network clients are still empty placeholders; the container files (`Dockerfile`, `.dockerignore`, `compose.yaml`, `scripts/deploy-check.sh`) are shipped and verified. `PLAN.md` is the source of truth for scope and sequencing; read it before adding code.

## Commands

```bash
make help                     # list every target
make check                    # build + vet + test + gofmt (the full verification suite)
make doctor                   # probe prerequisites and report what is missing
make build                    # -> ./agent

go build -mod=vendor ./...         # compile everything
go vet -mod=vendor ./...           # static checks
go test -mod=vendor ./...          # tests (use fixtures, not live APIs)
gofmt -l cmd internal migrations   # must print nothing (vendor/ is upstream code, not ours)
python3 scripts/check-ddl.py       # schema smoke check: applies the migrations, proves the FTS5 join-back
./scripts/deploy-check.sh           # prove the container deployment: build, doctor in the image, compose config
go run ./cmd/agent sources validate   # strict-parse sources.yaml (no credentials needed)
```

**Always build and test with `-mod=vendor`.** Dependencies are vendored (see **Conventions**), and the sandboxed worker has no network and a hidden module cache, so `go mod tidy` / `go get` there always fail.

Run target: `go build -mod=vendor -o agent ./cmd/agent`. The five original `cmd/*` stubs have already collapsed into `cmd/agent/` with subcommands `ingest`, `embed`, `query`, `stats`, `serve` (Decision 7) — do not add new top-level `cmd/` dirs.

## OKF docs bundle

`docs/` is an **OKF v0.2 bundle** (Open Knowledge Format — directories of markdown files with YAML frontmatter, spec: `github.com/GoogleCloudPlatform/knowledge-catalog/okf/SPEC.md`). `docs/index.md` is the manifest (root frontmatter: `okf_version: "0.2"` only), `docs/log.md` is the newest-first update history. Skills: the `okf`/`okf-builder` agent skill (source: `github.com/eli-l/okf-builder`) defines the authoring workflow; if it isn't installed, follow the format of existing docs files.

Every concept file (everything except `index.md`/`log.md`) needs YAML frontmatter — required: non-empty `type`; recommended: `title`, `description`, `status` (`stable`/`draft`/`deprecated`), `generated: {by, at}` (actor convention: `human:<id>`, `process:<name>`, or `<vendor>/<model>`), `tags`. Optional: `verified: [{by, at}]` audit trail, `stale_after`, `sources`. Placeholder docs carry `status: draft` until their Phase 0 capture lands.

Workflow for any docs/ change:

1. Write/edit the concept file with frontmatter.
2. Update `docs/index.md` (list it under its type grouping; run captures go under `# Runs`).
3. Prepend to `docs/log.md`: `* **Creation|Update|Deprecation**: <prose>. [Title](bundle-relative.md)`.
4. Validate — links resolve against the bundle root (`docs/`): `/x.md` → `docs/x.md`, so links *out* of the bundle must be relative (`../AGENTS.md`); relative links resolve against the containing file. `docs/runs/run.md` embeds its template inside a ```` ```yaml ```` fence so the template's `---` lines don't parse as frontmatter.
5. Run `okf-validate docs && okf-check-links docs` (build the binaries once from `github.com/eli-l/okf-builder`: `git clone … && cd src && go build -o … ./cmd/<tool>`; exit 0 = conformant). The validator is a separate checkout and is **not** required to be installed — if it is unavailable, check the frontmatter and link rules by hand against the rules above.

`docs/runs/` holds timestamped validator-run captures (`type: Run`): command, trimmed output, timestamp, `verified:` when a human confirmed the result. Capture a run after notable validator passes; never paste secrets into `output:`.

## Layout

- `cmd/agent/` — the single binary: `main.go` (dispatch), `sources.go` (`sources list` / `sources validate`), and one file per remaining subcommand (`ingest.go`, `embed.go`, `query.go`, `stats.go`, `serve.go`) as they land.
- `sources.yaml` — static import config: every provider, its `enabled` flag,
  and its options. Secrets are never in it — credentials are `{ env: VAR }`
  references.
- `internal/config` — `sources.yaml` loader: strict YAML decode
  (`KnownFields(true)`), version check, per-provider option validation,
  credential resolution (values never printed).
- `internal/sources/{provider.go,registry.go,ghost,postiz,shared}` — the
  `Provider` interface, the `type → constructor` registry, upstream clients,
  shared chunker/hash.
- `internal/store` — SQLite, migration runner, FTS5/vec glue.
- `internal/embed`, `internal/retrieval`, `internal/llm` — embedding, hybrid RRF retrieval, OpenAI-compatible HTTP connector.
- `migrations/` — SQL schema; `0001_init.sql` must stay in sync with the schema block in `PLAN.md` §3. `scripts/check-ddl.py` enforces that sync.
- `Dockerfile` — multi-stage image: a `golang:1.27` builder with
  `CGO_ENABLED=0` and `-mod=vendor` (the build needs no network), and a
  distroless runtime carrying only the binary, the CA bundle, `sources.yaml`
  and a writable `/data` owned by a non-root user. No `EXPOSE` (the agent is
  a CLI) and no `HEALTHCHECK` (the binary has no health mode).
- `.dockerignore` — keeps the build context to what the build needs:
  `vendor/`, `go.mod`, `go.sum`, `cmd/`, `internal/`, `migrations/` and
  `sources.yaml`.
- `compose.yaml` — the agent service only. `LOCALAI_BASE_URL` is required
  (the default would point at the localhost of the container itself),
  `/data` is the named volume `agent-data`, credentials are passed through
  as `${VAR:-}`, and there is no model server and no healthcheck.
- `scripts/deploy-check.sh` — the one-command deployment proof: docker, the
  daemon, compose, the model server and its model catalog, the image build,
  `agent doctor` inside the image, and `docker compose config`.
  `--skip-build` re-checks without rebuilding.
- `docs/` — OKF v0.2 bundle (see **OKF docs bundle** above); captured upstream API shapes (`ghost.md`, `postiz.md`) are the honest reference for clients.
- `Makefile` — `setup` / `check` / `doctor` / `build` / `test` entry points; `make doctor` is the first thing to run on a new machine.

## Conventions

- Two config layers, no overlap: `sources.yaml` declares **which** providers
  exist and how they are configured; env vars carry **secrets and runtime
  values** (`POSTIZ_API_KEY`, `GHOST_API_KEY`, `LOCALAI_BASE_URL`,
  `EMBED_MODEL`, `CHAT_MODEL`, `AGENT_DB_PATH`). `.env` is gitignored and
  never committed; never echo or log keys. `.env.example` lists the
  variable names with no values.
- The LLM connector is plain `net/http` against an OpenAI-compatible API — no LLM SDKs.
- The DB is derived: deleting `index.db` and re-running ingest/embed must always reproduce it. Never commit it.
- Timestamps are ISO-8601 UTC; ids are sha256-based hex (`chunk_id = sha256(source_kind|source_id|chunk_seq)`). Here `source_id` is the upstream item id (Ghost uuid / Postiz id), not the config id; `chunk_seq` is the ordinal within that item across all its platform chunks.
- `agent doctor` is the preflight contract: it reports variable **names** and set-state, never a credential value, and every failure names the variable or file that fixes it.
- Dependencies are **vendored** (`vendor/`): the sandboxed worker has no network and its module cache is hidden, so `go mod tidy` / `go get` there always fail. Add a dep from the host shell (`go get` + `go mod vendor` + a stub file that imports it so `tidy` keeps it), commit `go.mod`, `go.sum`, and `vendor/`, and build with `-mod=vendor`.
- Keep `chunks` and `chunks_fts` writes in one transaction.
- Write prose for humans: name the requirement, not the machine. Hostnames,
  IP addresses, and absolute paths belong in the non-normative reference
  appendix (`PLAN.md` §7), never in the normative text.

## Avoid

- Never write to Ghost or Postiz — this repo is strictly read-only against upstream sources.
- Never put credential values in `sources.yaml` — reference env vars only.
- Don't expect plain `sqlite3` to apply `migrations/0001_init.sql`: the `vec0` DDL fails unless the sqlite-vec extension is loaded first. Use `scripts/check-ddl.py`, which applies everything else and verifies the extension-dependent table by shape (or fully, when `SQLITE_VEC_PATH` points at the extension).
- Don't make `chunks_fts` contentless (`content=''`): stored columns must read back for join-back on `chunk_id` — this was a deliberate fix, see `migrations/0001_init.sql`.
- Don't add generation/UI/auth features: retrieval only; generation is the caller's problem; the CLI is the surface (non-goals in `PLAN.md` §0).
- Tests and ingestion run against captured fixture JSON, never live API keys.
- Don't add a dependency, a feature, or an abstraction that no committed code path needs. The MVP seam is the provider registry, `sources.yaml`, and the one OpenAI-compatible connector — extend those, don't invent a layer in front of them.
