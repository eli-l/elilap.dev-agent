# API shapes

Populated in Phase 0 from live `postiz posts:list` and Ghost
`/ghost/api/admin/posts/` responses.

Goal: capture the **actual** response shape from each upstream
(not as their docs claim, but as we observe), so the schema
in `migrations/0001_init.sql` is honest.

Sections:

- [`ghost.md`](./ghost.md) — populated after Phase 0 capture.
- [`postiz.md`](./postiz.md) — populated after Phase 0 capture.

Until these exist, the `migrations/0001_init.sql` schema is
based on best-effort inference, not verified data.
