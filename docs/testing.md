---
type: Guide
title: End-to-end testing - CI, suites, ports, configs, and reports
description: The single reference for testing - the CI gate for formatting, vet, and unit tests, the two end-to-end suites (what Ragas and promptfoo each check, the ports and local models they use, the exact commands, where every config lives, where every report is written), and what is committed versus gitignored.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-11T07:55:00Z
tags: [testing, ci, evals, ragas, promptfoo, ports, reports]
sources:
  - ../.github/workflows/ci.yml
  - ../AGENTS.md
  - ../evals/README.md
  - ../evals/evaluate.py
  - ../evals/agent.py
  - ../tests-ai/README.md
  - ../tests-ai/package.json
  - ../tests-ai/run-promptfoo.sh
  - ../tests-ai/promptfooconfig.yaml
  - ../tests-ai/promptfooconfig.smoke.yaml
  - ../example.env
---

# End-to-end testing - suites, ports, configs, and reports

There are exactly **two** end-to-end suites, and they answer different
questions. Do not add a third harness or a second entry point inside either
directory: retrieval quality is graded through the Ragas answer metrics, and
adversarial behaviour through promptfoo.

| Suite | Question it answers | Directory | Runtime | Tool |
|---|---|---|---|---|
| **Ragas** | Are the answers faithful to the cited excerpts, correct against recorded facts, and refusing what the corpus cannot answer? | `evals/` | Python 3.12 + `uv` | Ragas |
| **promptfoo** | Does the agent behave under adversarial input: bias, harmful content, PII, excessive agency, hijacking, hallucination, fabricated citations? | `tests-ai/` | Node.js ≥ 20 + npm | promptfoo `0.125.1` |

Both talk to the same system under test: the real Go agent over
`POST /v1/ask`, backed by a real `data/index.db`. Tests never run against stubs or
live upstream APIs.

## Ports and endpoints

| Port | Used by | Notes |
|---|---|---|
| `8080` | interactive development | Default `agent serve`; never used by the suites. |
| `8081` | container A2A | `docker compose up` publishes `127.0.0.1:8081` to the container's 8080; the host 8080/18080 stay free. |
| `18080` | both test suites | `./agent serve --addr 127.0.0.1:18080`; both harnesses target `127.0.0.1:18080/v1/ask`. |
| `8000` | the local judge model | `EVAL_BASE_URL=http://localhost:8000/v1`, model `mtplx-qwen36-35b-a3b-optimized-speed`. Used by Ragas and by promptfoo. |
| `15500` | promptfoo report UI | `npm run redteam:report`, reads the local database. |

The agent's own model backend (`LOCALAI_*`, default
`http://192.168.78.243:8080/v1`) is independent of the judge endpoint. The
suites use the judge for scoring (and promptfoo for attack generation), not
for answering.

Start the target once, in its own terminal:

```bash
# from the repository root
go build -mod=vendor -tags sqlite_fts5 -o agent ./cmd/agent
./agent serve --addr 127.0.0.1:18080
```

## Ragas — answer quality

**Tool**: `evals/evaluate.py`, the only entry point. It asks the real agent
each question, reads the body of every cited chunk from `data/index.db`, and scores
the answer with the local judge.

**What is used**

| Path | Role |
|---|---|
| `evals/evaluate.py` | The tool: loads configs, asks the agent, runs the metrics, prints a summary, writes CSVs. |
| `evals/agent.py` | The system under test: `agent ask --format json` (default `cli` transport) or `POST /v1/ask` (`--transport http`); resolves cited chunk ids to excerpt bodies. |
| `evals/configs/answers.yaml` | 15 answerable questions with human-verified `expect_chunk_ids` and `must_mention` facts. |
| `evals/configs/refusals.yaml` | 14 questions the corpus cannot answer. |
| `evals/test_llm.py` | Judge-endpoint precondition check — not part of scoring. |
| `evals/uv.lock` | Committed lockfile for reproducible installs. |

**Metrics**

* `answers`: `Faithfulness`, `ContextUtilization`, and a `correctness`
  discrete metric built from `must_mention`. A refusal on an answerable row is
  `correctness: fail` and skips the numeric metrics.
* `refusals`: `refusal_discipline` — did the agent decline with
  `"Not in my published writing"` instead of inventing an answer?

**How to run**

```bash
cd evals
uv sync
uv run python test_llm.py                            # exit 0 = judge endpoint usable
uv run python evaluate.py --kind answers --limit 3   # cheap smoke run
uv run python evaluate.py                            # both datasets
```

Options: `--kind answers|refusals|both` (default `both`), `--limit N`,
`--transport cli|http` (default `cli`), `--ask-url URL` (default
`http://127.0.0.1:18080/v1/ask`). The `http` transport needs the server on
18080; `cli` needs the built `./agent` binary. `EVAL_BASE_URL`,
`EVAL_MODEL`, and `EVAL_API_KEY` are declared in `example.env`; the harness
does not load `.env` itself — pass `uv run --env-file ../.env …` or export
them.

**Where the reports go**: `evals/output/` — `datasets/*.csv` (the rows) and
`experiments/*.csv` (one row per question with every metric), plus any trace
files. It is gitignored; `.gitkeep` keeps the directory. The exit status fails
on incomplete rows and on any missed quality gate: by default correctness
≥ 70%, faithfulness ≥ 0.75, context utilization ≥ 0.80, and refusal
discipline 100%. The stricter 80%/0.80 target is available with
`--min-correctness 0.80 --min-faithfulness 0.80`; the printed metrics are
always the detail.

## promptfoo — adversarial behaviour

**Tool**: promptfoo, pinned locally in `tests-ai/package.json`. A run is two
steps: generate attack probes, then evaluate them against the target.

**What is used**

| Path | Role |
|---|---|
| `tests-ai/promptfooconfig.yaml` | The full scan: HTTP target on 18080, purpose, 43 plugins, `basic` + `jailbreak:meta`, 5 tests per plugin. |
| `tests-ai/promptfooconfig.smoke.yaml` | One `hallucination` plugin, one probe, `basic` only — the fully local wiring check. |
| `tests-ai/package.json`, `package-lock.json` | Pinned promptfoo `0.125.1` and the run scripts. |
| `tests-ai/run-promptfoo.sh` | Single place for the local-only environment: `.promptfoo/` database, no sharing/telemetry, non-interactive mode. |
| `tests-ai/.npmrc` | `save-exact=true`. |
| `tests-ai/go.mod` | Marker only: keeps the repo-root `go build ./...` out of `node_modules/` (promptfoo ships Go sources). |

**Local model**: both configs set `redteam.provider` to the same endpoint as
the Ragas judge (`openai:chat:mtplx-qwen36-35b-a3b-optimized-speed`,
`apiBaseUrl: http://localhost:8000/v1`, `apiKey: local`). Local-capable
plugins generate with it and **every** result is graded with it. promptfoo
marks some plugins/strategies as remote-only (`harmful:*`, bias,
`hijacking`, `politics`, `bola`/`bfla`/`rbac`, `jailbreak:meta`); the full
scan sends those to promptfoo-hosted inference. The smoke config disables
remote generation, so it is fully local.

**How to run**

```bash
cd tests-ai
npm ci

npm run redteam:smoke    # one plugin, fully local; ~1 minute
npm run redteam          # generate redteam.yaml, then evaluate it
npm run redteam:report   # browser UI on http://localhost:15500
```

Step control: `npm run redteam:generate`, `npm run redteam:eval`. Add
promptfoo flags after `--`, e.g. `npm run redteam:eval -- -j 2` or
`npm run redteam:generate -- --force`.

**Where the reports go**

| Artifact | Path | Committed |
|---|---|---|
| Full-scan probes | `tests-ai/redteam.yaml` | no |
| Full-scan results | `tests-ai/reports/results.json` | no |
| Full-scan static report | `tests-ai/reports/results.html` | no |
| Smoke probes + results | `tests-ai/reports/smoke/redteam.yaml`, `results.json`, `results.html` | no |
| promptfoo database, cache, logs (report UI source) | `tests-ai/.promptfoo/` | no |

Open `reports/results.html` (or `reports/smoke/results.html`) directly, or
run the report UI. The scripts target the eval port 18080 and leave the
interactive server on 8080 alone.

## Reading test behaviour

* **A refusal is not a failure.** `"Not in my published writing"` is the
  expected safe answer to an off-corpus or adversarial question. In Ragas it
  is only a failure on an `answers` row; in promptfoo it is the expected safe
  result.
* **The local chat models are non-deterministic.** Re-run a suspected finding
  before treating it as real, and keep `--force` for config/generation changes
  rather than blaming one noisy run.
* **Citations matter.** Ragas scores against the excerpts the answer cited
  (read back from `data/index.db`). A fabricated citation therefore has no
  supporting context and shows up as a faithfulness/attribution problem.
* **Tests use the real loop.** They exercise ingest → embed → retrieve →
  grounded answer; a retrieval miss surfaces as a correctness or faithfulness
  miss, not as a separate hit@k number.

## Committed versus generated

Committed: `evals/evaluate.py`, `evals/agent.py`, `evals/configs/*.yaml`,
`evals/uv.lock`, `tests-ai/promptfooconfig*.yaml`, `tests-ai/package*.json`,
`tests-ai/run-promptfoo.sh`, `tests-ai/.npmrc`, `tests-ai/go.mod`, and both
README files. Generated and gitignored: `evals/output/`, `evals/.venv/`,
`tests-ai/node_modules/`, `tests-ai/.promptfoo/`, `tests-ai/redteam.yaml`, and
`tests-ai/reports/*` (the `.gitkeep` files keep the directories). `data/` is
tracked by `.gitkeep` only; the index, WAL/SHM, backups, and A2A token file
inside it are generated and gitignored, and `data/` is excluded from the image
build context.

When a run is worth keeping as evidence, capture a trimmed record under
`docs/runs/` using the `type: Run` format (command, trimmed output, timestamp)
rather than committing the raw reports.

## CI

`.github/workflows/ci.yml` runs on every push to `main` and every pull
request. It is deliberately limited to checks that need no model host, no
promptfoo account, and no network beyond the Go module download:

1. `gofmt -l cmd internal migrations` must print nothing.
2. `go vet -tags sqlite_fts5 ./...`
3. `go test -tags sqlite_fts5 -count=1 ./...`

CGO is enabled because `mattn/go-sqlite3` and sqlite-vec are CGO packages, and
the `sqlite_fts5` tag is required by the driver and the schema tests. The
Ragas and promptfoo suites, `agent ingest`/`embed`/`ask`, and every other path
that touches LocalAI stay local, on-demand runs against a populated
`data/index.db`; they are not part of CI.

## Related

* [Ragas answer evaluation (evals)](/ragas-eval.md)
* [Red-team evaluation with promptfoo](/red-team.md)
* [Runbook - build, ingest, embed, query, ask, serve](/runbook.md)
* [Environment configuration and .env loading](/environment.md)
* `AGENTS.md` at the repository root for the two-tool policy.
