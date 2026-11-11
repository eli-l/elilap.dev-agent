---
type: Reference
title: Ragas answer evaluation (evals)
description: The single Python/Ragas tool in evals/ - the configs holding hand-verified ground truth, the agent-ask client that becomes the system under test, the faithfulness/context-utilization/correctness metrics, refusal discipline, the enforceable quality gates, the EVAL_* variables, the gitignored output directory, and the captured 4B-model baseline.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-11T13:20:00Z
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
| `agent.py` | The system under test. Runs `agent ask --format json` (or `POST /v1/ask` with `--transport http`) and reads the body of every cited chunk from `data/index.db`, so the metrics see exactly the excerpts the answer was allowed to cite. |
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
uv run python evaluate.py --kind answers --limit 3 --no-thresholds  # smoke
uv run python evaluate.py                            # both datasets
```

Options: `--kind answers|refusals|both`, `--limit N`, `--transport cli|http`,
`--ask-url` for the HTTP target, the `--min-*` gates, and `--no-thresholds`
for a report-only smoke run. The `cli` transport needs the binary
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

## Quality gates

`evaluate.py` fails on quality, not just completeness. A row that cannot be
scored still fails the run; when every row is scored, the thresholds decide.

| Metric | Default gate | Notes |
|---|---:|---|
| `correctness` | 70% | pass rate over answer rows |
| `faithfulness` | 0.75 | mean over non-refused answer rows |
| `context utilization` | 0.80 | mean over non-refused answer rows |
| `refusal discipline` | 100% | all refusal rows must decline |

The defaults are a regression floor for the local 4B model. The stricter
target is 80% correctness / 0.80 faithfulness:

```bash
uv run python evaluate.py --min-correctness 0.80 --min-faithfulness 0.80
```

`--no-thresholds` reports the metrics without failing a smoke run.

## Run status (2026-11-11)

The first full 29-case run on the shipped model baseline completed on
2026-11-11: `Spark-X2.6-4B-q8_0` as the generator and the default
`mtplx-qwen36-35b-a3b-optimized-speed` judge. All 15 answer rows and all 14
refusal rows scored, with no task failures.

| Metric | Result | Default gate | 80% target |
|---|---:|---:|---:|
| Correctness | 12/15 (80.0%) | 70% | 80% |
| Faithfulness | 0.864 (n=13) | 0.75 | 0.80 |
| Context utilization | 0.977 (n=13) | 0.80 | — |
| Refusal discipline | 14/14 (100%) | 100% | 100% |

The baseline meets the stricter 80%/0.80 target on this run, while the default
gate stays lower because one 29-case sample is noisy. See
[Ragas baseline on the 4B Q8 model](/runs/ragas-baseline-2026-11-11.md).

The 2026-11-10 run on the merged eval-harness branch remains historical: it
scored all 29 rows but used stand-in models (`spark-x2.5-1.7b-q4` and the
`...-fp16` judge) because the pinned `spark-x2.5-4b` would not load. See
[Full validator sweep on the eval-harness branch](/runs/validator-sweep-2026-11-10.md).
The older `evals/output/experiments/` single-row runs remain local because
`output/` is gitignored.

## Known limits

* `AnswerRelevancy` and the other embedding-based metrics are not wired up:
  the judge endpoint serves no embedding model, and adding one means a new
  `EVAL_EMBED_*` variable in the repository env inventory.
* The loop is sampled and the chat model is small, so the same row can refuse
  on one run and answer on the next. One run is a capability sample, not a
  reliability measurement; the 80% target sits exactly on the 12/15 baseline.

See the in-repo [`evals/README.md`](../evals/README.md) for the command
surface and `PLAN.md` for the retrieval work these answers depend on.
