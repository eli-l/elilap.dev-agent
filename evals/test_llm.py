"""Smoke test for the local OpenAI-compatible LLM endpoint.

Finishes in three steps, each of which must pass:

1. **access**     – ``GET /v1/models`` answers and lists the configured model.
2. **generation** – a plain chat completion returns non-empty text.
3. **structured** – a ragas ``DiscreteMetric`` (the same path ``evaluate.py`` uses,
   via ``ragas.llms.llm_factory``) produces a valid discrete score.

Configured through environment variables, defaulting to the local llama-server
instance (no auth):

    EVAL_BASE_URL  default http://localhost:8000/v1
    EVAL_MODEL     default mtplx-qwen36-35b-a3b-optimized-speed
    EVAL_API_KEY   default "local"   (ignored by the server)

Run directly:

    python test_llm.py

Exit code 0 means every check passed, 1 means at least one failed.
"""

from __future__ import annotations

import asyncio
import os
import sys
import time
from typing import List, Tuple

from openai import AsyncOpenAI
from ragas.llms import llm_factory
from ragas.metrics import DiscreteMetric

DEFAULT_BASE_URL = "http://localhost:8000/v1"
DEFAULT_MODEL = "mtplx-qwen36-35b-a3b-optimized-speed"
DEFAULT_API_KEY = "local"  # local servers take any non-empty key
REQUEST_TIMEOUT = 180.0

BASE_URL = os.getenv("EVAL_BASE_URL") or DEFAULT_BASE_URL
MODEL = os.getenv("EVAL_MODEL") or DEFAULT_MODEL
API_KEY = os.getenv("EVAL_API_KEY") or DEFAULT_API_KEY

Check = Tuple[str, bool, str]


def make_client() -> AsyncOpenAI:
    """Client for the local endpoint, in async form (ragas scores asynchronously)."""
    return AsyncOpenAI(
        base_url=BASE_URL,
        api_key=API_KEY,
        timeout=REQUEST_TIMEOUT,
        max_retries=1,
    )


async def check_access(client: AsyncOpenAI) -> Check:
    """Confirm the endpoint answers and exposes the requested model."""
    try:
        listing = await client.models.list()
    except Exception as exc:  # noqa: BLE001 - report, don't crash
        return (
            "access",
            False,
            f"GET {BASE_URL}/models failed: {type(exc).__name__}: {exc}",
        )

    available = [m.id for m in listing.data]
    if MODEL in available:
        return ("access", True, f"{BASE_URL} serves {len(available)} model(s): {', '.join(available)}")
    return (
        "access",
        False,
        f"{BASE_URL} is reachable but does not serve {MODEL!r}; available: {available}",
    )


async def check_generation(client: AsyncOpenAI) -> Check:
    """Confirm the model actually generates text."""
    started = time.perf_counter()
    try:
        response = await client.chat.completions.create(
            model=MODEL,
            messages=[
                {"role": "system", "content": "Answer in one short sentence."},
                {"role": "user", "content": "What is the capital of Latvia?"},
            ],
            temperature=0,
            # The model is a reasoning model: it spends tokens in
            # `reasoning_content` before emitting `content`, so a small cap
            # truncates the answer to an empty string.
            max_tokens=1024,
        )
    except Exception as exc:  # noqa: BLE001
        return (
            "generation",
            False,
            f"chat completion failed: {type(exc).__name__}: {exc}",
        )

    elapsed = time.perf_counter() - started
    choice = response.choices[0] if response.choices else None
    message = getattr(choice, "message", None)
    text = (getattr(message, "content", None) or "").strip()
    reasoning = (getattr(message, "reasoning_content", None) or "").strip()
    finish_reason = getattr(choice, "finish_reason", None)
    usage = response.usage
    tokens = (
        f"{usage.prompt_tokens} prompt + {usage.completion_tokens} completion tokens"
        if usage
        else "usage not reported"
    )

    if not text:
        hint = (
            f"finish_reason={finish_reason!r}"
            + (f", {len(reasoning)} chars of reasoning but no answer" if reasoning else "")
        )
        return ("generation", False, f"model returned an empty completion ({hint})")
    if "riga" not in text.lower():
        return (
            "generation",
            False,
            f"model answered but not as expected ({elapsed:.1f}s): {text!r}",
        )
    return ("generation", True, f"{elapsed:.1f}s, {tokens}: {text!r}")


async def check_structured_output(client: AsyncOpenAI) -> Check:
    """Confirm ragas' metric path works, i.e. schema-constrained generation."""
    llm = llm_factory(MODEL, provider="openai", client=client)
    metric = DiscreteMetric(
        name="factual_accuracy",
        allowed_values=["correct", "incorrect"],
        prompt=(
            "Evaluate whether the response is factually correct.\n\n"
            "Question: {question}\n"
            "Response: {response}\n\n"
            "Return 'correct' or 'incorrect'."
        ),
    )

    started = time.perf_counter()
    try:
        result = await metric.ascore(
            llm=llm,
            question="What is the capital of Latvia?",
            response="Riga is the capital of Latvia.",
        )
    except Exception as exc:  # noqa: BLE001
        return (
            "structured",
            False,
            f"metric.ascore failed: {type(exc).__name__}: {exc}",
        )

    elapsed = time.perf_counter() - started
    value = getattr(result, "value", None)
    reason = getattr(result, "reason", "") or ""

    if value not in metric.allowed_values:
        return ("structured", False, f"score {value!r} outside {metric.allowed_values}")
    if value != "correct":
        return ("structured", False, f"{elapsed:.1f}s: scored a true answer as {value!r}: {reason}")
    return ("structured", True, f"{elapsed:.1f}s: {value} — {reason}")


async def main() -> int:
    print(f"endpoint : {BASE_URL}")
    print(f"model    : {MODEL}")
    print()
    client = make_client()
    results: List[Check] = [
        await check_access(client),
        await check_generation(client),
        await check_structured_output(client),
    ]

    for name, ok, detail in results:
        print(f"{'PASS' if ok else 'FAIL'}  {name:<10} {detail}", flush=True)

    failed = [name for name, ok, _ in results if not ok]
    print()
    if failed:
        print(f"{len(failed)} check(s) failed: {', '.join(failed)}", flush=True)
        return 1
    print(f"all {len(results)} checks passed against {MODEL}", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
