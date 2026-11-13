"""The RAG under test: the Go agent's grounded loop.

Everything else in ``evals/`` is the harness. This module is the only place
that knows how to ask the real agent a question, so pointing the eval at a
different transport is a change here and nowhere else.

Two transports, same loop:

* ``cli``  — ``./agent ask --format json "<question>"`` (default). No server
  process to manage; ``cmd/agent/ask.go`` and ``POST /v1/ask`` render the same
  ``agentloop.Answer``.
* ``http`` — ``POST {ask_url}`` with ``{"question": ...}``, the loopback dev
  surface a peer would actually call. The default target is port 18080, the
  port reserved for evaluation runs; 8080 stays free for interactive work.

Retrieved contexts matter for the Ragas metrics. ``/v1/ask`` and ``agent ask``
return one citation per excerpt the answer was grounded on, and each citation
carries that excerpt's text. The contexts are therefore exactly what the model
was allowed to see - not a fresh retrieval that might differ, and not read back
from the index by chunk id, which the response never exposes.
"""

from __future__ import annotations

import json
import subprocess
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
AGENT_BIN = REPO / "agent"

# The loop runs several chat completions; a hung model must not hang the eval.
ASK_TIMEOUT_SECONDS = 300

# 18080 is the evaluation port (see docs/runbook.md): the suites own it so a
# long eval run never collides with the interactive dev server on 8080.
DEFAULT_ASK_URL = "http://127.0.0.1:18080/v1/ask"


@dataclass
class AgentAnswer:
    """One grounded answer plus the evidence the agent attached to it."""

    answer: str
    refused: bool
    citations: list[dict] = field(default_factory=list)
    sources: list[dict] = field(default_factory=list)
    contexts: list[str] = field(default_factory=list)
    tool_iterations: int = 0
    elapsed_ms: int = 0


class AgentRAG:
    """Asks the real agent and returns an answer with its cited contexts."""

    def __init__(
        self,
        transport: str = "cli",
        ask_url: str = DEFAULT_ASK_URL,
    ) -> None:
        if transport not in {"cli", "http"}:
            raise ValueError(f"transport must be 'cli' or 'http', got {transport!r}")
        self.transport = transport
        self.ask_url = ask_url

    def query(self, question: str) -> AgentAnswer:
        payload = self._ask_cli(question) if self.transport == "cli" else self._ask_http(question)
        citations = list(payload.get("citations") or [])
        return AgentAnswer(
            answer=payload.get("answer", ""),
            refused=bool(payload.get("refused")),
            citations=citations,
            sources=list(payload.get("sources") or []),
            contexts=[c["excerpt"] for c in citations if c.get("excerpt")],
            tool_iterations=int(payload.get("tool_iterations") or 0),
            elapsed_ms=int(payload.get("elapsed_ms") or 0),
        )

    def _ask_cli(self, question: str) -> dict:
        if not AGENT_BIN.exists():
            raise RuntimeError(
                f"agent binary not found at {AGENT_BIN}; build it with "
                "'go build -mod=vendor -tags sqlite_fts5 -o agent ./cmd/agent'"
            )
        proc = subprocess.run(
            [str(AGENT_BIN), "ask", "--format", "json", question],
            cwd=str(REPO),
            capture_output=True,
            text=True,
            timeout=ASK_TIMEOUT_SECONDS,
        )
        if proc.returncode != 0:
            raise RuntimeError(
                f"agent ask exited {proc.returncode}: {proc.stderr.strip()[-400:]}"
            )
        return _last_json_object(proc.stdout)

    def _ask_http(self, question: str) -> dict:
        body = json.dumps({"question": question}).encode()
        req = urllib.request.Request(
            self.ask_url,
            data=body,
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=ASK_TIMEOUT_SECONDS) as resp:
                return json.loads(resp.read().decode())
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode(errors="replace")[:400]
            raise RuntimeError(f"{self.ask_url} returned {exc.code}: {detail}") from exc


def _last_json_object(stdout: str) -> dict:
    """The CLI writes one JSON object last; debug logs go to stderr."""
    for line in reversed([ln for ln in stdout.splitlines() if ln.strip()]):
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict) and "answer" in value:
            return value
    raise RuntimeError("no answer object found on stdout")
