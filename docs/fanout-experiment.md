---
type: Experiment
title: Query fan-out (multi-query retrieval over model-written variants)
description: The query-expansion answer path that was built, switched on, measured against the tool-calling loop on 2026-11-03/05, and removed - what it looked like, the numbers, the two prompt failures it exposed, and why the loop stays.
status: deprecated
generated:
  by: process:localai-ab
  at: 2026-11-05T10:20:00Z
verified:
  - by: process:localai-probe
    at: 2026-11-03T19:20:00Z
  - by: process:localai-ab
    at: 2026-11-05T10:15:00Z
tags: [experiment, retrieval, query-expansion, rag, fanout, deprecated]
sources: ["../PLAN.md", "../AGENTS.md"]
---

# Query fan-out (multi-query retrieval over model-written variants)

**Status: built, measured, removed on 2026-11-05.** The code is gone; this
record is kept because the measurements, the failure modes, and the design
are reusable. The shipped answer path is the tool-calling loop described in
[agent-architecture.md](/agent-architecture.md).

## What was built

An alternative to the model-driven tool call. Instead of the model calling
`search_writing`, the service ran the search itself:

1. **Rewrite** - one chat call at `reasoning_effort: none` returned labelled
   query variants as JSON.
2. **Fan-out** - `retrieval.Search` ran for the original question plus every
   variant (four queries by default). Retrieval itself was unchanged: same
   FTS5 BM25 + vec0 KNN + RRF, same filters, same index. This is a
   **query-side** strategy only.
3. **Fuse** - the per-query ranked lists were merged by summing their
   reciprocal-rank contributions (`1/(60 + rank)`), so a chunk several
   variants agreed on outranked one that a single query happened to place
   first. Ties broke on `chunk_id`, so a merge was deterministic.
4. **Answer** - one chat call at `reasoning_effort: low` composed from the
   merged excerpts, with the same identity fact, citation rules, and
   `Not in my published writing` refusal as the loop. `finalize()` was
   untouched, so citation-or-refusal enforcement was identical.

It sat behind `AGENT_SEARCH_MODE=loop|fanout` (plus `agent ask
--search-mode`), which is what made an A/B possible without code edits.

In the RAG literature this is **multi-query retrieval / query expansion**.
The three labelled variants mapped onto three named techniques:

| label | what it asked for | family |
|---|---|---|
| `exact_terms` | the question's own words, identifiers verbatim | keyword extraction |
| `corpus_vocabulary` | the question in the words the writing would use | vocabulary bridging / HyDE-lite |
| `narrower` | one subtopic of the question | one-step query decomposition |

## Why it was worth trying

The loop's weak link on the small chat models was **tool-call emission**, not
retrieval: at `reasoning_effort: none`, `spark-x2.5-1.7b-q4` replied with the
refusal string and never searched in 3 of 3 probes, and
`Spark-X2.6-1.7B-Q4_K_M` skipped the tool call in 1 of 3. A fan-out path has
no tool grammar to emit: the rewrite call only has to produce JSON we can
parse, and an unparseable reply degrades to searching the question alone.

## Measurements

All runs: `Spark-X2.6-1.7B-Q4_K_M` over the real 886-chunk index, one run
per cell unless stated, `max_tokens` 4,096 (8,192 floor for the fan-out
answer call), 4,000-token excerpt budget.

**Round 1 - unlabelled variants ("3 rephrasings")**

| question | loop | fanout |
|---|---|---|
| how does retrieval work | 5 citations, 11.9s | refused, 4.8s |
| why vendored dependencies | 3 citations, 7.4s | 4 citations, 20.1s |
| what is the harness take | 1 citation, 9.5s | 3 citations, 31.7s |
| single-file sqlite index | refused, 8.2s | 1 citation, 21.7s |

**Round 2 - labelled variants** (`exact_terms` / `corpus_vocabulary` /
`narrower`)

| question | loop | fanout |
|---|---|---|
| how does retrieval work | 4 citations, 9.6s | refused, 18.1s |
| why vendored dependencies | 2 citations, 5.0s | 3 citations, 20.0s |
| what is the harness take | 2 citations, 6.9s | 2 citations, 21.1s |
| single-file sqlite index | refused, 7.1s | 1 citation, 33.1s |

**Round 3 - labelled variants, prompt cut** (compact payload, five chunks,
short system prompt)

| question | loop | fanout |
|---|---|---|
| how does retrieval work | 1 citation, 10.2s | refused, 23.1s |
| why vendored dependencies | 3 citations, 5.3s | refused, 16.4s |
| what is the harness take | 2 citations, 5.8s | 3 citations, 32.4s |
| single-file sqlite index | refused, 12.2s | refused, 26.7s |

Grounding across every fan-out run, on the same four questions:
**3/4, 3/4, 2/4, 1/5, 1/4**. The loop sat at 2/3-3/4 in the same sessions.
Fan-out was never better and was consistently 2-3x slower.

## Four findings worth keeping

1. **Variant quality is the ceiling, and `none` effort caps it.** A bare
   "give me 3 variants" returned near duplicates - `retrieval mechanism` /
   `retrieval system` / `retrieval technique` - so the extra queries bought
   little recall. Naming each slot's job helped.
2. **A worded example in a prompt gets copied verbatim.** Explaining
   `corpus_vocabulary` with "a question asks *does the agent remember
   things*; this key says *memory context window*" made the 1.7b model emit
   the literal string `memory context window` for every question, including
   ones about vendoring. The fix: describe each key, carry no example
   variant, and add "never reuse words from these instructions".
3. **Cutting the answer prompt does not cut latency.** The payload shrank
   from the whole `retrieval.Hit` (with `source_kind`, `source_id`, `author`,
   `published_at`, and the path-native scores) to
   `chunk_id`/`source_url`/`title`/`section`/`body`, the merged set dropped
   from eight chunks to five, and the system prompt kept only the rules the
   finalizer enforces: prompt tokens fell from ~3,500-4,100 to
   ~1,350-2,050 (-55%) with no change in wall clock. The cost is the answer
   call's **reasoning**: 400-6,600 completion tokens for near-identical
   prompts.
4. **Capping excerpt bodies at 250 words destroyed grounding.** The
   answer-bearing part of a long chunk usually sits past the cut; fan-out
   fell to 1 of 5 questions grounded. The only safe body limit is the total
   excerpt-word budget that `trimHits` already enforces.

## Why it was dropped

* **No accuracy win.** The loop's failures are model-quality failures (a
  1.7b skipping a tool call, or refusing a question the corpus answers), and
  fan-out did not fix them - it traded one model weakness for another
  (imagination instead of adaptivity).
* **2-3x the latency.** One answer call reading five excerpts at `low`
  effort costs 16-33s; the loop's adaptive rounds cost 5-12s because each
  call reads far less.
* **A second prompt surface to keep honest.** Two answer prompts, two
  retrieval policies, and a fusion rule are three more things to test
  against a model that changes underneath them.
* **The switch itself was the cost.** Two code paths in one loop means every
  loop change has to be made twice or explicitly skipped, and the A/B was
  still noisy enough that single runs proved nothing.

## What a next attempt should do differently

1. **Pseudo-relevance feedback (PRF)** instead of an LLM rewrite: run the
   question, take the top 3 chunks, pull their distinctive terms, and run
   one second deterministic pass. No extra model call, so no variant-quality
   ceiling and no copy-the-example failure.
2. **HyDE-lite** instead of keyword variants: have the rewrite write one
   short pseudo-answer paragraph and embed that. Usually beats keyword
   variants when the corpus and the question use different vocabulary, and
   it uses the vector path the corpus already has.
3. **Fix the truncation hole first.** Both paths map a
   `finish_reason: length` on the answer call to the refusal string, so a
   token overrun is indistinguishable from "not in my writing". A guard plus
   one retry at a larger budget is worth more than any retrieval change.
4. **Measure with N runs per cell.** Per-question variance on this model is
   larger than the effect being measured: the same loop cell produced 5, 4,
   then 0 citations across runs. A single run per cell cannot settle a
   comparison.
