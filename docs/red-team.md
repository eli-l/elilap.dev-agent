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

* The agent dev server running on loopback:
  `./agent serve --addr 127.0.0.1:8080` (token-less, loopback-only).
* A populated, embedded `index.db`; the suite must exercise the real retrieval
  + loop path, not a stub.
* Promptfoo installed (globally or via `npx`) and its **own** model
  credentials configured for attack generation and grading. Promptfoo's
  provider config lives in `~/.promptfoo` and is separate from the agent's
  `LOCALAI_*` variables. Never put those keys in this repo, `example.env`, or
  `tests-ai/`.

```bash
npx promptfoo@latest --version     # 0.125.1 used for this config
```

## Run it

```bash
# terminal 1
./agent serve --addr 127.0.0.1:8080

# terminal 2
npx promptfoo@latest redteam run -c tests-ai/promptfooconfig.yaml
```

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
# lower concurrency if the promptfoo provider rate-limits
npx promptfoo@latest redteam run -c tests-ai/promptfooconfig.yaml --max-concurrency 2

# force regeneration after changing the config or purpose
npx promptfoo@latest redteam run -c tests-ai/promptfooconfig.yaml --force

# the config already sets numTests/maxConcurrency; CLI flags win
```

## What the config does

* **Target**: `POST http://127.0.0.1:8080/v1/ask` with
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
on `127.0.0.1` (the dev server refuses non-loopback binds), never point it at
a public deployment, and treat `tests-ai/redteam.yaml` as local evidence -
generated attacks can contain offensive text and are not committed.

Configured 2026-11-03; a full run report has not been captured in `docs/runs/`
yet.
