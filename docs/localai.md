---
type: Reference
title: Model server contract
description: What the agent requires of any OpenAI-compatible model server - embeddings, chat completions, dimension, readiness probe, and the reasoning-model token caveat - plus the non-normative reference environment it was verified against.
status: draft
generated:
  by: dave
  at: 2026-10-25T06:05:00Z
tags: [model-server, embeddings, chat, contract, phase-1]
---

# Model server contract

The model backend is an **external** service that this repo consumes over
HTTP. `PLAN.md` Decision 10 records the decision; this file states the
contract the agent may rely on, so that any OpenAI-compatible server can
replace the reference one by changing one base URL.

This is a contract, not a vendor list. Nothing here requires a particular
product, version, host, or address.

## Required endpoints

| endpoint | purpose | contract |
|---|---|---|
| `POST {base}/embeddings` | embeddings | OpenAI-compatible request/response; returns one vector per input |
| `POST {base}/chat/completions` | chat | OpenAI-compatible request/response; returns `choices[].message.content` |
| `GET {base}/models` | catalogue | lists the model ids the server serves |
| `GET {base root}/readyz` | liveness | **optional** - some servers expose it; the agent does not depend on it |

`{base}` is `LOCALAI_BASE_URL` and carries the `/v1` suffix
(for example `http://host:8080/v1`).

The agent checks the endpoint by **listing its models** (`GET {base}/models`)
rather than by a separate health endpoint: that single call proves the
server is reachable, that the base URL is right (a wrong base answers `404`),
and that the configured models exist. `agent doctor` runs exactly that
check and reports each part separately.

## Embeddings

* **Dimension is fixed at 768** in this project: every stored vector records
  its own dimension in `chunk_vectors.dim`, and the model contract fixes that
  dimension at 768. The connector must assert the returned vector length
  before writing, and **refuse to write** on a mismatch rather than silently
  truncate or pad.
* The default embedding model is `embeddinggemma-300m` (`EMBED_MODEL`). Any
  server-side model that returns 768-dimension vectors works.
* Batch input in one request when the server supports it; the corpus is
  small (≤1,500 chunks), so throughput is not the constraint — correctness
  is.

## Chat completions

* The default chat model is `spark-x2.5-4b` (`CHAT_MODEL`); a smaller model
  (`spark-x2.5-1.7b-q4` in the reference environment) is the documented
  fallback when latency matters more than quality.
* **Reasoning-model caveat.** Some chat models are *reasoning* models: the
  response carries a `reasoning` field next to `content`, and the token
  budget is consumed by reasoning first. At a small `max_tokens` the
  reasoning eats the budget and `content` comes back **empty**. The client
  must therefore:
  1. budget `max_tokens` for reasoning as well as the answer, and
  2. treat an empty `content` as an error worth reporting loudly — never as
     an empty answer to hand back to the user.

  This caveat is a property of reasoning models generally, not of one
  server build.

## Configuration

| variable | default | meaning |
|---|---|---|
| `LOCALAI_BASE_URL` | `http://localhost:8080/v1` | OpenAI-compatible base URL |
| `LOCALAI_API_KEY` | unset | sent as a bearer token when set; many self-hosted servers need none |
| `EMBED_MODEL` | `embeddinggemma-300m` | embedding model id |
| `CHAT_MODEL` | `spark-x2.5-4b` | chat model id |
| `AGENT_DB_PATH` | `./index.db` | SQLite index path |

The variable is named `LOCALAI_BASE_URL` for historical reasons; the
contract it points at is the OpenAI-compatible one, and any server that
implements it is equally valid.

## Reference environment (non-normative)

Everything below was observed on **one** environment. It is a record, not a
requirement — a different server, address, and model set are all valid.

* Reference host: Ubuntu 26.04.1 LTS, x86_64.
* Model server: a LocalAI instance on the LAN at `http://192.168.78.243:8080`
  (the reference build host is `192.168.78.242`); OpenAI-compatible base
  `http://192.168.78.243:8080/v1`.
* Version: `GET /version` → `v4.12.0 (15e37df9a468c798a5a511624f054057da4380a1)`.
* Liveness: `GET /readyz` → `200`.
* Loaded models: `embeddinggemma-300m`, `spark-x2.5-4b`,
  `spark-x2.5-1.7b-q4`. No API key required.
* Verified 2026-10-23: `POST /v1/embeddings` with `embeddinggemma-300m` and
  input `"hello world"` returned **768** floats, matching the schema;
  `POST /v1/chat/completions` with `spark-x2.5-4b` returned HTTP 200 with a
  completion; the reasoning-model caveat above was observed at
  `max_tokens=10`.

`PLAN.md` §7 carries the same facts in the plan's own appendix.
