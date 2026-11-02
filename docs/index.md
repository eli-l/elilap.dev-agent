---
okf_version: "0.2"
---

# Docs

* [API shapes](api-shapes.md) - Index of captured upstream API response shapes (Ghost, Postiz) that migrations/0001_init.sql and the provider packages must stay honest to.
* [Ghost API shape (live capture)](ghost.md) - Observed Ghost Content/Admin post shapes, key formats, incremental filters, pagination, and the uuid-versus-id fetch detail behind the importer.
* [Postiz API shape (live capture)](postiz.md) - The observed Postiz public-API listing: required date range, no pagination, no read-by-id, one record per platform copy, post states, and the provider's item/chunk mapping.
* [Self-hosted LocalAI endpoint](localai.md) - Verified facts about the LocalAI instance at http://192.168.78.243:8080: version, model ids, embedding dimensions, and chat behaviour.
* [Import configuration and provider contract](sources-config.md) - The sources.yaml schema and chunking validation, credential warnings and strict mode, item identity, the Provider interface, the incremental/full ingest ledger, and the CLI surface for enabling, disabling, and reconfiguring providers.
* [Environment configuration and .env loading](environment.md) - The environment layer: godotenv loading .env at startup, real-environment precedence, the envconfig structs behind every static setting, the full variable inventory with defaults, the example.env rule for new variables, and the dynamic credential lookup for sources.yaml.
* [The in-service agent loop and the A2A surface](agent-architecture.md) - The grounded tool-calling loop that lives in this service, the `search_writing` tool over hybrid retrieval, the Phase 2 loopback dev HTTP surface (`/v1/retrieve`, `/v1/ask`), and the Phase 4 A2A `message/send` surface with citation-or-refusal policy.
* [Logging configuration and module loggers](logging.md) - The zerolog setup, the four module sub-loggers (main, http, sources, db), the global `--log-level`/`--log-format` flags and `AGENT_LOG_*` variables, and what must never reach a log line.

# Runs

* [Postiz full-corpus ingest](runs/ingest-postiz-2026-11-02.md) - First live Postiz public-API ingest: 1,710 posts listed, 280 LinkedIn cross-posts indexed into 298 chunks, and an idempotent incremental rerun.
* [Ghost full-corpus ingest (clean rebuild)](runs/ingest-ghost-full-2026-11-02.md) - A deleted index was rebuilt from the Content API: 126 posts, 588 chunks and FTS rows, and a near-no-op incremental rerun.
* [Ghost windowed ingest (last week)](runs/ingest-ghost-2026-11-02.md) - First live Ghost Content API ingest; two posts, 17 chunks and FTS rows, idempotent rerun.
* [Source config review verification](runs/source-config-review-2026-10-25.md) - Offline build, vet, tests, credential probes, and schema/documentation checks.
* [SQLite schema DDL check](runs/0001-init-ddl-check.md) - The migrations/0001_init.sql smoke check passed with the contentless-FTS5 join-back fix verified.

# Subdirectories

* [runs/](runs/) - Timestamped OKF captures of validator runs (build, DDL check); newest first, one file per run.
