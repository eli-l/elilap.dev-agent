---
type: Run
title: SQLite schema DDL check
description: "Deprecated: the migrations/0001_init.sql smoke check passed at bootstrap, but the scripts/check-ddl.py command it names was never committed; superseded by the Go TestSchemaDDL run."
status: deprecated
generated:
  by: z-ai/glm-5.3-flashx
  at: 2026-10-21T21:34:39Z
verified:
  - by: human:eli
    at: 2026-10-21T21:34:39Z
run:
  type: check-ddl
  cmd: python3 scripts/check-ddl.py
output: |
  DDL + FTS5 stored-column join-back OK (contentless fix verified)
---

# Result

Schema check green at bootstrap commit. `0001_init.sql` matches the
PLAN.md §3 schema block, including the non-contentless `chunks_fts`
join-back contract.

**Deprecated 2026-11-08.** This capture names `python3 scripts/check-ddl.py`,
but that script was never added to the repository, so the run is not
reproducible. The check now lives in Go as `TestSchemaDDL`
(`internal/store/ddl_test.go`) and exercises the real driver, build tag, and
sqlite-vec registration instead of a separate Python `sqlite3` connection.
[SQLite schema smoke check in Go](/runs/ddl-check-2026-11-08.md)
