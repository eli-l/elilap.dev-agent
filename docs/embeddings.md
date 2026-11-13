---
type: Reference
title: Embedding pass and the vector index
description: Why embeddings are a separate, resumable pass after ingest; how chunks_vec, the embedded_with tag, batching, resume, --full re-embedding, and the --progress reporter work.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-02T20:56:00Z
tags: [embeddings, vectors, sqlite-vec, localai, cli, progress]
sources:
  - internal/embed/embed.go
  - internal/llm/client.go
  - internal/store/vectors.go
  - internal/store/vecschema.go
  - internal/store/chunks.go
  - cmd/agent/embed.go
  - cmd/agent/progress.go
  - migrations/0001_init.sql
  - PLAN.md
---

# Embedding pass and the vector index

Embedding is a **separate pass after ingest**, not part of the ingest run
(PLAN.md Decision 14 and Decision 17). Ingest fills `chunks` + `chunks_fts`;
`agent embed` fills `chunks_vec` and stamps `chunks.embedded_with` /
`embedded_at`.

## Why not during ingest

* **Ingest never needs the model host.** The providers, chunker, FTS mirror,
  and ledger are useful without LocalAI reachable; coupling them would make a
  model outage abort an upstream sync.
* **Failures have different shapes.** A fetch failure should be retried by
  re-listing upstream; an embedding failure should be retried by re-reading
  the already-written chunks. Keeping them separate means neither retry
  re-does the other's work.
* **The work is batchable and resumable.** One HTTP request carries
  `--batch` chunks (default 32). Committed batches stay committed, so an
  interrupted run resumes at the first chunk without a current vector.
* **Full re-embedding is explicit.** A model change, a text-template change,
  or a corrupted `chunks_vec` is repaired with `agent embed --full`, instead
  of being a hidden side effect of the next ingest.

## Pipeline

```text
agent ingest  ──> chunks + chunks_fts            (one transaction per item)
agent embed   ──> pending chunks ──> /v1/embeddings ──> chunks_vec
                                                         + embedded_with/embedded_at
```

`internal/embed` owns batching and resume; `internal/llm` is the plain
`net/http` OpenAI-compatible connector (`POST /v1/embeddings`, no SDK); the
shared retrying transport handles 429/5xx and network errors.

## What is embedded

The vector input is `title`, `section`, then `body`, blank-line separated
(`embed.EmbedText`). Leading a chunk with its title and heading path keeps a
split section interpretable on its own. Tags stay FTS-only. The template is
named by `embed.TextVersion` (`v1`); bump it when the composition changes.

## Invalidation tag

`chunks.embedded_with` stores
`localai:<model>/text-<version>` (for example
`localai:embeddinggemma-300m/text-v1`). A chunk is pending when any of these
holds:

* `embedded_with` is `NULL` (new chunk, or a rewritten item — see below);
* `embedded_with` differs from the current tag (model or template changed);
* there is no row in `chunks_vec` for the chunk (manually cleared, or the
  vec table was rebuilt by an `EMBED_DIMS` change).

The `LEFT JOIN chunks_vec` in the pending query makes the vec table, not just
the bookkeeping column, the authority on "has a vector".

Changing `EMBED_DIMS` (a model with another vector width) makes the next
read-write open rebuild `chunks_vec` at the configured width, drop the old
vectors, and clear the tags, so the whole corpus becomes pending and
`agent embed` re-embeds it. Read-only commands compare the index width with
`EMBED_DIMS` first and fail with `index vector dimensions do not match
EMBED_DIMS` instead of querying a space they cannot compare against. The tag
keeps its `localai:` prefix; it names the embedding space, not the host, and
the width is what actually changes it.

## Store invariants

* **Rewritten or deleted chunks lose their vectors in the same transaction.**
  `ReplaceItemChunks`, `DeleteItem`, and `RemoveItem` (the drift-deletion
  path ingest uses) delete from `chunks_vec` before deleting the chunk rows. A rewritten chunk therefore comes back as pending;
  a dropped chunk can never leave a vector that KNN returns but nothing can
  join back to `chunks`.
* **A vec write is stamped first, inserted second.** `WriteEmbeddings`
  updates `embedded_with`/`embedded_at` only when `chunk_id` *and*
  `content_hash` still match the selected text. A chunk rewritten between
  selection and write (chunk ids are positional, so the id survives but the
  body does not) is skipped rather than stamped with a stale vector; one that
  vanished entirely is skipped the same way.
* **vec0 has no `INSERT OR REPLACE` on its primary key.** Each row is an
  explicit `DELETE` followed by an `INSERT`.
* **Dimensions are enforced twice.** The runner rejects any vector whose
  length differs from the index's width (`store.Store.Dims()`, set from
  `EMBED_DIMS` when the store opens), and the store refuses the same before
  opening its transaction. `EMBED_DIMS` defaults to 768, the width
  `migrations/0001_init.sql` declares.

## CLI

```bash
agent embed                     # embed every pending chunk, in batches
agent embed --batch 64          # larger requests
agent embed --limit 100         # sample/partial run; rerun resumes
agent embed --full              # re-embed the whole corpus
agent embed --dry-run           # report the pending count only
agent embed --progress          # live progress line on stderr
```

`--progress` writes one overwriting `\r` line to **stderr** — stdout keeps
the result summary (JSONL under `AGENT_LOG_FORMAT=json`, so `| jq` still
works). Under JSON logs the flag is ignored (a warning is logged): the
unterminated progress record would corrupt the JSONL stderr stream. Run
`AGENT_LOG_FORMAT=text ./agent embed --progress` to see the live line; the
runner also suppresses its start/complete records while the line is active. The line shows processed/total chunks and percentage, the in-flight
batch, elapsed time, throughput, and ETA. A one-second ticker redraws it even
while a single embeddings request is slow. The final line reports
`done N/N chunks in Xs (R chunks/s)`; the summary that follows lists
`pending`, `embedded`, `skipped`, `remaining`, and `batches`.
