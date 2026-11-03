---
type: Run
title: Hybrid retrieval eval (20 queries)
description: First run of eval/queries.yaml through the hybrid engine - 19/20 queries found a human-verified chunk in the top five, with one known miss on a section that vector and BM25 retrieval both rank below the cut.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-02T22:10:00Z
run:
  type: retrieval-eval
  cmd: go run -mod=vendor ./scripts/eval
output: |
  PASS  harness-lock-in            harness as vendor lock-in
  PASS  mcp-noise-tax              the MCP noise tax
  PASS  builder-layer              builder layer decide and act
  PASS  autonomous-local-agent     fully autonomous local agent
  PASS  realtime-assistant         realtime conversation assistant hackathon
  PASS  riga-cursor-hackathon      Riga Cursor hackathon
  PASS  okf-agent-memory           expanding agent memory with OKF
  PASS  social-media-loop          AI-augmented social media loop
  PASS  dont-hire-ai               why you should not hire AI
  PASS  context-switching          builders fail from context switching
  PASS  self-promotion             engineers struggle with self-promotion
  PASS  perfect-stack-cost         the cost of perfect stack obsession
  PASS  choose-tech-solo           how to choose the right tech when building solo
  PASS  flexible-scheduling        NoStrict scheduling vs flexible scheduling
  PASS  escalation-in-agents       the missing part is escalation bots should know when to stop
  FAIL  local-pipeline-state       state became more important than intelligence
        expected one of: 456c97337e
        1. We went through this before. /  (9e0f201c3c)
        2. I was bullied through the elementary school ... (869d76a70b)
        3. What's important today? / Summary (3332adfd8e)
        4. What's important today? / Summary (0202719832)
        5. <p>Everyone talks about faster AI.</p> ... (b5975f2216)
  PASS  reduce-friction            how to reduce friction so your work flows
  PASS  independent-engineer-traps hidden traps of being an independent engineer
  PASS  energy-management          energy management for people who do not rest naturally
  PASS  babylon-tower              AI might be another Babylon tower

  19/20 passed (threshold 15)
---

# Result

The eval passed 19 of 20 queries, above the Phase 2 target of 15/20. The
ground truth was chosen from content a human verified, not from whatever
ranked first: several queries expect a section chunk that is not the top hit
(for example `riga-cursor-hackathon` ranks the RigaComm recap first and the
event post second; that still passes).

The one miss, `local-pipeline-state`, is a real retrieval signal rather than a
broken expectation: chunk `456c97337e…` is the "State Became more important
than Intelligence" section of the realtime-assistant write-up, and the query
paraphrases that heading, but neither path puts it in the top five. It is left
in the artifact as a known miss instead of being rewritten to fit the current
output.

The runner shells out to `agent query --format jsonl`, so the eval grades the
same hybrid engine, filters, and serialization the CLI and HTTP surfaces use.
It reads the YAML strictly (unknown fields are errors), defaults the pass
threshold to 75% when unset, and fails below the artifact's threshold.
