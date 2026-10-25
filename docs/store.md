---
type: Reference
title: Index store
description: The SQLite index layer - the pure-Go driver and its pragmas, the embedded migrations, the chunk write path, and the BM25 and cosine read paths.
status: draft
generated:
  by: dave
  at: 2026-10-25T13:40:00Z
tags: [store, sqlite, fts5, vectors, schema, phase-1]
---

# Index store

The index is one SQLite file. `internal/store` owns it end to end: the
schema, the migrations, and every read and write against it. [PLAN.md](../PLAN.md)
§3 is the normative plan; this file describes the code that exists today.

## The driver

[github.com/ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) is
pure Go - SQLite compiled to WebAssembly. Two consequences the plan depends
on:

* No CGO, so no C toolchain is needed to build, and the binary
  cross-compiles for linux/amd64, linux/arm64 and darwin/arm64 with
  `CGO_ENABLED=0`.
* FTS5 is not compiled into the driver; it comes from the driver's own
  `ext/fts5` extension, which `internal/store/sqlite.go` registers with
  `sqlite3.AutoExtension` so that every connection has it.

The file is opened with three pragmas in the DSN: `busy_timeout(5000)`,
`journal_mode(WAL)` and `foreign_keys(1)`. The connection pool is capped
at one connection, because SQLite has a single writer and a second
connection would only queue behind it.

## The schema

[`migrations/0001_init.sql`](../migrations/0001_init.sql) and
[`migrations/0002_state.sql`](../migrations/0002_state.sql) are the whole
schema. The parts the store depends on:

| table | what it holds | who writes it |
|---|---|---|
| `chunks` | one row per chunk: identity, source, timestamps, body, tags in `meta_json` | the ingest pipeline, through `PutChunk` |
| `chunks_fts` | the full-text row of a chunk: id, title, body, tags | the store, in the same transaction as the chunk |
| `chunk_vectors` | the embedding of a chunk: id, dimension, float32 bytes | the store, when the chunk carries an embedding |
| `source_state`, `source_items` | the ingest ledger | the ingest pipeline |
| `schema_migrations` | which migration files have been applied | the store |

`chunks_fts` is *self-contained*, not contentless: its stored columns
read back, which is what makes the join-back on `chunk_id` work at query
time.

## Migrations

The SQL is embedded into the binary by [`migrations/embed.go`](../migrations/embed.go),
so the executable carries its own schema and "delete the index and re-run"
rebuilds it without a schema file next to the binary.

The store applies the files in file-name order inside one transaction and
records each applied file in `schema_migrations`. Three consequences:

* Re-opening an index applies nothing - the ledger already names the
  files.
* A migration that fails rolls the whole transaction back, so the index is
  left exactly as it was, and the error names the file that failed and the
  SQLite error that stopped it.
* A file must never be renamed after it has been applied: the file name is
  the migration's identity.

## Writes

`PutChunk` writes the `chunks` row, the `chunks_fts` row and - when the
chunk carries an embedding - the `chunk_vectors` row, all in one
transaction. A reader therefore never sees a chunk without its searchable
text.

Writing the same chunk id again replaces the previous rows: the full-text
row and the vector are deleted and re-inserted, and the chunk row is an
upsert. Nothing accumulates.

Two deliberate exceptions:

* A chunk written *without* an embedding keeps the vector it already has.
  Re-ingesting an unchanged chunk must not throw away an embedding the
  embed command already produced.
* `PutChunk` does not touch `chunks.embedded_with` or
  `chunks.embedded_at`. That audit trail belongs to the embed command,
  which knows which model produced the vector.

The chunk id is supplied by the caller: it is
`sha256(source_kind|source_id|chunk_seq)` in hex, so the ingest pipeline
owns chunk identity and the store never guesses it.

## Reads

Two read paths, one result shape, so the retrieval layer can fuse the two
ranks:

* `SearchBM25` runs an FTS5 `MATCH` joined back to `chunks` and returns
  the raw `bm25()` score. Lower is better, and the raw value is returned
  as-is so the caller can fuse ranks rather than guess at a scale.
* `SearchVector` loads the stored vectors, scores them by cosine
  similarity in Go, and returns the best ones. Higher is better. Rows
  whose dimension differs from the query's are skipped: they were produced
  by another model and cannot be compared.

A limit of zero or less means ten hits. An empty query is an error for the
full-text path and an empty result for the vector path.

## Why not sqlite-vec

sqlite-vec ships as a platform-specific loadable extension: a native
`.so` (or its per-platform equivalents) next to every deployed binary, and
a C toolchain in the build. The corpus here is small - a few hundred to a
few thousand chunks - so the difference an approximate vector index would
make is unmeasurable. Storing the vectors as float32 bytes and scoring
them in Go does the same work with no native dependency.

## What is not here yet

* The CLI does not call the store yet: `agent ingest`, `agent embed` and
  `agent stats` are still Phase 1 tasks in [PLAN.md](../PLAN.md) §3.
* The embed command, which owns `embedded_with` and `embedded_at`, does
  not exist yet.
* The retrieval layer (hybrid BM25 + cosine with RRF merging) does not
  exist yet; this file only describes the two rankings it will fuse.
