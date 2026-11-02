---
type: Reference
title: Postiz API shape (live capture)
description: The observed Postiz public-API listing shape - required date range, no pagination, one record per platform copy, post states - and how the provider maps it to items and chunks.
status: stable
generated:
  by: z-ai/glm-5.3-flashx
  at: 2026-11-02T20:10:00Z
tags: [postiz, api, capture, phase-0, ingest]
---

# Postiz API shape (live capture)

Captured against `https://postiz.mist.lv` on 2026-11-02 with a public API
key. The capture returned 1,710 posts (2025-03-12 → 2026-11-02) and is the
honest reference for `internal/sources/postiz`; the trimmed fixture the tests
replay lives in `internal/sources/postiz/testdata/posts.json`.

## Endpoint and auth

```
GET <instance>/api/public/v1/posts?startDate=<ISO8601>&endDate=<ISO8601>
Authorization: <api key>
```

* The key travels in the **bare** `Authorization` header (no `Bearer`
  prefix). A wrong key answers `401 {"msg":"Invalid API key"}`.
* `startDate` and `endDate` are **required** and must be ISO-8601; omitting
  either answers `400 {"message":["startDate must be a valid ISO 8601 date
  string", ...]}`. An unbounded listing therefore asks from the Unix epoch.
* `page`/`limit` are accepted but **ignored**: a request with `limit=5`
  returned all 937 posts in its window. There is no pagination metadata and
  no cursor - one request per window returns everything.
* There is **no read-by-id endpoint**: `GET /api/public/v1/posts/<id>`
  answers `404`. The listing is the only source of post content, which is why
  the provider caches the listing for `Fetch`.
* `GET /api/public/v1/posts` with no query is the API root redirect
  (`307` → `/auth`); the API itself lives under `/api/public/v1/`.
* The full 1,710-post history measured 1.6 MB of JSON in one response; the
  client caps a response at 64 MB.

The instance base URL may be written either way (`https://host` or
`https://host/api`); the client normalizes both to the same API root.

## Response shape

`GET /api/public/v1/posts` returns `{"posts":[...]}` - no envelope metadata.
Fields the importer consumes:

```json
{
  "id": "cmcf8xhds0012nx5i70ynwyln",
  "content": "After a week of AI-buzz around me I finally sat down…",
  "publishDate": "2025-06-27T20:09:00.000Z",
  "releaseURL": "https://www.linkedin.com/feed/update/urn:li:share:7344456950408970240",
  "releaseId": "urn:li:share:7344456950408970240",
  "state": "PUBLISHED",
  "error": null,
  "intervalInDays": null,
  "group": "…",
  "creationMethod": "API",
  "settings": "{\"__type\":\"linkedin\"}",
  "tags": [],
  "integration": {
    "id": "cm866hm190007rkm6stkugtgn",
    "providerIdentifier": "linkedin",
    "name": "Ilja Lapkovsky",
    "picture": "https://postiz.mist.lv/uploads/…"
  }
}
```

Observed details:

* **One record per platform copy.** A cross-post to five networks is five
  records with five ids; `integration.providerIdentifier` carries the
  platform (`linkedin`, `x`, `bluesky`, `threads`, `mastodon`, `instagram`).
  The `group` value was unique per record in this capture, so it is kept as
  metadata only - it does not reliably link copies.
* `content` is **plain text** (no HTML, no markup), with paragraphs separated
  by blank lines. There is no title field.
* `settings` is a JSON **string**, not an object, and its `__type` does not
  always match `integration.providerIdentifier` (376 of 1,710 records
  differed: thread finisher settings on Bluesky copies, for example). The
  provider uses `providerIdentifier`, never `settings.__type`.
* `tags` was empty on every captured record.
* There is no `updatedAt` and no ETag. `publishDate` is the only timestamp.
* `error` was `null` on every record, including the 95 `ERROR` ones.
* `releaseURL` is present on published records and `null` on unpublished
  ones.

## Post states

| state | count | meaning | imported |
|---|---|---|---|
| `PUBLISHED` | 1,579 | live upstream | always |
| `ERROR` | 95 | failed to publish | never |
| `QUEUE` | 33 | scheduled, publish time in the past | only with `options.include_scheduled` |
| `DRAFT` | 3 | never scheduled | never |

Future-scheduled posts are never imported, even with `include_scheduled`: the
listing's upper bound is "now", so a post whose `publishDate` has not arrived
is outside the window.

## Provider mapping

* `shared.Item.ID` is the Postiz **post id** (per platform copy), and
  `Item.Ref` is empty because there is nothing to fetch by id.
* `Item.ETag` is `sha256:` over the content hash, state, publish date and
  platform, because upstream exposes no ETag or modified time. A `QUEUE` post
  that gets published changes this fingerprint even though its publish date
  does not move.
* `Item.LastModified` is `publishDate` - the honest stand-in for a
  modification time.
* `Fetch` serves the post from the listing made earlier in the same run. An
  item that was not listed fails with an explicit error instead of inventing
  content.
* The document title is the first line of `content` (max 120 runes, trimmed
  at a word boundary): Postiz has no title, and an empty one would leave every
  cross-post chunk untitled in FTS5.
* The author is `integration.name` (the connected account), with the platform
  identifier as the fallback.
* `Document.Meta` carries `postiz_id`, `state`, `group`, `release_url`,
  `release_id`, `integration_id`, `integration`, `creation_method`, and
  `publish_date`; `url` is the release URL when the post has one.
* Chunking is one chunk per post at a **320-token** cap with no overlap
  (`PLAN.md` §3); a longer post splits on blank-line paragraphs. All chunks of
  one record share its `source_id`.

## Limitations

* **Incremental windows use `publishDate`.** A post published at its
  scheduled time falls inside the incremental window, so the normal
  `QUEUE → PUBLISHED` flip is caught. A post whose publish date is old and
  whose state changes later (or whose content is edited) is *not* seen by an
  incremental run: the API offers no modification time to filter on. Run
  `agent ingest --source postiz --full` to reconcile those.
* Drift deletion (an upstream post disappearing) is only visible on an
  unbounded first run, like every other provider: the API always bounds a
  listing by date, and a bounded list cannot prove absence.
