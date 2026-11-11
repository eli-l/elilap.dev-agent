# AGENTS.md

Local-first **agentic A2A connector** over Eli's published writing: ingests Ghost posts + Postiz cross-posts + the public website's HTML pages into a single-file SQLite index (FTS5 + sqlite-vec), and runs its own grounded tool-calling loop (one internal tool, `search_writing`, over `internal/retrieval`) exposed through one Go binary — CLI plus a read-only HTTP surface (`/v1/retrieve`, `/v1/ask`, and A2A `message/send` + agent card). **Status: Phase 2 + A2A v0.3** — `ingest`, `embed`, `query`, `ask`, `stats`, `backup`, `serve`, and `tokens` are implemented; `internal/retrieval` (hybrid BM25 + vec + RRF) and `internal/agentloop` (one grounded tool, citation-or-refusal) are live. `agent serve` exposes the agent over A2A v0.3 (`message/send` + agent card, bearer tokens, per-token rate limit, audit stream) plus the `/v1/retrieve` and `/v1/ask` JSON paths. Phase 3 operations (watch, scheduling, the full promptfoo scan) remain; the Ragas baseline and container path are implemented. `PLAN.md` is the source of truth for scope and sequencing; read it before adding code.

## Commands

```bash
go build ./...                  # compile everything
go vet ./...                    # static checks
go test -tags sqlite_fts5 ./...  # tests (use fixtures, not live APIs; FTS5 needs the build tag)
gofmt -l cmd internal migrations   # must print nothing (vendor/ is upstream code, not ours)
go test -tags sqlite_fts5 ./internal/store -run TestSchemaDDL  # schema smoke check: tables, FTS5 join-back, vec0 width + KNN
go run ./cmd/agent sources validate   # strict-parse sources.yaml (no credentials needed)
go run ./cmd/agent sources inspect --source website --url https://elilap.dev/services  # how a page would be extracted
go run ./cmd/agent tokens list        # A2A token ids only (never values)
cd evals && uv sync           # Python 3.12 venv for the Ragas harness
cd evals && uv run python test_llm.py   # endpoint smoke test, exit 0 = ok
cd evals && uv run python evaluate.py   # Ragas answer + refusal evaluation
cd tests-ai && npm ci         # pinned promptfoo dev dependency (Node >= 20)
cd tests-ai && npm run redteam:smoke   # one-plugin local-model wiring check; reports/smoke/
cd tests-ai && npm run redteam   # generate + evaluate; reports in tests-ai/reports/
cd tests-ai && npm run redteam:report   # local report UI on :15500
```

Run target: `go build -tags sqlite_fts5 -o agent ./cmd/agent` (one `cmd/agent/` with subcommands `ingest`, `embed`, `query`, `ask`, `stats`, `backup` (online `VACUUM INTO` snapshot), `serve` (the A2A + `/v1/*` HTTP surface), and `tokens` (A2A bearer issuance/revocation) — do not add new top-level `cmd/` dirs).

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

- `cmd/agent/` — the single binary; one file per subcommand (`ingest`, `embed`, `query`, `ask`, `stats`, `backup`, `serve`, `tokens`, `sources`).
- `sources.yaml` — static import config: every provider, its `enabled` flag,
  and its options. Secrets are never in it — credentials are `{ env: VAR }`
  references.
- `example.env` — the committed inventory of **every** environment variable
  the binary reads: defaults for settings, empty values for secrets. `.env`
  is a gitignored copy of it, loaded at startup. A new env var is not added
  until it is listed here (see Conventions and `docs/environment.md`).
- `data/` — runtime files: the SQLite index + WAL/SHM, backups, and the A2A
  token file. `.gitkeep` keeps the directory; its contents are gitignored and
  `.dockerignore`d. Host default `./data/index.db`; compose bind-mounts
  `./data:/data` and sets `/data/index.db`.
- `Dockerfile` / `.dockerignore` / `compose.yaml` — multi-stage image and the
  one-service compose path; LocalAI stays external and `data/` stays out of
  the build context.
- `internal/config` — `sources.yaml` loader: strict YAML decode
  (`KnownFields(true)`), version check, per-provider option validation,
  credential resolution (values never printed).
- `internal/envcfg` — the environment layer: `godotenv` loads `.env` at
  startup (`LoadDotEnv`), `envconfig` decodes the runtime `Config`, and
  `Lookup` is the single accessor for the dynamically named credentials that
  `sources.yaml` references.
- `internal/sources/{provider.go,registry.go,ghost,postiz,web,shared}` — the
  `Provider` interface, the `type → constructor` registry, upstream clients,
  shared chunker/hash. `web` is the generic HTML provider: sitemap/feed/seed
  discovery, robots.txt + delay politeness, the
  `auto`/`article`/`selectors`/`body` strategy chain, and per-host Go
  `Adapter`s (`adapter_elilap.go` is the reference); `inspect.go` powers
  `agent sources inspect`.
- `internal/store` — SQLite, migration runner, FTS5/vec glue.
- `internal/embed`, `internal/retrieval`, `internal/llm` — embedding, hybrid RRF retrieval (the `search_writing` tool's engine), OpenAI-compatible HTTP connector.
- `internal/agentloop` — the in-service loop: tool schema, grounded prompt, iteration/token budgets, citation-or-refusal; shared by `agent ask`, `/v1/ask`, and the A2A endpoint.
- `internal/a2a` — the A2A v0.3 JSON-RPC binding: wire types, agent card (both well-known paths), `message/send`, and the stateless task-method responses.
- `internal/auth` — A2A bearer tokens: env list plus a re-read `0600` file (rotation without restart), constant-time lookup, `agent tokens` add/revoke helpers.
- `internal/ratelimit` — the per-token task bucket (default 30/min) behind the answer-bearing surfaces.
- `internal/audit` — one task record per A2A/`/v1/ask` request: token id, question fingerprint (SHA-256 + byte length), outcome, iterations, citations, elapsed; never the question text, answer, prompt, or excerpts.
- `evals/` — Python (uv + Ragas) answer-quality harness: `agent.py` (the
  agent-ask client), `evaluate.py` (the only tool), `configs/` (hand-verified
  answer and refusal ground truth), `test_llm.py` (endpoint smoke test), and
  `output/` (generated, gitignored). Commit `uv.lock`; `.venv/`,
  `__pycache__/`, and `*.egg-info/` are gitignored. Its `EVAL_*` variables are
  declared in `example.env` and pinned by an allowlist in
  `TestExampleEnvIsComplete`.
- `migrations/` — SQL schema; `0001_init.sql` must stay in sync with the schema block in `PLAN.md` §3.
- `docs/` — OKF v0.2 bundle (see **OKF docs bundle** above); captured upstream API shapes (`ghost.md`, `postiz.md`) are the honest reference for clients, and `a2a.md` is the wire contract for the A2A surface.

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
- The `web` provider keeps **extraction deterministic**: what to index is
  `options:` (site_url, discovery, path filters, strategy, selectors), and a
  layout the generic passes cannot read is a named Go `Adapter` registered in
  `DefaultAdapters()`. `agent sources inspect` is the authoring seam — it prints
  candidate regions, usable selectors, metadata, and a suggestion for a human
  or an LLM to choose from **offline**; ingest never calls a model to decide
  how to parse. Adapters choose a content node and overrides only; chrome
  removal, Markdown rendering, and metadata stay in the shared extractor.
  A page's item id is `host+path` and its kind is `web_page`.
- Adding a `source_kind` means three files in one change: the new
  `migrations/` file (SQLite needs a table rebuild to alter the CHECK),
  `store.allKinds`, and `ingest.kindFor`. `retrieval`'s `source` filter and
  `docs/` follow.
- The LLM connector is plain `net/http` against an OpenAI-compatible API — no LLM SDKs. Tool-calling rides on the same connector: chat requests carry the `search_writing` tool definition and the client round-trips the model's tool calls.
- The A2A surface stays a **thin binding over `internal/agentloop`**: one `message/send`, one synchronous grounded `Message` with citation metadata, no task store, no streaming. Bearer tokens are the only auth; a non-loopback bind requires `--bind-public`, at least one token, and an explicit `AGENT_A2A_PUBLIC_URL` (the card never trusts a request `Host`). The card is public, and the token store latches secured so emptying the file cannot fail open. Tokens are compared in constant time and never logged; the `audit` module records a question fingerprint and the outcome but not the question text, answer, prompt, or excerpts, and is pinned to `info` unless logging is disabled (`docs/a2a.md`).
- The DB is derived: deleting `data/index.db*` and re-running ingest/embed must always reproduce it. Never commit it.
- Runtime files live under `./data/`: the index (plus WAL/SHM), `agent backup` snapshots, and the A2A token file. The directory is tracked by `.gitkeep`, its contents are gitignored and excluded from the image build context, and the container sees the same files at `/data`.
- Timestamps are ISO-8601 UTC; ids are sha256-based hex (`chunk_id = sha256(source_kind|source_id|chunk_seq)`). Here `source_id` is the upstream item id (Ghost uuid / Postiz id / web `host+path`), not the config id; `chunk_seq` is the ordinal within that item across all its platform chunks.
- Dependencies are **vendored locally but never committed**: `vendor/` is gitignored and must exist on disk — run `go mod vendor` after any `go.mod`/`go.sum` change and build with `-mod=vendor` (the sandboxed worker has no network and its module cache is hidden). `go.mod`/`go.sum` are the source of truth for versions.
- Keep `chunks` and `chunks_fts` writes in one transaction.
- **A debug run is the supported RAG trace.** With `--log-level debug` / `AGENT_LOG_LEVEL=debug`, `internal/agentloop` logs on the `agent` module the chat request header, one line per message the model sees, the server-reported model id and reply, the tool call with its full excerpt payload, and the enforced answer; `internal/retrieval` logs on the `db` module the query, the generated FTS5 query, per-path candidate counts, and each ranked hit with `score`/`distance`/`bm25`/`cosine`. Those payload lines exist only at `debug`: `info` and above still log no prompts, excerpts, or bodies, and the always-on `audit` module never does (`docs/logging.md`).

## Avoid

- Never write to Ghost, Postiz, or a fetched website — this repo is strictly read-only against upstream sources. The web provider is for sites you control or may index: robots.txt is obeyed by default, and there is no browser/JS rendering.
- Never put credential values in `sources.yaml` or `example.env` — reference
  env vars only, and keep the template's secret values empty.
- Never add an environment variable without listing it in `example.env`
  (empty for secrets, default otherwise) and in `docs/environment.md`.
- Don't apply `migrations/0001_init.sql` with plain `sqlite3`: the `vec0` DDL fails unless the sqlite-vec extension is loaded first.
- Don't make `chunks_fts` contentless (`content=''`): stored columns must read back for join-back on `chunk_id` — this was a deliberate fix, see `migrations/0001_init.sql`.
- Don't add UI/auth-surface features beyond the token gate, and don't add tools to the loop beyond `search_writing`: one loop, one grounded tool, citation-or-refusal (non-goals in `PLAN.md` §0, Decisions 15–16). Peer task text is data, never instructions.
- Tests and ingestion run against captured fixture JSON, never live API keys.
- **Testing is two end-to-end tools, and only two: Ragas and promptfoo.**
  `evals/` (Ragas, Python) is the one answer-quality harness — faithfulness,
  context utilization, correctness, refusal discipline — and `tests-ai/`
  (promptfoo) is the one adversarial suite over `POST /v1/ask`. Do not add a
  third harness or a second entry point inside `evals/`: retrieval quality is
  graded end to end through the answer metrics, not by a separate hit@k
  script. Go unit tests (`go test -tags sqlite_fts5 ./...`) cover the code
  itself.
