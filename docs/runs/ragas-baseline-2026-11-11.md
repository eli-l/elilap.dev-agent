---
type: Run
title: Ragas baseline on the 4B Q8 model
description: First full 29-case Ragas run on the shipped Spark-X2.6-4B-q8_0 baseline with enforceable quality gates - 12/15 correctness, 0.864 faithfulness, 0.977 context utilization, and 14/14 refusal discipline.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-11T13:20:00Z
tags: [validation, ragas, baseline, thresholds, localai]
run:
  type: ragas-baseline
  cmd: |
    cd evals
    CHAT_MODEL=Spark-X2.6-4B-q8_0 uv run python evaluate.py
output: |
  answers: 15 rows

  rows scored        : 15
  thresholds         : correctness >= 70%, faithfulness >= 0.75, context util. >= 0.80
  correctness pass   : 12/15 (80.0%) PASS
  refused            : 2/15
  faithfulness mean  : 0.864 (n=13) PASS
  context util. mean : 0.977 (n=13) PASS
  refusals: 14 rows

  rows scored        : 14
  thresholds         : refusal discipline >= 100%
  refusal discipline : 14/14 (100.0%) PASS
  set refused flag   : 14/14

  results: /Users/eli/projects/elilap.dev-agent/evals/output/experiments
  exit 0
---

# Result

This is the first complete Ragas run on the shipped model baseline, not a
stand-in:

- **Generator**: `Spark-X2.6-4B-q8_0` (the host's working 4B q8 model; see
  [Self-hosted LocalAI endpoint and model catalog](/localai.md)).
- **Judge**: `mtplx-qwen36-35b-a3b-optimized-speed` on `localhost:8000`
  (`EVAL_MODEL` default).
- **Loop**: 3 tool iterations, 3,000 estimated excerpt tokens
  (4-chars-per-token), 2,048 completion tokens, and compact tool results
  that carry only citation fields plus body. All 29 rows scored with no task
  failures.

The default gates are a regression floor for this local model: correctness
≥ 70%, faithfulness ≥ 0.75, context utilization ≥ 0.80, and refusal
discipline 100%. The baseline also meets the stricter target of 80%
correctness / 0.80 faithfulness on this run:

| Metric | Result | Default gate | 80% target |
|---|---|---:|---:|
| Correctness | 12/15 (80.0%) | 70% | 80% |
| Faithfulness | 0.864 (n=13) | 0.75 | 0.80 |
| Context utilization | 0.977 (n=13) | 0.80 | — |
| Refusal discipline | 14/14 (100%) | 100% | 100% |

Two answerable rows refused, so correctness is the metric to watch. One run is
a capability sample, not a reliability measurement; a re-run can move a row or
two. To enforce the target rather than the floor:

```bash
cd evals
CHAT_MODEL=Spark-X2.6-4B-q8_0 uv run python evaluate.py \
  --min-correctness 0.80 --min-faithfulness 0.80
```

See [Ragas answer evaluation (evals)](/ragas-eval.md) for the flags and
[The in-service agent loop and the A2A surface](/agent-architecture.md) for
the compact tool payload and budget policy.
