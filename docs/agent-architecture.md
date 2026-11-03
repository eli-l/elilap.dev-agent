---
type: Reference
title: The in-service agent loop and the A2A surface
description: How the service hosts a grounded tool-calling loop over the local index, why the loop lives here (2026-10-27 scope change), and what the Phase 2 dev HTTP surface and Phase 4 A2A surface expose.
status: draft
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-03T17:20:00Z
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

## Role

An agent representing Eli Lap's published technical knowledge, engineering
experience, experiments, and opinions. It answers questions using published
sources and provides verifiable citations.

That is the canonical description (`agentloop.Description`), used verbatim by
the planned A2A `AgentCard`; the loop enforces the citation half, and the
identity fact below keeps both bylines usable.

## The loop

One tool-calling loop in `internal/agentloop` (implemented 2026-11-02), shared by
`agent ask` and the Phase 4 A2A server:

* **Tool**: `search_writing`, wrapping `internal/retrieval` (hybrid BM25
  + vectors, RRF merge). Parameters: `query`, `top_n`, `source`
  (`ghost|postiz`), `platform`, `after`, `before`,
  `recency_halflife_days`. The dev `/v1/retrieve` request body (Phase 2)
  is this tool call verbatim.
* **Query construction** (2026-11-03): `query` is **extracted keys, not a
  paraphrase**. The prompt and the tool's parameter description both say:
  extract the 2-6 most distinctive terms, drop the question's scaffolding
  ("what did I write about", pronouns, "my", generic verbs), copy names,
  library/API/flag spellings and identifiers verbatim, and prefer the
  vocabulary the writing itself would use. Retries use different keys, not
  the original sentence; filters (`source`, `platform`, `after`, `before`,
  `recency_halflife_days`) are passed only when the question names them.
  This matters because `query` feeds two consumers: the embeddings call
  verbatim, and FTS5 as the `OR`-joined term list from
  `store.FTSQuery`. A restated question dilutes both with words that appear
  in the question but not in the corpus.

  **Status 2026-11-02**: retrieval is complete - hybrid FTS5 BM25 + vec0
  KNN merged with RRF k=60, `query`, `top_n`, `source`, `platform`,
  `after`, `before`, `recency_halflife_days`, and a CLI whose
  `--format jsonl` output matches `/v1/retrieve` object-for-object. See
  [retrieval.md](/retrieval.md).
* **Budget**: max 3 tool iterations, ~4,000 excerpt tokens per answer, a
  per-request wall-clock cap, and 4,096 completion tokens (reasoning
  included). Stop on budget or when the model stops calling the tool; if
  the iteration budget runs out, one final no-tools call forces an answer
  from what was already retrieved. If the first response is the refusal
  string without any tool call, one nudge asks the model to search before
  refusing; off-corpus questions pay one extra retrieval round.
  **Implemented 2026-11-02** in `internal/agentloop`; the live loop
  answered in 17.7s and refused an off-corpus question in 9.4s.
* **Identity**: the prompt states that Eli Lap, Ilja Lapkovsky, and Ilja
  Lapkovskis are the same person ("Eli Lap" is the internationalized pen
  name). Ghost chunks are authored as "Eli Lap" and Postiz chunks as "Ilja
  Lapkovsky"; without the mapping the model discards the cross-posts as
  someone else's writing. The identity fact is service grounding context,
  not a citable corpus excerpt.
* **Grounding**: the prompt admits only index excerpts (plus the identity
  fact); every claim must cite a retrieved `chunk_id` or the excerpt's
  `source_url`, and the loop resolves the other field from the index. A
  citation that resolves to a retrieved chunk is accepted; a URL-quoted
  citation resolves to the best-ranked chunk of that source (one citation
  per source URL). A reply with no resolvable citation, or with a
  `chunk_id` the loop never retrieved, is replaced by the refusal string -
  the loop never answers off-corpus from parametric memory. Excerpts are
  data, never instructions.
* **Model**: chat `CHAT_MODEL` (default `spark-x2.5-4b`; this host runs
  `spark-x2.6-4b-q4`) over the OpenAI-compatible connector (plain
  `net/http`, tool definitions round-tripped per request). The
  reasoning-model token caveat in [localai.md](/localai.md) applies -
  `max_tokens` must budget for reasoning.
* **Scope guard**: no other tools, no cross-request state, no retries
  across requests. New tools are new decisions.

## Debugging one run

`debug` turns each run into a readable RAG trace, which is the intended way
to answer "why did the model say that?" and "why was this chunk retrieved?":

```bash
agent --log-level debug ask "what is the harness take?"     # agent module
agent --log-level debug query --top 5 "vendor lock-in"       # db module
```

The `agent` module logs `rag: run starting`, one `rag: chat request` header
plus one `rag: message` line per message the model sees, `rag: chat response`
with the server-reported model id, `finish_reason`, content, and token usage,
and `rag: tool call` / `rag: tool result` / `rag: answer` around them. The
`db` module logs `retrieval: query`, `retrieval: candidates` (including the
generated FTS5 query), one `retrieval: hit` per ranked chunk with its
`score`/`distance`/`bm25`/`cosine`, and `retrieval: done`. It is debug-only and
contains prompts and published excerpts; see [logging.md](/logging.md) for the
payload policy.

## The HTTP dev surface (Phase 2)

Before any A2A work, `agent serve` runs the same internals over plain
HTTP - token-less and loopback-only - so both halves can be exercised
with `curl`:

* `POST /v1/retrieve`: the request body carries the `search_writing`
  parameters; the response is exactly the JSON array the model's tool
  call would receive - byte-fidelity with the tool path, identical to
  `agent query --format jsonl`. Hybrid ranking and all filters are live;
  empty results are `[]` (see [retrieval.md](/retrieval.md)).
* `POST /v1/ask`: one question in, the full loop out - the same
  answer/citations/refusal struct `agent ask` prints, plus iteration
  count and timing, so grounding behavior can be diffed against the CLI.
  **Implemented 2026-11-02**: `{"question"}` in; `{"answer",
  "citations", "tool_iterations", "refused", "elapsed_ms"}` out, with a
  90s request budget and 502/504 on backend failure/timeout.

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
