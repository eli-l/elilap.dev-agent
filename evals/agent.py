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

Retrieved contexts matter for the Ragas metrics: ``/v1/ask`` and ``agent ask``
return citations (chunk ids + urls) but not the excerpt text. We read the body
of every cited chunk from the index, read-only, so the contexts are exactly
what the answer was allowed to see — not a fresh retrieval that might differ.
"""

from __future__ import annotations

import json
import os
import sqlite3
import subprocess
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
AGENT_BIN = REPO / "agent"
DEFAULT_DB = Path(os.getenv("AGENT_DB_PATH") or REPO / "index.db")

# The loop runs several chat completions; a hung model must not hang the eval.
ASK_TIMEOUT_SECONDS = 300

# 18080 is the evaluation port (see docs/runbook.md): the suites own it so a
# long eval run never collides with the interactive dev server on 8080.
DEFAULT_ASK_URL = "http://127.0.0.1:18080/v1/ask"


@dataclass
class AgentAnswer:
    """One grounded answer plus the context the agent was allowed to cite."""

    answer: str
    refused: bool
    citations: list[str] = field(default_factory=list)
    contexts: list[str] = field(default_factory=list)
    tool_iterations: int = 0
    elapsed_ms: int = 0


def _resolve_db(path: Path) -> Path:
    """AGENT_DB_PATH may be relative to the repo root, not to this module."""
    if path.is_absolute():
        return path
    candidate = REPO / path
    return candidate if candidate.exists() else path


def fetch_contexts(chunk_ids: list[str], db_path: Path | None = None) -> list[str]:
    """Return the body of each cited chunk, in citation order.

    Read-only URI so the eval can run while the agent holds the database. A
    chunk id that is not in the index (a fabricated citation) is skipped: the
    answer then has fewer contexts than citations, which the metrics will see
    as unsupported claims.
    """
    if not chunk_ids:
        return []
    db = _resolve_db(db_path or DEFAULT_DB)
    if not db.exists():
        return []
    conn = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    try:
        bodies: dict[str, str] = {}
        for i in range(0, len(chunk_ids), 200):
            batch = chunk_ids[i : i + 200]
            placeholders = ",".join("?" * len(batch))
            for chunk_id, body in conn.execute(
                f"select chunk_id, body from chunks where chunk_id in ({placeholders})",
                batch,
            ):
                bodies[chunk_id] = body
        return [bodies[c] for c in chunk_ids if c in bodies]
    finally:
        conn.close()


class AgentRAG:
    """Asks the real agent and returns an answer with its cited contexts."""

    def __init__(
        self,
        transport: str = "cli",
        ask_url: str = DEFAULT_ASK_URL,
        db_path: Path | None = None,
    ) -> None:
        if transport not in {"cli", "http"}:
            raise ValueError(f"transport must be 'cli' or 'http', got {transport!r}")
        self.transport = transport
        self.ask_url = ask_url
        self.db_path = db_path or DEFAULT_DB

    def query(self, question: str) -> AgentAnswer:
        payload = self._ask_cli(question) if self.transport == "cli" else self._ask_http(question)
        citations = [c["chunk_id"] for c in payload.get("citations") or [] if c.get("chunk_id")]
        return AgentAnswer(
            answer=payload.get("answer", ""),
            refused=bool(payload.get("refused")),
            citations=citations,
            contexts=fetch_contexts(citations, self.db_path),
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
