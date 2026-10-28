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
