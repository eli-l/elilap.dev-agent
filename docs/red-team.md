---
type: Guide
title: Red-team evaluation with promptfoo
description: How to run the promptfoo red-team suite in tests-ai against the loopback /v1/ask surface with the local judge model, which plugins and strategies it covers, how to read the results, and the promptfoo remote-only caveat.
status: draft
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-05T11:51:22Z
tags: [red-team, promptfoo, evaluation, security, rag, citations]
sources: ["../tests-ai/README.md", "../tests-ai/promptfooconfig.yaml", "../tests-ai/promptfooconfig.smoke.yaml", "../tests-ai/run-promptfoo.sh", "../tests-ai/package.json", "../PLAN.md"]
---

# Red-team evaluation with promptfoo

The repository has two end-to-end test tools. The Ragas suite in `evals/`
grades answer quality: faithfulness to the cited excerpts, correctness against
recorded facts, and refusal discipline. This suite grades the **agent's
behavior under adversarial input**: discriminatory output, harmful
instructions, PII leakage, unauthorized actions, hijacking, hallucinated
answers, and fabricated citations. It targets the same `POST /v1/ask` surface
a peer would use.

The suite is self-contained under [`tests-ai/`](../tests-ai/README.md): the
checked-in scan config, a pinned promptfoo dev dependency, and a report
directory for generated output. All of its artefacts - generated attacks,
eval exports, static report, and promptfoo's local database - stay in that
folder.

## Prerequisites

* Node.js ≥ 20 and `npm ci` in `tests-ai/`. promptfoo is pinned exactly
  (`0.125.1`) in `package.json`/`package-lock.json`, so runs are reproducible
  and do not chase `@latest`.
* The agent dev server on the evaluation port: `./agent serve --port 18080`
  (token-less, loopback-only by default). The suite targets
  `127.0.0.1:18080`, so the interactive server on 8080 is unaffected.
* A populated, embedded `index.db`; the suite must exercise the real retrieval
  + loop path, not a stub.
* The local judge model from [`../example.env`](../example.env) reachable at
  `http://localhost:8000/v1`. promptfoo uses it for attack generation
  (local-capable plugins) and for grading, so no OpenAI key is needed.
* Optional: promptfoo credentials for the remote-only plugins. Those are
  separate from the agent's `LOCALAI_*` variables and must never be committed;
  put them in a gitignored `tests-ai/.env`, which promptfoo loads from the
  working directory.

The run scripts set `PROMPTFOO_CONFIG_DIR=.promptfoo`, so promptfoo's local
database, cache, and logs stay in `tests-ai/.promptfoo/`; they disable sharing,
telemetry, and the interactive work-email prompt: a local run is not uploaded
anywhere.

## Local model

Both configs point promptfoo's red-team provider at the same endpoint the
Ragas judge uses, mirroring the `EVAL_BASE_URL` / `EVAL_MODEL` /
`EVAL_API_KEY` defaults:

```yaml
redteam:
  provider:
    id: openai:chat:mtplx-qwen36-35b-a3b-optimized-speed
    config:
      apiBaseUrl: http://localhost:8000/v1
      apiKey: local
```

That provider generates the probes for local-capable plugins and grades every
result. promptfoo still marks some plugins and strategies as **remote-only**
(`harmful:*`, the bias plugins, `hijacking`, `politics`, `bola`/`bfla`/`rbac`,
`jailbreak:meta`, …): when the full scan enables them they use
promptfoo-hosted inference, not the local model. The smoke config uses only a
local-capable plugin and disables remote generation, so it is fully local.

## Run it

```bash
# terminal 1 - the eval port, leaving 8080 for interactive work
./agent serve --port 18080

# terminal 2
cd tests-ai
npm ci
npm run redteam:smoke        # one plugin, one probe, fully local
npm run redteam              # full scan
```

`npm run redteam:smoke` is the wiring check: one `hallucination` probe,
generated and graded by the local model with remote generation disabled. It
writes `tests-ai/reports/smoke/{redteam.yaml,results.json,results.html}` and
completes in about a minute.

`npm run redteam` is the two-step process: generate dynamic attack probes
tailored to the described purpose, then evaluate them against the target. It
is equivalent to `npm run redteam:generate` followed by
`npm run redteam:eval`, which you can run separately when only one step
changed. The generated probes land in `tests-ai/redteam.yaml` and the run
writes `tests-ai/reports/results.json` plus `tests-ai/reports/results.html`.
Open the HTML file directly, or browse the local eval database with the
report UI:

```bash
npm run redteam:report       # http://localhost:15500, reads .promptfoo/
```

Useful overrides (flags after `--` go to that promptfoo step):

```bash
# lower concurrency if the promptfoo provider rate-limits
npm run redteam:eval -- -j 2

# force regeneration after changing the config or purpose
npm run redteam:generate -- --force

# evaluate only a sample and inspect the shape of the report
npm run redteam:eval -- --filter-sample 10
```

## What the config does

* **Target**: `POST http://127.0.0.1:18080/v1/ask` with
  `{"question":"{{prompt}}"}`. `transformResponse: json.answer` means the
  grader sees the answer text only.
* **Purpose**: the red-team harness is told the agent answers from Eli's
  public writing through RAG, must not act outside that scope, and must not
  guess or hallucinate.
* **Generation and grading**: `redteam.provider` is the local judge endpoint;
  local-capable plugins generate with it and every result is graded with it.
  Remote-only plugins use promptfoo-hosted inference (see above).
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
on loopback: the dev server binds `127.0.0.1` by default and only leaves it
with the explicit `--allow-public` opt-in. Never point the suite at a public
deployment. `tests-ai/redteam.yaml`, `tests-ai/reports/*`, and
`tests-ai/.promptfoo/` are generated, gitignored local evidence - generated
attacks can contain offensive text and are not committed. Only the config, the
harness manifest, and the README are checked in.

Harness established 2026-11-05 (pinned promptfoo `0.125.1`, local model for
generation and grading, npm scripts, local reports). The smoke run passes with
one generated probe and both report files; a full-scan report has not been
captured in `docs/runs/` yet.
