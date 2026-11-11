# Product priorities

Updated: 2026-11-09. Status: proposed product direction.

## Product promise

Other agents can consult this agent to understand Eli Lap's published
technical knowledge, experience, and opinions, then reuse the answer with
verifiable evidence and explicit limits.

When the writing does not answer a caller's question, the caller can explicitly
submit the missing knowledge as a request for Eli to review.

The primary caller is an agent researching a topic, preparing a brief, or
checking an attribution. Its core question is: “What does Eli's published
writing establish about this, and what evidence can I carry forward?”

This document prioritizes product outcomes. [PLAN.md](PLAN.md) remains the
source of truth for implementation scope, sequencing, and exit criteria.
Proposals here do not change the existing wire contract or authorize changes
to locked decisions; accepted work should be reconciled with the plan before
implementation. Current contracts are in [docs/a2a.md](docs/a2a.md) and
[docs/agent-architecture.md](docs/agent-architecture.md).

Value assessments below are qualitative judgments about usefulness to agent
callers, not measured demand. **Critical** means necessary for dependable
reuse; **High** means a substantial improvement to a core caller task;
**Medium** means useful in a narrower workflow; **Low** means limited added
value without evidence of demand. Order also accounts for dependencies and
existing functionality.

## MVP capabilities, in priority order

The MVP is a dependable question-to-evidence service. It should let a caller
discover the service, ask a question, verify the result, and decide what to
do next without interpreting ambiguous prose.

| Priority | Capability | Value to other agents | MVP scope and current gap |
|---|---|---|---|
| 1 | Grounded answers with reusable evidence | **Critical:** callers can incorporate your knowledge while preserving attribution and checking its basis. | Preserve the shipped Q&A loop and return a versioned structured answer with claims linked to evidence. Include exact supporting excerpts, chunk IDs, source URLs, titles, publication dates, and content hashes. Today citations carry chunk IDs and URLs, but do not provide this complete evidence package. |
| 2 | Explicit answerability and failure outcomes | **Critical:** callers can distinguish missing evidence from a service failure and choose whether to narrow, retry, or consult another source. | Distinguish `answered`, `partial`, and `insufficient_evidence`; report unanswered parts. Keep timeout, backend failure, and budget exhaustion as operational failures with appropriate retry guidance. Preserve the refusal text for compatibility where required. The documented token-exhaustion-to-refusal behavior needs correction. |
| 3 | Submit missing-knowledge requests and present them to Eli | **High for callers and Eli:** turns an unanswered question into an actionable request and reveals which knowledge other agents actually need. | After a missing or partial answer, let the caller explicitly submit its question and optional reason/context. Persist it durably, return a request ID, and present a reviewable inbox to Eli through the CLI. Ordinary questions and refusals do not automatically create requests. See the request workflow below. |
| 4 | Discoverable capabilities and a stable caller contract | **High:** agents can decide when to delegate and integrate without guessing response semantics. | Extend the existing agent card and integration docs with concrete examples, scope limits, response schema/version, supported filters, and error/retry behavior. Preserve existing clients when extending output. One working peer request and response should demonstrate evidence consumption. |
| 5 | Corpus coverage and freshness | **High:** callers can judge whether the corpus is relevant and whether a negative result is meaningful. | Expose enabled indexed sources, indexed publication-date ranges, last successful sync per source, pending embedding count, and a corpus revision on answers. Distinguish the age of writing from the freshness of ingestion. Indexed date ranges must not imply complete historical coverage. |
| 6 | Reliable unattended availability and refresh | **Critical for ongoing use:** callers can depend on the service and receive newly published knowledge without operator intervention. | Complete scheduled ingest followed by resumable embedding, deployment and recovery work already planned, and basic readiness visibility. Keep model failures distinguishable from corpus gaps. Capture full Ragas and promptfoo runs before treating the MVP as dependable. |

Build priorities 1–3 first while closing the existing evaluation gaps, then
finish discovery, freshness, and unattended operation. All six are required
for the proposed MVP; optional features below should not delay them.

## Missing-knowledge request workflow

This is an explicit submission capability, separate from the grounded answer
loop. The example flow is: a peer receives `insufficient_evidence`, submits
“I need Eli's position on X to finish an architecture comparison,” receives a
durable request ID, and Eli sees the question and reason in his request inbox.

* **Explicit submission — High value:** missing and partial answers advertise
  how to submit a request. The caller chooses whether to send the question
  and optional context for Eli to read. A refusal alone never records the
  question text. Submission requires no additional model tool or model call.
* **Durable receipt — Critical value for this workflow:** acknowledge success
  only after persistence. Store a request ID, UTC submission time, question,
  optional reason/context, authenticated token ID, status, and available
  originating answer outcome/corpus revision. A caller-supplied idempotency
  key prevents duplicate records when retrying the same submission.
* **Owner inbox — Critical value for this workflow:** provide an owner-local
  CLI list and detail view showing new requests, their questions, context,
  dates, and status. Eli can mark requests `reviewed`, `resolved`, or
  `declined`, with an optional note or published-source URL. Start at `new`;
  recording alone is incomplete without this presentation and triage path.
* **Human control — High value:** a receipt confirms recording, not a promise
  that Eli will answer or publish. A private triage note is not corpus
  evidence; answers remain grounded in published sources.
* **Bounded, private storage — High value:** use the existing token gate and
  bounded/rate-limited submission rules. Keep submissions visible to Eli,
  not to other callers. Submitted text is intentionally stored for review,
  but remains excluded from routine audit logs and is never executed as
  instructions. Provide owner-local deletion for unwanted submissions.
* **Rebuild-safe persistence — Critical value:** requests are original data,
  unlike the derived writing index. Store them separately from the
  disposable `data/index.db`; index rebuilds must preserve them, and backup and
  recovery must cover the request store.

Implementation requires an explicit PLAN.md/wire-contract update: the current
peer surface is read-only and stateless. The proposed exception permits only
local request submission and owner triage; upstream Ghost/Postiz access stays
read-only and the answer loop retains its single `search_writing` tool.

## Evidence and outcome requirements

These requirements define the value of the MVP response rather than a final
wire schema:

* **Claims map to evidence — Critical value:** every substantive claim has
  supporting evidence IDs. A citation resolving to a retrieved chunk is
  necessary but does not prove that the claim follows from the chunk.
* **Quotes match retrieved text — Critical value:** validate exact excerpts
  against source text so a caller can distinguish a quotation from generated
  interpretation. Keep excerpts within an explicit response budget and
  report incomplete coverage when that budget prevents a full answer.
* **Provenance survives edits — High value:** include a content hash and
  corpus revision. Chunk IDs identify source/sequence, so the same ID alone
  does not establish that the text is unchanged after re-ingestion.
* **Absence is scoped — Critical value:** `insufficient_evidence` means the
  service did not establish an answer from the indexed writing. It does not
  establish that Eli never expressed a position or that a claim is false.
* **Partial answers remain useful — High value:** return supported portions
  and explicit gaps instead of presenting an incomplete response as complete.
* **Confidence reflects evidence — High value:** prefer observable coverage
  and limitations over an uncalibrated model confidence percentage.

## Optional capabilities after the MVP

Everything in this section is optional. The order is a suggested order for
validation, not a commitment to build every item.

| Order | Capability | Assessed value | Smallest useful version / condition to build |
|---|---|---|---|
| 1 | Check an attribution or claim | **High:** prevents agents from misrepresenting your published positions. | Given “Eli recommends X,” return `supported`, `contradicted`, `mixed`, or `not_found`, with evidence and dates. Missing evidence must not become contradiction. Reuse the existing loop and search tool. |
| 2 | Compare positions across time and sources | **High:** gives research agents a synthesis that is harder to obtain from isolated search hits. | Return dated statements, agreements, tensions, and gaps. Describe a changed position only when evidence supports that interpretation; different contexts or dates alone are insufficient. |
| 3 | Curated non-blog website ingestion | **High if it fills observed gaps; otherwise Medium:** adds useful background, project, and experience facts absent from Ghost/Postiz. | Revisit the deferred website provider for selected non-blog pages; keep Ghost canonical for blog posts and avoid duplicate ingestion. Prioritize pages based on real unanswered questions. |
| 4 | Topic evidence briefs | **Medium–High:** saves a caller repeated searches when preparing a research brief. | Produce a bounded collection of key claims, sources, dates, and gaps for one topic. Do not imply exhaustive corpus coverage. |
| 5 | Canonical sources and cross-post grouping | **Medium:** prevents duplicate social/blog copies from appearing to be independent corroboration. | Group clearly matching copies and expose the preferred source while preserving each published reference. Promote earlier if duplication harms MVP answers. |
| 6 | Read-only evidence lookup | **Medium:** lets callers revisit a cited passage without repeating semantic search. | Resolve a chunk ID together with a revision/hash; explicitly report changed or unavailable content. Add only if the MVP evidence response is insufficient for real callers. |
| 7 | Corpus change summaries | **Medium:** helps returning agents refresh prior research incrementally. | Report new, updated, and removed indexed items since a corpus revision. Begin with a caller-requested comparison; push notifications are a separate decision. |
| 8 | Additional client adapters, including MCP | **Conditional High:** lowers integration effort for callers unable to use the existing A2A/HTTP interfaces. | Build a thin adapter only after a concrete client needs it; preserve one shared answer loop and contract. |
| 9 | Response caching and bounded concurrency | **Medium:** reduces repeated work and protects latency on the shared model host. | Add when observed latency or contention warrants it. Key caches by corpus/model/contract revision as appropriate and retain auth and rate-limit guarantees. |
| 10 | More retrieval or model tuning | **Conditional High:** valuable when it fixes demonstrated answer failures. | Diagnose with debug traces and existing answer evaluations first. Revisit PRF, model choice, or tool-call reliability only against a measured failure set; fan-out previously failed to improve grounding and increased latency. |
| 11 | Streaming | **Low initially:** offers earlier feedback but does not itself improve evidence quality. | Revisit when a peer needs it. Keep partial output distinguishable from a finalized, validated answer. |
| 12 | Persistent conversations and resumable tasks | **Low initially:** helps long workflows but adds state, lifecycle, and recovery complexity. | Require a demonstrated caller workflow and an explicit revision to the current stateless A2A decision. |
| 13 | Human-facing UI | **Low for the agent-first goal:** helps manual inspection but adds little to machine consumption. | Consider only after recurring human workflows justify it; retain CLI/debug traces as the current inspection path. |
| 14 | Request digests and caller follow-up | **Medium:** makes request review more convenient and closes the loop when published evidence becomes available. | Add owner notifications/digests, caller-visible status, or opt-in completion notifications after the CLI inbox is useful. These delivery mechanisms are optional; durable submission and owner review are MVP. |

## Boundaries

Retain the existing read-only upstream posture, one internal `search_writing`
tool, grounded citation-or-refusal policy, bounded requests, and token gate.
Peer task text and corpus excerpts remain data, never instructions.
The proposed local request inbox is the narrowly scoped persistence exception
described above; it does not grant write access to the corpus or upstreams.

Writing upstream, acting on Eli's behalf, open-domain advice, payments/auth
expansion, and internal multi-agent orchestration remain outside the product
scope. They are not optional extensions approved by this document. Published
writing is evidence of published statements, not authority to invent Eli's
current preferences or make commitments for him.

## MVP acceptance and prioritization evidence

* **Evidence reuse — Critical value:** a peer can ask a representative
  question and build an attributed summary from the returned evidence alone.
  Exact quotes validate against retrieved text; claim support is assessed
  through the existing answer-quality evaluation.
* **Correct next action — Critical value:** supported, partially supported,
  and unsupported questions produce distinguishable outcomes. Token
  exhaustion, timeout, and backend errors never masquerade as corpus absence.
* **Request reaches Eli — Critical value:** a peer explicitly submits a
  missing question, receives a durable ID, and Eli can list, read, and triage
  it locally. Retrying with the same idempotency key does not duplicate it;
  restarting or rebuilding the writing index does not lose it. An ordinary
  refusal creates no request, other callers cannot read its text, and routine
  audit logs contain no submitted question or context.
* **Usable freshness — High value:** a newly published fixture passes through
  ingest and embed into a cited answer, and the caller can see the resulting
  revision. Failed refreshes leave honest last-success information.
* **Compatibility and safe operation — Critical value:** existing consumers
  continue to work; authentication, rate limits, request budgets, and the
  audit privacy contract remain intact.
* **Quality evidence — Critical value:** capture the full existing Ragas
  answer/refusal run and promptfoo adversarial run against the thresholds in
  PLAN.md. Use Go unit tests for code contracts; add no third end-to-end
  harness.
* **Observed caller usefulness — High value:** complete the planned week of
  shadow usage and record which real tasks succeeded, failed, or required
  follow-up. Use consented examples or privacy-preserving summaries rather
  than adding question text to the audit log. Promote optional work when it
  resolves repeated caller failures.

The first product milestone is a peer agent producing a correctly attributed,
independently verifiable summary of Eli's published position, with explicit
gaps whenever the indexed writing does not support a complete answer.
