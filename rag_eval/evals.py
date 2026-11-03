"""Run the demo RAG through a Ragas discrete-metric experiment.

The system under test is the small in-memory keyword RAG in ``rag.py``: the
upstream Ragas example, kept as a runnable harness until the Go agent's
``POST /v1/ask`` surface is wired in as the RAG under test.

The scoring LLM and the RAG's generation model both come from one
OpenAI-compatible endpoint, configured with the same ``EVAL_*`` variables as
``test_llm.py``:

    EVAL_BASE_URL  default http://localhost:8000/v1
    EVAL_MODEL     default mtplx-qwen36-35b-a3b-optimized-speed
    EVAL_API_KEY   default "local"   (ignored by local servers)

Generated files (all gitignored) land under ``evals/``:

    evals/datasets/test_dataset.csv     the evaluation rows
    evals/experiments/<name>.csv        one row per question, with the score
    evals/logs/rag_run_*.json           RAG traces for each question

Run from this directory:

    uv run python evals.py

Exit code 0 means every question passed the metric, 1 means at least one did
not (or the run raised).
"""

from __future__ import annotations

import asyncio
import os
import sys
from pathlib import Path

from openai import OpenAI

from ragas import Dataset, experiment
from ragas.llms import llm_factory
from ragas.metrics import DiscreteMetric

# Make ``rag`` importable when this file is run as a script.
ROOT = Path(__file__).resolve().parent
sys.path.insert(0, str(ROOT))

from rag import default_rag_client  # noqa: E402  (after sys.path setup)

DEFAULT_BASE_URL = "http://localhost:8000/v1"
DEFAULT_MODEL = "mtplx-qwen36-35b-a3b-optimized-speed"
DEFAULT_API_KEY = "local"  # local servers take any non-empty key

BASE_URL = os.getenv("EVAL_BASE_URL") or DEFAULT_BASE_URL
MODEL = os.getenv("EVAL_MODEL") or DEFAULT_MODEL
API_KEY = os.getenv("EVAL_API_KEY") or DEFAULT_API_KEY

DATASET_NAME = "test_dataset"
EVAL_DIR = ROOT / "evals"


def _init_clients():
    """Build the scoring LLM and the RAG under test against one endpoint."""
    client = OpenAI(base_url=BASE_URL, api_key=API_KEY)
    llm = llm_factory(model=MODEL, provider="openai", client=client)
    rag_client = default_rag_client(
        llm_client=client,
        model=MODEL,
        logdir=str(EVAL_DIR / "logs"),
    )
    return llm, rag_client


llm, rag_client = _init_clients()


DATA_SAMPLES = [
    {
        "question": "What is ragas 0.3",
        "grading_notes": (
            "- experimentation as the central pillar "
            "- provides abstraction for datasets, experiments and metrics "
            "- supports evals for RAG, LLM workflows and Agents"
        ),
    },
    {
        "question": "how are experiment results stored in ragas 0.3?",
        "grading_notes": (
            "- configured using different backends like local, gdrive, etc "
            "- stored under experiments/ folder in the backend storage"
        ),
    },
    {
        "question": "What metrics are supported in ragas 0.3?",
        "grading_notes": (
            "- provides abstraction for discrete, numerical and ranking metrics"
        ),
    },
]


def load_dataset() -> Dataset:
    """Write the fixed evaluation rows and return them as a Ragas dataset."""
    dataset = Dataset(
        name=DATASET_NAME,
        backend="local/csv",
        root_dir=str(EVAL_DIR),
    )
    for sample in DATA_SAMPLES:
        dataset.append(sample)
    dataset.save()
    return dataset


my_metric = DiscreteMetric(
    name="correctness",
    prompt=(
        "Check if the response contains points mentioned from the grading "
        "notes and return 'pass' or 'fail'.\n"
        "Response: {response} Grading Notes: {grading_notes}"
    ),
    allowed_values=["pass", "fail"],
)


@experiment()
async def run_experiment(row):
    # ``rag_client`` is synchronous (blocking HTTP), so keep it off the loop.
    response = await asyncio.to_thread(rag_client.query, row["question"])

    score = await my_metric.ascore(
        llm=llm,
        response=response.get("answer", ""),
        grading_notes=row["grading_notes"],
    )

    return {
        **row,
        "response": response.get("answer", ""),
        "score": score.value,
        "log_file": response.get("logs", ""),
    }


async def main() -> int:
    dataset = load_dataset()
    dataset_path = EVAL_DIR / "datasets" / f"{DATASET_NAME}.csv"
    print(f"dataset: {len(dataset)} samples ({dataset_path})")

    results = await run_experiment.arun(dataset)

    passed = sum(1 for row in results if row.get("score") == "pass")
    experiment_path = EVAL_DIR / "experiments" / f"{results.name}.csv"
    print(f"experiment {results.name}: {passed}/{len(results)} passed")
    print(f"results: {experiment_path}")

    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
