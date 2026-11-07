---
type: Guide
title: Runbook - build, ingest, embed, query, ask, serve
description: The operator path from a clean checkout to grounded answers - building the binary, rebuilding the index, running hybrid queries and the agent loop, the loopback dev server, the two end-to-end test suites (Ragas and promptfoo), and the failures you are likely to hit.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-03T17:20:00Z
tags: [runbook, operations, cli, http, eval, troubleshooting]
sources: ["../PLAN.md", "../AGENTS.md"]
---

# Runbook

## Prerequisites

* Go 1.27 and a CGO toolchain (the SQLite driver needs both).
* A populated `.env` (start from `example.env`; credentials live only there).
* The model host in `LOCALAI_BASE_URL` reachable:
  `curl "${LOCALAI_BASE_URL%/v1}/readyz"` (the default is
  `http://192.168.78.243:8080`). It serves both embeddings (`EMBED_MODEL`)
  and chat (`CHAT_MODEL`).

## Build

```bash
go mod vendor                  # after any go.mod/go.sum change; vendor/ is gitignored
go build -tags sqlite_fts5 -o agent ./cmd/agent
```

Every command below is read-only over the index except `ingest` and `embed`.

## Rebuild the index

```bash
./agent ingest                 # Ghost + Postiz, incremental by default
./agent ingest --full          # re-fetch and re-chunk everything
./agent embed --progress       # vectorize pending chunks; resumable
./agent stats                  # chunks / fts rows / vec rows / embedded / sources
```

`chunks`, `chunks_fts`, and `chunks_vec` should report the same count, and
`embedded` should equal `vec rows`. A second `./agent ingest` is a near no-op.

## Query (no LLM)

```bash
./agent query "harness vendor lock-in"                       # text, top 5
./agent query --format jsonl --top 10 "agents and memory"     # one JSON per chunk
./agent query --source ghost --after 2026-01-01 "burnout"
./agent query --source postiz --platform linkedin "harness"
./agent query --recency-halflife-days 90 "what changed lately"
./agent query --hybrid=false --format jsonl "vector only"
```

`score` is RRF in hybrid mode and cosine with `--hybrid=false`; `distance`,
`bm25`, and `cosine` show the path-native numbers.

## Ask (grounded loop)

```bash
./agent ask "What does Eli say about harnesses and vendor lock-in?"
./agent ask --format json --timeout 120s "What did he build at the hackathon?"
./agent ask "What is the capital of France?"     # -> "Not in my published writing"
```

Every claim must cite a retrieved `chunk_id` or that excerpt's `source_url`
(the small chat model often copies the URL but drops the 64-hex id; both
resolve to the same citation). A reply without a resolvable citation, or with
a `chunk_id` the loop never retrieved, is replaced by the refusal string.
`tool_iterations` counts tool-executing rounds (max 3 by default).
On this CPU host an answer takes roughly 10-25 seconds.

## Dev server

```bash
./agent serve                                  # 127.0.0.1:8080 (interactive)
./agent serve --port 18080                     # the evaluation port
./agent serve --host 0.0.0.0 --allow-public    # every interface; trusted network only
```

Ports are split on purpose: **8080** is yours for interactive development, and
**18080** is reserved for the evaluation suites (`evals/` and `tests-ai/`),
which target it by default. A long eval run therefore never collides with the
server you keep running for your own work.

```bash
curl -sS -X POST http://127.0.0.1:8080/v1/retrieve \
  -H 'Content-Type: application/json' \
  -d '{"query":"harness vendor lock-in","top_n":3,"source":"ghost"}'

curl -sS -X POST http://127.0.0.1:8080/v1/ask \
  -H 'Content-Type: application/json' \
  -d '{"question":"What does Eli say about harnesses?"}'
```

The dev server is token-less and loopback-only by default. A non-loopback
`--host` (`0.0.0.0` included) is a startup error unless `--allow-public` is
passed, and then it logs a warning: reach it from a container bridge or a
trusted LAN, never from an untrusted network.

## Testing the agent

The consolidated reference - ports, commands, configs, and report locations -
is [End-to-end testing](testing.md). Two tools, two questions, no third
harness:

| Tool | Location | Question it answers |
|---|---|---|
| **Ragas** | `evals/` | Do the agent's answers stay faithful to the cited excerpts, state the right facts, and refuse what it cannot know? |
| **promptfoo** | `tests-ai/` | Does the agent behave under adversarial input (bias, PII, hijacking, hallucination, fabricated citations)? |

**Run status (2026-11-05):** both suites are wired and smoke-tested, but
neither has a captured full run yet — `evals/output/` holds single-row smoke
runs, and promptfoo's local-model smoke report is in `tests-ai/reports/smoke/`
(gitignored). Capturing both at full size is a Phase 3 task (`../PLAN.md`).

Retrieval quality is not graded on its own. There is no retrieval-eval runner
and no query fixture: the Ragas answer metrics grade end-to-end behaviour,
which is what a caller actually experiences. To inspect ranking by hand:

```bash
./agent query --format jsonl --top 5 "harness vendor lock-in"
```

### Ragas (answer quality)

`evals/` grades answers with an LLM judge. It has its own endpoint variables
and is not part of the Go build:

```bash
cd evals
uv sync
uv run python test_llm.py     # endpoint check first; exit 0 = usable
uv run python evaluate.py --kind answers --limit 3   # cheap smoke test
uv run python evaluate.py     # both datasets -> output/
```

`EVAL_BASE_URL` / `EVAL_MODEL` / `EVAL_API_KEY` are in `example.env`; the
harness does not read `.env` itself (use `uv run --env-file ../.env …`).
Generated files are gitignored. The `http` transport targets the eval port
18080 by default; start the target first with `./agent serve --port 18080`.
See [ragas-eval.md](/ragas-eval.md).

### promptfoo (adversarial behaviour)

The red-team suite targets `POST /v1/ask` over loopback and is self-contained
under `tests-ai/` (pinned promptfoo dev dependency, configs, scripts, reports).
Attack generation and grading use the same local judge endpoint as the Ragas
harness (`EVAL_*` defaults, `http://localhost:8000/v1`), so no OpenAI key is
needed:

```bash
# terminal 1 - the eval port, so 8080 stays free
./agent serve --port 18080

# terminal 2
cd tests-ai
npm ci
npm run redteam:smoke         # one plugin, fully local; writes reports/smoke/
npm run redteam               # full scan; writes reports/results.{json,html}
npm run redteam:report        # browser UI on http://localhost:15500
```

Open `reports/results.html` directly or browse the local promptfoo database
with the report UI. The scripts keep promptfoo's database, cache, and logs in
`tests-ai/.promptfoo/` and disable sharing, telemetry, and the interactive
work-email prompt. A few plugins and strategies (`harmful:*`, bias,
`hijacking`, `politics`, `bola`/`bfla`/`rbac`, `jailbreak:meta`) are
remote-only in promptfoo; the full scan sends those to promptfoo-hosted
inference, while the smoke run disables remote generation and stays fully
local. Generated attacks (`redteam.yaml`) and reports are gitignored because
they can contain offensive text. The suite covers bias, harmful content, PII,
excessive agency, hijacking, hallucination, and fabricated RAG citations, plus
`basic` and `jailbreak:meta` strategies. See [red-team.md](/red-team.md) and
[`tests-ai/README.md`](../tests-ai/README.md) for plugin details, the
response-transform caveat, and why a suspected finding needs a re-run on this
non-deterministic model.

## Not yet in this runbook (Phase 3)

This runbook covers the **Go-host path**, which is complete end to end. All
of the following is Phase 3 work:

* the container path — `Dockerfile`, `compose.yaml`, and
  `docker compose run --rm agent agent ingest`;
* `agent ingest --watch` plus the launchd/cron schedule;
* `agent backup` (`VACUUM INTO`);
* `scripts/ingest-all.sh` — **currently broken**, it still execs the removed
  `go run ./cmd/ingest`.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `no index at ...; run agent ingest and agent embed first` | The index is missing or empty. Run ingest, then embed. |
| `index has no embeddings` | `./agent embed` has not run or wrote no rows; run it, then check `agent stats`. |
| `index vectors are stamped X, but EMBED_MODEL=Y expects Z` | The index was embedded by another model/text template. Run `./agent embed` (the tag makes every chunk pending) or point `EMBED_MODEL` at the model that built it. |
| `--host ... is not loopback` | The dev server refuses to bind a public interface by design. Use `127.0.0.1` (or `--host 0.0.0.0 --allow-public` on a trusted network). |
| `llm: embeddings returned ...` / `chat returned 503` | The model host is down or the model is not loaded. Check `/readyz` and `curl $LOCALAI_BASE_URL/models`. |
| Answers are refusals for answerable questions | Model non-determinism on a small chat model; retry once, then inspect `./agent query` for the same wording. If retrieval misses, improve the query or the index. |
| `agent query` says `provider is not enabled`-style config errors | `sources.yaml` scope: run `./agent sources validate`. |

Logs go to stderr. Use `--log-level debug` or `AGENT_LOG_LEVEL=debug`; the
HTTP server logs one line per request on the `http` module logger, and at debug
level the `agent` and `db` modules trace the whole RAG run - the chat request
messages, the model's reply, and every retrieval hit - without logging prompts
or excerpts at `info` or above. See [logging.md](/logging.md).
