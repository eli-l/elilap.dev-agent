---
type: Reference
title: Ragas answer evaluation (evals)
description: The single Python/Ragas tool in evals/ - the configs holding hand-verified ground truth, the agent-ask client that becomes the system under test, the faithfulness/context-utilization/correctness metrics, refusal discipline, the EVAL_* variables, and the gitignored output directory.
status: draft
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-04T12:10:56Z
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
with `./agent serve --port 18080` in its own terminal.

Outputs land under `evals/output/`: `datasets/*.csv` (the rows),
`experiments/*.csv` (one row per question with every metric), and any trace
files. Values are overwritten per run and never committed.

## Why it is slow, and why rows are serialized

Each row is two or three LLM-judged calls, and faithfulness decomposes the
answer into statements. One local model server cannot sustain parallel
structured-output calls — they return truncated JSON
(`IncompleteOutputException`) — so rows run one at a time and the judge is
given an explicit `max_tokens=4096`. Expect roughly a minute per answer row.

## Run status (2026-11-05)

`status: draft` on purpose: the tool is written and smoke-tested, but no full
run has been captured. `evals/output/experiments/` holds three single-row runs
covering two cases (`--limit 1`): one answer case twice (faithfulness 1.00 then
0.57, correctness `pass` both times) and one off-corpus refusal at `pass`. The
15 answer and 14 refusal cases in `configs/` have therefore never been scored
in one run, so "the agent meets the Ragas thresholds" is still an open Phase 2
exit criterion (`../PLAN.md`). Nothing in the harness is missing to close it —
only the run and its `docs/runs/` capture.

## Known limits

* `AnswerRelevancy` and the other embedding-based metrics are not wired up:
  the judge endpoint serves no embedding model, and adding one means a new
  `EVAL_EMBED_*` variable in the repository env inventory.
* The loop is sampled and the chat model is small, so the same row can refuse
  on one run and answer on the next. One run is a capability sample, not a
  reliability measurement.

See the in-repo [`evals/README.md`](../evals/README.md) for the command
surface and `PLAN.md` for the retrieval work these answers depend on.
