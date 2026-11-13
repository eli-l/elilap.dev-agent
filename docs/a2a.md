---
type: Reference
title: A2A surface - agent card, message/send, tokens, and budget
description: "The A2A v0.3 JSON-RPC binding served by `agent serve`: agent-card discovery at the well-known paths, the message/send request and response contract with citation metadata, why the service answers with a Message instead of a Task, the three access modes (dev, public token-less, bearer token), the caller identity the spend budget charges, the four daily/in-flight gates that bound a public deployment's model bill, and the audit record that never carries the question, answer, or excerpts."
status: stable
generated:
  by: process:agent-a2a
  at: 2026-11-13T18:00:00Z
verified:
  - by: process:localai-probe
    at: 2026-11-05T10:49:40Z
tags: [a2a, protocol, agent-card, jsonrpc, auth, rate-limit, budget, identity, audit, serve]
sources:
  - internal/a2a/types.go
  - internal/a2a/card.go
  - internal/a2a/handler.go
  - internal/auth/tokens.go
  - internal/ratelimit/limiter.go
  - internal/budget/budget.go
  - internal/audit/audit.go
  - cmd/agent/serve.go
  - cmd/agent/tokens.go
---

# A2A surface

`agent serve` exposes the in-service loop as an **A2A v0.3 agent** over a
single JSON-RPC endpoint. A peer discovers the agent card, sends one
`message/send` with a question, and receives a grounded answer whose
citations are the excerpts it was grounded on and whose sources are the
documents behind them — or the exact refusal string
`Not in my published writing`. Neither list carries a chunk id: that
identifier is logged for tracing and never returned. The loop, tool,
budgets, and response contract are in
[agent-architecture.md](/agent-architecture.md); this file is the wire
contract.

A2A is the peer-facing surface; `/v1/retrieve` and `/v1/ask` are the JSON
equivalents the CLI and the Ragas/promptfoo suites use. All three are served
by the same binary and read the same index; `/v1/ask` and `/a2a` share one
budget, while `/v1/retrieve` is a private debugging path.

## Discovery

The card is public (a peer needs it before it has a token) and is served at
both well-known names:

| Path | Purpose |
|---|---|
| `GET /.well-known/agent-card.json` | A2A v0.3 canonical location |
| `GET /.well-known/agent.json` | v0.2 alias, kept for older clients |

The card advertises exactly what the service does and nothing it cannot do:

* `protocolVersion: "0.3.0"`, `preferredTransport: "JSONRPC"`, and one
  `additionalInterfaces` entry repeating the main `url` (`<root>/a2a`).
* `description` is `agentloop.Description` verbatim — the canonical copy.
* One skill, `grounded-writing-qa`, whose description names both the citation
  contract and the refusal string.
* `capabilities.streaming`, `pushNotifications`, and
  `stateTransitionHistory` are all `false`: the loop answers synchronously
  and no task state is kept.
* `securitySchemes` + `security` appear only when at least one token is
  configured. The scheme is `{"type":"http","scheme":"bearer"}`.

`url` is `<root>/a2a`, where `<root>` comes from `AGENT_A2A_PUBLIC_URL` when
set. A loopback bind derives `http://<bind-addr>`; a `--bind-public` bind
**requires** `AGENT_A2A_PUBLIC_URL`, because an advertised endpoint must never
be built from an untrusted `Host` or `X-Forwarded-Proto` header. The configured
value is validated at startup (absolute `http`/`https`, host required, no
userinfo), and a path prefix such as `https://example.com/agent` is allowed.

**Spec note:** A2A v0.3 uses `url`, `preferredTransport`, and
`additionalInterfaces` on the card, and recommends
`/.well-known/agent-card.json`; `supportedInterfaces` and
`/.well-known/agent.json` are older drafts. The implementation follows the
v0.3 spec and still serves the old path as an alias; `PLAN.md` Decision 16
records the corrected shape (2026-11-05).

## `message/send`

`POST /a2a`, `Content-Type: application/json`, body ≤64 KiB. The question is
the concatenation of the message's text parts and is capped at 8 KiB
(`agentloop.MaxQuestionBytes`).

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "message/send",
  "params": {
    "message": {
      "kind": "message",
      "role": "user",
      "messageId": "m-2",
      "contextId": "ctx-smoke",
      "parts": [{"kind": "text", "text": "What has Eli written about vendor lock-in?"}]
    }
  }
}
```

The result is a single agent `Message` — not a `Task` (see below):

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "kind": "message",
    "role": "agent",
    "messageId": "<generated hex>",
    "contextId": "ctx-smoke",
    "parts": [{"kind": "text", "text": "Eli's take: harnesses are a form of vendor lock-in, and the zero-harness detour cost more than it saved."}],
    "metadata": {
      "citations": [
        {
          "source_kind": "ghost_post",
          "source_url": "https://read.elilap.dev/i-gave-up-on-zero-harness-and-here-is-why/",
          "title": "I gave up on zero-harness and here is why",
          "section": "The harness tax",
          "published_at": "2026-11-02T13:21:35Z",
          "excerpt": "the exact excerpt text the model was shown"
        }
      ],
      "sources": [
        {
          "kind": "ghost_post",
          "url": "https://read.elilap.dev/i-gave-up-on-zero-harness-and-here-is-why/",
          "title": "I gave up on zero-harness and here is why",
          "author": "Eli Lap",
          "published_at": "2026-11-02T13:21:35Z"
        }
      ],
      "refused": false,
      "tool_iterations": 1,
      "elapsed_ms": 2987
    }
  }
}
```

Contract details enforced by `internal/a2a`:

* `message.kind` must be `"message"`, `role` must be `"user"`, `messageId`
  must be present, and `parts` must contain at least one non-blank text part.
* File and data parts are rejected with `-32005 ContentTypeNotSupportedError`
  and `data.supported_input_modes: ["text/plain"]`.
* `configuration.acceptedOutputModes`, when present, is parsed as media types:
  `text/plain`, `text/*`, and `*/*` are accepted; parameters are ignored, so
  `text/plain; charset=utf-8` is accepted too. Anything else is `-32005`.
* `configuration.pushNotificationConfig` is rejected with `-32003`.
* A peer `contextId` is echoed; absent, the service generates one.
  `metadata.citations` (one entry per grounded excerpt) and
  `metadata.sources` (one per distinct document) are always arrays and are
  `[]` on a refusal. Neither carries a `chunk_id`; `source_url` joins the
  two lists. A refusal's `parts[0].text` is the refusal string.
* The loop timeout (`agentloop.DefaultMaxElapsed`, 90s) is the task budget;
  a timeout is `-32000` with `data.retryable: true`, a backend failure is
  `-32603`. Details stay in the server log.

### Why a Message and not a Task

The service is stateless per request by design (`PLAN.md` Decision 16): the
loop answers in one request or fails, and nothing is persisted. `tasks/get`
and `tasks/cancel` are implemented and return `-32001 TaskNotFound`; a
request that carries a `taskId` is rejected the same way. Streaming
(`message/stream`, `tasks/resubscribe`) returns `-32004`; push-notification
methods return `-32003`; `agent/getAuthenticatedExtendedCard` returns
`-32007` because no extended card is configured. Unknown methods return
`-32601`. This is the honest subset: the card advertises no capability the
server will not honor.

## Access modes

Three modes, decided by the bind and by whether any token is configured:

| Mode | When | Behaviour |
|---|---|---|
| Dev | Loopback bind, no tokens | Every path is open; the card advertises no security |
| Public, token-less | `--bind-public`, no tokens | `/a2a` and `/v1/ask` are open to any peer; a self-asserted identity keys the spend budget. The card advertises no security |
| Token-gated | At least one token | `Authorization: Bearer <token>` is required on `/a2a`, `/v1/ask`, and (when served) `/v1/retrieve` |

`--bind-public` is the only way to bind a non-loopback address. It requires
an explicit `AGENT_A2A_PUBLIC_URL` and non-zero daily quotas
(`--ask-quota`, `--address-quota`, `--quota-global`); it does **not** require a
token. A public bind with quotas disabled is a startup error — a public agent
without a spend ceiling is the one configuration this service refuses.
A 401 carries `WWW-Authenticate: Bearer realm="elilap.dev-agent"`; on `/a2a`
the body is a JSON-RPC `-32600` error.

### Caller identity

A token is optional, so a public answer is charged to an identity the caller
asserts. It is an **accounting key, not an authorization decision**: it cannot
unlock data, and anyone can claim any id.

| Surface | Identity | Source |
|---|---|---|
| A2A `message/send` | `agent:<id>` | `params.message.metadata.agentId`, else the `X-Agent-Id` header |
| `/v1/ask` | `user:<id>` | the `X-User-Id` header |
| fallback | `ip:<peer>` | the remote address |
| token present | `token:<id>` | the bearer credential (wins over all of the above) |

An id must match `[A-Za-z0-9._-]{1,64}`; anything else is rejected (`400` on
`/v1/ask`, `-32602` on `/a2a`). Absent or empty means the peer address. Because
an id is unauthenticated, the **global daily cap** and the **per-address cap**
are the boundary that protects the model bill; the per-identity quota is
fairness between honest callers.

Tokens come from two sources, both declared in `example.env`:

* `AGENT_A2A_TOKENS` — comma-separated `id:token` pairs, fixed at process
  start (containers, cron, launchd).
* `AGENT_A2A_TOKENS_FILE` — a `0600` file of `id:token` lines (default
  `./data/a2a-tokens.txt`, gitignored). The server re-reads it when its mtime or
  size changes, so rotation needs **no restart**. A group/world-readable
  file is a startup error, and a broken reload keeps the last good set and
  logs a warning.

Ids are the audit label and must match `[A-Za-z0-9._-]{1,64}`; values are at
least 32 characters and generated with 32 random bytes (43 URL-safe base64
characters). Values cannot contain whitespace, control characters, `#`, or
`,`, so they parse identically in the env list and the file. Tokens are
compared in constant time, and values never appear in logs or errors.

Once any token has been configured, the store stays **secured**: deleting the
token file or revoking the last entry makes every request `401`, never
unauthenticated. Adding a token to a server that started token-less enables
auth on the next request; disabling it requires a restart with no tokens.

```bash
./agent tokens add --id alice        # prints "alice:<token>" exactly once
./agent tokens list                  # ids only
./agent tokens revoke --id alice     # takes effect on the next request
```

`agent tokens add` writes the token file atomically with mode `0600`; it
refuses to edit a file with looser permissions. Revoking an id that only
exists in `AGENT_A2A_TOKENS` is an error: edit the environment instead.

## Budget

`agent serve` enforces four gates on every answer-bearing request (`/a2a`
`message/send` and `/v1/ask`), in `internal/budget`. All of them must pass:

| Gate | Flag | Default | Key | Purpose |
|---|---|---|---|---|
| minute rate | `--ask-rate` | 3/min | identity | burst |
| identity quota | `--ask-quota` | 20/day | identity | a leaked id cannot drain the day |
| address quota | `--address-quota` | 20/day | peer address | backstop for rotating ids |
| global quota | `--quota-global` | 60/day | process | **the spend ceiling** |
| in-flight | `--max-inflight` | 2 | process | one host cannot be queued into the ground |

`0` disables a gate on a loopback bind and is rejected with `--bind-public`;
negative values are startup errors. The budget is **on for every public bind**
and **off for a bare loopback bind**: a loopback server is the dev/eval surface
(`evals/` and `tests-ai/` run their whole suite against
`agent serve --addr 127.0.0.1:18080` with no budget flags), so a cap there
would silently fail those runs. Passing any budget flag (`--ask-rate`,
`--ask-quota`, `--address-quota`, `--quota-global`, `--max-inflight`) turns the
whole budget on for a loopback bind, with the defaults above for the gates you
did not name (`--ask-quota 0` therefore enables the budget with the daily
identity gate off). `--quota-file` alone does not enable it. A large budget is
deliberately *not* the
shape of the protection: `--ask-rate` alone would allow 43,200 requests a day,
so the daily global quota is the number that bounds a chat-model bill. At the
loop's own ceilings (3 iterations, 2,048 completion tokens) one answer costs
roughly $0.005–0.014 on a mid-priced cloud model, so 60/day is tens of dollars a
month, not hundreds.

Daily counters live in `<db dir>/quota.json` (`--quota-file` overrides; default
`./data/quota.json`), mode `0600`, written atomically and keyed by UTC day,
because `serve` opens the index read-only. A restart does not refund the day.
An unreadable or unparsable file is a startup error (it fails closed rather
than silently resetting to zero); a failure to write is logged and the counters
stay enforced in memory. Counters reset at 00:00 UTC, and a daily refusal's
`Retry-After` is the number of seconds until then.

A request is charged **after** body validation and immediately before the loop
runs, so a malformed body or a rejected question costs nothing. A refusal is
HTTP 429 with `Retry-After`; `/a2a` returns JSON-RPC `-32000` with
`data.reason: "quota_exceeded"`, `data.scope` (one of `rate`, `identity_daily`,
`address_daily`, `global_daily`, `busy`), and `data.retry_after_seconds`.
`-32000` is the documented implementation-defined server code for budget
problems. Refusals are logged with their scope, throttled by peer address
so a refusal flood and a rotating self-asserted id cannot become their own log
DoS; the line never carries the question.

`/v1/retrieve` is **private**: it is not registered on a public bind unless
`--allow-public-retrieve` is given, since the loop already retrieves internally
and a public embedding endpoint is only an abuse surface. When served, it has
its own bucket (`--rate-limit`, default 30/min per token or peer address) and
is not charged against the answer quota. Failed bearer-auth attempts have a
separate `--auth-limit` budget (default 120/min per address, `0` disables); a
successful lookup is not charged. The lookup happens before the charge, so one
peer cannot lock out valid tokens behind the same NAT.

## Audit log

Every completed task writes one line on the `audit` module (see
[logging.md](/logging.md)):

```json
{"level":"info","module":"audit","token_id":"","principal":"agent:peer-7","method":"a2a/message/send","question_sha256":"<64 hex>","question_bytes":45,"outcome":"answered","tool_iterations":1,"citations":1,"sources":1,"refused":false,"elapsed_ms":2987,"message":"task"}
```

`principal` is the identity the budget charged (`agent:<id>`, `user:<id>`,
`token:<id>`, or `ip:<addr>`); `token_id` is set only when a bearer token
gated the request. `outcome` is `answered`, `refused`, `timeout`, `canceled`, or `error`. The
question is identified by its SHA-256 and byte length, so repeated questions
can be correlated without storing peer text. The record deliberately excludes
the question, the answer, the prompt, and excerpts. The audit
logger is pinned to `info` unless logging is explicitly `disabled`, so a
quieter `--log-level` cannot silently drop the trail. `/v1/ask` writes the
same record with `method: "v1/ask"`.

## Security posture

* Read-only: no method mutates the index or any upstream source.
* Peer text is data, never instructions. The grounded prompt says so, and
  refusing any answer produced without a retrieved excerpt is the
  backstop: an ungrounded reply becomes the refusal string.
* Chunk ids never leave the service. They are logged on the `rag: answer`
  debug line and in the retrieval trace; the peer sees `source_url`,
  `title`, `section`, and the excerpt text instead.
* Loopback bind by default; public exposure requires `--bind-public`, an
  explicit `AGENT_A2A_PUBLIC_URL`, and non-zero daily quotas. A token is
  optional and, when set, gates every path.
* A self-asserted identity never authorizes anything: it only shares a
  global spend ceiling the operator fixed. Rotating it cannot exceed the
  per-address cap.
* Tokens are high-entropy, rotatable without restart, and never logged.
* The question (≤8 KiB) and the whole body (≤64 KiB, `413` when exceeded) are
  bounded; the audit stream records only the question fingerprint.

## Verification

`docs/runs/a2a-surface-2026-11-05.md` captures the first live pass: card
retrieval at both names, validation of the card against the official A2A
v0.3 JSON schema (Draft 7, `definitions.AgentCard`), a 401 without a token,
`tasks/get` → `-32001`, and a real `message/send` that returned a cited
answer in ~3s with a matching audit line. The unit suite covers the same
contract with a stub loop (`internal/a2a`), token rotation and permissions
(`internal/auth`), the bucket (`internal/ratelimit`), and the middleware
wiring (`cmd/agent`).
