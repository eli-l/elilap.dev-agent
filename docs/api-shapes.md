---
type: Reference
title: API shapes
description: Index of captured upstream API response shapes (Ghost, Postiz) that migrations/0001_init.sql and the provider packages must stay honest to.
status: stable
generated:
  by: z-ai/glm-5.3-flashx
  at: 2026-11-02T20:15:00Z
tags: [api, capture, phase-0, schema]
---

# API shapes

Both captures are real. Each one records what the upstream actually
returned, not what its documentation claims, so `migrations/0001_init.sql`
and the provider packages stay honest to observed data.

Sections:

- [Ghost API shape (live capture)](/ghost.md) — Content/Admin key shapes,
  `updated_at` filters, the 100-post page cap, and the uuid-versus-id fetch
  detail.
- [Postiz API shape (live capture)](/postiz.md) — the required date-range
  listing, no pagination and no read-by-id, one record per platform copy,
  and the post states that decide what gets indexed.
- [HTML website provider and per-site extraction strategies](/web.md) — the
  observed `elilap.dev` page shapes (Astro, no JSON-LD, `<article>` on case
  studies and `<main>` elsewhere, a JS-date `<time datetime>`), the trimmed
  fixtures the tests replay, and the strategy/adapter seam.

Fixtures replayed by the provider tests:

- Ghost: inline capture-shaped JSON in `internal/sources/ghost/provider_test.go`
  (no testdata directory).
- Web: trimmed HTML/XML captures in `internal/sources/web/testdata/`
  (`case-study.html`, `services.html`, `article-jsonld.html`, `noindex.html`,
  `sitemap-index.xml`, `sitemap-pages.xml`, `sitemap-posts.xml`, `rss.xml`).
- Postiz: `internal/sources/postiz/testdata/posts.json` — a trimmed Postiz
  capture covering published, scheduled, draft, and failed records.
