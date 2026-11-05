---
type: Reference
title: Import configuration and provider contract
description: The sources.yaml schema, the Provider interface, the incremental and full ingest ledger, and the CLI surface for enabling, disabling, and reconfiguring import providers.
status: stable
generated:
  by: dave
  at: 2026-10-23T20:48:00Z
tags: [config, providers, ingest, yaml, plan]
---

# Import configuration and provider contract

How providers are declared, how the ingestor decides what to re-fetch, and
how a full reindex differs from an incremental one. `PLAN.md` §2.6 is the
normative version; this file is the readable summary.

**Status: shipped.** Both providers, the strict decoder, credential
resolution, `agent sources list|validate`, and the incremental/full ingest
modes are in the binary; `docs/runbook.md` has the operator commands.

## The config file: `sources.yaml`

One static file at the repo root. It declares every import provider and
nothing else - no secrets, no runtime state.

```yaml
version: 1

defaults:
  chunking: { target_tokens: 400, overlap_tokens: 60 }
  user_agent: "elilap-dev-agent/0.1 (+https://elilap.dev)"

sources:
  - id: ghost
    type: ghost
    enabled: true
    credentials:
      api_url: { env: GHOST_API_URL }
      api_key:  { env: GHOST_API_KEY }
    options: { include_drafts: false, limit: 500 }

  - id: postiz
    type: postiz
    enabled: true
    credentials:
      api_url: { env: POSTIZ_API_URL }
      api_key:  { env: POSTIZ_API_KEY }
    options: { platforms: [instagram, linkedin, x], include_scheduled: false }
```

| key | meaning |
|---|---|
| `version` | schema version; an unknown version is an error |
| `id` | stable name for this provider instance (the ledger key) |
| `type` | which provider implementation to construct (`ghost`, `postiz`) |
| `enabled` | `false` skips the provider without deleting its declaration |
| `credentials` | `{ env: VAR }` references only - values are read from the environment |
| `options` | provider-specific settings, validated against that provider's schema |

Rules: the YAML is decoded **strictly** (`KnownFields(true)`), so an unknown
key is a hard error rather than a silently ignored setting.
`sources validate` warns about credential variables that are unset or blank and
exits 0 by default; `sources validate --strict` treats them as errors and
exits 1. Blank counts as missing because a `.env` copied from `example.env`
contains the credential names with empty values.
`sources list` reports set/unset state and exits 0 for missing credentials;
`--strict` is a validate-only flag. Credential values are never printed.
`--config` overrides the path (default `./sources.yaml`).

Credential **values** come from the environment under the names declared here;
because those names are configuration data, they cannot be `envconfig` struct
fields. They are read through `envcfg.Lookup` after the binary has loaded
`.env` at startup, so a local `.env` works for `agent sources list` and
`validate` too. The committed inventory of those names is `example.env`; see
[Environment configuration and .env loading](environment.md).

`defaults.chunking.target_tokens` is required and must be positive.
`overlap_tokens` defaults to zero and must be non-negative and less than
`target_tokens`. Invalid chunking defaults are configuration errors.

## The provider contract

```go
type Provider interface {
    Type() string
    List(ctx context.Context, since time.Time) ([]Item, error) // cheap candidates
    Fetch(ctx context.Context, it Item) (Document, error)      // full content
}
```

`List` is cheap, `Fetch` is expensive - that split is what makes an
incremental run cheap. A registry maps the config `type` to an options
parser and a constructor; an unknown `type` or invalid options is a startup
error. A type may have options but no constructor yet (a provider whose
client has not landed): ingest reports it as not implemented instead of
silently importing nothing. Both built-ins, `ghost` and `postiz`, are
implemented.

The Ghost provider accepts both credential shapes: an Admin key
(`<id>:<secret>`) uses the JWT-authenticated Admin API, while a bare key uses
the read-only Content API with `?key=`. See [Ghost API shape](ghost.md) for
the captured requests and the uuid-versus-id detail. `options.include_drafts`
only takes effect with an Admin key; with a Content key the provider warns
and returns published posts only. `options.limit` is clamped to the API's
100-post page size in both clients, so a larger configured value pages
instead of failing.

Both clients share `shared.NewHTTPClient`: a 60-second per-request timeout
and a retry transport for network errors and `408`/`429`/`5xx` (three total
attempts, exponential backoff, `Retry-After` honored, cancelled contexts
stop immediately). Retries live in the transport, so every provider gets the
same behavior without touching its request code. Credential `{ env: VAR }`
references are resolved through `shared.ResolveEnvRef`, which names a missing
variable in its error and never prints a value.

The Postiz provider reads `GET <instance>/api/public/v1/posts`, which
**requires** a `startDate`/`endDate` window and returns every post in it: no
pagination, and no read-by-id route. `options.platforms` selects the
`integration.providerIdentifier` values to import (`linkedin`, `x`,
`bluesky`, `threads`, `mastodon`, ...; normalized to lower case). Published
posts are always imported; drafts and failures never are; scheduled (`QUEUE`)
posts only with `options.include_scheduled`, and only once their publish date
has passed. Because the API exposes neither an ETag nor a modification time,
the ledger fingerprint hashes content, state, publish date, and platform,
`last_modified` carries `publishDate`, and an incremental window is bounded
by `publishDate` - a post whose publish date is old and whose state changes
later needs `--full`. `Fetch` serves the post from the listing made in the
same run, since that is the only place the content exists. Cross-posts chunk
at 320 tokens with no overlap, independent of the configured defaults. See
[Postiz API shape](postiz.md) for the captured requests and fields.

## The ledger

`migrations/0002_state.sql` adds two tables:

* `source_state` - one row per configured provider: `last_run_at`,
  `last_full_at`, `item_count`, and `config_hash` (the hash of the
  resolved provider config).
* `source_items` - one row per upstream item: `content_hash`, `etag`,
  `last_modified`, `first_seen_at`, `last_seen_at`, and `seen_run_id`.

`chunks.source_id` remains the upstream **item id** (Ghost uuid / Postiz
id), not the config id. `chunk_id = sha256(source_kind|source_id|chunk_seq)`
therefore distinguishes different items; `chunk_seq` is an ordinal within
that item, including all of its platform chunks. The ledger's `source_id`
is the config id, and `source_items.item_id` corresponds to `chunks.source_id`.
`chunks.content_hash` already stores the hash of normalized chunk text for
change detection; no additional chunk hash column is needed.

Phase 1 must preserve this distinction when joining the ledger to chunks
and deleting items. The current index shares an item across configurations
with the same source kind and upstream item id; independent upstreams with
colliding item ids require a namespace/schema decision before support is added.

## Ingest modes

* **Incremental (default)** - list candidates since
  `last_run_at - overlap`, skip items whose cheap fingerprint is unchanged,
  and fetch and chunk the rest. The listing is bounded, so it does **not**
  delete items it did not see: an older item that quietly fell outside the
  watermark window is indistinguishable from a deleted one. Drift deletion
  happens on an unbounded list only (a full run without `--since`).
* **Full reindex (`--full`)** - re-fetch, re-chunk and **re-embed**
  everything. Decided 2026-10-23: the index is worth rebuilding exactly as
  the sources stand today, and the corpus is small enough that the CPU cost
  is acceptable.
* **Windowed (`--since`)** - an explicit lower bound for listing:
  RFC3339, `YYYY-MM-DD`, or a relative duration (`168h`, `7d`, `1w`).
  `--since 7d` indexes the last week. A windowed list cannot prove that an
  item outside the window was deleted upstream, so drift deletion is
  disabled for that run; like an incremental run, only a full unbounded
  list may delete drift.
  The bound is applied to `updated_at`, so a post published earlier but
  edited this week is intentionally re-fetched. Postiz exposes no modified
  time, so its window is bounded by `publishDate` instead (see the provider
  notes above).

## CLI

```
agent ingest                      # incremental, all enabled providers
agent ingest --source ghost       # one provider
agent ingest --full               # full reindex: re-fetch + re-chunk + re-embed
agent ingest --full --source postiz
agent ingest --dry-run            # what would change, no writes
agent ingest --source ghost --since 7d   # windowed run: the last week
agent stats                       # chunk, FTS, vector and ledger counts
agent sources list                # enabled/disabled + resolved options (no secrets)
agent sources validate            # validate config/options; missing credentials warn
agent sources validate --strict   # missing credentials fail (exit 1)
```

Each run prints one summary line per source, in the configured output format
(`AGENT_LOG_FORMAT` / `--log-format`):

```
ghost: since 2026-11-01T20:11:31Z: listed 1, indexed 0, unchanged 1, chunks +0/~0, removed 0
```

```json
{"source_id":"ghost","source_kind":"ghost_post","since":"2026-11-01T20:11:31Z","listed":1,"indexed":0,"unchanged":1,"chunks_added":0,"chunks_updated":0,"removed":0,"dry_run":false}
```

`listed` is what the provider returned for the window (bounded by the
incremental watermark, `--since`, or unbounded for a first/full run),
`indexed` is how many of those were actually fetched and written, and
`unchanged` is the rest - candidates the ledger already knew. An incremental
rerun on an unchanged corpus therefore reads `listed 2, indexed 0`, not
"indexed 2": listing is cheap and expected every run, writing is not.
`--dry-run` prints `would index <n> of <listed>` plus the planned item ids
and never writes; in JSON it is the same object with `"dry_run": true` and
`planned` / `planned_removals` arrays.

## Deferred

The website (HTML) is not a provider yet - deferred 2026-10-23. When it
returns it is one `sources.yaml` entry plus one package, because the
registry is the seam.
