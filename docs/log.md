# Directory Update Log

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
