---
okf_version: "0.2"
---

# Docs

* [API shapes](api-shapes.md) - Index of captured upstream API response shapes (Ghost, Postiz) that migrations/0001_init.sql must stay honest to.
* [Ghost Admin API shape (live capture)](ghost.md) - Placeholder for the captured Ghost Admin posts response; the honest schema source for migrations/0001_init.sql once Phase 0 lands.
* [Postiz API shape (live capture)](postiz.md) - Placeholder for the captured Postiz posts response; the honest schema source for migrations/0001_init.sql once Phase 0 lands.
* [Model server contract](localai.md) - What the agent requires of any OpenAI-compatible model server: endpoints, the 768-dimension embedding rule, the reasoning-model max_tokens caveat, and the configuration surface.
* [MVP bring-up review](mvp-review.md) - The prioritized review of the setup for an MVP that is extendable but trivial to bring up: findings with severities, the top five bring-up blockers, and the scope cuts to make.
* [Import configuration and provider contract](sources-config.md) - The sources.yaml schema and chunking validation, credential warnings and strict mode, item identity, the Provider interface, the incremental/full ingest ledger, and the CLI surface for enabling, disabling, and reconfiguring providers.
* [Index store](store.md) - The SQLite index layer: the pure-Go driver and its pragmas, the embedded migrations, the chunk write path, and the BM25 and cosine read paths.

# Runs

* [Store layer verification](runs/2026-10-25-store-layer.md) - The full verification suite after the store layer landed, including the DDL smoke check against the new chunk_vectors table.
* [Source config review verification](runs/source-config-review-2026-10-25.md) - Offline build, vet, tests, credential probes, and schema/documentation checks.
* [SQLite schema DDL check](runs/0001-init-ddl-check.md) - The migrations/0001_init.sql smoke check passed with the contentless-FTS5 join-back fix verified.
* [MVP bring-up verification](runs/0002-mvp-bringup-verification.md) - The full verification suite plus the DDL smoke check and the bring-up preflight, all green on the bring-up branch.

# Subdirectories

* [runs/](runs/) - Timestamped OKF captures of validator runs (build, DDL check); newest first, one file per run.
