---
type: Reference
title: Import configuration and provider contract
description: The sources.yaml schema, the Provider interface, the incremental and full ingest ledger, and the CLI surface for enabling, disabling, and reconfiguring import providers.
status: draft
generated:
  by: dave
  at: 2026-10-23T20:48:00Z
tags: [config, providers, ingest, yaml, plan]
---

# Import configuration and provider contract

How providers are declared, how the ingestor decides what to re-fetch, and
how a full reindex differs from an incremental one. `PLAN.md` §2.6 is the
normative version; this file is the readable summary.

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
`sources validate` warns about unset credential variables and exits 0 by
default; `sources validate --strict` treats them as errors and exits 1.
`sources list` reports set/unset state and exits 0 for missing credentials;
`--strict` is a validate-only flag. Credential values are never printed.
`--config` overrides the path (default `./sources.yaml`).

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
incremental run cheap. A registry maps the config `type` to a constructor;
an unknown `type` or invalid options is a startup error.

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

* **Incremental (default)** - list candidates since `last_run_at`, skip
  items whose cheap fingerprint is unchanged, fetch and chunk the rest, and
  delete the chunks of items that disappeared upstream.
* **Full reindex (`--full`)** - re-fetch, re-chunk and **re-embed**
  everything. Decided 2026-10-23: the index is worth rebuilding exactly as
  the sources stand today, and the corpus is small enough that the CPU cost
  is acceptable.

## CLI

```
agent ingest                      # incremental, all enabled providers
agent ingest --source ghost       # one provider
agent ingest --full               # full reindex: re-fetch + re-chunk + re-embed
agent ingest --full --source postiz
agent ingest --dry-run            # what would change, no writes
agent sources list                # enabled/disabled + resolved options (no secrets)
agent sources validate            # validate config/options; missing credentials warn
agent sources validate --strict   # missing credentials fail (exit 1)
```

## Deferred

The website (HTML) is not a provider yet - deferred 2026-10-23. When it
returns it is one `sources.yaml` entry plus one package, because the
registry is the seam.
