---
okf_version: "0.2"
---

# Docs

* [API shapes](api-shapes.md) - Index of captured upstream API response shapes (Ghost, Postiz) that migrations/0001_init.sql must stay honest to.
* [Ghost Admin API shape (live capture)](ghost.md) - Placeholder for the captured Ghost Admin posts response; the honest schema source for migrations/0001_init.sql once Phase 0 lands.
* [Postiz API shape (live capture)](postiz.md) - Placeholder for the captured Postiz posts response; the honest schema source for migrations/0001_init.sql once Phase 0 lands.
* [Self-hosted LocalAI endpoint](localai.md) - Verified facts about the LocalAI instance at http://192.168.78.243:8080: version, model ids, embedding dimensions, and chat behaviour.
* [Import configuration and provider contract](sources-config.md) - The sources.yaml schema and chunking validation, credential warnings and strict mode, item identity, the Provider interface, the incremental/full ingest ledger, and the CLI surface for enabling, disabling, and reconfiguring providers.
* [The in-service agent loop and the A2A surface](agent-architecture.md) - The grounded tool-calling loop that lives in this service, the `search_writing` tool over hybrid retrieval, the Phase 2 loopback dev HTTP surface (`/v1/retrieve`, `/v1/ask`), and the Phase 4 A2A `message/send` surface with citation-or-refusal policy.
* [Logging configuration and module loggers](logging.md) - The zerolog setup, the four module sub-loggers (main, http, sources, db), the global `--log-level`/`--log-format` flags and `AGENT_LOG_*` variables, and what must never reach a log line.

# Runs

* [Source config review verification](runs/source-config-review-2026-10-25.md) - Offline build, vet, tests, credential probes, and schema/documentation checks.
* [SQLite schema DDL check](runs/0001-init-ddl-check.md) - The migrations/0001_init.sql smoke check passed with the contentless-FTS5 join-back fix verified.

# Subdirectories

* [runs/](runs/) - Timestamped OKF captures of validator runs (build, DDL check); newest first, one file per run.
