---
type: Run
title: Full validator sweep on the eval-harness branch
description: Every repo validator on the merge of main into the eval-harness branch — build, vet, gofmt, 19 Go test packages, the CLI smoke checks, a live retrieval query — plus the first full-size Ragas run and the first passing promptfoo smoke run.
status: stable
tags: [validation, go-tests, evals, ragas, environment-drift]
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-10T07:53:56Z
run:
  type: validator-sweep
  cmd: |
    git merge origin/main          # PR #6: feat/eval-harness-and-serve-bind
    go mod vendor
    go build -mod=vendor -tags sqlite_fts5 ./...
    go vet -mod=vendor -tags sqlite_fts5 ./...
    gofmt -l cmd internal migrations
    go test -mod=vendor -tags sqlite_fts5 -count=1 ./...
    ./agent sources validate
    ./agent tokens list
    ./agent stats
    ./agent query "What did Eli write about Proxmox?"
    (cd evals && uv run python evaluate.py)
    (cd tests-ai && npm ci && npm run redteam:smoke)
output: |
  ok  	github.com/eli-l/elilap.dev-agent/cmd/agent	0.415s
  ok  	github.com/eli-l/elilap.dev-agent/internal/a2a	0.275s
  ok  	github.com/eli-l/elilap.dev-agent/internal/agentloop	0.252s
  ok  	github.com/eli-l/elilap.dev-agent/internal/audit	0.240s
  ok  	github.com/eli-l/elilap.dev-agent/internal/auth	0.252s
  ok  	github.com/eli-l/elilap.dev-agent/internal/config	0.258s
  ok  	github.com/eli-l/elilap.dev-agent/internal/embed	0.555s
  ok  	github.com/eli-l/elilap.dev-agent/internal/envcfg	0.215s
  ok  	github.com/eli-l/elilap.dev-agent/internal/ingest	0.265s
  ok  	github.com/eli-l/elilap.dev-agent/internal/llm	0.249s
  ok  	github.com/eli-l/elilap.dev-agent/internal/logging	0.205s
  ok  	github.com/eli-l/elilap.dev-agent/internal/ratelimit	0.179s
  ok  	github.com/eli-l/elilap.dev-agent/internal/retrieval	0.428s
  ok  	github.com/eli-l/elilap.dev-agent/internal/sources	0.356s
  ok  	github.com/eli-l/elilap.dev-agent/internal/sources/ghost	0.316s
  ok  	github.com/eli-l/elilap.dev-agent/internal/sources/postiz	0.323s
  ok  	github.com/eli-l/elilap.dev-agent/internal/sources/shared	0.298s
  ok  	github.com/eli-l/elilap.dev-agent/internal/store	0.569s

  # go vet: no findings; gofmt -l: no output
  sources.yaml: ok (version 1, 2 source(s), 2 enabled, 2 provider type(s) validated)
  no tokens configured

  chunks:    855
  fts rows:  855
  vec rows:  855
  embedded:  855
  sources:   3
  last run:  2026-11-09T12:31:59Z

  answers:    rows scored 15/15   correctness 9/15   refused 3/15
              faithfulness 0.561 (n=12)   context utilization 0.875 (n=12)
  refusals:   rows scored 14/14   refusal discipline 14/14   refused flag 14/14

  # tests-ai: local judge, generation + grading, nothing uploaded
  ✓ Red team complete (ID: eval-c6G-2026-11-10T11:07:18)
  Results: 1 passed (100%), 0 failed, 0 errors
  Duration: 28s (concurrency: 4)
  Writing output to reports/smoke/results.json, reports/smoke/results.html
---

# Result

Taken on **PR #6** (`feat/eval-harness-and-serve-bind`) after merging `main`
at `8f4cdd1` into it, so the numbers cover the branch that adds the evaluation
harnesses rather than `main` alone.

The merge is green on every validator the repo defines, and the retrieval
stack answers against the real index. Two of the branch's own claims also
held up: the Ragas suite ran at full size for the first time — all 15 answer
rows and all 14 refusal rows were scored — and the promptfoo smoke scan
generated a probe, got an answer from the local agent, graded it with the
local judge, and wrote its report. The Ragas answer-quality numbers are **not
a baseline**, because the configured chat model is down and the run used a
stand-in (below); the promptfoo smoke run is a wiring check, not a security
verdict.

## What passed

- **Build** — `go build ./...` succeeds with the `sqlite_fts5` tag. The only
  output is the cgo deprecation chatter from
  `sqlite-vec-go-bindings` against the macOS SDK
  (`sqlite3_auto_extension is deprecated`), which is upstream noise, not ours.
  `vendor/` was absent on the checkout and had to be recreated with
  `go mod vendor`, as `../AGENTS.md` requires.
- **Static checks** — `go vet` reports nothing; `gofmt -l cmd internal
  migrations` prints nothing.
- **Tests** — 19 of 19 packages pass with `-count=1` (no cache) on the merge,
  including the schema smoke test `TestSchemaDDL` described in
  [SQLite schema smoke check in Go](/runs/ddl-check-2026-11-08.md) and the
  branch's new `internal/sources/web` package.
- **CLI** — `sources validate` accepts the committed config (2 sources,
  2 enabled; 4 credential warnings because the run did not export `.env`),
  `tokens list` reports no tokens (correct: no file, no env var).
- **Both harnesses** — `tests-ai` installs from its committed lockfile
  (`promptfoo@0.125.1`) and its smoke scan passes against
  `./agent serve --addr 127.0.0.1:18080`, writing
  `reports/smoke/{redteam.yaml,results.json,results.html}`; the Ragas suite
  scores every row in `evals/configs/` (below). The 18080 evaluation port keeps
  both suites off the interactive 8080 server.
- **Live retrieval** — `agent stats` agrees across all three tables (855
  chunks / 855 FTS rows / 855 vec rows, all embedded), and `agent query`
  returns ranked hybrid hits with `score`/`cosine`/`distance`/`bm25` and real
  chunk ids, so RRF over `chunks_fts` + `chunks_vec` is working on this index.

## The full Ragas run, and why its numbers are not a baseline

`cd evals && uv run python evaluate.py` scored the complete suite in ~35
minutes: 15/15 answer rows and 14/14 refusal rows, every row producing a
verdict rather than a task error. Refusal discipline is 14/14 and the refusal
flag agrees on every row.

The answer metrics — correctness 9/15, faithfulness 0.561, context
utilization 0.875 — were produced under two substitutions that make them a
plumbing result, not a quality result:

1. **The configured chat model is broken.** `spark-x2.5-4b` (the value in
   `CHAT_MODEL`) fails to load on the LocalAI host at `192.168.78.243:8080`:
   `failed to load model with internal loader: ... rpc error: code =
   Unavailable desc = error reading from server: EOF`, after which the
   endpoint answers `503 model "spark-x2.5-4b" load is in cooldown` in a loop
   for tens of minutes. `spark-x2.6-4b-q4` fails the same way. The only
   working chat model on that host was `spark-x2.5-1.7b-q4`, a much weaker
   model, so the agent under test was not the agent this repo is tuned for.
2. **The configured judge id no longer exists.** `EVAL_MODEL` names
   `mtplx-qwen36-35b-a3b-optimized-speed`; `localhost:8000` serves
   `mtplx-qwen36-35b-a3b-optimized-speed-fp16`. The judge ran as the `-fp16`
   id.

Embeddings were unaffected (`embeddinggemma-300m` still serves on the LocalAI
host), so retrieval, citation resolution, and the metric pipeline were the
real thing; only the generator and judge were not.

**What this run does prove:** the harness works at full size — `evaluate.py`
walks both configs, `agent.py` reads the cited chunk bodies from `index.db`,
Ragas scores every row, and `output/experiments/` gets the CSV/trace
artifacts. **What it does not prove:** that the shipped defaults meet the
answer-quality bar. Re-running with a healthy `spark-x2.5-4b` and a
`spark-x2.5-4b`-class judge is still open Phase 3 work, and
[Ragas answer evaluation (evals)](/ragas-eval.md) stays `draft` until it
lands.

## Not run

- **The full promptfoo scan** — `npm run redteam:smoke` passes end to end in
  28s (one local-capable plugin, one probe, local grading, 0 findings), but
  `npm run redteam` is ~40 plugins x 5 probes x 2 strategies, every probe a
  real tool-using answer plus a 35B grading call, so it is hours of wall clock
  and was not attempted here. [Red-team evaluation with promptfoo](/red-team.md)
  stays `draft` until a full report is captured.
- **promptfoo without this branch** — worth recording, because it changes the
  story: `npx promptfoo@latest redteam run` on bare `main` stops at
  `Red team scans require email verification to continue` and has no grading
  provider configured. On this branch `tests-ai/run-promptfoo.sh` sets
  `CI=true` plus `PROMPTFOO_DISABLE_SHARING`/`_TELEMETRY`, and
  `redteam.provider` points generation and grading at the local judge, so the
  suite runs with no account and no cloud key.
- **`agent ask` / `/v1/ask` against the shipped model** — the loop itself
  works (`agent ask` with the stand-in model ran 3 tool iterations and
  refused a question the corpus does not answer, in 11 s), but no answer from
  `spark-x2.5-4b` could be produced while it fails to load.

## Environment drift this run exposed

Two values in the local `.env` are stale relative to the LocalAI host. Neither
is a repository file (`.env` is gitignored and `.env` is a copy of the
committed `example.env` inventory), but both will bite the next eval run:

- `EVAL_MODEL` → `mtplx-qwen36-35b-a3b-optimized-speed-fp16` (renamed).
- `CHAT_MODEL` → a model the host can actually load; `spark-x2.5-4b` and
  `spark-x2.6-4b-q4` both fail with the loader RPC error above.

[Self-hosted LocalAI endpoint](/localai.md) should be revisited once the host
is fixed, since it records the model catalog.
