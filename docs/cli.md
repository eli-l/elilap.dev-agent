---
type: Reference
title: Command line
description: The subcommands of the agent binary - ingest, embed, query, stats and doctor - what each of them owns, and the flags and environment variables they read.
status: draft
generated:
  by: dave
  at: 2026-10-25T14:00:00Z
tags: [cli, ingest, embed, query, doctor, phase-1]
---

# Command line

One binary, `agent`, with one file per subcommand in `cmd/agent/`. The
pipeline it drives is: a corpus in, an index in the middle, an answer out.

```bash
agent sources list       # the configured providers and their resolved options
agent sources validate   # strict validation of sources.yaml
agent doctor             # can this machine run the agent right now?
agent ingest             # the corpus into the index
agent embed              # the index into vectors
agent query "a question" # the question into an answer
agent stats              # what the index holds
```

Exit codes are the same everywhere: 0 on success, 1 on a runtime or
validation error, 2 on a usage error. No credential value is ever printed -
only the name of the environment variable and whether it is set.

## The environment

| variable | default | used by |
|---|---|---|
| `AGENT_DB_PATH` | `./index.db` | every command that touches the index |
| `LOCALAI_BASE_URL` | `http://localhost:8080/v1` | `embed`, `query`, `doctor` |
| `LOCALAI_API_KEY` | empty | `embed`, `query` |
| `EMBED_MODEL` | `embeddinggemma-300m` | `embed`, `query`, `stats` |
| `CHAT_MODEL` | `spark-x2.5-4b` | `doctor` |

Every command that touches the index also takes `--db <path>`, which wins
over `AGENT_DB_PATH`. The config path is `--config <path>`, default
`./sources.yaml`.

## ingest

```bash
agent ingest [--source <id>] [--full] [--dry-run] [--limit N]
```

Without `--source`, every **enabled** source in the config runs. With
`--source`, exactly that source runs, and it has to exist and be enabled.
`--limit N` caps the number of items per source.

The run is **ledger-driven**. The index carries a ledger - `source_state`
and `source_items` - that records what the last run of every source saw,
and the run decides from it what to fetch, what to rewrite and what to
delete:

* an item whose cheap fingerprint still matches the one the ledger holds is
  not even fetched;
* an item whose content hash still matches is left alone: the chunks in
  the index are still the ones it produces;
* an item that changed is fetched, chunked and written. The chunk id is
  `sha256(source_kind|source_id|chunk_seq)` in hex, so the same chunk is
  replaced, not duplicated;
* **the chunks an item no longer produces are deleted.** An item that was
  edited so that it got shorter used to leave its old chunks in the index
  forever: they kept matching queries, and they kept their vectors. The
  run says so: `removed 3 stale chunk(s) for note.md`;
* the items the ledger holds but the run did not see are gone upstream:
  their chunks and their ledger rows are deleted. A run with `--limit`
  does not do this - it did not look at the whole source.

A source whose client does not exist yet - the Ghost and Postiz clients
still wait on the Phase 0 API captures - is reported and skipped, and the
sources that do work still run. The command fails when no source could be
imported at all.

Each run has an id, and the ledger records the run that last saw every
item, the time the item was first seen and the time it was last seen. The
resolved configuration of a source is hashed into `source_state`: the
source id, its type, its options, the chunking policy and the **names** of
the credential environment variables - never a credential value, so a
rotated key does not look like a changed configuration. A configuration
that changed invalidates the chunks the last run wrote, so every item is
fetched and re-chunked.

The per-source summary line counts what the run did: items seen, items
fetched, chunks written, stale chunks removed, items removed and items
unchanged.

### `--full`

Re-fetches and re-chunks every item, whatever the ledger says, and records
the run in `source_state.last_full_at`. An incremental run keeps that
timestamp: only a full run moves it.

### `--dry-run`

Reports what would be written, skipped and removed, and writes **nothing**:
no chunk, no full-text row, no vector, no ledger row. It is the way to
see what a run would do to the index before letting it do it.

```bash
$ agent ingest --dry-run
dry run: nothing is written to /tmp/ledger-proof/index.db
dry-run files: would write 1 chunk(s) for beta.md
dry-run files: would remove 3 stale chunk(s) for alpha.md
dry-run files: 2 item(s) seen, 1 item(s) fetched, 1 chunk(s) would be written, 0 stale chunk(s) would be removed, 0 item(s) would be removed, 1 item(s) unchanged (index /tmp/ledger-proof/index.db)
```

A dry run still opens the index, so a fresh machine gets the schema; it
writes no data.

## embed

```bash
agent embed [--limit N] [--batch 32]
```

The work list is the chunks that have no vector, so an interrupted run is
resumed by running it again. The chunks are embedded in batches through the
OpenAI-compatible endpoint and every vector is written with `PutVector`,
which never touches the chunk it belongs to. `--limit N` caps the number
of chunks embedded in this run; `--batch` sets how many go into one
request.

## query

```bash
agent query "<text>" [--limit N] [--json]
```

The question is embedded once. The index is then asked twice - lexically
through FTS5 and semantically through the stored vectors - and the two
rankings are fused with Reciprocal Rank Fusion by `internal/retrieval`.
The adapters that connect the store to the retrieval layer live in the
command package, so neither package has to know about the other.

The default output is one line per hit: title, url, fused score. `--json`
prints the hits as a JSON array. **No hits is not an error**: a question
the corpus does not cover prints `no matches` and exits 0. An empty
question is a usage error.

## stats

```bash
agent stats
```

Prints the index path, the chunk, vector, source and full-text counts, and
the embedding model in use.

## doctor

```bash
agent doctor [--skip-network] [--timeout 5s]
```

The preflight. It validates the config and every provider's options, names
the credential variables that are not set, probes the model server and
checks that the configured models are served, and checks that the index
directory is writable.

The **store** check opens the index, runs SQLite's integrity check and
reports the chunk and vector counts. A missing index is a warning on a
fresh machine; a corrupt one is a failure that names the fix - the index
is derived, so it can be removed and rebuilt with `agent ingest`.

## The local markdown provider

`internal/sources/files` reads a directory of markdown files as upstream
items. It needs no credentials, so the whole pipeline can be run before the
upstream clients land, and it is the corpus the end-to-end test uses.

```yaml
sources:
  - id: files
    type: files
    enabled: false
    options: { dir: ./docs, pattern: "*.md" }
```

Its items are stored under the source kind `file_post`. The item id is
the file's path relative to `options.dir`, which is what the chunk id is
derived from.

## What is not here yet

* `chunks.embedded_with` and `chunks.embedded_at` are not written yet:
  the audit trail of the embed command still has to land.
* `agent serve` - the read-only A2A surface - is still a placeholder.
