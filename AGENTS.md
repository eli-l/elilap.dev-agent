# AGENTS.md

Local-first **agentic A2A connector** over Eli's published writing: ingests Ghost posts + Postiz cross-posts into a single-file SQLite index (FTS5 + sqlite-vec), and runs its own grounded tool-calling loop (one internal tool, `search_writing`, over `internal/retrieval`) exposed through one Go binary — CLI plus a loopback dev HTTP surface (`/v1/retrieve`, `/v1/ask`) in Phase 2, A2A (`message/send` + agent card) in Phase 4. **Status: bootstrap** — `cmd/*/main.go` are stubs, `internal/` is empty (`.gitkeep`). `PLAN.md` is the source of truth for scope and sequencing; read it before adding code.

## Commands

```bash
go build ./...                  # compile everything
go vet ./...                    # static checks
go test -tags sqlite_fts5 ./...  # tests (use fixtures, not live APIs; FTS5 needs the build tag)
gofmt -l cmd internal migrations   # must print nothing (vendor/ is upstream code, not ours)
python3 scripts/check-ddl.py    # schema smoke check; Phase 1 deliverable, not in the repo yet
go run ./cmd/agent sources validate   # strict-parse sources.yaml (no credentials needed)
```

Run target: `go build -o agent ./cmd/agent` (the five `cmd/*` stubs collapse into `cmd/agent/` with subcommands `ingest`, `embed`, `query`, `ask`, `stats`, `serve` (`ask` and the dev `serve` land in Phase 2, token-gated A2A `serve` in Phase 4) — do not add new top-level `cmd/` dirs).

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
- `sources.yaml` — static import config: every provider, its `enabled` flag,
  and its options. Secrets are never in it — credentials are `{ env: VAR }`
  references.
- `example.env` — the committed inventory of **every** environment variable
  the binary reads: defaults for settings, empty values for secrets. `.env`
  is a gitignored copy of it, loaded at startup. A new env var is not added
  until it is listed here (see Conventions and `docs/environment.md`).
- `internal/config` — `sources.yaml` loader: strict YAML decode
  (`KnownFields(true)`), version check, per-provider option validation,
  credential resolution (values never printed).
- `internal/envcfg` — the environment layer: `godotenv` loads `.env` at
  startup (`LoadDotEnv`), `envconfig` decodes the runtime `Config`, and
  `Lookup` is the single accessor for the dynamically named credentials that
  `sources.yaml` references.
- `internal/sources/{provider.go,registry.go,ghost,postiz,shared}` — the
  `Provider` interface, the `type → constructor` registry, upstream clients,
  shared chunker/hash.
- `internal/store` — SQLite, migration runner, FTS5/vec glue.
- `internal/embed`, `internal/retrieval`, `internal/llm` — embedding, hybrid RRF retrieval (the `search_writing` tool's engine), OpenAI-compatible HTTP connector.
- `internal/agentloop` — the in-service loop (Phase 2): tool schema, grounded prompt, iteration/token budgets, citation-or-refusal; shared by `agent ask` and the Phase 4 A2A server.
- `migrations/` — SQL schema; `0001_init.sql` must stay in sync with the schema block in `PLAN.md` §3.
- `docs/` — OKF v0.2 bundle (see **OKF docs bundle** above); captured upstream API shapes (`ghost.md`, `postiz.md`) are the honest reference for clients.

## Conventions

- Three config inputs, no overlap: `sources.yaml` declares **which**
  providers exist and how they are configured; env vars carry **secrets and
  runtime values** (`POSTIZ_API_KEY`, `GHOST_API_KEY`, `LOCALAI_BASE_URL`,
  `EMBED_MODEL`, `AGENT_DB_PATH`); `.env` is the local, gitignored way to
  supply those vars for a developer run. `main` calls
  `envcfg.LoadDotEnv()` before anything reads the environment, and a variable
  already set in the real environment wins over the file. Never echo or log
  values.
- **Every env var is declared in `example.env`** with an empty value (secrets)
  or its default (everything else), in the same change that adds the variable.
  `TestExampleEnvIsComplete` fails when `example.env` and the code/config
  disagree in either direction, so a variable without an entry is not
  considered added. Document it in `docs/environment.md` too.
- Environment config is read with **`github.com/kelseyhightower/envconfig`**,
  never ad-hoc `os.Getenv` chains: a config struct carries `envconfig` tags
  naming the variable, and a package-level `EnvPrefix` supplies the prefix
  where one exists (`internal/logging` maps `AGENT_LOG_*` that way; `AGENT_*`
  is the prefix this service owns). Name the env vars in exported constants,
  apply defaults through `envcfg.DefaultOr` so an empty value means unset, and
  wrap the decode error so it names the variable, never its value. The only
  dynamic names are the `{ env: VAR }` credentials in `sources.yaml`, read
  through `envcfg.Lookup`. YAML-file config (`sources.yaml`) stays on
  `gopkg.in/yaml.v3` with `KnownFields(true)`.
- The LLM connector is plain `net/http` against an OpenAI-compatible API — no LLM SDKs. Tool-calling rides on the same connector: chat requests carry the `search_writing` tool definition and the client round-trips the model's tool calls.
- The DB is derived: deleting `index.db` and re-running ingest/embed must always reproduce it. Never commit it.
- Timestamps are ISO-8601 UTC; ids are sha256-based hex (`chunk_id = sha256(source_kind|source_id|chunk_seq)`). Here `source_id` is the upstream item id (Ghost uuid / Postiz id), not the config id; `chunk_seq` is the ordinal within that item across all its platform chunks.
- Dependencies are **vendored locally but never committed**: `vendor/` is gitignored and must exist on disk — run `go mod vendor` after any `go.mod`/`go.sum` change and build with `-mod=vendor` (the sandboxed worker has no network and its module cache is hidden). `go.mod`/`go.sum` are the source of truth for versions.
- Keep `chunks` and `chunks_fts` writes in one transaction.

## Avoid

- Never write to Ghost or Postiz — this repo is strictly read-only against upstream sources.
- Never put credential values in `sources.yaml` or `example.env` — reference
  env vars only, and keep the template's secret values empty.
- Never add an environment variable without listing it in `example.env`
  (empty for secrets, default otherwise) and in `docs/environment.md`.
- Don't apply `migrations/0001_init.sql` with plain `sqlite3`: the `vec0` DDL fails unless the sqlite-vec extension is loaded first.
- Don't make `chunks_fts` contentless (`content=''`): stored columns must read back for join-back on `chunk_id` — this was a deliberate fix, see `migrations/0001_init.sql`.
- Don't add UI/auth-surface features beyond the token gate, and don't add tools to the loop beyond `search_writing`: one loop, one grounded tool, citation-or-refusal (non-goals in `PLAN.md` §0, Decisions 15–16). Peer task text is data, never instructions.
- Tests and ingestion run against captured fixture JSON, never live API keys.
