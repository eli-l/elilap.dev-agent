---
type: Reference
title: Logging configuration and module loggers
description: The zerolog setup behind the agent binary, the four module sub-loggers (main, http, sources, db), the global --log-level/--log-format flags and AGENT_LOG_* environment variables, and what must never reach a log line.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-10-27T21:35:00Z
tags: [logging, zerolog, cli, config, observability]
sources:
  - internal/logging/logging.go
  - internal/logging/http.go
  - cmd/agent/main.go
---

# Logging configuration and module loggers

The binary logs through **zerolog** (`github.com/rs/zerolog`, vendored). All
logging configuration and every logger lives in `internal/logging`; no other
package imports zerolog directly except through that seam.

## One root, four module loggers

`logging.Setup` builds a single root logger and derives one child per module.
Each child stamps a `module` field, so one level knob and one writer configure
the whole process while log lines stay attributable:

| Module | Value | Logger | Owns |
|---|---|---|---|
| Main | `main` | `logging.Main()` | Application lifecycle: startup, dispatch, exit code. |
| HTTP | `http` | `logging.HTTP()` | HTTP server requests via `logging.RequestMiddleware`. |
| Sources | `sources` | `logging.Sources()` | Upstream fetchers (Ghost, Postiz): pages listed, items fetched, request outcomes. |
| DB | `db` | `logging.DB()` | Database: open, migrations applied, chunk and ledger writes. |

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

## What never reaches a log line

Per `AGENTS.md`: credential values (the config layer logs environment
*variable names* at most), document bodies, chunk bodies, prompts, and user
answers. Upstream fetchers log item ids, slugs, page numbers, byte counts, and
statuses only.
