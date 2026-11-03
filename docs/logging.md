---
type: Reference
title: Logging configuration and module loggers
description: The zerolog setup behind the agent binary, the five module sub-loggers (main, http, sources, db, agent), the global --log-level/--log-format flags and AGENT_LOG_* environment variables, and what may only reach a log line at debug level.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-03T15:45:00Z
tags: [logging, zerolog, cli, config, observability]
sources:
  - internal/logging/logging.go
  - internal/logging/http.go
  - internal/agentloop/agentloop.go
  - internal/retrieval/retrieval.go
  - internal/llm/chat.go
  - cmd/agent/main.go
---

# Logging configuration and module loggers

The binary logs through **zerolog** (`github.com/rs/zerolog`, vendored). All
logging configuration and every logger lives in `internal/logging`; no other
package imports zerolog directly except through that seam.

## One root, five module loggers

`logging.Setup` builds a single root logger and derives one child per module.
Each child stamps a `module` field, so one level knob and one writer configure
the whole process while log lines stay attributable:

| Module | Value | Logger | Owns |
|---|---|---|---|
| Main | `main` | `logging.Main()` | Application lifecycle: startup, dispatch, exit code. |
| HTTP | `http` | `logging.HTTP()` | HTTP server requests via `logging.RequestMiddleware`. |
| Sources | `sources` | `logging.Sources()` | Upstream fetchers (Ghost, Postiz): pages listed, items fetched, request outcomes. |
| DB | `db` | `logging.DB()` | Database: open, migrations applied, chunk and ledger writes, and the retrieval read path. |
| Agent | `agent` | `logging.Agent()` | The grounded loop: the RAG request sent to the chat model, the messages it contains, the tool calls and results, and the reply. |

Before `Setup` runs, every module logger is a no-op, so library packages and
tests stay silent by default.

## Configuration

Global flags, accepted anywhere in the command line (they are stripped before a
subcommand parses its own `FlagSet`):

```bash
agent --log-level debug --log-format json sources validate
AGENT_LOG_LEVEL=debug AGENT_LOG_FORMAT=json agent sources validate
```

| Setting | Flag | Env | Values | Default |
|---|---|---|---|---|
| Level | `--log-level` | `AGENT_LOG_LEVEL` | `trace`, `debug`, `info`, `warn`, `error`, `fatal`, `panic`, `disabled` | `info` |
| Format | `--log-format` | `AGENT_LOG_FORMAT` | `text`, `json` | `text` |

The flag wins over the environment variable. An unknown level or format is a
usage error (exit 2) rather than a silent verbosity change. `text` is colored
console output when stderr is a terminal and plain otherwise; `json` is one
record per line, for containers and collectors.

Values are decoded by `envconfig` into `logging.Config`; the binary loads
`.env` at startup (via `envcfg.LoadDotEnv`), and a variable already present in
the real environment wins over the file. An unset *or empty* variable means
the default (`info`/`text`). The one inventory of every variable, including
these two, is `example.env`; see [Environment configuration and .env
loading](environment.md) for the load order and the rule for adding new
variables.

**Streams:** logs always go to stderr. stdout carries only command output, so
`agent ... | jq` is never polluted.

**Command output follows the same format switch.** `logging.Format()` reports
the resolved `text`/`json` value, and commands render their stdout summaries
through it: `agent ingest` prints one text line per source, or one JSON object
per source (JSONL) when the format is `json`, so a collector parses the run
summary without a second flag or a second code path. Human-readable data
output (`agent stats`, `agent sources list`'s YAML) stays as-is; the
`logs to stderr, output to stdout` split is what keeps the two separable.

**Defaults stay quiet:** `info` is reserved for real milestones (database
ready, migrations applied, items fetched). Per-page and per-config lines are
`debug`, so an unflagged CLI run prints only what it printed before logging
existed.

## HTTP request lines

`logging.RequestMiddleware(route, next)` wraps a handler and emits exactly one
line per request with the route label, method, path, status, response bytes,
and duration. Status drives the level: `info` for 2xx/3xx, `warn` for 4xx,
`error` for 5xx. It also implements `Unwrap()` so `http.ResponseController`
keeps working through the wrapper.

Server routes are labelled by path template; the **query string is never
logged**, because peer task text is data, not diagnostics.

## Debug traces: the RAG request and the model's reply

`debug` turns the two halves of a RAG run into a readable trace. Agent runs
(`agent ask`, `POST /v1/ask`) log on the `agent` module:

| Message | Meaning |
|---|---|
| `rag: run starting` | Question, chat model, iteration/excerpt/token/wall-clock budgets. |
| `rag: chat request` | One header per model call: model, `max_tokens`, whether tools were offered, message count. |
| `rag: message` | One line per message the model sees, in order: `role`, `content` verbatim, and `tool_calls` as `name(args)`. |
| `rag: chat response` | One line per reply: the model id the server reports, `finish_reason`, content, tool calls, token usage, latency. |
| `rag: tool call` / `rag: tool hits` | The tool arguments, the hit and kept counts, and the excerpt budget left. |
| `rag: tool result` | The exact JSON of excerpts handed to the model, i.e. the retrieval-to-prompt boundary. |
| `rag: answer` | The enforced outcome: `refused`, iterations, retrieved chunks, resolved citations, elapsed time, final answer. |

Retrieval runs (`agent query`, `POST /v1/retrieve`, and the loop's tool) log on
the `db` module: `retrieval: query` (text, resolved filters, embeddings model,
candidate depth), `retrieval: candidates` (the generated FTS5 query and how
many rows each path contributed), `retrieval: hit` (rank, `chunk_id`, title,
merged `score`, path-native `distance`/`bm25`/`cosine`, and a 200-character
preview), and `retrieval: done` (result count, duration).

```bash
agent --log-level debug ask "what is the harness take?"
agent --log-level debug query --top 5 "vendor lock-in"
```

This is the supported way to answer "why this answer?" and "why this chunk?":
the `rag:` lines show the prompt and the reply, and the `retrieval:` lines show
the ranking behind each excerpt. Nothing on these paths logs at `info`, so an
unflagged run is as quiet as before.

## What never reaches a log line

Per `AGENTS.md`: credential values (the config layer logs environment
*variable names* at most) and upstream document bodies never reach a log line
at `info` or above. Upstream fetchers log item ids, slugs, page numbers, byte
counts, and statuses only, and the HTTP request middleware never logs the query
string.

The single exception is **debug-level payload logging in `internal/agentloop`
and `internal/retrieval`**: prompt text, tool results with excerpts, model
replies, retrieval queries, and excerpt previews. It exists because retrieval
and grounding cannot be debugged end to end without seeing what was asked and
what came back, and it is confined to `debug` so a normal or `info` run still
carries no bodies. Treat those lines as local-development output: debug logs
then contain the question (including peer task text on `/v1/ask`) and published
excerpts, so do not enable them on a shared collector. The Phase 4 A2A audit
log is a separate, always-on record and still never contains chunk bodies,
prompts, or user answers (`PLAN.md` security posture).
