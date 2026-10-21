---
type: Reference
title: API shapes
description: Index of captured upstream API response shapes (Ghost, Postiz) that migrations/0001_init.sql must stay honest to.
status: draft
generated:
  by: z-ai/glm-5.3-flashx
  at: 2026-10-21T21:34:39Z
tags: [api, capture, phase-0, schema]
---

# API shapes

Populated in Phase 0 from live `postiz posts:list` and Ghost
`/ghost/api/admin/posts/` responses.

Goal: capture the **actual** response shape from each upstream
(not as their docs claim, but as we observe), so the schema
in `migrations/0001_init.sql` is honest.

Sections:

- [Ghost Admin API shape (live capture)](/ghost.md) — populated after Phase 0 capture.
- [Postiz API shape (live capture)](/postiz.md) — populated after Phase 0 capture.

Until these exist, the `migrations/0001_init.sql` schema is
based on best-effort inference, not verified data.
