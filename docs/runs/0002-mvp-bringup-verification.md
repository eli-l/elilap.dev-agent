---
type: Run
title: MVP bring-up verification
description: The full verification suite plus the DDL smoke check and the bring-up preflight, all green on the feat/mvp-platform-review branch.
status: stable
generated:
  by: dave
  at: 2026-10-25T06:40:00Z
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
  gofmt: clean (no output)
  ok   tables present: chunks, chunks_fts, source_state, source_items
  ok   chunks has the expected 17 columns
  ok   chunks_fts stores its columns: MATCH + join-back on chunk_id works
  ok   chunks_vec declares float[768] (checked by shape: the sqlite-vec extension is not loaded)
  ok   PLAN.md section 3 and migrations/0001_init.sql agree on the chunks table (17 columns)
  DDL + FTS5 stored-column join-back OK
---

# Result

The whole suite is green on the bring-up branch: the four required
verification commands (`go build`, `go vet`, `go test`, `gofmt -l`) pass,
and `scripts/check-ddl.py` - the schema smoke check that `AGENTS.md` and
`PLAN.md` referenced but that did not exist - now runs and proves the
FTS5 stored-column join-back contract plus the sync between
`migrations/0001_init.sql` and the schema block in `PLAN.md` section 3.

`gofmt -l cmd internal migrations` printed nothing.

The checker was negative-tested: dropping a column from the migration and
making `chunks_fts` contentless each make it exit 1 with a specific
message, so a green run is evidence and not a tautology.

`agent doctor` was run on the reference host as well: it reported the
config, the provider options, the four unset credential variables (by
name), and the model endpoint. No credential value is ever printed.
