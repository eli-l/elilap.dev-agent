---
type: Reference
title: Self-hosted LocalAI endpoint and model catalog
description: Verified facts about the LocalAI instance at http://192.168.78.243:8080 - version, model ids, embedding dimensions, chat behaviour, the reasoning_effort controls, and the current 4B baseline - captured from the AI Node on 2026-10-23, re-probed on 2026-11-03, and re-checked on 2026-11-11.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-11T11:30:00Z
verified:
  - by: process:localai-probe
    at: 2026-11-03T19:20:00Z
  - by: process:localai-probe
    at: 2026-11-11T07:09:04Z
  - by: process:localai-probe
    at: 2026-11-11T11:30:00Z
tags: [localai, embeddings, chat, models, reasoning, phase-0]
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
| `Spark-X2.6-4B-q8_0` | chat / LLM - **pinned default**; 8K context; the 2026-11-11 baseline |
| `Spark-X2.6-1.7B-Q8_0.gguf` | chat / LLM - lighter fallback; serves |
| `spark-x2.6-4b-q4` | chat / LLM - listed, but CUDA OOM on load (2026-11-11) |
| `Spark-X2.6-1.7B-Q4_K_M` | chat / LLM - listed, but CUDA OOM on load (2026-11-11) |

## Host drift (2026-11-11)

`GET /v1/models` on 2026-11-11 lists the five ids above. The previous
`spark-x2.5-4b` model is gone. A catalog entry is not a promise that the model
loads: `Spark-X2.6-4B-q8_0` and `Spark-X2.6-1.7B-Q8_0.gguf` served real chat
completions, while both Q4 variants failed with a CUDA out-of-memory loader
error. The 4B q8 model has an **8K context**, so the loop's excerpt budget was
retuned to 3,000 estimated tokens and the completion budget to 2,048; that
keeps the system prompt, tool schema, excerpts, and completion inside 8K.

The current Ragas baseline uses `Spark-X2.6-4B-q8_0`; see
[Ragas answer evaluation (evals)](/ragas-eval.md).

## Verified on 2026-10-23 (from the AI Node)

* `POST /v1/embeddings` with `embeddinggemma-300m` and input `"hello world"`
  returned 768 floats -> matches `chunks_vec.embedding float[768]` in
  `migrations/0001_init.sql`.
* `POST /v1/chat/completions` with `spark-x2.5-4b` returned HTTP 200 with a
  completion. **Caveat**: `spark-x2.5-4b` is a reasoning model - the
  response carries a `reasoning` field, and at `max_tokens=10` the budget was
  consumed by reasoning, leaving `content` empty. The client must budget
  `max_tokens` for reasoning and fail loudly when `content` is empty.

## Reasoning controls

`/v1/chat/completions` takes a flat top-level `reasoning_effort` string.
There is **no nested `reasoning` object** on that endpoint: `{"reasoning":{"effort":"none"}}`
is accepted and silently ignored, so the model reasons anyway.
`"reasoning_effort": null` and `""` are also not `none` - both decode to the
empty string, which means "model config".

| `reasoning_effort` | recognise by |
|---|---|
| omitted / `null` / `""` | model config; on `spark-x2.6-1.7b` this is unbounded - 14,544 reasoning chars and `finish_reason=length` at a 4,096 budget |
| `"none"` | no `reasoning` field at all; cheapest (0.6s, 55 completion tokens); the model may write its plan into `content` instead |
| `"low"` | 2,546 reasoning chars, 704 completion tokens on the same prompt |
| `"high"` | 9,258 reasoning chars, 2,695 completion tokens |
| anything else (`"medium"`, `"minimal"`, `"LOW"`, trailing space) | HTTP 500 `Unable to generate parser for this template` |

The response field is `reasoning` (streaming: `delta.reasoning`), never
`reasoning_content`. `content` is empty exactly when `finish_reason` is
`length` (thinking truncated before `</thinking>`) or `tool_calls` (a pure
tool call), so a sweep of the same prompt returns `content` at some budgets
and not others - reasoning length varies run to run. `POST /v1/responses`
is the exception: it takes the OpenAI nested shape,
`"reasoning": {"effort": ..., "summary": ...}`.

**Tool calling is effort-sensitive** (probed 2026-11-03 with the grounded loop
prompt and the `search_writing` tool, 3 runs each):

| model | `none` | `low` |
|---|---|---|
| `Spark-X2.6-1.7B-Q4_K_M` | 2/3 tool calls, plan leaked into `content` | 3/3, clean `content` |
| `spark-x2.5-1.7b-q4` | 0/3 - replies with the refusal string, never searches | 3/3, clean `content` |

## Usage

```bash
export LOCALAI_BASE_URL=http://192.168.78.243:8080/v1
export EMBED_MODEL=embeddinggemma-300m
export CHAT_MODEL=Spark-X2.6-4B-q8_0
```

`CHAT_MODEL` must name a model the host can currently load. On 2026-11-11 the
4B q8 id is the baseline; the Q4 ids are listed but fail with CUDA OOM.

No API key is required for this instance (`LOCALAI_API_KEY` stays unset).
