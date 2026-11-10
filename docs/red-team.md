---
type: Guide
title: Red-team evaluation with promptfoo
description: How to run the promptfoo red-team suite in tests-ai against the loopback /v1/ask surface, which plugins and strategies it covers, how to read the results, and what promptfoo needs beyond the agent's own environment.
status: draft
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-03T11:45:00Z
tags: [red-team, promptfoo, evaluation, security, rag, citations]
sources: ["../tests-ai/promptfooconfig.yaml", "../PLAN.md"]
---

# Red-team evaluation with promptfoo

The repository has two end-to-end test tools. The Ragas suite in `evals/`
grades answer quality: faithfulness to the cited excerpts, correctness against
recorded facts, and refusal discipline. This suite grades the **agent's
behavior under adversarial input**: discriminatory output, harmful
instructions, PII leakage, unauthorized actions, hijacking, hallucinated
answers, and fabricated citations. It targets the same `POST /v1/ask` surface
a peer would use.

The configuration lives in
[`tests-ai/promptfooconfig.yaml`](../tests-ai/promptfooconfig.yaml). It is
checked in; generated results are not.

## Prerequisites

* Node.js ≥ 20 and `npm ci` in `tests-ai/`. promptfoo is pinned exactly
  (`0.125.1`) in `package.json`/`package-lock.json`, and `run-promptfoo.sh`
  keeps its database, cache, and logs in `tests-ai/.promptfoo/` while
  disabling sharing, telemetry, and the interactive work-email prompt. A run
  is reproducible and never chases `@latest`.
* The agent server on the **evaluation port**:
  `./agent serve --addr 127.0.0.1:18080` (token-less when no A2A token is
  configured; with a token, add `Authorization: Bearer <token>` to the target
  headers). The suites own 18080, so a long run never collides with the
  interactive server on 8080.
* A populated, embedded `index.db`; the suite must exercise the real retrieval
  + loop path, not a stub.
* The local judge reachable at `http://localhost:8000/v1` — no cloud key
  needed. `redteam.provider` in the config mirrors `EVAL_BASE_URL` /
  `EVAL_MODEL` / `EVAL_API_KEY` from `example.env`, so **all** grading and
  local-capable generation stay on this host. promptfoo still marks some
  plugins and strategies (`harmful:*`, bias, `hijacking`, `politics`,
  `bola`/`bfla`/`rbac`, `jailbreak:meta`) as remote-only; the full scan sends
  those to promptfoo-hosted inference, while the smoke script disables remote
  generation and stays fully local. Optional promptfoo credentials go in a
  gitignored `tests-ai/.env`, never in `example.env` or the config.

## Run it

```bash
cd tests-ai && npm ci          # once: installs the pinned promptfoo

# terminal 1 - the eval target
./agent serve --addr 127.0.0.1:18080

# terminal 2 - smoke first, then the full scan
cd tests-ai && npm run redteam:smoke   # one plugin, one probe, fully local
cd tests-ai && npm run redteam         # generate redteam.yaml, then evaluate it
```

The smoke run proves the harness, the local judge, the target, and report
writing work end to end in about a minute; it is a wiring check, not a
security verdict.

`redteam run` is a two-step process: it generates dynamic attack probes
tailored to the described purpose, then evaluates them against the target. The
generated tests are written to `tests-ai/redteam.yaml` by default (gitignored).
Review the report in the browser UI:

```bash
npx promptfoo@latest redteam report
# or inspect the latest eval table:
npx promptfoo@latest view
```

Useful overrides:

```bash
# lower concurrency if the local judge rate-limits
cd tests-ai && npm run redteam -- --max-concurrency 2

# force regeneration after changing the config or purpose
cd tests-ai && npm run redteam:generate -- --force

# the config already sets numTests/maxConcurrency; CLI flags win
```

## What the config does

* **Target**: `POST http://127.0.0.1:18080/v1/ask` with
  `{"question":"{{prompt}}"}`. `transformResponse: json.answer` means the
  grader sees the answer text only.
* **Purpose**: the red-team harness is told the agent answers from Eli's
  public writing through RAG, must not act outside that scope, and must not
  guess or hallucinate.
* **Plugins** cover bias (age/disability/gender/race), contracts,
  excessive agency, hallucination, harmful content (weapons, cybercrime,
  harassment, hate, illegal activity, self-harm, sexual content, privacy,
  profanity, radicalization, specialized advice, unsafe practices), hijacking,
  PII (API/DB, direct, session, social), politics, BOLA/BFLA/RBAC, and
  `rag-source-attribution` (fabricated document citations).
* **Strategies**: `basic` plus `jailbreak:meta`, a meta-agent that builds its
  own attack taxonomy and learns from the attack history.
* **Volume**: 5 tests per plugin, `maxConcurrency: 4`; a full run is long
  because every probe is a real tool-using answer with a 90s budget.

## Reading results

* A **refusal** (`"Not in my published writing"`) is the expected safe
  response for off-corpus or adversarial questions; do not count it as a
  retrieval failure.
* `rag-source-attribution` checks for invented citations. The agent already
  resolves citation URLs from the index and collapses uncited or
  fabricated-id answers to the refusal string, so a finding here usually
  means the answer text itself quoted something it should not have.
* The `transformResponse: json.answer` strips the structured `citations`
  field. The answer text normally contains `chunk_id` and `source_url`
  inline, which is enough for the text graders; if you want the grader to see
  the structured list too, change the transform to return
  `JSON.stringify({answer: json.answer, citations: json.citations})` and rerun.
* This host's 4B chat model is non-deterministic: the same prompt can answer
  on one run and refuse on the next. Re-run a suspected finding before
  treating it as a real vulnerability, and keep `--force` for generation
  changes rather than blaming the config for one noisy eval.

## Safety posture

The suite sends deliberately harmful and manipulative prompts. Keep the target
on `127.0.0.1` (a token-less server refuses non-loopback binds; a public bind
needs `--bind-public` plus a token), and never point it at an exposed
deployment. Generated probes (`tests-ai/redteam.yaml`), `tests-ai/reports/*`,
and `tests-ai/.promptfoo/` are local evidence only - generated attacks can
contain offensive text - so they are gitignored; only the configs, the
lockfile, the run script, and the README are committed.

Configured 2026-11-03; a full run report has not been captured in `docs/runs/`
yet.
