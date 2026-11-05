# Ragas evaluation

One tool for one task: score the Go agent's grounded answers with Ragas.

```
uv run python evaluate.py
```

That asks the real agent every question in `configs/`, scores each answer, and
writes CSV results to `output/`. There is no other entry point.

## Layout

| Path | What it is |
|---|---|
| `evaluate.py` | The tool. Loads configs, asks the agent, runs the metrics, reports. |
| `agent.py` | The system under test: calls `agent ask --format json` (or `POST /v1/ask` with `--transport http`) and reads the body of every cited chunk from `index.db`, so Ragas scores against exactly the excerpts the answer was allowed to cite. |
| `configs/answers.yaml` | 15 answerable questions with verified evidence and the facts a correct answer must state. |
| `configs/refusals.yaml` | 14 questions the corpus cannot answer. |
| `output/` | Generated CSV datasets, experiment results, and traces. Gitignored. |
| `test_llm.py` | Endpoint precondition check: `/v1/models`, one completion, one structured score. Run it before a long eval — not part of the scoring. |

`uv.lock` is committed so installs are reproducible; `.venv/` is not.

## What is scored

**`answers`** — three Ragas metrics per row:

| Metric | Question it answers |
|---|---|
| `Faithfulness` | Is every claim in the answer supported by the cited excerpts? |
| `ContextUtilization` | Did the answer actually draw on the retrieved context? |
| `correctness` (DiscreteMetric) | Does it state the facts recorded in the review note? |

A refusal on an answerable row counts as `correctness: fail` and skips the two
numeric metrics — there are no claims to check.

**`refusals`** — one metric:

| Metric | Question it answers |
|---|---|
| `refusal_discipline` (DiscreteMetric) | Did it decline instead of inventing an answer? |

## Config format

Both configs share one shape:

```yaml
version: 1
queries:
  - id: unique-name
    kind: answer          # answer | refusal
    query: the question
    expect_chunk_ids: [<64-hex chunk id>, ...]   # evidence a human verified
    must_mention: [Proxmox]                      # facts a correct answer states
    note: reviewer context, also fed to the judge
```

`must_mention` becomes the grading note for `correctness`; `expect_chunk_ids`
is kept for traceability (and lets you confirm a citation is real evidence).

Add a case by reading the chunk first, then copying its full 64-character
`chunk_id`:

```bash
sqlite3 index.db "select chunk_id, section, body from chunks where title='<post>' order by rowid"
```

For a refusal case, prove the absence rather than assuming it:

```bash
sqlite3 index.db "select count(*) from chunks where body like '%<term>%'"
```

## Running

```bash
uv sync

uv run python test_llm.py                        # endpoint check, exit 0 = ok
uv run python evaluate.py --kind answers --limit 3   # cheap smoke test
uv run python evaluate.py --kind answers
uv run python evaluate.py --kind refusals
uv run python evaluate.py                        # both
```

Options:

| Flag | Meaning |
|---|---|
| `--kind answers\|refusals\|both` | Which dataset to run (default `both`). |
| `--limit N` | Score only the first N rows. |
| `--transport cli\|http` | `cli` shells out to `./agent ask`; `http` POSTs to `--ask-url`. |
| `--ask-url URL` | Target for `--transport http` (default `http://127.0.0.1:8080/v1/ask`). |

Build the binary first if you use the `cli` transport:

```bash
go build -mod=vendor -tags sqlite_fts5 -o agent ./cmd/agent
```

## Configuration

| Variable | Default | Notes |
|---|---|---|
| `EVAL_BASE_URL` | `http://localhost:8000/v1` | Judge endpoint (any OpenAI-compatible server) |
| `EVAL_MODEL` | `mtplx-qwen36-35b-a3b-optimized-speed` | Model the judge uses |
| `EVAL_API_KEY` | empty → `local` | Required non-empty by the SDK; local servers ignore it |

They are listed in `../example.env`. The harness does not load `../.env`
itself, so pass it explicitly (`uv run --env-file ../.env python evaluate.py`)
or export the variables.

## Why it is slow, and why rows run one at a time

Each row is two or three LLM-judged metric calls, and faithfulness decomposes
the answer into statements. One local model server cannot sustain parallel
structured-output calls — they come back truncated
(`IncompleteOutputException`) — so rows are serialized and the judge is given
an explicit `max_tokens=4096`. Expect roughly a minute per answer row.

## Known limits

* No `reference` answer column: writing one per question from the excerpts is
  how a gold set quietly becomes fiction. `correctness` grades against the
  reviewer's recorded facts instead.
* `AnswerRelevancy` and the embedding-based metrics are not wired up: the
  judge endpoint serves no embedding model, and adding one would mean a new
  `EVAL_EMBED_*` variable in the repo's env inventory.
* The agent's loop is sampled and the chat model is small, so the same row can
  refuse on one run and answer on the next. One run is a capability sample,
  not a reliability measurement — run several and compare.
