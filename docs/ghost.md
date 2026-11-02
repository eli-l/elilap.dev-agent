---
type: Reference
title: Ghost API shape (live capture)
description: Observed Ghost Content and Admin API post shapes, key formats, pagination, and incremental filters; the honest schema source for migrations/0001_init.sql and internal/sources/ghost.
status: stable
generated:
  by: command-code/deepseek/deepseek-v4.1-flash
  at: 2026-11-02T18:50:00Z
tags: [ghost, api, capture, phase-0, ingest]
---

# Ghost API shape (live capture)

Captured against `read.elilap.dev` on 2026-11-02. The instance is served by
Ghost's **Content API** for published posts; the configured
`GHOST_API_KEY` is a 26-character hex Content API key, not an Admin key.
`internal/sources/ghost` supports both key shapes.

## Key shapes

| key | format | API | access |
|---|---|---|---|
| Admin | `<24-hex id>:<64-hex secret>` | `/ghost/api/admin/*`, JWT in `Authorization: Ghost <jwt>` | read/write |
| Content | 26 hex characters, no colon | `/ghost/api/content/*`, `?key=<key>` | published posts, read-only |

A key containing `:` selects the Admin client; anything else is treated as a
Content key. Credential values are never logged or written to the repository.

## Content API requests

```
GET /ghost/api/content/posts/?key=<key>&limit=100&page=1
    &formats=html,plaintext&include=tags,authors
    &filter=updated_at:>='2026-10-26 00:00:00'
GET /ghost/api/content/posts/<id>/?key=<key>&formats=html,plaintext&include=tags,authors
GET /ghost/api/content/posts/slug/<slug>/?key=<key>...
```

Observed details:

* `limit` is capped at **100** by both the Content and the Admin API (the
  configured `options.limit: 500` is clamped in both clients, not rejected).
* Incremental listing uses `filter=updated_at:>='YYYY-MM-DD HH:mm:ss'`
  (space-separated UTC, quoted inside the single-quoted filter value).
  Admin listing uses `updated_since=<RFC3339>` instead.
* Read by **uuid is rejected** with 422 (`cannot read post`, `id undefined.id`).
  The read endpoint accepts the 24-hex `id` or `slug/<slug>`. The provider
  therefore keeps the Ghost `uuid` as the item id (the ledger/chunk key) and
  carries the 24-hex `id` as `shared.Item.Ref` for fetching.
* Pagination is `meta.pagination.{page,limit,pages,total,next,prev}`;
  `next` is `0` on the last page. 126 published posts were reported with
  `limit=2`.

## Response shape

`GET /posts/` returns `{"posts":[...],"meta":{"pagination":{...}}}`. Fields
the importer consumes:

```json
{
  "id": "6ae88d4e6637410001003249",
  "uuid": "d5f95a48-79f7-426c-9392-09902fec81a9",
  "title": "I gave up on zero harness and here is why",
  "slug": "i-gave-up-on-zero-harness-and-here-is-why",
  "html": "<p>…</p>",
  "plaintext": "…",
  "excerpt": "…",
  "published_at": "2026-11-02T13:21:35.000+00:00",
  "updated_at": "2026-11-02T13:29:58.000+00:00",
  "url": "https://read.elilap.dev/i-gave-up-on-zero-harness-and-here-is-why/",
  "visibility": "public",
  "primary_author": {"id": "68cdbd4a92c38e0001e302ba", "name": "Eli Lap", "slug": "eli"},
  "tags": [{"id": "…", "name": "ai", "slug": "ai"}]
}
```

`primary_author` is an **object**, not an id string. It was previously typed
as `string`, which would fail to decode against the real API. Content
responses omit `status` (only published posts are returned); Admin responses
include it and `include_drafts` controls whether drafts are listed.

## How the importer uses it

* `List` pages through the Content/Admin list with the incremental bound and
  keeps `uuid`, `id` (`Ref`), `url`, and `updated_at`.
* `Fetch` re-reads one post by `Ref`, then the chunker converts `html` to
  Markdown so chunks split on real `<h2>`/`<h3>` headings; `plaintext` is
  the fallback when no HTML is present.
* `updated_at` is the ledger's `last_modified` fingerprint, so an unchanged
  post is skipped without a fetch on the next incremental run.
* Both clients share the provider HTTP client
  (`shared.NewHTTPClient`): a 60-second per-request timeout and a transport
  that retries network errors and `408`/`429`/`5xx` up to three total
  attempts with exponential backoff, honoring `Retry-After`. A 126-post full
  ingest is 126 sequential requests, so one upstream hiccup must not abort
  the run; the run context still wins, and a canceled run stops retrying.
* `options.include_drafts: true` is only meaningful with an **Admin** key.
  With a Content key the provider logs a warning and returns published posts
  only, because the Content API does not expose drafts at all.
