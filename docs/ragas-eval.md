---
type: Reference
title: Ragas answer evaluation (evals)
description: The single Python/Ragas tool in evals/ - the configs holding hand-verified ground truth, the agent-ask client that becomes the system under test, the faithfulness/context-utilization/correctness metrics, refusal discipline, the EVAL_* variables, the gitignored output directory, and the captured full-size run with stand-in models.
status: draft
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-11T07:10:21Z
tags: [evaluation, ragas, python, uv, llm, rag, citations]
sources:
  - ../evals/README.md
  - ../evals/evaluate.py
  - ../evals/agent.py
  - ../evals/configs/answers.yaml
  - ../evals/configs/refusals.yaml
  - ../evals/test_llm.py
  - ../example.env
---

# Ragas answer evaluation (evals)

`evals/` grades the **agent's end-to-end answers** with an LLM judge. It is the
only answer-quality test in the repository: one tool, one config directory,
one output directory. Adversarial behaviour is the other half, covered by the
promptfoo suite in `tests-ai/`; see [red-team.md](red-team.md).

## What it runs

| File | Role |
|---|---|
| `evaluate.py` | The tool. Reads the configs, asks the agent, runs the metrics, prints a summary, writes CSVs to `output/`. |
| `agent.py` | The system under test. Runs `agent ask --format json` (or `POST /v1/ask` with `--transport http`) and reads the body of every cited chunk from `index.db`, so the metrics see exactly the excerpts the answer was allowed to cite. |
| `configs/answers.yaml` | 15 answerable questions with verified chunk ids and the facts a correct answer must state. |
| `configs/refusals.yaml` | 14 questions the corpus cannot answer — off-corpus, adjacent-but-absent, and personal. |
| `output/` | Generated datasets, experiment CSVs, and traces. Gitignored. |
| `test_llm.py` | Endpoint precondition check: `/v1/models`, one completion, one structured score. Not part of scoring. |

`uv.lock` is committed; `.venv/`, `__pycache__/`, and `*.egg-info/` are not.

## Metrics

Two datasets, two questions, one judge.

**`--kind answers`** — answerable questions:

| Metric | What it measures |
|---|---|
| `Faithfulness` | Whether every claim in the answer is supported by the cited excerpts |
| `ContextUtilization` | Whether the answer actually drew on the retrieved context |
| `correctness` (`DiscreteMetric`) | Whether the answer states the facts recorded in the review note |

A refusal on an answerable row scores `correctness: fail` and skips the two
numeric metrics, because there are no claims to check.

**`--kind refusals`** — unanswerable questions:

| Metric | What it measures |
|---|---|
| `refusal_discipline` (`DiscreteMetric`) | Whether the agent declined instead of inventing an answer |

## Ground truth

The configs are hand-verified evidence, not a recording of what the engine
returned. Each case names the chunk that answers the question and the facts a
correct answer must state; `must_mention` becomes the grading note for
`correctness`, and `expect_chunk_ids` stays for traceability. There is no
`reference` answer column on purpose: writing one per question from the
excerpts is how a gold set quietly becomes fiction.

## Configuration

The harness reads one endpoint, independent of the agent's `LOCALAI_*`
settings, so it can score with a different model than the one answering:

| Variable | Default | Notes |
|---|---|---|
| `EVAL_BASE_URL` | `http://localhost:8000/v1` | Any OpenAI-compatible server |
| `EVAL_MODEL` | `mtplx-qwen36-35b-a3b-optimized-speed` | Must be served by that endpoint |
| `EVAL_API_KEY` | empty → `local` | Non-empty key required by the SDK; local servers ignore it |

They are part of the repository inventory in `../example.env` and in
[Environment configuration](environment.md). The Python code does not load
`.env`, so pass it explicitly (`uv run --env-file ../.env …`) or export the
variables.

## Running it

```bash
cd evals
uv sync
uv run python test_llm.py                            # exit 0 = judge endpoint ok
uv run python evaluate.py --kind answers --limit 3   # cheap smoke test
uv run python evaluate.py                            # both datasets
```

Options: `--kind answers|refusals|both`, `--limit N`, `--transport cli|http`,
and `--ask-url` for the HTTP target. The `cli` transport needs the binary
(`go build -mod=vendor -tags sqlite_fts5 -o agent ./cmd/agent`); the `http`
transport defaults to `http://127.0.0.1:18080/v1/ask`, the port reserved for
evaluation runs (the interactive dev server stays on 8080). Start the target
with `./agent serve --addr 127.0.0.1:18080` in its own terminal.

Outputs land under `evals/output/`: `datasets/*.csv` (the rows),
`experiments/*.csv` (one row per question with every metric), and any trace
files. Values are overwritten per run and never committed.

## Why it is slow, and why rows are serialized

Each row is two or three LLM-judged calls, and faithfulness decomposes the
answer into statements. One local model server cannot sustain parallel
structured-output calls — they return truncated JSON
(`IncompleteOutputException`) — so rows run one at a time and the judge is
given an explicit `max_tokens=4096`. Expect roughly a minute per answer row.

## Run status (2026-11-11)

`status: draft` on purpose: the harness runs at full size, but its answer
metrics have not yet been produced by the shipped models. The first full
29-case run was captured on the merged eval-harness branch on 2026-11-10: all
15 answer rows and all 14 refusal rows scored, refusal discipline 14/14, and
correctness 9/15 (faithfulness 0.561, context utilization 0.875 over the 12
non-refused answer rows). See
[Full validator sweep on the eval-harness branch](/runs/validator-sweep-2026-11-10.md).

Those answer numbers are a **plumbing result, not a baseline**. `spark-x2.5-4b`
(the pinned `CHAT_MODEL`) failed to load on the LocalAI host, so the agent ran
on `spark-x2.5-1.7b-q4`; the judge ran as
`mtplx-qwen36-35b-a3b-optimized-speed-fp16` because the configured `EVAL_MODEL`
id had been renamed. Embeddings were unaffected, so retrieval, citation
resolution, and the metric pipeline were the real thing. A full run on healthy,
shipped generator and judge models is still open (`../PLAN.md` Phase 3), and
`PLAN.md` still has no numeric pass/fail threshold for these metrics; the
harness exit status currently reports completeness, not quality.

The older `evals/output/experiments/` single-row runs remain in a local
checkout only, because `output/` is gitignored. The full-run numbers above are
recorded in the run capture, not in the local artifacts.

## Known limits

* `AnswerRelevancy` and the other embedding-based metrics are not wired up:
  the judge endpoint serves no embedding model, and adding one means a new
  `EVAL_EMBED_*` variable in the repository env inventory.
* The loop is sampled and the chat model is small, so the same row can refuse
  on one run and answer on the next. One run is a capability sample, not a
  reliability measurement.

See the in-repo [`evals/README.md`](../evals/README.md) for the command
surface and `PLAN.md` for the retrieval work these answers depend on.
