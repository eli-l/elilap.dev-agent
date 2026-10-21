---
type: Run
title: SQLite schema DDL check
description: The migrations/0001_init.sql smoke check passed with the contentless-FTS5 join-back fix verified.
status: stable
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
