---
type: Run
title: CLI vertical slice verification
description: The full verification suite plus the hands-on ingest, embed and query run against the self-hosted model server, after the CLI vertical slice landed.
status: stable
generated:
  by: dave
  at: 2026-10-25T14:00:00Z
run:
  type: go-test
  cmd: go build -mod=vendor ./... && go vet -mod=vendor ./... && go test -mod=vendor -count=1 ./... && gofmt -l cmd internal migrations && python3 scripts/check-ddl.py
output: |
  $ go build -mod=vendor ./...
  exit=0
  $ go vet -mod=vendor ./...
  exit=0
  $ go test -mod=vendor -count=1 ./...
  ok  	github.com/eli-l/elilap.dev-agent/cmd/agent	0.039s
  ok  	github.com/eli-l/elilap.dev-agent/internal/chunk	0.009s
  ok  	github.com/eli-l/elilap.dev-agent/internal/config	0.012s
  ok  	github.com/eli-l/elilap.dev-agent/internal/embed	0.012s
  ok  	github.com/eli-l/elilap.dev-agent/internal/retrieval	0.006s
  ok  	github.com/eli-l/elilap.dev-agent/internal/sources	0.006s
  ok  	github.com/eli-l/elilap.dev-agent/internal/sources/files	0.007s
  ok  	github.com/eli-l/elilap.dev-agent/internal/store	0.110s
  exit=0
  $ gofmt -l cmd internal migrations
  (no output - clean)
  exit=0
  $ python3 scripts/check-ddl.py
  ok   tables present: chunks, chunks_fts, chunk_vectors, source_state, source_items
  ok   chunks has the expected 17 columns
  ok   chunks_fts stores its columns: MATCH + join-back on chunk_id works
  ok   chunk_vectors holds chunk_id/dim/vec and cascades from chunks(chunk_id)
  ok   PLAN.md section 3 and migrations/0001_init.sql agree on the chunks table (17 columns)
  DDL + FTS5 stored-column join-back OK
  exit=0

  The hands-on run against the self-hosted model server:

  $ ./agent ingest --source files
  files: 1 item(s) seen, 1 chunk(s) written, 0 item(s) unchanged (index /tmp/elilap-demo/index.db)
  ingest: 1 source(s) imported, 0 skipped, index /tmp/elilap-demo/index.db
  exit=0

  $ ./agent embed
  embed: 1 chunk(s) embedded with embeddinggemma-300m, 0 chunk(s) still without a vector (index /tmp/elilap-demo/index.db)
  exit=0

  $ ./agent query "how does the agent index markdown"
  Demo post | /tmp/elilap-demo/demo.md | score 0.032787
  exit=0

  $ ./agent stats
  index: /tmp/elilap-demo/index.db
  chunks: 1
  vectors: 1
  sources: 1
  fts rows: 1
  embedding model: embeddinggemma-300m

  $ ./agent ingest --source files
  files: 1 item(s) seen, 0 chunk(s) written, 1 item(s) unchanged (index /tmp/elilap-demo/index.db)
  ingest: 1 source(s) imported, 0 skipped, index /tmp/elilap-demo/index.db
---

# Result

The vertical slice works on the machine, not only in the tests: a
markdown file is ingested, embedded by the real model server and found
again by a question about it. A second ingest writes nothing, because the
item did not change.

The end-to-end test proves the same path without a model server: a fake
OpenAI-compatible endpoint serves deterministic vectors, and the test
asserts that a query about one file returns that file and that a query
about nothing returns no hits and no error.
