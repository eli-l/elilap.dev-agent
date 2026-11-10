---
type: Guide
title: Runbook - build, ingest, embed, query, ask, serve
description: The operator path from a clean checkout to grounded answers - building the binary, rebuilding the index, running hybrid queries and the agent loop, the HTTP server (A2A card + message/send, bearer tokens, /v1/* paths), the two end-to-end test suites (Ragas and promptfoo), and the failures you are likely to hit.
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
./agent ingest                 # Ghost + Postiz + website, incremental by default
./agent ingest --full          # re-fetch and re-chunk everything
./agent ingest --source website   # only the HTML site pages
./agent embed --progress       # vectorize pending chunks; resumable
./agent stats                  # chunks / fts rows / vec rows / embedded / sources
```

The `website` source indexes `https://elilap.dev` as HTML pages (services,
case studies, AI/project pages) while the Ghost source keeps owning
`read.elilap.dev` posts. Its extraction is configuration: when the site's
layout changes, re-derive it with

```bash
./agent sources inspect --source website --url https://elilap.dev/services
```

and update `options.strategy`/`options.selectors` (or the `elilap` adapter in
`internal/sources/web`) if the suggestion changed. See
[HTML website provider and per-site extraction strategies](/web.md).

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

## Server (A2A + dev JSON)

```bash
./agent serve                          # 127.0.0.1:8080, token-less, loopback only
./agent serve --addr 127.0.0.1:18080 --db ./index.db   # the evaluation port
./agent tokens add --id alice          # issue a token; prints alice:<token> once
AGENT_A2A_PUBLIC_URL=https://agent.example \
  ./agent serve --bind-public --addr 0.0.0.0:8080   # requires a token + URL
```

The ports are split on purpose: **8080** is yours for interactive work, and
**18080** is reserved for the evaluation suites (`evals/` and `tests-ai/`),
which target it by default.

Without a configured token the loopback server is the token-less dev surface.
Once a token exists (env or `./a2a-tokens.txt`) every `/a2a` and `/v1/*`
request needs `Authorization: Bearer <token>`. `--bind-public` is the only way
to bind a non-loopback address and it refuses to start without a token and an
explicit `AGENT_A2A_PUBLIC_URL` (the card never derives its URL from an
untrusted request `Host`).

```bash
# Agent card (public, no token): the discovery document a peer reads first.
curl -sS http://127.0.0.1:8080/.well-known/agent-card.json | jq .

# A2A message/send: one grounded answer as an A2A Message with citations.
curl -sS -X POST http://127.0.0.1:8080/a2a \
  -H "Authorization: Bearer $A2A_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{
        "kind":"message","role":"user","messageId":"m-1",
        "parts":[{"kind":"text","text":"What does Eli say about harnesses?"}]}}}'

# The plain JSON paths underneath it (same auth).
curl -sS -X POST http://127.0.0.1:8080/v1/retrieve \
  -H "Authorization: Bearer $A2A_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"harness vendor lock-in","top_n":3,"source":"ghost"}'

curl -sS -X POST http://127.0.0.1:8080/v1/ask \
  -H "Authorization: Bearer $A2A_TOKEN" -H 'Content-Type: application/json' \
  -d '{"question":"What does Eli say about harnesses?"}'
```

Rate limit: 30 model-touching requests per minute per token (`--rate-limit`,
0 disables; a negative value is a startup error); this covers `/a2a`,
`/v1/ask`, and `/v1/retrieve`. Failed bearer-auth attempts have a separate
`--auth-limit` budget (default 120/min per address, 0 disables). Over-limit is
HTTP 429 with `Retry-After`.
Each task is bounded by the loop budget (3 tool iterations, 4,000 excerpt
tokens, 90s wall clock) and a question is capped at 8 KiB. Tokens live in a `0600` file that is re-read on
change, so add/revoke needs no restart:

```bash
./agent tokens add --id bob     # hand the printed value to the peer
./agent tokens list             # ids only, never values
./agent tokens revoke --id bob
```

The audit stream is one `audit` module line per task: token id, question
fingerprint (SHA-256 + byte length), outcome, iterations, citations, elapsed.
It never carries the question text, the answer, prompt, or excerpts. Full wire
contract: [a2a.md](/a2a.md).

## Testing the agent

Two tools, two questions, no third harness:

| Tool | Location | Question it answers |
|---|---|---|
| **Ragas** | `evals/` | Do the agent's answers stay faithful to the cited excerpts, state the right facts, and refuse what it cannot know? |
| **promptfoo** | `tests-ai/` | Does the agent behave under adversarial input (bias, PII, hijacking, hallucination, fabricated citations)? |

**Run status (2026-11-05):** both suites are wired and smoke-tested, but
neither has a captured full run yet — `evals/output/` holds single-row smoke
runs, and promptfoo has no report. Capturing both at full size is a Phase 3
task (`../PLAN.md`).

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
Generated files are gitignored. See [ragas-eval.md](/ragas-eval.md).

### promptfoo (adversarial behaviour)

The red-team suite targets `POST /v1/ask` over loopback. promptfoo is pinned
(`0.125.1`) as a local npm dependency, so setup is `npm ci` and every run goes
through `tests-ai/run-promptfoo.sh` (database and cache in
`tests-ai/.promptfoo/`, sharing and telemetry off, non-interactive):

```bash
./agent serve --addr 127.0.0.1:18080         # terminal 1, eval target
(cd tests-ai && npm ci)                      # once
(cd tests-ai && npm run redteam:smoke)       # one plugin, fully local
(cd tests-ai && npm run redteam)             # generate + evaluate the full scan
(cd tests-ai && npm run redteam:report)      # browser report from the local db
```

Generation and grading use the local judge: `redteam.provider` in the config
mirrors `EVAL_BASE_URL` / `EVAL_MODEL` / `EVAL_API_KEY`, so no OpenAI key is
needed. promptfoo still marks some plugins and strategies (`harmful:*`, bias,
`hijacking`, `politics`, `bola`/`bfla`/`rbac`, `jailbreak:meta`) as
remote-only; the full scan sends those to promptfoo-hosted inference, while the
smoke run disables remote generation. Reports land in `tests-ai/reports/` and
are gitignored.

The loopback server above is token-less; if you configured an A2A token, add
`Authorization: Bearer <token>` to the target headers in
`tests-ai/promptfooconfig.yaml` or run a token-less loopback server for the
suite.
Generated tests land in `tests-ai/redteam.yaml` (gitignored). The suite covers
bias, harmful content, PII, excessive agency, hijacking, hallucination, and
fabricated RAG citations, plus `basic` and `jailbreak:meta` strategies. See
[red-team.md](/red-team.md) and [`tests-ai/README.md`](../tests-ai/README.md)
for plugin details, the response-transform caveat, and why a suspected finding
needs a re-run on this non-deterministic model.

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
| `--addr ... is not loopback` | Use `127.0.0.1:<port>`, or `--bind-public` with a configured token. |
| `--bind-public requires an A2A token` | Issue one with `./agent tokens add --id <id>`; the server will not expose an unauthenticated endpoint. |
| `--bind-public requires AGENT_A2A_PUBLIC_URL` | Set the absolute `https://…` URL peers should use; the card will not trust a request `Host` header. |
| `AGENT_A2A_PUBLIC_URL must be an absolute http(s) URL` | Use a full URL with a host (a path prefix is fine); userinfo and relative values are rejected. |
| `401 unauthorized` from `/a2a` or `/v1/*` | Send `Authorization: Bearer <token>`; confirm the id is listed by `agent tokens list`. |
| `auth: token file ... must not be group/world-accessible` | `chmod 600` the token file; `agent tokens add` creates it correctly. |
| `429 rate limit exceeded` | Wait for `Retry-After`; raise `--rate-limit` only if the model host can take it. |
| `llm: embeddings returned ...` / `chat returned 503` | The model host is down or the model is not loaded. Check `/readyz` and `curl $LOCALAI_BASE_URL/models`. |
| Answers are refusals for answerable questions | Model non-determinism on a small chat model; retry once, then inspect `./agent query` for the same wording. If retrieval misses, improve the query or the index. |
| `agent query` says `provider is not enabled`-style config errors | `sources.yaml` scope: run `./agent sources validate`. |

Logs go to stderr. Use `--log-level debug` or `AGENT_LOG_LEVEL=debug`; the
HTTP server logs one line per request on the `http` module logger, and at debug
level the `agent` and `db` modules trace the whole RAG run - the chat request
messages, the model's reply, and every retrieval hit - without logging prompts
or excerpts at `info` or above. See [logging.md](/logging.md).
