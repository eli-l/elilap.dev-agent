"""Ragas evaluation of the Go agent — the only entry point in this directory.

It does one job end to end: read the hand-verified configs in ``configs/``, ask
the real agent each question, score the answers with Ragas, and write CSV
results to ``output/``.

Two tests, one tool:

* ``answers``  — answerable questions. Scores ``Faithfulness`` (is every claim
  supported by the cited excerpts), ``ContextUtilization`` (did the answer use
  the retrieved context), and a ``correctness`` DiscreteMetric (does it state
  the facts a reviewer recorded).
* ``refusals`` — questions the corpus cannot answer. Scores
  ``refusal_discipline``: does it decline instead of inventing?

Metrics are LLM-judged, so they are sampled and slow. Rows are serialized on
purpose: one local model server cannot sustain parallel structured-output
calls, and parallel runs come back truncated.

    uv run python evaluate.py --kind answers --limit 3   # smoke test
    uv run python evaluate.py                            # everything
"""

from __future__ import annotations

import argparse
import asyncio
import os
import sys
from pathlib import Path

import yaml
from openai import AsyncOpenAI

from agent import AgentRAG
from ragas import Dataset, experiment
from ragas.backends import LocalCSVBackend
from ragas.llms import llm_factory
from ragas.metrics import DiscreteMetric
from ragas.metrics.collections import ContextUtilization, Faithfulness

HERE = Path(__file__).resolve().parent
CONFIG_DIR = HERE / "configs"
OUTPUT_DIR = HERE / "output"
ANSWER_CONFIG = CONFIG_DIR / "answers.yaml"
REFUSAL_CONFIG = CONFIG_DIR / "refusals.yaml"

DEFAULT_BASE_URL = "http://localhost:8000/v1"
DEFAULT_MODEL = "mtplx-qwen36-35b-a3b-optimized-speed"
DEFAULT_API_KEY = "local"

BASE_URL = os.getenv("EVAL_BASE_URL") or DEFAULT_BASE_URL
MODEL = os.getenv("EVAL_MODEL") or DEFAULT_MODEL
API_KEY = os.getenv("EVAL_API_KEY") or DEFAULT_API_KEY

# Datasets and experiments are written side by side under output/.
backend = LocalCSVBackend(root_dir=str(OUTPUT_DIR))

# One row at a time; see the module docstring.
_ROW_LOCK = asyncio.Semaphore(1)


# --- config -> rows -------------------------------------------------------


def load_cases(path: Path) -> list[dict]:
    """Read one config file and return its cases."""
    if not path.exists():
        raise FileNotFoundError(f"config not found: {path}")
    raw = yaml.safe_load(path.read_text())
    cases = raw.get("queries") or []
    if not cases:
        raise ValueError(f"{path} contains no cases")
    return cases


def grading_notes(case: dict) -> str:
    """Compose the reviewer's recorded facts into one grading note."""
    facts = case.get("must_mention") or []
    parts = []
    if facts:
        parts.append("A correct answer must state: " + ", ".join(facts) + ".")
    note = (case.get("note") or "").strip()
    if note:
        parts.append(note)
    return " ".join(parts) or "No specific facts recorded."


def answer_rows(path: Path = ANSWER_CONFIG) -> list[dict]:
    """Rows for the answer-quality experiment (answerable questions)."""
    return [
        {
            "case_id": case["id"],
            "question": case["query"],
            "grading_notes": grading_notes(case),
            "expected_chunk_ids": " ".join(case.get("expect_chunk_ids") or []),
        }
        for case in load_cases(path)
        if case.get("kind", "answer") == "answer"
    ]


def refusal_rows(path: Path = REFUSAL_CONFIG) -> list[dict]:
    """Rows for the refusal experiment (questions the corpus cannot answer)."""
    return [
        {
            "case_id": case["id"],
            "question": case["query"],
            "grading_notes": grading_notes(case),
        }
        for case in load_cases(path)
        if case.get("kind", "refusal") == "refusal"
    ]


def build(name: str, rows: list[dict]) -> Dataset:
    """Create a CSV-backed Ragas dataset under output/datasets/."""
    dataset = Dataset(name=name, backend=backend)
    for row in rows:
        dataset.append(row)
    dataset.save()
    return dataset


# --- judge and metrics ----------------------------------------------------


def build_llm():
    """Judge LLM from the same OpenAI-compatible endpoint as the agent.

    ``max_tokens`` is explicit: the metric prompts ask for statement-level JSON
    verdicts, and the server's default output cap truncated them mid-JSON
    (``IncompleteOutputException``). 4096 is enough for the largest metric
    payload with room to spare, and still bounded.
    """
    client = AsyncOpenAI(base_url=BASE_URL, api_key=API_KEY)
    return llm_factory(model=MODEL, provider="openai", client=client, max_tokens=4096)


llm = build_llm()

faithfulness = Faithfulness(llm=llm)
context_utilization = ContextUtilization(llm=llm)

correctness = DiscreteMetric(
    name="correctness",
    prompt=(
        "Decide whether the response states the facts in the grading notes, "
        "using only what the response itself says. Different wording is fine; "
        "a missing or contradicted fact is not. Return 'pass' or 'fail'.\n"
        "Response: {response}\nGrading Notes: {grading_notes}"
    ),
    allowed_values=["pass", "fail"],
)

refusal_discipline = DiscreteMetric(
    name="refusal_discipline",
    prompt=(
        "The sources do not contain the answer to this question. A correct "
        "response declines to answer and invents no facts. Return 'pass' if it "
        "declines, 'fail' if it answers anyway or invents details.\n"
        "Question: {question}\nResponse: {response}"
    ),
    allowed_values=["pass", "fail"],
)


async def _score(metric_call, attempts: int = 2):
    """Score with one retry: the judge can truncate structured output."""
    for attempt in range(attempts):
        try:
            return await metric_call()
        except Exception:
            if attempt == attempts - 1:
                raise


# --- experiments ----------------------------------------------------------


@experiment()
async def answer_quality(row, rag: AgentRAG | None = None):
    async with _ROW_LOCK:
        rag = rag or AgentRAG()
        result = await asyncio.to_thread(rag.query, row["question"])

        # A refusal has no claims to check; scoring it would measure noise.
        if result.refused or not result.contexts:
            return {
                **row,
                "answer": result.answer,
                "refused": result.refused,
                "citations": len(result.citations),
                "faithfulness": None,
                "context_utilization": None,
                "correctness": "fail",
            }

        faith = await _score(
            lambda: faithfulness.ascore(
                user_input=row["question"],
                response=result.answer,
                retrieved_contexts=result.contexts,
            )
        )
        utilization = await _score(
            lambda: context_utilization.ascore(
                user_input=row["question"],
                response=result.answer,
                retrieved_contexts=result.contexts,
            )
        )
        graded = await _score(
            lambda: correctness.ascore(
                llm=llm,
                response=result.answer,
                grading_notes=row["grading_notes"],
            )
        )

        return {
            **row,
            "answer": result.answer,
            "refused": result.refused,
            "citations": len(result.citations),
            "faithfulness": faith.value,
            "context_utilization": utilization.value,
            "correctness": graded.value,
        }


@experiment()
async def refusal_check(row, rag: AgentRAG | None = None):
    async with _ROW_LOCK:
        rag = rag or AgentRAG()
        result = await asyncio.to_thread(rag.query, row["question"])
        graded = await _score(
            lambda: refusal_discipline.ascore(
                llm=llm,
                question=row["question"],
                response=result.answer,
            )
        )
        return {
            **row,
            "answer": result.answer,
            "refused": result.refused,
            "citations": len(result.citations),
            "refusal_discipline": graded.value,
        }


# --- reporting ------------------------------------------------------------


def summarize_answers(results) -> int:
    n = len(results)
    if not n:
        print("no rows scored")
        return 1
    faith = [r["faithfulness"] for r in results if r.get("faithfulness") is not None]
    util = [
        r["context_utilization"]
        for r in results
        if r.get("context_utilization") is not None
    ]
    passed = sum(1 for r in results if r.get("correctness") == "pass")
    refused = sum(1 for r in results if r.get("refused"))

    print(f"\nrows scored        : {n}")
    print(f"correctness pass   : {passed}/{n}")
    print(f"refused            : {refused}/{n}")
    if faith:
        print(f"faithfulness mean  : {sum(faith) / len(faith):.3f} (n={len(faith)})")
    if util:
        print(f"context util. mean : {sum(util) / len(util):.3f} (n={len(util)})")
    return 0


def summarize_refusals(results) -> int:
    n = len(results)
    if not n:
        print("no rows scored")
        return 1
    passed = sum(1 for r in results if r.get("refusal_discipline") == "pass")
    exact = sum(1 for r in results if r.get("refused"))
    print(f"\nrows scored        : {n}")
    print(f"refusal discipline : {passed}/{n}")
    print(f"set refused flag   : {exact}/{n}")
    return 0


async def main() -> int:
    parser = argparse.ArgumentParser(description="Ragas evaluation of the Go agent.")
    parser.add_argument(
        "--kind",
        choices=["answers", "refusals", "both"],
        default="both",
        help="which dataset(s) to run",
    )
    parser.add_argument("--limit", type=int, default=0, help="score only the first N rows")
    parser.add_argument("--transport", choices=["cli", "http"], default="cli")
    parser.add_argument(
        "--ask-url",
        default="http://127.0.0.1:8080/v1/ask",
        help="POST target when --transport http",
    )
    args = parser.parse_args()

    rag = AgentRAG(transport=args.transport, ask_url=args.ask_url)
    failed = 0

    if args.kind in {"answers", "both"}:
        rows = answer_rows()[: args.limit or None]
        dataset = build("agent_answers", rows)
        print(f"answers: {len(dataset)} rows")
        results = await answer_quality.arun(dataset, backend=backend)
        if len(results) != len(dataset):
            print(f"only {len(results)}/{len(dataset)} rows scored")
            failed = 1
        failed |= summarize_answers(results)

    if args.kind in {"refusals", "both"}:
        rows = refusal_rows()[: args.limit or None]
        dataset = build("agent_refusals", rows)
        print(f"refusals: {len(dataset)} rows")
        results = await refusal_check.arun(dataset, backend=backend)
        if len(results) != len(dataset):
            print(f"only {len(results)}/{len(dataset)} rows scored")
            failed = 1
        failed |= summarize_refusals(results)

    print(f"\nresults: {OUTPUT_DIR / 'experiments'}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
