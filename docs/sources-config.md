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
key is a hard error rather than a silently ignored setting; a referenced
env var that is not set fails fast, and its value is never printed;
`--config` overrides the path (default `./sources.yaml`).

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

`chunks` gains `source_id` (the config id) and `chunk_hash`, so a re-run
can tell "text unchanged" from "text changed".

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
agent sources validate            # strict parse + provider option validation
```

## Deferred

The website (HTML) is not a provider yet - deferred 2026-10-23. When it
returns it is one `sources.yaml` entry plus one package, because the
registry is the seam.
