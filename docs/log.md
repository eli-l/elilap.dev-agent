## 2026-11-02

* **Update**: Applied the embedding-pass review fixes: the progress line now holds its lock across the write, `RemoveItem` clears vectors on the live drift-deletion path, embedding stamps are guarded by `content_hash` so a concurrent rewrite cannot store a stale vector, `--progress` is disabled under JSON logs, `Remaining` is filled on error paths, and `runEmbed`/store/LLM guards gained tests. [Embedding pass and the vector index](/embeddings.md)
* **Creation**: Recorded the first live embedding run - 886 chunks through LocalAI's `embeddinggemma-300m` in 12 batches, an interrupted run resumed at 512 without re-fetching, and 886 rows in each of `chunks`, `chunks_fts`, and `chunks_vec`. [LocalAI embedding of the full index](/runs/embed-localai-2026-11-02.md)
* **Creation**: Documented the embedding pass - why it is separate from ingest, the `localai:<model>/text-v1` tag and its three pending conditions, stale-vector cleanup inside the chunk transaction, batching/resume/`--full`, and the `--progress` line on stderr. [Embedding pass and the vector index](/embeddings.md)
* **Update**: Applied the code-review fixes: `--full` no longer double-counts `unchanged` against `indexed`, a blank credential is reported as missing (matching `MissingEnv`), an oversized Postiz response errors explicitly instead of as truncated JSON, multi-byte titles truncate by rune, the missing-platform warning only fires on an unbounded listing, and a real run's JSON summary no longer carries the dry-run plan. [Import configuration and provider contract](/sources-config.md)
* **Update**: Ingest output now follows the configured format: the per-source summary is JSONL under `AGENT_LOG_FORMAT=json` (one object with `listed`/`indexed`/`unchanged`/chunk counts, plus the dry-run plan), so `agent ingest | jq` works. [Logging configuration and module loggers](/logging.md)
* **Update**: Fixed the misleading ingest summary: it labelled the listed candidate count "indexed", so a no-op rerun looked like a small import (`indexed 2 item(s) ... fetched 0`). It now prints `listed`/`indexed`/`unchanged` separately, and `--dry-run` reports `would index n of listed`. [Import configuration and provider contract](/sources-config.md)
* **Creation**: Recorded the first live Postiz ingest - 1,710 posts listed, 280 LinkedIn cross-posts indexed into 298 chunks and FTS rows, and a zero-fetch incremental rerun - plus the endpoint and no-read-by-id findings that shaped it. [Postiz full-corpus ingest](/runs/ingest-postiz-2026-11-02.md)
* **Update**: Documented the live Postiz public-API shape - the required `startDate`/`endDate` listing at `/api/public/v1/posts`, ignored `page`/`limit`, the missing read-by-id endpoint, one record per platform copy, plain-text content, post states and the `QUEUE`/future-scheduled rules, and the content-etag/incremental limitation. [Postiz API shape (live capture)](/postiz.md)
* **Update**: The Postiz provider is implemented: registry constructor, platform and state filters, 320-token per-post chunking, and the shared credential resolver both providers now use. [Import configuration and provider contract](/sources-config.md)
* **Update**: Both capture index entries are real now, with the fixtures each provider test replays. [API shapes](/api-shapes.md)
* **Creation**: Recorded the clean-database full Ghost ingest - 126 posts, 588 chunks and FTS rows in about eight seconds across two paginated pages, with a near-no-op incremental rerun - plus the retry transport, Admin limit clamp, and `include_drafts` warning it verified. [Ghost full-corpus ingest (clean rebuild)](/runs/ingest-ghost-full-2026-11-02.md)
* **Update**: Documented the shared provider HTTP client (per-request timeout, retry with backoff on 408/429/5xx and network errors, `Retry-After`), the Admin API page-size clamp, and that `include_drafts` is inert with a Content key. [Ghost API shape (live capture)](/ghost.md)
* **Update**: Noted the Ghost option caveats (`include_drafts` needs an Admin key, `limit` clamps to 100) and the shared retrying HTTP client every provider now uses. [Import configuration and provider contract](/sources-config.md)
* **Creation**: Captured the live Ghost Content API shape - key formats, `updated_at` filter syntax, the 100-post page cap, `primary_author` as an object, and the uuid-versus-id read endpoint - from the first real ingest. [Ghost API shape (live capture)](/ghost.md)
* **Creation**: Recorded the first windowed Ghost ingest run: two posts from the last seven days, 17 chunks and FTS rows, and an idempotent rerun. [Ghost windowed ingest (last week)](/runs/ingest-ghost-2026-11-02.md)
* **Update**: Documented the provider constructor registry, the Content API fallback for bare keys, the `--since` windowed ingest (no drift deletion), and the new `agent stats` surface. [Import configuration and provider contract](/sources-config.md)

## 2026-10-28

* **Creation**: Documented the environment layer - `.env` loaded at startup by godotenv, real-environment precedence, the `envconfig` structs behind every static setting, the complete variable inventory with defaults, the `example.env` rule for new variables, and the dynamic credential lookup for `sources.yaml`. [Environment configuration and .env loading](/environment.md)
* **Update**: Clarified that a copied template's blank credentials count as missing, so validation matches the runtime resolver. [Environment configuration and .env loading](/environment.md)
* **Update**: Logging values now come from an `envconfig` struct decoded after the startup `.env` load, with empty treated as the default, and the doc links the environment inventory. [Logging configuration and module loggers](/logging.md)
* **Update**: Recorded that credential values are resolved through `envcfg.Lookup` after `.env` loading, that `example.env` is their committed inventory, and that blank credentials are validated as missing. [Import configuration and provider contract](/sources-config.md)

## 2026-10-27

* **Creation**: Documented the zerolog logging setup - the single root with four module child loggers (main, http, sources, db), the `--log-level`/`--log-format` flags over `AGENT_LOG_LEVEL`/`AGENT_LOG_FORMAT`, stderr-only output, and the no-secrets/no-bodies rule. [Logging configuration and module loggers](/logging.md)
* **Update**: Planned a Phase 2 loopback dev HTTP surface - `agent serve` with `/v1/retrieve` returning exactly what `search_writing` hands the model, and `/v1/ask` running the full agentic loop - graduating token-gated into the Phase 4 A2A server. [The in-service agent loop and the A2A surface](/agent-architecture.md)
* **Creation**: Added the agent-architecture concept - the in-service grounded tool-calling loop, the `search_writing` tool, the Phase 4 A2A `message/send` surface, and the citation-or-refusal policy - recording the 2026-10-27 scope change (the agent lives here). [The in-service agent loop and the A2A surface](/agent-architecture.md)

# Directory Update Log

## 2026-10-25

* **Creation**: Recorded offline review verification and the unavailable OKF validators. [Source config review verification](/runs/source-config-review-2026-10-25.md)
* **Update**: Corrected credential warnings and validate-only strict mode, documented chunking bounds, and reconciled item IDs and content hashes with the migrations. [Import configuration and provider contract](/sources-config.md)

## 2026-10-23

* **Creation**: Added the import-configuration concept - sources.yaml schema, the Provider interface and registry, the state ledger, and the incremental vs full ingest modes. [Import configuration and provider contract](/sources-config.md)
* **Creation**: Added the verified self-hosted LocalAI endpoint concept - endpoint, version, model catalog, the 768d embedding check, and the reasoning-model token caveat. [Self-hosted LocalAI endpoint](/localai.md)
* **Update**: Bundle manifest now lists every concept file; the Ghost and Postiz captures were previously missing from the index. [index.md](/index.md)

## 2026-10-21

* **Creation**: Authored the OKF v0.2 skill description and usage workflow for future agent runs. [AGENTS.md](../AGENTS.md)
* **Creation**: Authored the timestamped run-capture concept, defining how validator output gets recorded as verified bundle concepts. [runs/run.md](/runs/run.md)
* **Creation**: Authored the bundle-root manifest listing the docs concepts and the runs/ subdirectory. [index.md](/index.md)
* **Creation**: Authored the update-history log as the bundle's newest-first audit trail. [log.md](/log.md)
* **Creation**: Added OKF v0.2 frontmatter (type, title, description, status, generated, tags) to the three placeholder docs. [API shapes](/api-shapes.md), [Ghost Admin API shape (live capture)](/ghost.md), [Postiz API shape (live capture)](/postiz.md)
* **Creation**: Captured the observed SQLite DDL validator output and stamped the human-verified audit trail entry. [SQLite schema DDL check](/runs/0001-init-ddl-check.md)
