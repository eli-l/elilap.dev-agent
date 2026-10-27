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

The `chunks.source_kind` CHECK constraint accepts `ghost_post`,
`postiz_post` and `file_post`. The local markdown provider added
`file_post`, and `migrations/0003_source_kind_file_post.sql` rebuilds
the table for the indexes that were created before the change: the rows
are copied into a table with the widened constraint, the vectors are
carried over, and the indexes are recreated. An index that is rebuilt
ends up exactly where it started.

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
  embed command already produced. A chunk whose **text changed** loses its
  vector instead: the stored vector describes the text the chunk used to
  hold, so it would answer queries with a passage the chunk no longer
  contains - and the chunk, having no vector, is back on the embed
  command's work list.
* `PutChunk` does not touch `chunks.embedded_with` or
  `chunks.embedded_at`. That audit trail belongs to the embed command,
  which knows which model produced the vector.

The chunk id is supplied by the caller: it is
`sha256(source_kind|source_id|chunk_seq)` in hex, so the ingest pipeline
owns chunk identity and the store never guesses it.

`PutVector` writes the embedding of one chunk and replaces the vector
it had. The chunk must exist - a vector without a chunk would be
unreachable - and the write never touches the `chunks` or the
`chunks_fts` row: the embed command owns the vectors, the ingest
command owns the text.

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

`ListChunks` returns the chunks themselves, ordered by `published_at`
and then `chunk_id`, so two calls over the same index return them in
the same order. With `missingVector` set it returns only the chunks
that have no vector yet: that is the work list of the embed command.
`IntegrityCheck` runs SQLite's own integrity check, so the doctor can
tell a sound index from a corrupt one.

## The ledger

`source_state` and `source_items` are the bookkeeping of the ingest:
one row per configured source, one row per upstream item. The tables come
from `migrations/0002_state.sql`; `internal/store` owns them and
`cmd/agent/ingest.go` is their only writer.

| method | what it does |
|---|---|
| `SourceState(ctx, sourceID)` | the ledger row of a source, and whether it exists. A source that was never imported has no row, and that is not an error. |
| `PutSourceState(ctx, st)` | writes the row. The whole row is written, so the caller decides what stays: an incremental run passes the previous `last_full_at` back in. |
| `SourceItems(ctx, sourceID)` | the ledger rows of a source, ordered by item id. |
| `PutSourceItem(ctx, it)` | writes one item's row. The `first_seen_at` of an item is written once and never overwritten, so the ledger carries the age of an item and not the age of the last run. |
| `DeleteSourceItems(ctx, sourceID, keep)` | deletes the rows of the items that are not in `keep` and returns how many it deleted. An empty `keep` deletes every row of the source. |
| `DeleteChunksForItem(ctx, sourceKind, sourceID, keep)` | deletes the chunks of an item that are not in `keep`, with their full-text rows and their vectors, in one transaction, and returns how many chunks it deleted. |

`DeleteChunksForItem` spans three tables, so it is transactional. The
vectors go with the chunk - the foreign key cascades - but the full-text
rows do not: an FTS5 table has no foreign key to `chunks`, so its rows
are deleted explicitly. A full-text row that outlived its chunk would keep
matching queries with a body the index no longer holds.

The `etag` column holds the item's **cheap fingerprint**: the upstream
entity tag when the provider offers one, and the provider's own hint
otherwise. It is what lets a run skip an item without fetching it, so it
has to change whenever the content of the item changes. The
`content_hash` column holds the hash of the item's content: the strong
fingerprint, the one that says the text itself did not change.

## Why not sqlite-vec

sqlite-vec ships as a platform-specific loadable extension: a native
`.so` (or its per-platform equivalents) next to every deployed binary, and
a C toolchain in the build. The corpus here is small - a few hundred to a
few thousand chunks - so the difference an approximate vector index would
make is unmeasurable. Storing the vectors as float32 bytes and scoring
them in Go does the same work with no native dependency.

## What is not here yet

* The upstream clients (`internal/sources/ghost`, `postiz`) do not
  exist: they wait on the Phase 0 API captures. The local markdown
  provider is the corpus the pipeline runs against today.
* `chunks.embedded_with` and `chunks.embedded_at` are not written yet:
  the audit trail of the embed command has to land with it.
