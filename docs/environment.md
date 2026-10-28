---
type: Reference
title: Environment configuration and .env loading
description: The environment layer - godotenv loading .env at startup, the envconfig structs that decode every static setting, the full variable inventory with defaults, the example.env inventory rule, and the dynamic credential lookup sources.yaml relies on.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-10-28T13:04:26Z
tags: [config, environment, envconfig, godotenv, secrets, ops]
sources:
  - internal/envcfg/envcfg.go
  - internal/logging/logging.go
  - example.env
  - sources.yaml
  - cmd/agent/main.go
---

# Environment configuration and `.env` loading

Runtime configuration enters the process through two inputs with one
precedence: the **real environment wins over `.env`**, and a struct default
applies when a variable is unset *or empty*. That makes the same binary work
from a developer shell, cron, launchd, and a container without conditionals.

```text
main()
  └─ envcfg.LoadDotEnv()          godotenv: .env → process env (never overrides)
       ├─ logging.ConfigFromEnv() envconfig: AGENT_LOG_*
       └─ envcfg.Load()           envconfig: the rest, defaults applied
```

`LoadDotEnv` runs before anything decodes a variable. It is called from
`main`, not from `run`, so `go test ./...` never picks up a developer's
`.env`; tests set the variables they need with `t.Setenv`.

## The structs

Every static setting is a field on an **envconfig** struct. Nothing in the
codebase reads variables with ad-hoc `os.Getenv` chains.

| Struct | Package | Prefix | Variables |
|---|---|---|---|
| `envcfg.Config` | `internal/envcfg` | mixed by design | `AGENT_DB_PATH`, `LOCALAI_*`, `EMBED_MODEL`, `CHAT_MODEL` |
| `logging.Config` | `internal/logging` | `AGENT_LOG` | `AGENT_LOG_LEVEL`, `AGENT_LOG_FORMAT` |

The `envconfig` struct tag is always the **full variable name**, because the
surface mixes prefixes: `AGENT_*` for knobs this service owns and
upstream-style names (`LOCALAI_*`, `EMBED_MODEL`, `CHAT_MODEL`, `GHOST_*`,
`POSTIZ_*`) for everything else. Exported `Env*` constants are the source of
truth for the names and a test pins the struct tags to them.

`envconfig.Process` cannot distinguish an unset variable from one set to the
empty string, so each loader normalizes afterwards with `envcfg.DefaultOr`:
an empty value means the default. Defaults live in exported `Default*`
constants, not in struct tags, so there is exactly one definition.

## Variable inventory

| Variable | Owner | Default | Notes |
|---|---|---|---|
| `AGENT_LOG_LEVEL` | `internal/logging` | `info` | `trace`…`disabled`; `--log-level` wins over it |
| `AGENT_LOG_FORMAT` | `internal/logging` | `text` | `text` or `json`; `--log-format` wins over it |
| `AGENT_DB_PATH` | `internal/envcfg` | `./index.db` | `/data/index.db` in the container (PLAN.md Decision 11) |
| `LOCALAI_BASE_URL` | `internal/envcfg` | `http://192.168.78.243:8080/v1` | Any OpenAI-compatible server |
| `LOCALAI_API_KEY` | `internal/envcfg` | empty | Optional bearer token |
| `EMBED_MODEL` | `internal/envcfg` | `embeddinggemma-300m` | Must produce 768 dims |
| `CHAT_MODEL` | `internal/envcfg` | `spark-x2.5-4b` | Reasoning model, used by the loop |
| `GHOST_API_URL` | `sources.yaml` ref | — | Credential reference, value required |
| `GHOST_API_KEY` | `sources.yaml` ref | — | Secret |
| `POSTIZ_API_URL` | `sources.yaml` ref | — | Credential reference, value required |
| `POSTIZ_API_KEY` | `sources.yaml` ref | — | Secret |

Credentials are the one **dynamic** case: `sources.yaml` declares
`{ env: VAR }` references, so the names are configuration data, not Go
fields. `envcfg.Lookup` is the single accessor for them (used by
`internal/config.MissingEnv` and the Ghost client); everything static goes
through the structs above. See [Import configuration and provider
contract](sources-config.md) and [Logging configuration and module
loggers](logging.md).

## The `example.env` rule

`example.env` at the repo root is the committed **inventory** of every
environment variable the agent reads. `.env` is gitignored and is created by
copying the template:

```bash
cp example.env .env    # then fill in the credentials
```

**Every new environment variable must be added to `example.env` in the same
change that introduces it**, with an empty value when it is a secret and its
default value otherwise. A variable that is not in `example.env` is not
considered added. The same entry is required in the table above.

`cmd/agent` enforces this: `TestExampleEnvIsComplete` reads `example.env`,
derives the inventory from the `envconfig` struct tags in `envcfg.Config` and
`logging.Config` plus the credential names `sources.yaml` references, and
fails on a missing *or* stale entry. It also checks that defaulted settings
carry their `Default*` value and that every credential value is blank, so a
real secret cannot be committed in the template.

Because empty means "use the default", a `.env` copied verbatim from
`example.env` boots; only the four credential values need filling in. For
the credentials, blank is treated as missing: `agent sources validate` warns
and `--strict` exits 1 until they are filled, matching the runtime credential
resolver.

## Adding a variable

1. Add an exported `Env*` constant and (if it has one) a `Default*` constant
   in `internal/envcfg` or `internal/logging`.
2. Add the struct field with the full name in its `envconfig` tag.
3. Normalize with `envcfg.DefaultOr` in the loader so `VAR=` behaves as unset.
4. Add the entry to `example.env` with the default or an empty secret value.
5. Add the row to the table above; update `docs/log.md`.

## What `.env` is not

* Not committed, not shared, not backed up — it holds credentials.
* Not read in tests; tests use `t.Setenv`.
* Not required: a container passes variables via `compose.yaml`, cron and
  launchd via their own environment. A missing `.env` is not an error.
* Never echoed or logged: startup logs variable *names* and whether a secret
  is set, never values (`db_path` and `localai_base_url` are the only values
  that reach a debug line).
