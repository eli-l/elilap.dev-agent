---
type: Reference
title: Environment configuration and .env loading
description: The environment layer - godotenv loading .env at startup, the envconfig structs that decode every static setting, the full variable inventory with defaults, the example.env inventory rule, and the dynamic credential lookup sources.yaml relies on.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-11T08:50:00Z
tags: [config, environment, envconfig, godotenv, secrets, ops]
sources:
  - internal/envcfg/envcfg.go
  - internal/logging/logging.go
  - example.env
  - sources.yaml
  - cmd/agent/main.go
  - evals/README.md
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
| `envcfg.Config` | `internal/envcfg` | mixed by design | `AGENT_DB_PATH`, `LOCALAI_*`, `EMBED_MODEL`, `CHAT_MODEL`, `AGENT_A2A_*` |
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
| `AGENT_DB_PATH` | `internal/envcfg` | `./data/index.db` | `/data/index.db` in the container, where `./data` is bind-mounted at `/data` (PLAN.md Decision 11) |
| `LOCALAI_BASE_URL` | `internal/envcfg` | `http://192.168.78.243:8080/v1` | Any OpenAI-compatible server |
| `LOCALAI_API_KEY` | `internal/envcfg` | empty | Optional bearer token |
| `EMBED_MODEL` | `internal/envcfg` | `embeddinggemma-300m` | Must produce 768 dims |
| `CHAT_MODEL` | `internal/envcfg` | `Spark-X2.6-4B-q8_0` | Reasoning model, used by the loop; the host's 8K-context 4B q8 baseline |
| `AGENT_A2A_TOKENS` | `internal/envcfg` | empty | Comma-separated `id:token` bearer list for the A2A surface; empty and loopback = token-less dev |
| `AGENT_A2A_TOKENS_FILE` | `internal/envcfg` | `./data/a2a-tokens.txt` | `0600` token file, re-read on change (rotation without restart); gitignored, missing = no tokens |
| `AGENT_A2A_PUBLIC_URL` | `internal/envcfg` | empty | Absolute service root advertised in the agent card; required with `--bind-public`, otherwise a loopback bind derives `http://<addr>` |
| `EVAL_BASE_URL` | `evals/` (Python) | `http://localhost:8000/v1` | Judge endpoint for the Ragas harness; the promptfoo `redteam.provider` mirrors it. Independent of `LOCALAI_*` |
| `EVAL_MODEL` | `evals/` (Python) | `mtplx-qwen36-35b-a3b-optimized-speed` | Model the harness scores; promptfoo generates (local-capable plugins) and grades with it |
| `EVAL_API_KEY` | `evals/` (Python) | empty → `local` | Secret; local servers ignore its value. promptfoo's config uses the literal `local` |
| `GHOST_API_URL` | `sources.yaml` ref | — | Credential reference, value required |
| `GHOST_API_KEY` | `sources.yaml` ref | — | Secret |
| `POSTIZ_API_URL` | `sources.yaml` ref | — | Credential reference, value required |
| `POSTIZ_API_KEY` | `sources.yaml` ref | — | Secret |

Runtime files (the SQLite index and its WAL/SHM siblings, `agent backup`
snapshots, and the A2A token file) live under `./data/` on the host and
`/data` in the container. `.gitkeep` keeps the directory; its contents are
gitignored and excluded from the image build context, and compose bind-mounts
`./data:/data`.

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

**The A2A variables are the token store.** A token is a secret: it has an
empty value in `example.env` and is never committed. `AGENT_A2A_TOKENS` is a
fixed list read at startup; `AGENT_A2A_TOKENS_FILE` is re-read when its mtime
or size changes, so `agent tokens add|revoke` rotates credentials without a
restart. The file must be `0600`, and a malformed or loose-permission file is
a startup error rather than a silent downgrade to token-less serving. Once a
token has been configured the store stays secured: deleting the file or
revoking the last entry yields `401`, never an unauthenticated server. With no
tokens and a loopback bind the server stays token-less for development; a
non-loopback bind (`--bind-public`) requires at least one token and an
explicit `AGENT_A2A_PUBLIC_URL` (the card never trusts a request `Host`). See
[a2a.md](a2a.md) for issuance, rotation, and the audit record.

The `EVAL_*` rows belong to the Python harness in `evals/`, not to a Go
`envconfig` struct. `TestExampleEnvIsComplete` pins them with an explicit
allowlist (`evalVars`), so they are checked for presence and default value but
not derived from Go code. The harness does not load `.env`; run it with
`uv run --env-file ../.env …` or export the variables. The promptfoo red-team
suite in `tests-ai/` has no variables of its own: its
`redteam.provider` block repeats the same endpoint, model, and `local` key, so
the two files move together. See
[RAG evaluation with Ragas](ragas-eval.md) and
[Red-team evaluation with promptfoo](red-team.md).

## Adding a variable

1. Add an exported `Env*` constant and (if it has one) a `Default*` constant
   in `internal/envcfg` or `internal/logging`.
2. Add the struct field with the full name in its `envconfig` tag.
3. Normalize with `envcfg.DefaultOr` in the loader so `VAR=` behaves as unset.
4. Add the entry to `example.env` with the default or an empty secret value.
5. Add the row to the table above; update `docs/log.md`.

For a non-Go consumer (today only `evals/`), also add the name and default
to `evalVars` in `cmd/agent/example_env_test.go`; a variable in `example.env`
that no allowlist entry or struct tag knows is a test failure.

## What `.env` is not

* Not committed, not shared, not backed up — it holds credentials.
* Not read in tests; tests use `t.Setenv`.
* Not required: a container passes variables via `compose.yaml`, cron and
  launchd via their own environment. A missing `.env` is not an error.
* Never echoed or logged: startup logs variable *names* and whether a secret
  is set, never values (`db_path` and `localai_base_url` are the only values
  that reach a debug line).
