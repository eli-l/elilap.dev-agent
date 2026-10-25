---
type: Reference
title: Container deployment
description: How the agent is built and run as a container - the static image contract, the compose topology with an external model server, the SQLite volume mapping, and the deploy check that proves the whole story.
status: draft
generated:
  by: dave
  at: 2026-10-25T13:45:00Z
tags: [deployment, docker, compose, container, volume, phase-1]
---

# Container deployment

The agent ships as one static binary and one container image. The model
server is **external** ([`PLAN.md`](../PLAN.md) Decision 10): this repo never
runs one, so every deployment has exactly one moving part that this repo
owns - the agent.

## Image contract

| property | value | why |
|---|---|---|
| builder | `golang:1.27` | the Go floor from `go.mod`; the tag floats to the newest 1.27 patch |
| build | `CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags="-s -w" ./cmd/agent` | dependencies are vendored, so the build needs no network and no C toolchain |
| runtime | `gcr.io/distroless/static-debian12:nonroot` | no shell, no package manager, no source tree in the image |
| contents | the binary, the CA bundle, `sources.yaml`, an empty `/data` | nothing else is needed to run a command |
| user | uid 65532 (`nonroot`), owner of `/data` | the agent never runs as root, and it can create the index |
| entrypoint | `ENTRYPOINT ["/agent"]` | the subcommand is the argument: `docker run <image> doctor` |
| ports | none | this is a CLI, not a server - there is nothing to `EXPOSE` |
| healthcheck | none | the binary has no health mode, so none is faked |

`sources.yaml` ships in the image because every subcommand needs the static
import configuration and it holds no secret: credentials are `{ env: VAR }`
references, never values. Mount your own file over `/app/sources.yaml` to
change the providers.

If the SQLite driver ever turns out to need CGO, the runtime base has to
become glibc-based (`debian:bookworm-slim`) and the build has to set
`CGO_ENABLED=1`. That is a two-line change; the pure-Go driver is what
makes the distroless base possible today.

## Compose topology

`compose.yaml` defines the agent service and nothing else.

* `LOCALAI_BASE_URL` is **required**: `${LOCALAI_BASE_URL:?set LOCALAI_BASE_URL ...}`.
  Compose refuses to render the stack without it, which is the point - the
  usual default (`http://localhost:8080/v1`) resolves to the container
  itself inside a container, where nothing is listening. This trap is
  recorded in [`PLAN.md`](../PLAN.md) section 2.5 and in the
  [MVP bring-up review](/mvp-review.md) finding 5.
* Every credential is passed through as `${VAR:-}` - the file contains no
  secret, only variable names. A `.env` file is used for interpolation when
  the operator provides one, and is never committed.
* `AGENT_DB_PATH` is fixed to `/data/index.db` inside the container; the
  host default stays `./index.db`.
* The service has no healthcheck, because the agent is a CLI with no
  long-running mode. The default command is `doctor` - the one-shot preflight
  that proves the deployment works. Override it for real work:
  `docker compose run --rm agent ingest`.

## Volume contract

* `/data` is the named volume `agent-data`. `index.db`, `index.db-wal` and
  `index.db-shm` always share that one filesystem, which SQLite WAL
  requires - never split them across mounts.
* A bind mount (`-v "$PWD/data:/data"`) is supported when the file has to
  be host-visible, for inspection or backup. Bind mounts are slower on some
  platforms, so the named volume is the default.
* The index is derived data: delete the volume and re-run ingest and the
  whole thing is rebuilt from the upstream sources.

## The deploy check

`scripts/deploy-check.sh` proves the whole story on a host, one actionable
error per check:

```bash
./scripts/deploy-check.sh              # full check, including the image build
./scripts/deploy-check.sh --skip-build # re-check without rebuilding
```

The checks are, in order: the docker CLI, the daemon, the compose plugin,
`LOCALAI_BASE_URL` and its `/models` endpoint, the served model catalog
(`EMBED_MODEL` and `CHAT_MODEL` must be in it), the image build, `agent
doctor` inside the image, and `docker compose config`.

## Verified

On 2026-10-25 the image built, `agent doctor` passed inside it, and
`docker compose config` rendered:

```
$ docker run --rm -e LOCALAI_BASE_URL=http://<model-server>:8080/v1 elilap-dev-agent:local doctor
doctor: checking the runtime requirements for this machine
ok   config: sources.yaml (version 1, 2 source(s), 2 enabled)
ok   providers: 2 provider type(s) validated (ghost, postiz)
warn credentials: 4 environment variable(s) not set: GHOST_API_URL, GHOST_API_KEY, POSTIZ_API_URL, POSTIZ_API_KEY
ok   model server: http://<model-server>:8080/v1 is reachable (3 model(s))
ok   embed model: embeddinggemma-300m is served
ok   chat model: spark-x2.5-4b is served
ok   index: /data/index.db will be created here, directory is writable

doctor: 6 ok, 1 warning(s), 0 failure(s)
```

The credential warning is expected on a machine without the upstream API
keys; it names the variables, never their values. The same preflight in
container mode is the check that the model server is routable from inside
the container - an address that works on the host is not automatically
reachable from a container network.

## Known nuance

Compose passes an unset credential through as an empty string, and the
doctor treats a variable that is *present* as set. Inside the container the
credential check therefore only reports variables that are exported (or
present in `.env`); an exported-but-empty variable reads as set. This is
a property of the two halves meeting here, not a secret leak: no value is
ever printed.

## What is deliberately absent

* **A model server.** Operating one is out of scope (`PLAN.md` non-goals).
  The fallback - adding a model-server service to this compose file - is
  documented in `PLAN.md` section 2.5 and must pin a versioned tag.
* **A healthcheck.** The binary has no health mode; a `HEALTHCHECK` that
  always passes is worse than none.
* **A secret in the repository.** `.env` is gitignored, and every value in
  `compose.yaml` is a variable reference.
