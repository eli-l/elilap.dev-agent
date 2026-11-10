# tests-ai — promptfoo red-team suite

One of the repository's two end-to-end test tools. It grades the agent's
behaviour under adversarial input over the loopback `POST /v1/ask` surface:
bias, harmful content, PII, excessive agency, hijacking, hallucination, and
fabricated citations. Answer quality is graded separately by Ragas in
[`../evals/`](../evals/README.md).

promptfoo is pinned as a local dev dependency (`package.json` +
`package-lock.json`), so a run is reproducible and does not depend on whatever
`npx ...@latest` resolves to. Everything the suite produces stays in this
directory.

## Layout

| Path | Committed | What it is |
|---|---|---|
| `promptfooconfig.yaml` | yes | The full scan: target, purpose, plugins, strategies, volume. |
| `promptfooconfig.smoke.yaml` | yes | One-plugin wiring check that runs fully locally. |
| `package.json`, `package-lock.json` | yes | Pinned promptfoo `0.125.1` and the run scripts. |
| `run-promptfoo.sh` | yes | The one place the suite's local-only environment is set. |
| `.npmrc` | yes | `save-exact=true`, so adding a dependency never introduces a range. |
| `go.mod` | yes | Module marker only: keeps the repo-root `go build ./...` out of `node_modules/` (promptfoo ships Go sources). Not a Go package. |
| `README.md` | yes | This file. |
| `reports/.gitkeep` | yes | Keeps the report directory in a fresh checkout. |
| `redteam.yaml` | no | Generated attack probes for the full scan. May contain offensive text. |
| `reports/results.json` | no | Full eval export for the last full run. |
| `reports/results.html` | no | Static HTML report for the last full run. |
| `reports/smoke/*` | no | Generated probes and reports for the last smoke run. |
| `.promptfoo/` | no | promptfoo's local database, cache, and logs. |
| `node_modules/` | no | Installed dev dependency tree. |
| `.env` | no | Optional promptfoo credentials for remote-only plugins. |

## Local model

Both configs point promptfoo's red-team provider at the same local
OpenAI-compatible endpoint the Ragas judge uses, so attack generation for
local-capable plugins and **all grading** run on this host with no OpenAI key:

```yaml
redteam:
  provider:
    id: openai:chat:mtplx-qwen36-35b-a3b-optimized-speed
    config:
      apiBaseUrl: http://localhost:8000/v1
      apiKey: local
```

These values mirror the `EVAL_BASE_URL` / `EVAL_MODEL` / `EVAL_API_KEY`
defaults in [`../example.env`](../example.env); change both files together if
the judge endpoint moves.

promptfoo still marks some plugins and strategies as **remote-only**
(`harmful:*`, the bias plugins, `hijacking`, `politics`, `bola`/`bfla`/`rbac`,
`jailbreak:meta`, …). When the full config enables them, they use
promptfoo-hosted inference instead of the local model; that is a promptfoo
limitation, not a setting here. The smoke config uses only a local-capable
plugin and disables remote generation, so it is a fully local run.

## Prerequisites

* Node.js ≥ 20.
* A populated, embedded `index.db`, and the agent server on the **evaluation
  port** (the suite targets `http://127.0.0.1:18080/v1/ask`, so the interactive
  server on 8080 is untouched):

  ```bash
  ./agent serve --addr 127.0.0.1:18080      # from the repository root
  ```

* The local judge model from [`../example.env`](../example.env) reachable at
  `http://localhost:8000/v1` (no key needed locally).
* Optional: promptfoo credentials if you want the remote-only plugins. These
  are separate from the agent's `LOCALAI_*` variables and must never be
  committed. Put them in a gitignored `tests-ai/.env`, which promptfoo loads
  from the working directory.

## Setup

```bash
cd tests-ai
npm ci
```

`run-promptfoo.sh` sets `PROMPTFOO_CONFIG_DIR=.promptfoo`, so the promptfoo
database, cache, and logs live here instead of `~/.promptfoo`. It also sets
`PROMPTFOO_DISABLE_SHARING=true` and `PROMPTFOO_DISABLE_TELEMETRY=true` (a run
is not uploaded anywhere) and `CI=true`, because promptfoo otherwise stops
`redteam eval` at an interactive work-email prompt.

## Run

Start with the smoke check; it proves the harness, local model, target, and
report writing work end to end in about a minute:

```bash
npm run redteam:smoke        # one plugin, one probe, fully local
ls reports/smoke/            # redteam.yaml, results.json, results.html
```

Then run the full scan:

```bash
npm run redteam              # generate redteam.yaml, then evaluate it
```

Split the two steps when you want to inspect or re-run only one:

```bash
npm run redteam:generate     # regenerates redteam.yaml from the config
npm run redteam:eval         # evaluates the existing redteam.yaml
npm run redteam:report       # browser UI on http://localhost:15500
```

`redteam:eval` writes the full results to `reports/results.json` and
`reports/results.html`; open the HTML file directly for a static report.
Generation is cached by config content, so re-running `npm run redteam`
without a config change does not regenerate attacks. Pass promptfoo flags to a
single step after `--`:

```bash
npm run redteam:generate -- --force            # regenerate regardless
npm run redteam:eval -- -j 2 --filter-sample 10  # smaller, slower/faster run
```

## Reading results

* A refusal (`"Not in my published writing"`) is the expected safe response to
  an off-corpus or adversarial question; it is not a retrieval failure.
* `rag-source-attribution` means the answer cited a document that does not
  exist. The loop already collapses unresolvable citations to a refusal, so a
  finding usually means the answer text itself quoted something it should not
  have.
* The target's `transformResponse: json.answer` shows the grader the answer
  text only, not the structured `citations` array. The text normally contains
  the `chunk_id` / `source_url` inline.
* The agent's local chat model is non-deterministic: re-run a suspected
  finding before treating it as a real vulnerability.

## Safety

The suite sends deliberately harmful and manipulative prompts, and
`redteam.yaml` / the reports contain generated attack text. They are
gitignored on purpose and must stay local. Keep the target on loopback: the
a token-less server refuses any non-loopback bind, a public bind needs
`--bind-public` plus a token, and this suite should never point at a public
deployment.

## Updating promptfoo

The version is pinned exactly. To bump it, change `devDependencies.promptfoo`
in `package.json`, run `npm install` (which updates `package-lock.json`), and
commit both files together with any config or report-shape changes. Do not
replace the scripts with `npx promptfoo@latest`: the suite's output should be
reproducible from the lockfile.
