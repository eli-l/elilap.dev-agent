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
agent ingest [--source <id>] [--full] [--limit N]
```

Without `--source`, every **enabled** source in the config runs. With
`--source`, exactly that source runs, and it has to exist and be enabled.
`--limit N` caps the number of items per source.

For every item the provider lists, the body is chunked with the chunking
policy of the config and every chunk is written to the index. The chunk id
is `sha256(source_kind|source_id|chunk_seq)` in hex, so re-running the
import replaces the same rows instead of accumulating them.

An item whose chunks are already in the index with the same content hashes
is skipped, which makes a second run cheap; `--full` rewrites everything
anyway. A source whose client does not exist yet - the Ghost and Postiz
clients still wait on the Phase 0 API captures - is reported and skipped,
and the sources that do work still run. The command fails when no source
could be imported at all.

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

* The ingest ledger (`source_state`, `source_items`) is not consulted
  yet. The incremental decision is made on the chunk content hashes, so an
  edited item that grows fewer chunks leaves its old chunks behind.
* `chunks.embedded_with` and `chunks.embedded_at` are not written yet:
  the audit trail of the embed command still has to land.
* `agent serve` - the read-only A2A surface - is still a placeholder.
