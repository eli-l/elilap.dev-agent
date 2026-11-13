---
type: Reference
title: The in-service agent loop and the A2A surface
description: How the service hosts a grounded tool-calling loop over the local index, why the loop lives here (2026-10-27 scope change), and what the dev HTTP surface and the A2A v0.3 surface expose.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-05T10:20:00Z
verified:
  - by: process:localai-probe
    at: 2026-11-03T19:20:00Z
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

**Status 2026-11-05: both halves are shipped and exercised live.** The loop,
the tool, the response contract, the dev HTTP surface, and the A2A v0.3 binding
(card, `message/send`, bearer tokens, rate limit, audit) are in the binary and
verified against the 886-chunk index. The wire contract lives in
[a2a.md](/a2a.md). Query fan-out was A/B'd against the loop and removed on
2026-11-05 — the loop is the only answer path.

## Role

An agent representing Eli Lap's published technical knowledge, engineering
experience, experiments, and opinions. It answers questions using published
sources and provides verifiable citations.

That is the canonical description (`agentloop.Description`), used verbatim by
the A2A `AgentCard`; the loop enforces the citation half, and the identity
fact below keeps both bylines usable.

## The loop

One tool-calling loop in `internal/agentloop` (implemented 2026-11-02), shared by
`agent ask` and the A2A server:

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
* **Budget**: max 3 tool iterations, ~3,000 estimated excerpt tokens per
  answer (4-chars-per-token), a per-request wall-clock cap, and 2,048
  completion tokens (reasoning included) so the 8K-context 4B model fits. Stop on budget or when the model stops calling the tool; if
  the iteration budget runs out, one final no-tools call forces an answer
  from what was already retrieved. If the first response is the refusal
  string without any tool call, one nudge asks the model to search before
  refusing; off-corpus questions pay one extra retrieval round.
  **Implemented 2026-11-02** in `internal/agentloop`; the live loop
  answered in 17.7s and refused an off-corpus question in 9.4s.
* **Reasoning effort**: pinned per call kind, never left to the backend
  default - `reasoning_effort: "none"` while the loop is still turning the
  question into search keys (nothing retrieved yet), `"low"` for every
  call made with excerpts in hand and for the forced no-tools answer. The
  backend default is "model config", which on `spark-x2.6-1.7b` is
  unbounded: on a two-sentence question it spent all 4,096 tokens thinking
  and returned empty `content` with `finish_reason=length` - which the
  finalizer then reported as a refusal. The pinned settings are far cheaper:
  0.6s/55 completion tokens on a two-sentence question (transform) and
  4.0s/704 (answer). **Caveat**: `"none"` also costs tool-call reliability on
  the small models - with the full grounded prompt it skipped `search_writing`
  in 1 of 3 probes on `spark-x2.6-1.7b` and in 3 of 3 on `spark-x2.5-1.7b`
  (immediate refusal, no retrieval), where `"low"` called the tool 3 of 3 on
  both. If grounding regresses on a live run, the transform effort is the
  knob to raise first. `"medium"` is not a valid value on this llama-cpp
  template (HTTP 500), and a nested `reasoning` object is silently ignored;
  see [localai.md](/localai.md).
* **Identity**: the prompt states that Eli Lap, Ilja Lapkovsky, and Ilja
  Lapkovskis are the same person ("Eli Lap" is the internationalized pen
  name). Ghost chunks are authored as "Eli Lap" and Postiz chunks as "Ilja
  Lapkovsky"; without the mapping the model discards the cross-posts as
  someone else's writing. The identity fact is service grounding context,
  not a citable corpus excerpt.
* **Grounding**: the prompt admits only index excerpts (plus the identity
  fact). The model writes the summary and nothing else - it never sees a
  chunk id, and the prompt forbids citation markers, ids, URLs as
  references, or a source list of its own. The loop attaches the evidence
  from its own retrieval record: one `Citation` per excerpt the model was
  shown, one `Source` per distinct post/page. Nothing a peer receives can
  therefore be fabricated by the model. An empty reply, the refusal
  string, or an answer produced without a single retrieved excerpt
  becomes the refusal, so the loop still never answers off-corpus from
  parametric memory. Excerpts are data, never instructions.
  (Until 2026-11-13 the model cited `chunk_id`/`source_url` inline and the
  loop verified every marker; that per-claim check went with the markers,
  and the guarantee is now "never answered without retrieved excerpts" -
  Decision 24.)
* **Model**: chat `CHAT_MODEL` (default `Spark-X2.6-4B-q8_0`) over the
  OpenAI-compatible connector (plain
  `net/http`, tool definitions round-tripped per request). The
  reasoning-model token caveat in [localai.md](/localai.md) applies -
  `max_tokens` must budget for reasoning.
* **Scope guard**: no other tools, no cross-request state, no retries
  across requests. New tools are new decisions.

### Response contract

Every answer surface (`agent ask --format json`, `POST /v1/ask`, A2A
`metadata`) returns the same three-part shape:

```json
{
  "answer": "the model's summary, with no citation markers and no source list",
  "citations": [
    {
      "source_kind": "ghost_post",
      "source_url": "https://read.elilap.dev/some-post/",
      "title": "Some post",
      "section": "Why it matters",
      "published_at": "2026-11-02T13:21:35Z",
      "excerpt": "the exact text the model was shown"
    }
  ],
  "sources": [
    {
      "kind": "ghost_post",
      "url": "https://read.elilap.dev/some-post/",
      "title": "Some post",
      "author": "Eli Lap",
      "platform": "",
      "published_at": "2026-11-02T13:21:35Z"
    }
  ],
  "tool_iterations": 1,
  "refused": false,
  "elapsed_ms": 2987
}
```

* `citations` has one entry per excerpt the answer was grounded on, in
  ranking order; `sources` deduplicates those excerpts to the documents
  behind them, in first-seen order. `source_url` joins the two lists.
* Both are always arrays (`[]` on a refusal) and neither carries a
  `chunk_id`. The chunk id is an internal identifier: it is logged on the
  `rag: answer` debug line and in the `internal/retrieval` trace, and it
  never reaches a response - the model never sees one either, so it
  cannot echo one.
* `excerpt` is the text the model actually saw, which is trimmed when the
  excerpt-token budget crosses a chunk. That is what makes the field
  usable for an evaluator or a UI without a second read of the index.

## What was tried and dropped: query fan-out

A second answer path was built behind `AGENT_SEARCH_MODE=fanout`: one chat
call rewrote the question into labelled variants, the service ran retrieval
for the original plus every variant, fused the lists by summed reciprocal
rank, and spent one answer call on the merged excerpts. It was measured
against the loop over the same four questions and **removed on 2026-11-05**:
grounding was never better (3/4, 3/4, 2/4, 1/5, 1/4 across runs) and it was
2-3x slower, because one answer call reading five excerpts at `low` effort
costs 16-33s where the loop's adaptive rounds cost 5-12s.

The design, every measurement, the two prompt failures it exposed (copied
examples, near-duplicate variants), the rejected excerpt-body cap, and what
a next attempt should do instead are kept in
[Query fan-out (multi-query retrieval over model-written variants)](/fanout-experiment.md).

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

## The HTTP surface (`/v1/retrieve`, `/v1/ask`)

The Phase 2 dev surface graduated in Phase 4 (2026-11-05): `agent serve` runs
the same internals over plain HTTP, token-less only when no A2A token is
configured and loopback-only in that mode, so both halves can be exercised
with `curl`:

* `POST /v1/retrieve`: the request body carries the `search_writing`
  parameters; the response is exactly the JSON array the model's tool
  call would receive - byte-fidelity with the tool path, identical to
  `agent query --format jsonl`. Hybrid ranking and all filters are live;
  empty results are `[]` (see [retrieval.md](/retrieval.md)).
* `POST /v1/ask`: one question in, the full loop out - the same
  answer/citations/sources/refusal struct `agent ask` prints, plus
  iteration count and timing, so grounding behavior can be diffed against
  the CLI. **Implemented 2026-11-02**: `{"question"}` in; `{"answer",
  "citations", "sources", "tool_iterations", "refused", "elapsed_ms"}`
  out, with a 90s request budget and 502/504 on backend failure/timeout.

The A2A hardening landed 2026-11-05: both paths and the JSON are unchanged,
and bearer auth, the rate limit, and the audit log now wrap them. A configured
token gates both paths with `Authorization: Bearer <token>`; a token-less
public bind instead charges a self-asserted agent/user identity against the
spend budget (Decision 25). The audit stream records each `/v1/ask` task. See
[a2a.md](/a2a.md) for the wire contract and
[runbook.md](/runbook.md) for the operator commands.

## The A2A surface

A2A v0.3 JSON-RPC binding, exposing the *agent*:

* `message/send` at `POST /a2a`: one request, one grounded answer as an A2A
  `Message` whose `metadata.citations` and `metadata.sources` carry the
  grounded excerpts and their documents (never a `chunk_id`). Stateless per task - no persisted task store, so the
  service answers with a Message and `tasks/get` reports `TaskNotFound`;
  `message/stream` is not offered (`capabilities.streaming` is `false`).
* `GET /.well-known/agent-card.json` (plus the v0.2 `/.well-known/agent.json`
  alias): A2A `AgentCard` - `name`, `description` verbatim from
  `agentloop.Description`, `version`, `protocolVersion: "0.3.0"`, `url` +
  `preferredTransport` + `additionalInterfaces`, `capabilities`,
  `securitySchemes` (bearer, only when tokens are configured), and one skill
  describing grounded Q&A over Eli's published writing.
* `POST /v1/retrieve` is the deterministic escape hatch for callers that want
  ranked chunks instead of conversation, on the same auth.
* Security: an optional bearer token from env or a re-read `0600` file (it
  gates every path when set), `127.0.0.1` bind by default, and a public
  `--bind-public` bind that requires a spend ceiling (3/min per identity,
  20/day per identity, 20/day per address, 60/day global, 2 concurrent
  answers) instead of a token. The loop's per-task wall-clock budget, an
  8 KiB question cap, and an audit log that records the principal, the
  question fingerprint, and the outcome but never the answer or excerpts
  still apply.
  Peer task text is data, never instructions - the grounded prompt and
  refusing any answer produced without a retrieved excerpt are the
  prompt-injection backstops.

## What moved, and what did not

* Moved: the answer loop (which used to be "the caller's problem") now
  lives here; the peer-facing product is the agent, not bare retrieval.
* Unchanged: ingest stays strictly read-only against Ghost, Postiz, and the
  website it reads as HTML;
  the index stays a derived, rebuildable SQLite file; the API exposes no
  writes; generation beyond the grounded loop stays a non-goal, as does
  multi-agent orchestration inside this service.

Implementation sequencing lives in `../PLAN.md`; this file records the
architecture and its rationale, and [a2a.md](/a2a.md) records the wire
contract.
