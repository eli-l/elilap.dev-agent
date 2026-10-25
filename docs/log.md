# Directory Update Log

## 2026-10-25

* **Update**: The build plan's machine-specific facts are out of the normative text: the platform-agnostic pass and the shipped `sources.yaml` behaviour are both reflected in §1.5, the decisions table, the compose snippet, and the Phase 1 checklist, and the compose snippet now requires `LOCALAI_BASE_URL` instead of defaulting to `localhost`. [MVP bring-up review](/mvp-review.md)
* **Creation**: Recorded offline review verification and the unavailable OKF validators. [Source config review verification](/runs/source-config-review-2026-10-25.md)
* **Update**: Corrected credential warnings and validate-only strict mode, documented chunking bounds, and reconciled item IDs and content hashes with the migrations. [Import configuration and provider contract](/sources-config.md)
* **Creation**: Captured the MVP bring-up verification run - the four required verification commands plus the DDL smoke check, all green. [MVP bring-up verification](/runs/0002-mvp-bringup-verification.md)
* **Creation**: Added the MVP bring-up review - the top five things that would slow down or break a first-time bring-up, a prioritized findings table with severities and concrete recommendations, and the scope cuts that make the smallest coherent vertical slice explicit. [MVP bring-up review](/mvp-review.md)
* **Update**: The model server contract now states that the agent verifies the endpoint by listing its models, and marks `/readyz` as optional. [Model server contract](/localai.md)
* **Update**: Rewrote the self-hosted LocalAI endpoint concept as the platform-agnostic model server contract - required endpoints, the fixed 768-dimension embedding rule, the reasoning-model max_tokens caveat, the readiness probe, and the configuration surface. The reference-environment facts are kept, explicitly marked non-normative. [Model server contract](/localai.md)
* **Update**: The bundle manifest now describes the model server contract instead of one machine's endpoint. [index.md](/index.md)

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
