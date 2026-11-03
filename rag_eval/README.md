# RAG evaluation (Ragas)

A small Python harness for scoring grounded answers with
[Ragas](https://docs.ragas.io): an endpoint smoke test, a discrete
correctness metric, and per-question tracing. It is **experimental** — the
system under test is still the upstream Ragas demo RAG, not the Go agent.

> The Go retrieval eval in `../eval/queries.yaml` (run with
> `go run -mod=vendor ./scripts/eval`) grades raw retrieval. This harness
> grades end-to-end answers with an LLM judge.

## What is here

| Path | Purpose |
|---|---|
| `rag.py` | The demo RAG under test: a keyword retriever over five in-memory documents, an OpenAI-compatible chat call, and JSON tracing. |
| `evals.py` | Ragas `Dataset` + `@experiment` + `DiscreteMetric`: asks three questions, scores each answer `pass`/`fail`, writes an experiment CSV. |
| `test_llm.py` | Three-step endpoint check: `GET /v1/models`, one chat completion, one schema-constrained metric score. Run this before an eval. |
| `evals/` | Generated data (gitignored): `datasets/`, `experiments/`, `logs/`. |

`uv.lock` is committed so installs are reproducible; `.venv/` is not.

## Setup

```bash
cd rag_eval
uv sync                 # creates .venv/ (Python 3.12)
```

The harness reads **only** these variables (defaults in parentheses):

| Variable | Default | Notes |
|---|---|---|
| `EVAL_BASE_URL` | `http://localhost:8000/v1` | Any OpenAI-compatible endpoint |
| `EVAL_MODEL` | `mtplx-qwen36-35b-a3b-optimized-speed` | Must be served by that endpoint |
| `EVAL_API_KEY` | `local` | Local servers ignore it; required non-empty by the SDK |

They are listed in `../example.env` with the same defaults. The Python code
does **not** load `../.env` itself, so either export them or pass the file to
uv:

```bash
uv run --env-file ../.env python test_llm.py
```

## Run

```bash
uv run python test_llm.py    # exit 0 = endpoint, generation, and metric work
uv run python evals.py       # exit 0 = every question scored "pass"
```

`evals.py` writes:

```text
evals/datasets/test_dataset.csv     the questions + grading notes
evals/experiments/<name>.csv        answers, scores, trace-log paths
evals/logs/rag_run_*.json           one trace file per question
```

All three locations are gitignored; only the `.gitkeep` files are tracked.

## Next step

Point the experiment at the service instead of the demo RAG: replace
`default_rag_client` in `evals.py` with a client that calls
`POST http://127.0.0.1:8080/v1/ask` and returns its `answer` plus citations
as `grading_notes` context. The metric, dataset, and output layout stay.
