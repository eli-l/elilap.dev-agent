---
type: Run
title: Container path on Docker Compose
description: Built and exercised the multi-stage image and compose service on 2026-11-11 - in-image stats, a hybrid query exercising vec0 KNN, a VACUUM INTO backup, a clean-data ingest + embed, and the token-gated A2A card and /v1/retrieve surface.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-11T08:52:00Z
tags: [validation, container, docker, compose, sqlite, vec, ingest, a2a]
run:
  type: container
  cmd: |
    docker compose build
    docker compose run --rm agent agent stats
    docker compose run --rm agent agent query --format jsonl --top 1 "OKF knowledge bundle"
    docker compose run --rm agent agent backup --out /data/container-test.db
    docker compose run --rm agent agent stats --db /data/container-test.db

    # clean-data proof against a temporary host directory
    mkdir -p data-test
    docker run --rm --env-file .env -e AGENT_DB_PATH=/data/index.db \
      -v "$PWD/data-test:/data" --tmpfs /tmp \
      elilap-dev-agent:local agent ingest --timeout 20m
    docker run --rm --env-file .env -e AGENT_DB_PATH=/data/index.db \
      -v "$PWD/data-test:/data" --tmpfs /tmp \
      elilap-dev-agent:local agent embed --timeout 20m
    docker run --rm --env-file .env -e AGENT_DB_PATH=/data/index.db \
      -v "$PWD/data-test:/data" --tmpfs /tmp \
      elilap-dev-agent:local agent stats
    docker run --rm --env-file .env -e AGENT_DB_PATH=/data/index.db \
      -v "$PWD/data-test:/data" --tmpfs /tmp \
      elilap-dev-agent:local agent query --format jsonl --top 1 "OKF knowledge bundle"

    # token-gated serve on the published port
    docker compose run --rm agent agent tokens add --id container-test
    docker compose up -d
    curl -sS http://127.0.0.1:8081/.well-known/agent-card.json
    curl -sS -X POST http://127.0.0.1:8081/v1/retrieve \
      -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
      -d '{"query":"OKF knowledge bundle","top_n":1}'
    docker compose down
output: |
  Image elilap-dev-agent:local Built

  db:        /data/index.db
  chunks:    949
  fts rows:  949
  vec rows:  949
  embedded:  949
  sources:   3
  last run:  2026-11-11T07:09:36Z

  {"chunk_id":"...","source_kind":"web_page","source_id":"elilap.dev/ai/okf-builder","source_url":"https://elilap.dev/ai/okf-builder","title":"Knowledge that survives the session.",...}

  db:     /data/index.db
  backup: /data/container-test.db
  bytes:  7831552
  db:        /data/container-test.db
  chunks:    949
  fts rows:  949
  vec rows:  949
  embedded:  949
  sources:   3

  {"source_id":"ghost","source_kind":"ghost_post","since":null,"listed":103,"indexed":103,"unchanged":0,"chunks_added":498,"chunks_updated":0,"removed":0,"dry_run":false}
  {"source_id":"postiz","source_kind":"postiz_post","since":null,"listed":286,"indexed":286,"unchanged":0,"chunks_added":305,"chunks_updated":0,"removed":0,"dry_run":false}
  {"source_id":"website","source_kind":"web_page","since":null,"listed":12,"indexed":12,"unchanged":0,"chunks_added":56,"chunks_updated":0,"removed":0,"dry_run":false}
  {"model":"embeddinggemma-300m","tag":"localai:embeddinggemma-300m/text-v1","full":false,"pending":859,"embedded":859,"remaining":0,"batches":27}
  db:        /data/index.db
  chunks:    859
  fts rows:  859
  vec rows:  859
  embedded:  859
  sources:   3
  {"chunk_id":"...","source_kind":"web_page","source_id":"elilap.dev/ai/okf-builder","source_url":"https://elilap.dev/ai/okf-builder","title":"Knowledge that survives the session.",...}

  NAME                       IMAGE                    SERVICE   STATUS         PORTS
  elilap-dev-agent-agent-1   elilap-dev-agent:local   agent     Up 3 seconds   127.0.0.1:8081->8080/tcp
  Eli Lap's published writing    http://127.0.0.1:8081/a2a    JSONRPC
  results 1
  top web_page Knowledge that survives the session.
  exit 0
---

# Result

The container path is implemented and exercised on this host:

- **Image**: multi-stage `Dockerfile` — `golang:1.27-bookworm` builder with
  CGO and `libsqlite3-dev` (sqlite-vec's cgo header needs `sqlite3.h`), then
  `debian:bookworm-slim` with only the binary, CA certificates, tzdata,
  `migrations/`, and `sources.yaml`. The binary is at `/usr/local/bin/agent`
  and the image CMD is the token-gated serve command.
- **Data mapping**: compose bind-mounts `./data` at `/data` and sets
  `AGENT_DB_PATH=/data/index.db` and
  `AGENT_A2A_TOKENS_FILE=/data/a2a-tokens.txt`. The existing 949-chunk host
  index was visible in the container unchanged.
- **vec0 KNN in-image**: `agent query` performed a real hybrid retrieval and
  returned the expected `web_page` hit, so the vector extension loads and the
  KNN path runs in the runtime image, not just in Go tests on the host.
- **Backup in-image**: `agent backup --out /data/container-test.db` wrote a
  7.8 MB self-contained snapshot; `agent stats --db` on it agreed with the
  source (949 rows in each table).
- **Clean-data ingest**: a temporary `data-test` bind mount produced a fresh
  index with `agent ingest` (103 Ghost items, 286 Postiz items, 12 web pages)
  and `agent embed` (859 chunks, 27 batches), then a retrieval hit. The
  temporary directory was removed afterwards. The fresh full listing is
  smaller than the host's 949-chunk index (whose ledger knows 126 Ghost
  items), so a later `agent ingest --full` on the host will reconcile that
  stale-item drift; this test deliberately did not touch the live index.
- **Serve**: a token created with `agent tokens add` let the compose service
  bind `0.0.0.0:8080` (published as `127.0.0.1:8081`), serve the agent card,
  and answer an authenticated `/v1/retrieve`. The token file was removed after
  the test.

Compose hardening: the root filesystem is read-only, `/tmp` is a tmpfs, all
capabilities are dropped, `no-new-privileges` is set, and the only writable
mount is `./data:/data`. The container runs as root so the bind mount works on
any host; on Linux, host editing may need a `chown` of `data/`.

The `agent` service command is `agent serve --bind-public --addr
0.0.0.0:8080`, so a fresh `docker compose up` requires a token and
`AGENT_A2A_PUBLIC_URL`. One-shot commands (`ingest`, `embed`, `stats`,
`backup`, `tokens`) run through `docker compose run --rm agent …` and do not
need the server bind.
