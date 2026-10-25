---
type: Review
title: MVP bring-up review
description: A prioritized review of the elilap.dev-agent setup for an MVP that is extendable in structure but trivial to bring up and run - findings, severities, and the concrete next moves.
status: draft
generated:
  by: dave
  at: 2026-10-25T06:30:00Z
tags: [review, mvp, bring-up, dx, priorities]
---

# MVP bring-up review

Reviewed 2026-10-25 against the tree at `feat/mvp-platform-review`.

**The question this document answers:** what would actually slow down or
break a first-time bring-up, and what should be cut so the MVP ships?

**The verdict in one line:** the *structure* is in good shape — the config
layer, the provider registry, and the ledger are right-sized seams — but the
*thing you are trying to run* does not exist yet, and the plan still treats
the container path and the two-provider ingest as one phase. Fix the
vertical slice first; defer everything that multiplies the surface.

## Top 5 things that would slow down or break a first bring-up

1. **The pipeline does not exist.** `agent ingest`, `embed`, `query`,
   `stats`, and `serve` are placeholders. A newcomer following `README.md`
   runs `make doctor`, sees it pass, and then hits a command that does
   nothing. *Now fixed in part:* the placeholders exit non-zero with an
   actionable message instead of exiting 0 and looking like a success.
2. **The SQLite driver decision is unmade.** Plan Decision 2b defers
   `mattn/go-sqlite3` + sqlite-vec (CGO, `sqlite_fts5` build tag) versus
   ncruces (pure Go) to a "driver spike" inside Phase 1. Every remaining
   piece — store, embed, query, the container image — depends on that
   choice. It is the single largest unknown in bring-up, and it is also the
   cheapest thing to resolve.
3. **No credentials and no captured fixtures.** Phase 0 is blocked on two
   API keys, and `docs/ghost.md` / `docs/postiz.md` are still empty
   placeholders, so the schema in `migrations/0001_init.sql` is inference
   rather than observation. Until one real payload is captured, every
   downstream parser is written against a guess.
4. **The container path is unbuilt, and its default is broken.** There is no
   `Dockerfile` and no `compose.yaml`, yet the plan's exit criteria promise
   `docker compose up` on a clean host. Worse, the compose snippet in §2.5
   defaults `LOCALAI_BASE_URL` to `http://localhost:8080/v1` — inside a
   container that is the container itself, so the shipped default cannot
   work. A newcomer who copies the snippet gets a confusing failure.
5. **Nothing proves the pipeline end to end.** The tests cover the config
   layer and the doctor. There is no test that runs ingest → embed → query
   against fixtures, so "it works" is a claim rather than a fact, and a
   newcomer has no one-command way to prove their own setup.

## Findings

Ordered by what matters for shipping an MVP fast.

| # | Finding | Severity | Effort | Concrete recommendation |
|---|---|---|---|---|
| 1 | `ingest`/`embed`/`query`/`stats`/`serve` are placeholders; the usable product does not exist | blocker | 3–4 d | Build the vertical slice on **Ghost only** first: one provider, one chunker, the embedder, hybrid retrieval. Postiz lands second on a proven pipeline. |
| 2 | The SQLite driver (CGO `mattn/go-sqlite3` + sqlite-vec vs. pure-Go ncruces) is undecided, and everything depends on it | blocker | 0.5 d | Do the spike now, as its own commit: pin the driver, prove FTS5 BM25 + `vec0` KNN round-trip, then vendor it. Decide before writing store code, not during it. |
| 3 | No Ghost/Postiz credentials and no captured payload fixtures | blocker | human | Obtain both keys; capture one real Ghost post JSON and one real Postiz posts response into `docs/` **and** into `internal/sources/*/testdata/` so the parsers are written against observed data. |
| 4 | No end-to-end test of the pipeline; a newcomer cannot prove their setup in one command | blocker | 1 d | Land `TestE2EIngestEmbedQuery`: a fixture corpus → fake model server (`httptest`) → ingest → embed → query → assert the known chunk comes back. No network, no credentials. `make doctor` already covers the preconditions. |
| 5 | The container path does not exist, and §2.5's compose default (`localhost:8080`) cannot work inside a container | should | 1 d | **Cut the container path from the MVP.** Ship host mode. When it returns: require `LOCALAI_BASE_URL` in compose (no `localhost` default), document the container-vs-host address trap, and add the image to the Phase 1 exit criteria only after host mode is green.  **Partially fixed**: the plan's compose snippet now requires the variable (`${LOCALAI_BASE_URL:?set LOCALAI_BASE_URL}`) instead of defaulting to `localhost`; the container path itself is still unbuilt and is recommended to be cut from the MVP. |
| 6 | `make doctor` cannot check the SQLite driver, the extension, or a corrupt index | should | 0.5 d | Once the driver is pinned, add a `sqlite` check: open the index read-only, `PRAGMA integrity_check`, load the vec extension, and report a corrupt DB as a failure with the fix (`rm index.db && agent ingest`). |
| 7 | The registry is described as a *constructor* registry but is only an options-parser registry | should | 0.25 d | Fix the plan's wording (Decision 13, §2.6) to say "type → options parser", and add the constructor seam when the first real `Provider` lands. Do not add an empty constructor registry now. |
| 8 | A `store` interface and an `llm` interface are implied but do not exist; both would be premature | should | 0 | Do not create them. One implementation each; test the HTTP boundary with `httptest` and the store with a real SQLite file. Introduce an interface when a second implementation exists. |
| 9 | `internal/config.Credentials` is hardcoded to `api_url` + `api_key`, so a provider needing a third credential means editing `internal/config` | should-not | 0 | Leave it. Both current providers need exactly these two. Revisit when a third provider appears — if it does, widen the schema to a map *then*, not speculatively now. |
| 10 | `sources.yaml` is the only structure config, but chunking defaults live in it while runtime defaults live in Go — the split is not obvious | should-not | 0 | Keep it. `sources.yaml` = per-source structure; Go defaults = process-wide runtime. Document the split in `docs/sources-config.md` (one paragraph), which the file already half-does. |
| 11 | The binary does not load `.env`, but `.env` exists and looks loadable | should | 0.25 d | Do not add a dotenv dependency. Document the load order explicitly (`.env.example` header, `README.md`, and `make setup` printing `set -a; . ./.env; set +a`). The binary reads the process environment only. |
| 12 | `scripts/ingest-all.sh` called the removed `./cmd/ingest` path | should | 0 | **Fixed** — it now calls `./cmd/agent ingest` with `-mod=vendor`. |
| 13 | `scripts/check-ddl.py` was referenced by `AGENTS.md` and `PLAN.md` but did not exist | should | 0.25 d | **Fixed** — written. It applies the migrations to a scratch DB, proves the FTS5 stored-column join-back, checks `float[768]`, and keeps `0001_init.sql` in sync with the PLAN §3 schema block. |
| 14 | `.gitignore` had `/.env.*`, which also excluded `.env.example` | should | 0 | **Fixed** — added `!/.env.example`. |
| 15 | `agent ingest`/`embed`/`query` exited **0** while doing nothing | should | 0 | **Fixed** — they now exit 1 with a message pointing at the phase. A command that did nothing must not look like it worked. |
| 16 | The plan promised a `/readyz` startup probe the code does not implement (it lists models instead) | should | 0 | **Fixed** — the plan now describes the model-catalog check, and `/readyz` is documented as optional. |
| 17 | `AGENTS.md` and `README.md` described a bootstrap repo that no longer exists (`cmd/*` stubs, empty `internal/`) | should | 0 | **Fixed** — both now describe the real tree and carry no host specifics. |
| 18 | No CI: the four verification commands only run if a human remembers | should | 0.25 d | Add a GitHub Actions workflow running `go build/vet/test -mod=vendor` and `gofmt -l`. The dependency set is vendored, so CI needs no module downloads. |
| 19 | `docs/architecture.md` and `docs/runbook.md` are in the §2 target layout but do not exist | should-not | 0 | They are Phase 2 / Phase 3 deliverables. The §2 layout is explicitly a *target*, and the "current state vs. target" paragraph now says so. Leave them. |
| 20 | `docs/runs/0001-init-ddl-check.md` records a run of a script that did not exist at the time | should | 0 | Superseded by a fresh run capture of the real check (the script now exists and is green). |
| 21 | The Phase 1 exit criteria bundle host mode, container mode, drift detection, full reindex, and the ledger into one gate | should | 0.25 d | Split the gate: the MVP gate is "Ghost-only ingest → embed → query over fixtures, plus `agent doctor` green"; container mode and Postiz move to the following gate. |
| 22 | No `eval/queries.yaml` yet, so retrieval quality is unmeasurable | should | 1 d | Write 20 queries with ground-truth chunk ids once the Ghost slice is green — before adding Postiz, so the eval measures the pipeline, not the corpus. |

## Assessment

### 1. Bring-up cost

The real path today, on a host with Go 1.27:

```
git clone …            # 1
make setup             # 2  toolchain check, .env from .env.example, build
make doctor            # 3  config, provider options, credentials, model server, index path
make check             # 4  build + vet + test + gofmt
./agent ingest         # 5  not implemented yet
```

Steps 1–4 work and take about a minute. **There is exactly one documented
path** and it is now a single `make` entry point per step. Every
prerequisite is explicit and machine-checkable: Go ≥ 1.27 (`make setup`
checks the version and refuses to continue otherwise), CGO (needed only once
the driver lands — see finding 2), FTS5 (proved by `scripts/check-ddl.py`),
sqlite-vec (the driver spike, finding 2), and a model server (`make doctor`
probes it and names the failing variable).

The gap is step 5: the loop does not exist. Nothing else about bring-up is
expensive.

### 2. MVP scope

**Phase 1 is doing too much and the wrong things first.** It mixes the
config layer (done), the driver spike, the connector, the container image,
two providers, the ledger, the chunker rules, and the full reindex into one
phase with one exit gate.

* **Cut from the MVP:** the container path (Dockerfile, compose, volume
  contract, healthcheck). Its only benefit is "no host Go toolchain", which
  is not the bottleneck — the bottleneck is credentials and the pipeline.
  It also triples the failure surface: CGO in-image, volume permissions,
  WAL siblings on one mount, and the `localhost`-means-the-container trap.
* **Defer:** `agent backup` (`VACUUM INTO` is a nicety; `cp` works),
  `agent ingest --watch` (cron is enough), Postiz ingest (see below),
  the ledger's `--full` re-embed path (the `config_hash` mismatch already
  forces a re-embed when it matters).
* **Must exist for a usable end-to-end MVP:** one provider's credentials and
  a captured fixture; a pinned SQLite driver with FTS5 + `vec0` proven;
  `agent ingest` writing chunks and FTS rows in one transaction; `agent
  embed` over the connector; `agent query` doing BM25 + vector with RRF;
  a fixture-based end-to-end test; and `agent doctor`.

**The decisive recommendation: Ghost first.** A Ghost post is one JSON
object with one body. A Postiz post is a fan-out across platforms with
statuses (`draft`, `failed`, `scheduled`) and release ids. Green on Ghost
proves the store, the chunker, the embedder, and the retriever; Postiz's
cross-post linkage then rides on a pipeline that is already known to work.
Shipping both at once means every bug is ambiguous between "the pipeline is
wrong" and "the Postiz shape is wrong".

**The smallest coherent vertical slice is:** one Ghost post fixture →
`agent ingest` → `agent embed` → `agent query` returns that chunk →
`agent doctor` green. Everything else is deferred to the next gate.

### 3. Extensibility without ceremony

The seams that are right:

* `sources.yaml` for structure + environment for secrets and runtime. Adding
  a provider is one YAML entry plus one package. That claim is true today
  for options parsing; it becomes fully true when the constructor lands.
* The `Provider` interface with the cheap `List` / expensive `Fetch` split.
  That split is what makes incremental ingest affordable *and* fixture-
  testable without credentials. Keep it; do not flatten it into one method.
* The strict YAML decode (`KnownFields(true)`) plus `agent sources
  validate`: a typo is a hard error, not a silently disabled provider.
* The single OpenAI-compatible connector. Swapping model servers is one
  base URL and two model ids — no SDK, no per-vendor code path.

The seams that are wrong or premature:

* **The registry is called a constructor registry but is only an options
  parser registry.** Rename the concept in the plan (finding 7) instead of
  building an empty constructor layer.
* **A `store` interface and an `llm` interface** would be premature: one
  implementation each, and both would exist only to be mocked by tests that
  can use a real SQLite file and an `httptest` server instead. Finding 8.
* **The concrete implementation currently blocked by an abstraction:** none
  is blocked. The deferred website provider is genuinely one package plus
  one config entry, as the plan claims. That is the test the registry
  should pass — and it does.

### 4. Config and secrets

The two layers are coherent and, after this pass, complete: `sources.yaml`
declares providers and options; the environment carries secrets
(`GHOST_API_URL`, `GHOST_API_KEY`, `POSTIZ_API_URL`, `POSTIZ_API_KEY`) and
runtime values (`LOCALAI_BASE_URL`, `LOCALAI_API_KEY` optional,
`EMBED_MODEL`, `CHAT_MODEL`, `AGENT_DB_PATH`). `.env.example` now lists all
nine with no values.

Nothing leaks. `config.MissingEnv()` returns names only; `agent sources
list` and `agent doctor` print variable names and a set/unset flag; there is
no logging layer that could print a value. Two tests assert this
(`TestRunSourcesList`, `TestDoctorNeverPrintsCredentialValues`).

Two gaps remain, both documented above: the binary does not read `.env`
(finding 11), and the credential schema is fixed at two fields (finding 9).

### 5. Failure modes and DX

| failure | current behaviour | verdict |
|---|---|---|
| model server down | `agent doctor` fails, names `LOCALAI_BASE_URL`, distinguishes "cannot be reached" from "answered 404" | good |
| wrong model id | `agent doctor` warns and lists the models the server does serve | good |
| credential missing | `agent doctor` warns and names the variables; `sources validate --strict` exits 1 | good |
| config typo | strict YAML decode fails with the key name | good |
| unimplemented command | exits 1 with a message pointing at the phase | fixed |
| missing config file | `read config` error with the path | good |
| SQLite driver missing / extension missing | **not checked** — no store yet | finding 6 |
| corrupt index | **not checked** — no store yet | finding 6 |

`agent doctor` is the one-command answer to "can this machine run the
agent?", and `make check` is the one-command answer to "is this tree
healthy?".

### 6. Verification story

What exists today: `make check` (build, vet, test, gofmt), `make doctor`
(are the preconditions met), and `scripts/check-ddl.py` (is the schema
sound and in sync with the plan). The fixture-based end-to-end test covers
config → strict validate → sources list → doctor, against a fake model
server and a fake upstream, with no network and no credentials.

What is missing: an end-to-end test of the actual pipeline (finding 4). That
is the test that will catch the store, the chunker, the connector, and the
retriever regressions at once — and it is the one that lets a newcomer prove
their setup in a single command.

### 7. Repo hygiene

* **Dead files:** `scripts/ask` is a Phase 2 stub that exits 1 — correct
  behaviour for a not-yet-implemented command. `internal/{store,embed,
  llm,retrieval}` hold only `.gitkeep`. Nothing else is dead.
* **Stale docs:** `AGENTS.md` and `README.md` described a bootstrap repo
  that no longer exists — fixed. The `docs/runs/` capture of the DDL check
  referenced a script that did not exist — superseded by a fresh capture.
* **Plan vs. code:** the plan promised a `/readyz` probe the code does not
  implement — fixed. The plan's §2 layout listed `Makefile`, `.env.example`
  and `scripts/check-ddl.py` as targets — all three now exist.
* **`.gitignore`:** the `/.env.*` rule swallowed `.env.example` — fixed.
  The DB rules (`*.db`, `*.db-wal`, `*.db-shm`) already cover the derived
  index, including the WAL siblings.
* **`vendor/`:** keep vendored. The constraint is real (the worker has no
  network and a hidden module cache) and it makes CI trivial — no module
  downloads, no proxy, no flaky resolve. `go.mod`/`go.sum`/`vendor/` were
  not touched.
* **CI:** none — finding 18.

## What was changed by this review

Deliberately small, and all of it serves bring-up:

* `Makefile` with `setup` / `check` / `doctor` / `build` / `test` (plus
  `fmt`, `fmt-check`, `e2e`, `verify`, `clean`).
* `agent doctor` — probes the config, provider options, credential
  variables, the model endpoint and the index path; never prints a
  credential value.
* `.env.example` — the full nine-variable surface, no values, with the load
  order stated.
* `scripts/check-ddl.py` — the DDL smoke check the plan and `AGENTS.md`
  referenced but that did not exist.
* Fixture-based end-to-end test (`TestE2EConfigToDoctor`) plus the doctor's
  own tests, including the "model server down" and "no credential leak"
  cases.
* `.gitignore` fix, `scripts/ingest-all.sh` path fix, non-zero exit for
  unimplemented subcommands, and the plan/doctor probe-wording alignment.
* The de-hosting pass on `PLAN.md` (including the new non-normative §7
  appendix), `README.md`, `AGENTS.md`, and `docs/`.

## What was deliberately not done

* **No container work.** No `Dockerfile`, no `compose.yaml`, no
  `.dockerignore`. Finding 5 says defer it; doing it now would be the
  opposite of the review's advice.
* **No driver spike.** Choosing and vendoring the SQLite driver changes
  `go.mod`, `go.sum`, and `vendor/`, which this pass was told not to do
  without a strict need. It is finding 2 — the next task, not this one.
* **No store, embed, retrieval, or provider client code.** That is the
  pipeline (finding 1), and it is the next gate.
* **No `eval/queries.yaml`, no `docs/architecture.md`, no
  `docs/runbook.md`.** Phase 2 / Phase 3 deliverables.
* **No new dependencies** — not even a dotenv loader. The env-var surface
  is documented instead.
* **No second implementation behind a new interface.** The `store` and
  `llm` interfaces stay unwritten until a second implementation exists.
