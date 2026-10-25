---
type: Run
title: Store layer verification
description: The full verification suite after the store layer landed - build, vet, tests, gofmt, and the DDL smoke check against the new chunk_vectors table - all green on the feat/store-layer branch.
status: stable
generated:
  by: dave
  at: 2026-10-25T13:45:00Z
run:
  type: verify
  cmd: go build -mod=vendor ./... && go vet -mod=vendor ./... && go test -mod=vendor ./... && gofmt -l cmd internal migrations && python3 scripts/check-ddl.py
output: |
  BUILD OK
  VET OK
  ok   github.com/eli-l/elilap.dev-agent/cmd/agent
  ok   github.com/eli-l/elilap.dev-agent/internal/config
  ok   github.com/eli-l/elilap.dev-agent/internal/sources
  ?    github.com/eli-l/elilap.dev-agent/internal/sources/ghost  [no test files]
  ?    github.com/eli-l/elilap.dev-agent/internal/sources/postiz [no test files]
  ok   github.com/eli-l/elilap.dev-agent/internal/store
  ?    github.com/eli-l/elilap.dev-agent/migrations  [no test files]
  gofmt: clean (no output)
  ok   tables present: chunks, chunks_fts, chunk_vectors, source_state, source_items
  ok   chunks has the expected 17 columns
  ok   chunks_fts stores its columns: MATCH + join-back on chunk_id works
  ok   chunk_vectors holds chunk_id/dim/vec and cascades from chunks(chunk_id)
  ok   PLAN.md section 3 and migrations/0001_init.sql agree on the chunks table (17 columns)
  DDL + FTS5 stored-column join-back OK
---

# Result

The suite is green on the store-layer branch. The four required
verification commands (§§ Commands in ../../AGENTS.md) pass, and
scripts/check-ddl.py proves the new schema contract: the chunk_vectors
table carries chunk_id, dim and vec, its foreign key to chunks cascades,
and the schema block in PLAN.md section 3 still matches
migrations/0001_init.sql.

gofmt -l cmd internal migrations printed nothing.

The store tests cover the contracts that matter: the migrations apply
once and re-opening applies nothing; a chunk written twice leaves one
row in chunks, one in chunks_fts and one in chunk_vectors; the full-text
row of a replaced chunk is gone rather than duplicated; the same vector
scores about 1.0 and an unrelated one scores lower; a row whose
dimension differs from the query's is skipped; a failing migration rolls
back and names the file; and Open refuses an empty path and a path whose
parent directory does not exist.

The OKF validators (okf-validate, okf-check-links) are not installed on
this host, so the bundle was checked by hand instead: every concept file
has the required frontmatter, and every link in docs/ resolves.
