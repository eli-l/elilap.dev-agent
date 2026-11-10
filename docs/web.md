---
type: Reference
title: HTML website provider and per-site extraction strategies
description: The generic web provider - sitemap/feed/seed discovery, robots-and-delay politeness, the auto/article/selectors/body strategy chain, host Adapters for sites the generic passes cannot read, the `agent sources inspect` authoring loop, and how page chunks are identified as web_page.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-09T11:30:00Z
tags: [providers, web, html, ingest, extractors, config]
sources: ["../PLAN.md", "../sources.yaml", "sources-config.md"]
---

# HTML website provider and per-site extraction strategies

`internal/sources/web` is the third provider type, after `ghost` and `postiz`.
It indexes a **public website** as pages: `elilap.dev` today, where the Astro
site carries services, case studies, and project/AI pages that the Ghost
publication (`read.elilap.dev`) does not.

**Status: implemented and exercised live on 2026-11-09** (`agent sources
inspect` on two pages, a full `ingest --source website` of 12 pages into 56
chunks, an idempotent incremental rerun, and the `web_page` migration). The
Ghost source remains the owner of blog posts; this provider deliberately does
not re-index them.

## Why a separate provider

The Ghost provider talks to an API. A static site has no API: the content is
HTML, the page list is a sitemap, and the "what is the content" question has a
different answer per layout. The web provider keeps those two concerns apart:

* **which pages** to index is configuration (`options:` in `sources.yaml`);
* **how to read one page** is a *strategy*, selected by name.

Ingest is deterministic: no model is called to decide anything at runtime. The
LLM-assisted part happens **offline, while authoring the config**, through
`agent sources inspect` ([see below](#authoring-a-strategy-with-an-llm)).

## Configuration

```yaml
  - id: website
    type: web
    enabled: true            # no credentials block: the site is public
    options:
      site_url: https://elilap.dev
      sitemap: true
      feeds: false
      strategy: elilap
      author: "Eli Lap"
      max_pages: 100
      include_paths: ["^/$", "^/ai(/|$)", "^/case-study/", "^/services$"]
      exclude_paths: ["^/blog$", "^/tag/"]
```

| option | meaning |
|---|---|
| `site_url` | **required** origin (`http`/`https`, no path, query, or fragment); every URL outside its host is ignored |
| `sitemap` | read `/sitemap.xml`, following one `sitemapindex` level (default `true`) |
| `feeds` | probe `/rss.xml`, `/feed.xml`, `/atom.xml`, `/feed` (default `true`) |
| `start_urls` | extra seeds, absolute or site-relative; always listed |
| `crawl_depth` | follow same-site links from `start_urls`, 0-3 hops (default `0`; `1` lists the links found on the seeds) |
| `include_paths` / `exclude_paths` | regular expressions on the URL path; include first, then exclude |
| `max_pages` | cap per listing (default `200`); see the truncation rule below |
| `strategy` | `auto` (default), `article`, `selectors`, `body`, or a registered adapter name |
| `selectors.content/title/author/tags/date/remove` | CSS selectors for the generic extraction |
| `author` | fallback byline for pages that publish none |
| `min_chars` | text a candidate region must hold before the next candidate wins (default `200`) |
| `delay_ms` | politeness delay between requests (default `250`; explicit `0` disables) |
| `respect_robots` | obey `/robots.txt` (default `true`) |

A web source has **no credentials**. `credentials` is optional and unused:
`api_url`/`api_key` exist for API providers, and a public URL is configuration,
not a secret, so it lives in `options.site_url`. Adding this source therefore
added no `example.env` variable.

`include_paths` matters more than it looks. A sitemap lists tag/listing pages,
assets, and often a `tags` page that would flood the index with navigation
text; the elilap.dev sitemap lists 61 URLs of which 12 are real content pages.
`/blog` is excluded because it is a listing that links to Ghost posts, which
`read.elilap.dev` already provides through the Ghost provider.

## Discovery

`List` merges, deduplicates by normalized URL, and sorts:

1. `sitemap.xml` - a `<urlset>`, or a `<sitemapindex>` whose children are read
   (capped at 20). Each entry's `lastmod` becomes the item date.
2. The conventional feeds - RSS 2.0 or Atom. A feed entry's `pubDate` /
   `published` / `updated` becomes the item date.
3. `start_urls`.
4. An optional same-site link crawl from `start_urls`.

Every candidate is filtered to the `site_url` host - the same rule the client,
the adapters, and `agent sources inspect` use, so the `www`' alias of a
configured apex counts as the same site (`www` pages are ingested and fetched,
not dropped) - dropped when its extension is an asset, and checked against
include/exclude patterns. The **newest**
timestamp any discovery source reports wins - sitemap `lastmod`, Atom `updated`
vs `published`, RSS `<dc:date>` vs `pubDate` - so an edit inside the
incremental window is never hidden behind the original publication date.

Failure policy is deliberately asymmetric:

* an **absent** source contributes nothing - a 404/410, a refused probe
  (401/403), a `/sitemap.xml` that answers `200` with HTML (an SPA fallback or
  soft-404), and a robots.txt-disallowed feed probe all mean "not served here",
  so feeds and seeds still list the site;
* a **200 HTML page at `/feed`** is an absent feed, not a broken one: the
  conventional probes are best-effort and log at debug;
* a **failed sitemap or child sitemap** is a hard error on every run, because
  the page set cannot be trusted at all;
* any **failed crawl page** - not just a failed seed - makes the crawl's
  contribution *incomplete*: the page is fetched to discover its links, so
  losing it hides pages from the listing. A **failed feed probe (HTTP/transport)
  or crawl** is reported as *incomplete discovery*: an unbounded run refuses it
  outright, while an incremental window logs a warning and proceeds (a windowed
  listing can never delete drift);
* an **empty listing** is refused on an unbounded run even when no source
  reported an error, since zero candidates would delete every chunk of the
  source;
* a `sitemapindex` with more than 20 children is refused rather than truncated
  (same reason), a child sitemap on another host is never fetched, and a child
  that is *itself* a `sitemapindex` is refused rather than silently dropped -
  the index is site-controlled content, not operator content.

A listing is *unbounded* when `since` is zero - the first run and `--full`.
If `max_pages` would cut such a listing short, `List` **fails** rather than
truncate: otherwise the ingestor would treat the cut pages as drift and delete
their chunks. A bounded (incremental) listing may truncate, because it never
deletes anything - it keeps the **newest** pages first (undated pages last), so
the cut drops the least recently changed candidates.

## Extraction strategies

`Fetch` downloads one page, parses it with `golang.org/x/net/html`, and runs
the configured strategy. CSS selectors are matched with
`github.com/andybalholm/cascadia`, added for this provider.

| strategy | behaviour |
|---|---|
| `article` | first `<article>`, else `<main>`, else `[role=main]`, else `<body>`; a candidate must hold `min_chars` of text, `<body>` is the last resort |
| `selectors` | `selectors.content` must match, else the page fails with an error naming the selector |
| `body` | the whole `<body>`, chrome removal still applied |
| `auto` | configured `selectors.content` if set, else a matching Adapter, else the `article` chain |
| `<adapter name>` | a Go adapter, used unconditionally |

Chrome removal runs on the selected region before rendering: `nav`, `footer`,
`aside`, `form`, `iframe`, `svg`, `button`, `script`, `style`, `noscript`,
`template`, plus any `selectors.remove`. `<header>` is deliberately kept - many
article layouts put the `<h1>` inside it - so the headline is read *before*
removal.

Metadata is read from the page, in this order:

* **title** - `selectors.title`, first `h1` (then `h2`) in the region,
  `og:title`, the JSON-LD `headline`/`name`, `<title>`;
* **author** - `selectors.author`, JSON-LD `author`, `meta[name=author]`,
  `article:author` (ignored when it is a profile URL), then `options.author`;
* **dates** - `selectors.date`, the first `<time datetime>` in the region,
  `article:published_time`, `article:modified_time`, JSON-LD
  `datePublished`/`dateModified`, then the discovery timestamp;
* **tags** - `selectors.tags`, `article:tag`/`keywords` meta, JSON-LD
  `keywords`/`articleSection`, and finally the first URL path segment;
* **JSON-LD** - objects, arrays, and `@graph` are all decoded; a malformed
  block is ignored rather than failing the page.

`Body` is the Markdown rendering (through the shared `HTMLToMarkdown`, so no
second HTML-to-text path exists) and `HTML` is the content region itself, which
`ingest.BuildChunks` prefers because it splits on real headings.

Queues and edge cases:

* `meta[name=robots]` containing `noindex` - the page is recorded in the
  ledger, **no chunks are written**, and a warning names it.
* a page with no extractable text (`ErrNoContent`, e.g. elilap.dev's empty
  `/cases` route) is skipped the same way: ledger row, no chunks, one warning.
  One content-free URL never fails a whole site.
* `agent sources inspect` reports both cases in its `preview` block
  (`skipped: noindex` / `skipped: page has no extractable content`), so the
  authoring output never claims ingest will write a page it will not. The same
  block reports the title, Markdown size, tags, and the publish/update dates
  ingest would write - including the sitemap/feed timestamp the discovery pass
  supplies.
* a non-HTML content type is an error - a PDF served as a page is a bug, not
  content.

## Per-site adapters (the Go seam)

A site whose layout no selector can express gets an `Adapter`: choose the
content node, supply metadata overrides, let the shared renderer do the rest.

```go
type Adapter interface {
    Name() string                                     // options.strategy name
    Match(u *url.URL) bool                            // used by strategy auto
    Select(doc *html.Node, u *url.URL) (Selection, error)
}

type Selection struct {
    Node            *html.Node
    Title, Author   string
    Tags            []string
    RemoveSelectors []string
}
```

Registration is one file plus one line in `DefaultAdapters()`
(`internal/sources/web/adapter.go`), the same seam shape as the provider
registry. `adapter_elilap.go` is the reference implementation: it prefers
`<article>` (case studies), falls back to `<main>`, then `<body>`, sets the
byline the Astro site only renders as text, tags pages by URL section
(`/case-study/...` -> `case-study`), and removes share/newsletter blocks.

**Use an adapter only when configuration cannot do the job.** A selector change
is a `sources.yaml` edit; an adapter is Go code, a test, and a rebuild. The
`elilap` adapter exists because the site publishes no JSON-LD, no byline, and
uses two different content containers across page families.

## Authoring a strategy with an LLM

```
agent sources inspect --source website --url https://elilap.dev/services
agent sources inspect --url https://elilap.dev/services --format json
```

`inspect` fetches the page with the source's real options, then reports:

* status, content type, title, matching adapters, JSON-LD types, headings;
* **content candidates**: every region holding text, with a stable CSS selector
  (`main > section:nth-of-type(3) > article`, ids used where present, classes
  deliberately not used), its text volume, link density, and heading count.
  Candidates are ranked by text volume discounted by link density with a bonus
  for `<article>`, and ancestors that merely repeat a descendant's text are
  collapsed to the deepest node;
* what each discovery source returns (sitemap/feed/seed/crawl counts), and the
  reason when supplementary discovery failed (`incomplete: ...`);
* a **suggested** `strategy` + `selectors.content` block with a one-line reason.

That report - text for a human, JSON for a model - is the whole "let the LLM
pick the strategy" story: paste it into a session (or read it yourself), then

* if the winner is `<article>`/`<main>`, keep `strategy: auto`;
* if it is another element, set `strategy: selectors` and paste the reported
  selector as `selectors.content`;
* if the page cannot be expressed with one selector (content split across
  siblings, metadata only in prose), write an Adapter and set
  `strategy: <name>`.

`inspect` prefers `--source`; without it, it uses the first `type: web` source
whose host matches the page (`www`-insensitive, ports ignored) - **including a
disabled one**, which is the normal pre-enable authoring case. It enumerates
the sitemap and feeds for the counts and the page timestamp but **skips the
crawl**, so an inspection never walks the site. A sibling source with invalid options is
skipped with a warning instead of aborting the report. `--format` is validated
before any network request.

Re-run `inspect` after a layout change; ingest never consults a model, so the
extraction a run uses is always the one written down in the config or the code.

## Item identity, kind, and the ledger

* Item id = **host + path** (`elilap.dev/services`), stable across scheme,
  query, fragment, and trailing slash. `chunks.source_id` is shared by every
  web source, so the host name keeps two sites from colliding on `/services`.
* `source_kind` is **`web_page`**, added by `migrations/0003_web_kind.sql`
  (SQLite cannot alter a CHECK constraint in place, so the migration rebuilds
  `chunks` and recreates its indexes; `chunks_fts`/`chunks_vec` reference
  `chunk_id` only and are untouched). `store.allKinds` lists every allowed kind
  and is what drift removal iterates.
* A page exposes no ETag, so the ledger fingerprints it by its discovery
  timestamp. A page with **no** timestamp at all is re-fetched every run:
  `store.DiffLedger` treats an empty ETag *and* empty `last_modified` as "no
  fingerprint", because `"" == ""` would otherwise freeze that page forever.
* Chunking uses the `defaults.chunking` budget; pages are prose, not social
  posts, so no per-kind cap applies.
* `retrieval` accepts `source=web` (or `web_page`) alongside `ghost`/`postiz`.

## Politeness

Every request goes through one client that sets the configured `User-Agent`
(`defaults.user_agent`, falling back to `elilap-dev-agent/0.1`), waits
`delay_ms` between requests, caps a response at 8 MiB, and - when
`respect_robots` is on - obeys `/robots.txt`: `User-agent`, `Allow`, `Disallow`
with `*` and `$` patterns, longest match wins, `Allow` breaks a tie, and the
group naming the agent wins over `*`. A robots.txt that cannot be read is
treated as permissive and logged; an absent one allows everything. `robots.txt`
is read once per origin per run.

This provider is for sites you control or have permission to index. The
`elilap.dev` source is Eli's own site, and its `/robots.txt` allows the agent.

## Tests and fixtures

`internal/sources/web/testdata/` holds **trimmed real captures** (2026-11-09):
two `elilap.dev` pages (`case-study.html`, `services.html` - `<main>` kept,
head meta kept, a header/nav/footer and a sentinel ticker added so chrome
removal is provable), a synthetic JSON-LD article, a `noindex` page, the
sitemap index plus one child urlset, and the RSS feed.

```bash
go test -tags sqlite_fts5 ./internal/sources/web
```

Coverage: option parsing/validation and defaults, sitemap/feed parsing
(including Atom, relative locs, and timestamp formats), URL normalization and
path filters, robots parsing/matching, all four strategies plus the adapter,
JSON-LD metadata, `ErrNoContent`, `noindex`, and - through `httptest` - `List`
filtering/`since`/truncation rules, `Fetch` content-type and robots
enforcement, and the `inspect` report.

## Limits (deliberately out of scope)

* **No browser.** JavaScript-rendered content is invisible; a site whose
  content only exists after hydration needs its API or a rendered capture.
* **No pagination.** `?page=2` listings are excluded by path filters rather
  than followed.
* **No per-page concurrency.** Requests are sequential with a politeness
  delay; the corpus is dozens of pages, not thousands.
* **No upstream writes.** Fetching is read-only, like every other provider.
