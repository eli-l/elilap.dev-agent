---
type: Run
title: LocalAI embedding of the full index
description: The first live `agent embed` run vectorized the 886-chunk index through LocalAI's embeddinggemma-300m in 12 batches, resuming an interrupted run at 512 chunks with zero re-fetching.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-02T20:56:00Z
run:
  type: agent-embed
  cmd: ./agent embed --batch 32 --progress --timeout 20m
output: |
  embedding: done 374/374 chunks in 14.3s (26.2 chunks/s)
  {"model":"embeddinggemma-300m","tag":"localai:embeddinggemma-300m/text-v1","full":false,"pending":374,"embedded":374,"skipped":0,"remaining":0,"batches":12}
  $ ./agent stats
  chunks:    886
  fts rows:  886
  vec rows:  886
  embedded:  886
---

# Result

LocalAI at `http://192.168.78.243:8080` was reachable (`GET /readyz` →
200) and `EMBED_MODEL=embeddinggemma-300m` returned 768-dimensional vectors,
matching `chunks_vec.embedding float[768]`.

An earlier invocation was interrupted after committing 512 embeddings. The
rerun found exactly 374 pending chunks — the first 512 were skipped without
a model call, proving the resume path — and embedded them in 12 batches of
32 (the last batch held 22) in about 14 seconds, roughly 26 chunks/s on this
hardware. Well inside the Phase 1 exit criterion of under five minutes.

`agent stats` then reported 886 chunks, 886 FTS rows, 886 vec rows, and 886
embedded rows, so `chunks`, `chunks_fts`, and `chunks_vec` agree.

The `--progress` line was rendered to stderr while the JSON summary went to
stdout unchanged; captured terminal output shows the carriage-return redraws
as adjacent lines.
