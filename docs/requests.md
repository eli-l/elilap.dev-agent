---
type: Reference
title: Deferred knowledge requests - submit, notify, resolve
description: "The one write surface: how a peer turns an unanswered question into a durable request over A2A, how the owner is notified and triages it with `agent requests`, how resolving runs the grounded loop before claiming an answer, and how the submitting agent gets the completed task back - webhook push or `tasks/get` polling. Includes the separate store, the submission budget, the https-only push rule, and the privacy boundary around stored question text."
status: stable
generated:
  by: process:agent-requests
  at: 2026-11-13T12:00:00Z
tags: [requests, a2a, tasks, push-notifications, webhook, ssrf, inbox, write-surface]
sources:
  - internal/requests/requests.go
  - internal/requests/schema.sql
  - internal/notify/notify.go
  - internal/a2a/requests.go
  - internal/a2a/handler.go
  - cmd/agent/requests.go
  - cmd/agent/serve.go
  - example.env
---

# Deferred knowledge requests

The agent answers from what Eli has published. When the writing does not cover
a question, the loop returns exactly `Not in my published writing` — and that
used to be the end of it. A peer that needed the answer had nowhere to put the
question.

This capability closes that gap: **the peer can explicitly submit the missing
question**. It is stored durably, the owner is notified and answers it by
publishing, and the peer's agent learns about the answer through a webhook it
registered or by polling its task. Nothing about a refusal is recorded on its
own: a question becomes a request only when the caller sends it as one.

This is the **only write surface** in the service (PLAN.md Decision 26). It is
narrow on purpose: no corpus mutation, no upstream writes, no second loop, no
second tool. A stored request is a note for a human, never corpus evidence.

## Where the request lives

| Property | Value |
|---|---|
| Store | its own SQLite file: `AGENT_REQUESTS_DB_PATH`, default `requests.db` beside the index (`./data/requests.db`, `/data/requests.db` in the container) |
| Schema | embedded in `internal/requests/schema.sql`, applied on open |
| Statuses | `new` → `reviewed` → `resolved` \| `declined`, plus `canceled` (peer withdrawal); all terminal states are final |
| Id | `req_` + 128 random bits; it is the peer's only handle and is unguessable |
| Readers | `agent requests` (owner, on the host) and the peer that holds the id; ordinary logs and the audit stream never carry the text |
| Survives | `rm data/index.db*`, a full re-ingest, and a `serve` restart |

The index is derived data that may be deleted at any time (AGENTS.md). A
request is not: a peer may be waiting on it. Putting it in `index.db` would
mean a rebuild silently drops questions, so it gets its own file, its own
`VACUUM INTO` snapshot (`agent backup` writes `requests-<stamp>.db`), and its
own counters (`request-quota.json`). `serve` opens the index read-only and this
store read-write.

## Submitting over A2A

A submission is an ordinary `message/send` with one extra metadata field. No
new RPC method, no second endpoint.

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "message/send",
  "params": {
    "message": {
      "kind": "message",
      "role": "user",
      "messageId": "m-7",
      "contextId": "ctx-architecture-review",
      "parts": [{"kind": "text", "text": "What is Eli's position on event sourcing for small teams?"}],
      "metadata": {
        "agentId": "peer-arch-review",
        "knowledgeRequest": {
          "reason": "finishing an architecture comparison",
          "context": "choosing between CRUD and event-sourced persistence",
          "idempotencyKey": "arch-review-42"
        }
      }
    },
    "configuration": {
      "pushNotificationConfig": {"url": "https://peer.example/a2a-hook", "token": "shared-secret"}
    }
  }
}
```

The result is a **`Task`**, not an answer:

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "kind": "task",
    "id": "req_5f0c…",
    "contextId": "ctx-architecture-review",
    "status": {"state": "submitted", "timestamp": "2026-11-13T11:46:21Z"},
    "metadata": {
      "knowledgeRequest": {"id": "req_5f0c…", "status": "new", "origin": "peer_request", "submitted_at": "2026-11-13T11:46:21Z"}
    }
  }
}
```

`knowledgeRequest` accepts an object or `true` (`true` is `{}`). Fields are
strict — an unknown key is `-32602`, so a typo cannot silently drop the reason.
When the submission also carried `configuration.pushNotificationConfig`, the
returned metadata says `"push_registered": true|false`, so a peer whose webhook
failed to stick knows to poll or re-register (the request itself is durable
either way):

| Field | Limit | Meaning |
|---|---|---|
| `reason` | 1 KiB | why the peer needs it (shown in the inbox) |
| `context` | 4 KiB | optional supporting detail |
| `idempotencyKey` | `[A-Za-z0-9._-]{1,128}` | retry safety: the same key returns the same task, a different question under the same key is `-32602` |
| `origin` | one of `insufficient_evidence`, `partial`, `peer_request` | defaults to `peer_request` |

An idempotent retry returns the existing task and writes nothing; the response
is identical from the peer's point of view — including when the inbox is at
`--max-pending` and when the submission budget is exhausted, because refusing a
retry would leave the peer unable to learn the id of a row it already owns. A
retry spends no submission credit: no row is written.

**A deployment without a request store answers `-32004`** (unsupported) rather
than accepting a question it would drop, and its refusals carry no
`requestSubmission` hint.

### Discovery

Two places say the capability exists, and both are honest:

* On a **refusal**, the answer message's `metadata.requestSubmission` explains
  how to submit, that the question is stored for the owner, and that a
  submission is not a promise of an answer.
* The **agent card** advertises the `https://elilap.dev/a2a/extensions/knowledge-request/v1`
  capability extension with the same instructions, so a peer can plan before
  it asks.

## Task states and the two ways back

| Store status | A2A task state | Set by |
|---|---|---|
| `new` | `submitted` | the submission |
| `reviewed` | `working` | `agent requests review` |
| `resolved` | `completed` | `agent requests resolve` |
| `declined` | `rejected` | `agent requests decline` |
| `canceled` | `canceled` | `tasks/cancel`, or the owner |

A `completed` task carries one artifact named `answer` whose text part is the
grounded answer and whose metadata holds `citations`, `sources`, and
`source_url` — the same evidence shape `/a2a` answers and `/v1/ask` return.
`tasks/cancel` withdraws an open request; canceling a closed one is `-32002`.

Two delivery paths, and a peer picks either:

**1. Push.** Register a webhook, once, for a specific task:

```json
{"jsonrpc":"2.0","id":5,"method":"tasks/pushNotificationConfig/set","params":{
  "taskId":"req_5f0c…",
  "pushNotificationConfig":{"url":"https://peer.example/a2a-hook","token":"shared-secret"}
}}
```

The same object may be sent inline on the submission
(`configuration.pushNotificationConfig`), which is the one-call form. When the
request is resolved (or declined), the service POSTs the completed (or
rejected) `Task` to that URL:

```text
POST https://peer.example/a2a-hook
Content-Type: application/json
X-A2A-Notification-Token: shared-secret
X-Elilap-Signature: sha256=<HMAC-SHA256 of the body, keyed by the same token>
```

`tasks/pushNotificationConfig/get|list|delete` manage the registration;
`tasks/pushNotificationConfig/set` and friends are `-32003` unless the
operator started `agent serve --push-notifications` (the card's
`capabilities.pushNotifications` matches).

**2. Poll.** `tasks/get` with `params.id` returns the task and its current
state at any time. No registration is needed, and polling is the fallback when
a delivery fails.

Request ids are bearer handles: whoever holds one can read that task. They are
128 random bits, they never appear in a public listing, and the owner inbox is
local-only. If a task call *does* assert an identity (a bearer token, or an
`X-Agent-Id` header), that identity must match the submitting principal;
otherwise the answer is `TaskNotFound`, so one identified peer cannot cancel,
re-point, or read another peer's request. A tokenless call that asserts nothing
is authorized by the id alone.

## The operator side

```bash
./agent requests list                    # newest first; pending count at the end
./agent requests list --status new --json
./agent requests show req_5f0c…
./agent requests review req_5f0c… --note "worth a post on persistence choices"
./agent requests resolve req_5f0c… --source-url https://read.elilap.dev/… 
./agent requests decline req_5f0c… --note "out of scope for the blog"
./agent requests delete req_5f0c…
./agent requests purge --older-than 180d         # closed requests only
```

**A missing store is an error, never a new empty one.** `list`/`show` open the
store read-only; every triage command requires the file to exist. A typo'd
`--db` therefore reports `no knowledge-request store at <path>` instead of
creating a second inbox (and `delete`/`purge` reporting success on it). Every
subcommand accepts `--db`, and `resolve` also accepts `--index`, a **read-only**
path used for the grounded answer; if the server was started with explicit
`--db`/`--requests-db` flags, pass the same paths here or you are looking at a
different store.

Peer text is sanitized on the way to the operator's terminal: control and
escape characters (C0, DEL, C1) are stripped from questions, reasons, context,
notes, answers, and push state before printing, so an OSC 52 clipboard write or
a carriage return cannot forge a line in `show` or `list`. Full text remains
available as JSON (`--json`), where the encoder escapes it safely.

`resolve` is the honest path and the default: it runs the **same grounded
loop** on the stored question against the current index. It refuses a request
that is already resolved, declined, or canceled *before* running the loop, so
a re-run never pays for a model call whose answer could not be recorded. There
is no `--ask` flag (asking is the default) and `--timeout`/`--push-timeout`
must be positive, matching `agent ask`. If the corpus still
returns the refusal string, resolution fails and nothing is recorded — which
is the check that the article actually addresses what was asked. The operator
flow is therefore: write the post, `agent ingest`, `agent embed`,
`agent requests resolve <id> --source-url <the post>`. The stored answer,
citations, and sources come from the index, never from the operator's note (a
triage note is private, never corpus evidence).

`--no-ask --answer "…"` is the explicit override for a resolution recorded
without a model run. It is documented, visible in the command line, and
requires the answer text.

On success the CLI flips the task to `completed` and delivers the peer
notification, printing `peer notification: delivered in N attempt(s)` or the
failure plus the reminder that `tasks/get` still works.

### Owner notification

Set `AGENT_REQUEST_NOTIFY_URL` to a webhook and `agent serve` POSTs one event
per new submission:

```json
{"event":"knowledge_request.created","request":{"id":"req_5f0c…","question":"…","reason":"…","context":"…","principal":"agent:peer-arch-review","submitted_at":"…","status":"new"}}
```

The queue is bounded (32) and delivery happens off the request path, so a slow
hook never delays a peer's acknowledgement; a full queue drops the
notification with a warning and the inbox still has the request. This URL is
**operator configuration**, not peer input, so a local `http://` hook is
allowed — a home-automation endpoint or a small script. It is validated at
startup: a malformed value (missing scheme, userinfo, oversize) exits 2 naming
`AGENT_REQUEST_NOTIFY_URL`, instead of silently disabling the notification for
the life of the process. With no URL, the inbox CLI is the notification.

## Budgets and limits

Submissions are writes, so they get their own budget instance and their own
counter file (`request-quota.json`), never the answer budget:

| Gate | Flag | Default |
|---|---|---|
| minute rate | `--request-rate` | 1/min per identity |
| identity quota | `--request-quota` | 3/day per identity |
| address quota | `--request-address-quota` | 5/day per peer address |
| global quota | `--request-global-quota` | 20/day |
| open requests | `--max-pending` | 500 |

`--bind-public` requires all three daily quotas to be non-zero — a public
inbox without a cap is a disk-filling surface — and the submission budget is
independent of the answer budget: passing a `--request-*` flag never turns on
the answer spend limiter on a loopback bind, and vice versa. A refusal is a
429 with `Retry-After` and a `scope` (`rate`, `identity_daily`,
`address_daily`, `global_daily`, `inbox`). A refused submission stores
nothing. `--max-pending` is a **soft** bound (count then insert, no
transaction): it protects disk from a flood, not state from a race. A full
inbox asks the peer to come back later rather than quietly dropping the
question, except for an idempotent retry of a request it already owns.

`agent requests purge` only deletes closed requests; the store rejects an open
status outright, so housekeeping can never drop a question a peer is still
waiting on. The store is created `0600` before SQLite opens it, because the
WAL/SHM sidecars inherit the main file's mode.

## Security posture

* **https is mandatory on a public bind.** A peer-supplied push URL must use
  `https` and resolve to a *globally routable* address; the sender dials the
  vetted IP directly (a second DNS answer cannot redirect it), follows no
  redirects, and can be restricted to an operator allowlist with
  `--push-allow-host hooks.example.com` or `.example.com`. The address check
  covers the non-obvious cases on both families: IPv4 CGNAT (including cloud
  metadata `100.100.100.200`), benchmark/documentation/reserved ranges, and
  IPv6 site-local, documentation, benchmarking, Teredo, ORCHID, discard-only,
  and NAT64 (well-known `64:ff9b::/96` **and** local-use `64:ff9b:1::/48`,
  which embeds the IPv4 after the prefix) and 6to4 addresses, whose embedded
  IPv4 is decoded and re-checked. `http://` and
  private targets are allowed only with `--push-allow-insecure`, which exists
  for loopback testing and is a **startup error with `--bind-public`**.
* **Backups carry the same text, so they carry the same mode.** `agent backup`
  snapshots the request store from a **read-only** handle (no schema write, no
  `chmod`, no write lock against a live `serve`) and writes the snapshot
  `0600`, with its own unique name beside the index snapshot.
* **Question text is stored, deliberately and narrowly.** It never enters the
  audit record (which keeps a SHA-256 fingerprint and byte length), never a
  normal log line, and never a peer's answer. `agent requests show` and the
  owner hook are the only readers; `delete`/`purge` remove it.
* **A stored request is not corpus evidence.** The owner's note is private;
  the answer comes from the index. The grounded prompt and refusal rules are
  unchanged.
* **No new capability inside the loop.** Submission runs no model call, adds
  no tool, and cannot influence an answer except by adding writing the owner
  published and ingested.

## Testing it locally

```bash
# a throwaway receiver on loopback
python3 -m http.server 18999   # or any hook script that dumps the body

AGENT_REQUESTS_DB_PATH=/tmp/requests.db \
AGENT_REQUEST_NOTIFY_URL=http://127.0.0.1:18999/owner \
  ./agent serve --addr 127.0.0.1:18088 --push-notifications --push-allow-insecure

# submit with a loopback push target, then triage and resolve with the same flag
./agent requests list
./agent requests resolve <id> --no-ask --answer "…" --push-allow-insecure
```

The `--push-allow-insecure` flag on both sides is what makes the http test
path work; it cannot be combined with `--bind-public`.
