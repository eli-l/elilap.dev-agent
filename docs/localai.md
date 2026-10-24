---
type: Reference
title: Self-hosted LocalAI endpoint and model catalog
description: Verified facts about the LocalAI instance at http://192.168.78.243:8080 - version, model ids, embedding dimensions, and chat behaviour - captured from the AI Node on 2026-10-23.
status: draft
generated:
  by: human:dave
  at: 2026-10-23T20:16:00Z
tags: [localai, embeddings, chat, models, phase-0]
---

# Self-hosted LocalAI endpoint and model catalog

The model backend is an already-running LocalAI instance, not a container
this repo deploys. `PLAN.md` Decision 10 records the decision.

## Endpoint

* Base URL: `http://192.168.78.243:8080` (LAN host `dev`; the AI Node is
  `192.168.78.242`).
* OpenAI-compatible base for the Go client: `http://192.168.78.243:8080/v1`
  -> `LOCALAI_BASE_URL`.
* `GET /readyz` -> `200` (liveness).
* `GET /version` -> `v4.12.0 (15e37df9a468c798a5a511624f054057da4380a1)`.

## Model catalog (`GET /v1/models`)

| id | role |
|---|---|
| `embeddinggemma-300m` | embeddings - **768 dims** |
| `spark-x2.5-4b` | chat / LLM - default |
| `spark-x2.5-1.7b-q4` | chat / LLM - lighter fallback |

## Verified on 2026-10-23 (from the AI Node)

* `POST /v1/embeddings` with `embeddinggemma-300m` and input `"hello world"`
  returned 768 floats -> matches `chunks_vec.embedding float[768]` in
  `migrations/0001_init.sql`.
* `POST /v1/chat/completions` with `spark-x2.5-4b` returned HTTP 200 with a
  completion. **Caveat**: `spark-x2.5-4b` is a reasoning model - the
  response carries a `reasoning` field, and at `max_tokens=10` the budget was
  consumed by reasoning, leaving `content` empty. The client must budget
  `max_tokens` for reasoning and fail loudly when `content` is empty.

## Usage

```bash
export LOCALAI_BASE_URL=http://192.168.78.243:8080/v1
export EMBED_MODEL=embeddinggemma-300m
export CHAT_MODEL=spark-x2.5-4b
```

No API key is required for this instance (`LOCALAI_API_KEY` stays unset).
