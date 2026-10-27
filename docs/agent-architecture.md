---
type: Reference
title: The in-service agent loop and the A2A surface
description: How the service hosts a grounded tool-calling loop over the local index, why the loop lives here (2026-10-27 scope change), and what the Phase 2 dev HTTP surface and Phase 4 A2A surface expose.
status: draft
generated:
  by: dave
  at: 2026-10-27T18:30:00Z
tags: [agent, a2a, loop, retrieval, tools, citations]
sources: ["../PLAN.md"]
---

# The in-service agent loop and the A2A surface

Decided 2026-10-27 (`PLAN.md` open question 8, Decisions 15-16): this repo
is an **agentic A2A connector**. A remote peer sends a conversational
task; *our* service runs the loop - retrieve, ground, answer with
citations - against the local index. RAG (`internal/retrieval`) is an
internal tool of the service, not the external product; `agent ask` is
the loop's CLI shadow. A Phase 2 dev HTTP surface (`agent serve`) exposes
both the raw tool and the loop over loopback HTTP, so each half is
curl-testable before A2A exists.

## The loop

One tool-calling loop in `internal/agentloop` (Phase 2), shared by
`agent ask` and the Phase 4 A2A server:

* **Tool**: `search_writing`, wrapping `internal/retrieval` (hybrid BM25
  + vectors, RRF merge). Parameters: `query`, `top_n`, `source`
  (`ghost|postiz`), `platform`, `after`, `before`,
  `recency_halflife_days`. The dev `/v1/retrieve` request body (Phase 2)
  is this tool call verbatim.
* **Budget**: max 3 tool iterations, ~4,000 excerpt tokens per answer, a
  per-request wall-clock cap. Stop on budget or when the model stops
  calling the tool.
* **Grounding**: the prompt admits only index excerpts; every claim must
  cite `chunk_id` + `source_url` from the retrieved set. If grounding
  fails, the answer is the refusal string - the loop never answers
  off-corpus from parametric memory.
* **Model**: chat `spark-x2.5-4b` over the OpenAI-compatible connector
  (plain `net/http`, tool definitions round-tripped per request). The
  reasoning-model token caveat in [localai.md](/localai.md) applies -
  `max_tokens` must budget for reasoning.
* **Scope guard**: no other tools, no cross-request state, no retries
  across requests. New tools are new decisions.

## The HTTP dev surface (Phase 2)

Before any A2A work, `agent serve` runs the same internals over plain
HTTP - token-less and loopback-only - so both halves can be exercised
with `curl`:

* `POST /v1/retrieve`: the request body carries the `search_writing`
  parameters; the response is exactly the JSON array the model's tool
  call would receive - byte-fidelity with the tool path, identical to
  `agent query --format jsonl`.
* `POST /v1/ask`: one question in, the full loop out - the same
  answer/citations/refusal struct `agent ask` prints, plus iteration
  count and timing, so grounding behavior can be diffed against the CLI.

Phase 4 keeps the paths and the JSON and adds auth, rate limits, and the
audit log - nothing on this surface is thrown away.

## The A2A surface (Phase 4)

A2A v0.3 JSON-RPC binding, exposing the *agent*:

* `message/send` at `/a2a`: one request, one grounded answer as an A2A
  `Message` with citation metadata. Stateless per task - no persisted
  task store, no `message/stream` in v1 (`capabilities.streaming` is
  `false`) until a peer actually needs it.
* `GET /.well-known/agent.json`: A2A `AgentCard` - `name`,
  `description`, `version`, `supportedInterfaces` (JSONRPC + protocol
  version), `capabilities`, `securitySchemes` (bearer), one skill
  describing grounded Q&A over Eli's published writing.
* `POST /v1/retrieve` graduates from the Phase 2 dev surface - same
  path, same JSON, now token-gated - as the deterministic escape hatch
  for callers that want ranked chunks instead of conversation.
* Security: bearer token, `127.0.0.1` bind by default, 30 tasks/min per
  token plus a per-task wall-clock budget, audit log without bodies or
  prompts. Peer task text is data, never instructions - the grounded
  prompt and citation enforcement are the prompt-injection backstops.

## What moved, and what did not

* Moved: the answer loop (which used to be "the caller's problem") now
  lives here; the peer-facing product is the agent, not bare retrieval.
* Unchanged: ingest stays strictly read-only against Ghost and Postiz;
  the index stays a derived, rebuildable SQLite file; the API exposes no
  writes; generation beyond the grounded loop stays a non-goal, as does
  multi-agent orchestration inside this service.

Implementation sequencing lives in `../PLAN.md` (Phase 2 tasks, Phase 4
tasks); this file records the architecture and its rationale.
