---
type: Reference
title: The in-service agent loop and the A2A surface
description: How the service hosts a grounded tool-calling loop over the local index, why the loop lives here (2026-10-27 scope change), the switchable deterministic fan-out answer path, and what the Phase 2 dev HTTP surface and Phase 4 A2A surface expose.
status: draft
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-03T21:10:00Z
verified:
  - by: process:localai-probe
    at: 2026-11-03T19:20:00Z
  - by: process:localai-ab
    at: 2026-11-03T21:05:00Z
tags: [agent, a2a, loop, retrieval, tools, citations, fanout]
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

## The answer-path switch (`AGENT_SEARCH_MODE`)

Two answer paths share the prompt rules, the budgets, and the
citation-or-refusal finalizer; they differ in who decides the search
queries. `AGENT_SEARCH_MODE` (or `agent ask --search-mode`) selects one,
and an unset value means `loop`, so the shipped behavior is unchanged.

* **`loop`** (default) - the model calls `search_writing` itself, described
  above. Adaptive: it reads a result set and may search again.
* **`fanout`** (`internal/agentloop/fanout.go`) - one chat call writes
  rephrasings, the service runs retrieval for the original question plus
  every variant, fuses the lists, and one answer call composes from the
  merged excerpts. No tool-call dependency, and the excerpt payload enters
  the context once instead of once per tool round. The rephrasing call runs
  at `reasoning_effort: none` (it only emits three short strings, and an
  unparseable reply falls back to searching the question alone, so the
  failure mode is "one query" rather than "refusal"). The answer call runs
  at `low` with a completion floor of 8,192 tokens: with eight excerpts the
  prompt is ~4,000 tokens, and at 4,096 the answer call spent the whole
  budget thinking and returned empty content in 2 of 4 probed questions.

Fusion is a sum of the per-query reciprocal-rank scores, so a chunk several
variants agree on outranks one that a single query happened to rank first;
ties break on `chunk_id`, so a merge is deterministic. The merged set is
capped at eight chunks and two per source URL, then trimmed to the excerpt
budget. On the fan-out path no tool rounds run, so `tool_iterations` is
`0`, but the debug trace carries `reasoning_effort`, the variants, the
queries, and the candidate/kept counts, so a run is still auditable.

**Variants are labelled, not just "three rephrasings"**. The rewrite prompt
asks for three named keys - `exact_terms` (the question's own words,
identifiers verbatim), `corpus_vocabulary` (the same question in a post
title's words), `narrower` (one subtopic) - and the parser accepts that
shape plus the older `{"variants": [...]}` array and a bare array, so a
model that ignores the labels still works. Two measured lessons are baked
into the prompt:

* A bare "give me 3 variants" instruction produced near duplicates
  (`retrieval mechanism` / `retrieval system` / `retrieval technique`).
* Naming the job in a worded example backfired: the 1.7b model copied the
  example string verbatim into `corpus_vocabulary` on every question. The
  prompt therefore describes each key and carries no example variant.

**The answer-call prompt was cut, and it did not help latency.** Three changes
went in together: the payload carries only `chunk_id`, `source_url`, `title`,
`section`, and `body` (the old one serialized the whole `retrieval.Hit`, with
`source_kind`, `source_id`, `author`, `published_at`, and the path-native
scores); the merged set is five chunks instead of eight; and the system prompt
states only the rules the finalizer enforces. The answer call's prompt dropped
from ~3,500-4,100 to ~1,350-2,050 tokens (-55%) with no measurable change in
grounding or latency.

A fourth idea - capping each excerpt body at 250 words - was measured and
rejected: the answer-bearing part of a long chunk usually sits past that
cut, and fan-out fell to 1 of 5 questions grounded. `trimHits`' total word
budget is the only body limit.

**A/B on 2026-11-03** (`Spark-X2.6-1.7B-Q4_K_M`, one run per cell, final
code): the loop stays ~3x faster, and fan-out's grounding is no better and
noticeably less stable across runs (3/4, 3/4, 2/4, 1/5, 1/4 on the same four
questions):

| question | `loop` | `fanout` |
|---|---|---|
| how does retrieval work | 1 citation, 10.2s | refused, 23.1s |
| why vendored dependencies | 3 citations, 5.3s | refused, 16.4s |
| what is the harness take | 2 citations, 5.8s | 3 citations, 32.4s |
| single-file sqlite index | refused, 12.2s | refused, 26.7s |

The wall clock is the answer call's *reasoning*, not its input: with a
1,350-token prompt it still spent 2,700-5,500 completion tokens, and the
range across probed questions is 400-6,600 for near-identical prompts. That
variance - not prompt size - is what a next experiment has to attack:
`reasoning_effort: none` on the answer call (cheap, but the model then
writes its plan into `content`), a `finish_reason=length` guard with a
retry, or a model that does not reason before answering.

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
