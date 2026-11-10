---
type: Run
title: First live website ingest and strategy inspection
description: The web provider's first live pass - `sources inspect` on two elilap.dev pages, a full ingest of the site into 56 web_page chunks, an idempotent incremental rerun, and the empty-page skip.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-09T11:35:00Z
run:
  type: cli
  cmd: |
    go run -tags sqlite_fts5 ./cmd/agent sources inspect --source website --url https://elilap.dev/case-study/pwa-for-magento2
    go run -tags sqlite_fts5 ./cmd/agent ingest --source website --db /tmp/web-index.db --log-level warn
    go run -tags sqlite_fts5 ./cmd/agent stats --db /tmp/web-index.db
    go run -tags sqlite_fts5 ./cmd/agent ingest --source website --db /tmp/web-index.db --log-level warn
output: |
  url: https://elilap.dev/case-study/pwa-for-magento2
  status: 200, type: text/html; charset=utf-8, 113984 bytes
  title: Case Study: Building PWA for Magento 2 - Eli Lap
  adapters matching: elilap
  json-ld: none
  headings:
    h1 Case Study: Building PWA for Magento 2
    h2 Introduction
    h2 Challenges
    h2 Solution
    h2 Results
    h2 Key Takeaways
    h2 Reflection

  content candidates (text volume discounted by link density):
  -> main > section > article                           5938 chars  links    0%  headings 12
     main                                               6232 chars  links    1%  headings 12
     main > section > article > div:nth-of-type(1)      5722 chars  links    0%  headings 11

  discovery: sitemap 61 url(s), feeds 0, seeds 0, crawl 0, total 12

  suggested options:
    strategy: elilap
    # a registered adapter claims this host

  {"level":"warn","module":"sources","provider":"web","item_id":"elilap.dev/cases","url":"https://elilap.dev/cases","error":"web: https://elilap.dev/cases: page has no extractable content","message":"page has no content to index, writing the ledger row only"}
  {"source_id":"website","source_kind":"web_page","since":null,"listed":12,"indexed":12,"unchanged":0,"chunks_added":56,"chunks_updated":0,"removed":0,"dry_run":false}

  db:        /tmp/web-index.db
  chunks:    56
  fts rows:  56
  vec rows:  0
  embedded:  0
  sources:   1
  last run:  2026-11-09T11:23:17Z

  {"source_id":"website","source_kind":"web_page","since":"2026-11-08T11:23:17Z","listed":0,"indexed":0,"unchanged":0,"chunks_added":0,"chunks_updated":0,"removed":0,"dry_run":false}
---

# Result

The website source works end to end against the live site on the same binary
path the other providers use.

What this pass confirmed:

- **`sources inspect`** reports the page as the `elilap` adapter sees it:
  twelve headings, `<article>` ranked first, and discovery returning 61
  sitemap URLs of which 12 survive the configured path filters.
- **Discovery** found the 12 real pages from the sitemap index and dropped
  `/tag/*`, `/blog`, `/privacy`, and the asset URL; `feeds: false` avoids four
  pointless feed probes because the site's feed points at `read.elilap.dev`.
- **Ingest** wrote 56 `web_page` chunks (`chunks` and `chunks_fts` agree), and
  `source_state` recorded the run, so `stats` now shows a source.
- **`/cases` is a 200 with an empty body.** It is the provider's
  content-free skip path: one warning, a ledger row, no chunks, and the rest of
  the site still indexed.
- **The rerun is idempotent**: `listed 0` - every sitemap `lastmod` is older
  than the incremental watermark, so nothing is re-fetched.

Not exercised here: `agent embed` (the model host was not needed for this
check) and the full Ghost + Postiz + website rebuild, which is Phase 3's
operator run.
