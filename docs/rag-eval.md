---
type: Reference
title: RAG evaluation with Ragas (rag_eval)
description: The experimental Python/Ragas harness under rag_eval - the demo keyword RAG it scores, the endpoint smoke test, the discrete correctness experiment, the EVAL_* variables, and the gitignored datasets, experiments, and trace logs.
status: draft
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-03T14:50:00Z
tags: [evaluation, ragas, python, uv, llm, rag]
sources:
  - ../rag_eval/README.md
  - ../rag_eval/rag.py
  - ../rag_eval/evals.py
  - ../rag_eval/test_llm.py
  - ../example.env
---

# RAG evaluation with Ragas

The Go retrieval eval (`eval/queries.yaml`, run by `scripts/eval`) grades
**raw retrieval**: did the expected chunk appear in the top five? `rag_eval/`
is a separate, Python-side experiment that grades **end-to-end answers** with
an LLM judge. It is checked in so the harness is reviewable and reproducible,
not because it is wired into the agent yet.

## What it runs

| File | Role |
|---|---|
| `rag.py` | The system under test: `ExampleRAG`, a keyword retriever over five in-memory documents plus one chat completion, with a JSON trace per query. |
| `evals.py` | Loads three questions into a Ragas `Dataset` (`local/csv`), runs the `@experiment` per row, and scores each answer with a `DiscreteMetric` (`pass`/`fail`) against hand-written grading notes. |
| `test_llm.py` | Three-step precondition check: `/v1/models` lists the model, a chat completion returns text, and a `DiscreteMetric` produces a valid value through the same `llm_factory` path. |
| `evals/` | Output root for the CSV dataset, experiment CSVs, and trace JSON. Only the `.gitkeep` files are tracked. |

`uv.lock` is committed; `.venv/`, `__pycache__/`, `*.egg-info/`, and the
generated `evals/` files are gitignored.

## Configuration

The harness reads one endpoint, independent of the agent's `LOCALAI_*`
settings so it can score a different model:

| Variable | Default | Notes |
|---|---|---|
| `EVAL_BASE_URL` | `http://localhost:8000/v1` | Any OpenAI-compatible server |
| `EVAL_MODEL` | `mtplx-qwen36-35b-a3b-optimized-speed` | Must be served by that endpoint |
| `EVAL_API_KEY` | empty → `local` | A non-empty key is required by the SDK; local servers ignore it |

They are part of the repository inventory in `../example.env` and in
[Environment configuration](environment.md). The Python code does not load
`../.env`, so pass it explicitly (`uv run --env-file ../.env …`) or export the
variables.

## Running it

```bash
cd rag_eval
uv sync
uv run python test_llm.py     # exit 0 = endpoint, generation, metric all work
uv run python evals.py        # exit 0 = every answer scored "pass"
```

Outputs land under `rag_eval/evals/`: `datasets/test_dataset.csv`,
`experiments/<generated-name>.csv` (one row per question with `score` and
`log_file`), and `logs/rag_run_*.json`. They are overwritten per run and are
never committed.

## Known limits

* The corpus and questions are the upstream Ragas demo ("what is ragas 0.3"),
  not Eli's writing. Wiring `evals.py` to `POST /v1/ask` is the next step; the
  metric and output layout stay.
* One discrete metric (`correctness`) and one LLM judge. There is no
  retrieval-context metric yet.
* Runs need the model endpoint up; `test_llm.py` is the cheap failure check
  before burning an eval run.

See the in-repo [`rag_eval/README.md`](../rag_eval/README.md) for the command
surface and `PLAN.md` for the Go retrieval eval this is adjacent to.
