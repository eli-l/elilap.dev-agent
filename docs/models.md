---
type: Guide
title: Model backends - embeddings and chat configured independently
description: The two model roles the agent calls, why each has its own base URL, key, model, and knobs, how to point only the chat model at an OpenAI-compatible cloud provider (openai gpt6-luna), how EMBED_DIMS rebuilds the vector table, and what a cloud endpoint means for the data leaving the host.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-13T09:30:00Z
tags: [models, providers, openai, embeddings, chat, environment, chunking]
sources:
  - internal/envcfg/envcfg.go
  - internal/llm/client.go
  - internal/llm/chat.go
  - internal/store/vecschema.go
  - internal/embed/embed.go
  - example.env
---

# Model backends

The agent calls two model roles, and they are **configured independently**:

| Role | Calls | Variables |
|---|---|---|
| Embeddings | `POST /v1/embeddings` | `EMBED_BASE_URL`, `EMBED_API_KEY`, `EMBED_MODEL`, `EMBED_DIMS` |
| Chat | `POST /v1/chat/completions` | `CHAT_BASE_URL`, `CHAT_API_KEY`, `CHAT_MODEL`, `CHAT_TOKEN_PARAM` |

Both roles speak the OpenAI wire format, so "provider" is just an endpoint:
the self-hosted LocalAI host, OpenAI, or any compatible gateway. Nothing in
the code assumes the two roles share a URL, a key, or a model family. A
typical mixed setup keeps embeddings on the local host (cheap, batched, no
data leaves the network) and sends only the answer-producing chat calls to a
cloud model.

## Which role runs where

* **Embeddings** vectorize every chunk at ingest time and every query at
  retrieval time. They are the expensive, high-volume side and the one tied to
  the index: a change of model or width invalidates stored vectors.
* **Chat** is the model in the grounded loop (`internal/agentloop`): it picks
  the `search_writing` arguments, reads the excerpts, and composes the cited
  answer or the refusal. It runs a handful of times per question.

`agent embed` and `agent query` use the embeddings role only; `agent ask`,
`/v1/ask`, and A2A `message/send` use both. `agent serve` builds one connector
per role at startup.

## Pointing chat at a cloud provider

Nothing else changes: same grounded loop, same retrieval-or-refusal policy
and the same answer/citations/sources response.
Replace the chat block in `.env`:

```bash
# Embeddings stay local.
EMBED_BASE_URL=http://192.168.78.243:8080/v1
EMBED_MODEL=embeddinggemma-300m
EMBED_DIMS=768

# Chat moves to OpenAI.
CHAT_BASE_URL=https://api.openai.com/v1
CHAT_API_KEY=sk-...            # secret; only ever in .env / the environment
CHAT_MODEL=gpt6-luna
CHAT_TOKEN_PARAM=max_completion_tokens
```

* `CHAT_TOKEN_PARAM=max_completion_tokens` is what OpenAI's reasoning models
  need: they reject `max_tokens` with a 400. LocalAI and most compatible
  servers take the default, `max_tokens`. The loop keeps setting one budget
  (`--max-tokens`, default 2048) and the connector emits the field the backend
  accepts.
* Some models refuse a reasoning effort next to **function tools**: `gpt-6-luna`
  answers `400 Function tools with reasoning_effort are not supported ... set
  reasoning_effort to 'none'`. The loop needs tools for `search_writing`, so
  the connector re-sends that tool-bearing call once with
  `reasoning_effort: "none"` and remembers the constraint: later tool calls go
  straight to `none`, while the forced tools-withdrawn final call keeps its
  `low` effort (the restriction covers function tools only). No configuration
  is involved, and no other 400 is retried.
* Getting `CHAT_TOKEN_PARAM` wrong no longer fails the question. A 400 whose
  body names both fields (`Unsupported parameter: 'max_tokens' ... Use
  'max_completion_tokens' instead`) is recognized as the model asking for the
  other one: the connector re-sends the same request once with the other field
  and remembers the choice, so the loop's later calls (and every request after
  it) go straight to the accepted field. Any other 400 is returned verbatim and
  never retried. Set `CHAT_TOKEN_PARAM` anyway to skip the first rejected
  attempt.
* `reasoning_effort` passes through unchanged: the loop sends `none` while it
  is still turning the question into search keys and `low` once excerpts are
  in hand. Both are valid OpenAI values.
* `CHAT_MODEL` must be the provider's exact model id. A gateway that routes
  elsewhere is fine; the debug log records the id the server reports.
* No CLI flag overrides the endpoint. `agent ask --chat-model` and
  `agent serve` read the model id, but URL and key come from the environment,
  so a cloud switch is one `.env` change.

To do the opposite — cloud embeddings with a local chat model — set
`EMBED_BASE_URL`, `EMBED_API_KEY`, `EMBED_MODEL`, and `EMBED_DIMS` to the
cloud embedder and run a full re-embed (below). `EMBED_DIMS` must be the width
the model actually returns; the OpenAI-compatible `dimensions` request field is
not sent, so a model that returns 1536 needs `EMBED_DIMS=1536`.

## What a cloud endpoint changes

* **Data leaves the host.** Chat requests carry the system prompt, the
  question, and the retrieved excerpt bodies (the whole point of grounding).
  `agent embed` sends chunk text. The audit record still stores only a
  question fingerprint, outcome, citations, and timings — but the provider
  sees the text. Decide per deployment.
* **Secrets live only in the environment.** `CHAT_API_KEY` / `EMBED_API_KEY`
  are empty in `example.env`, never in `sources.yaml`, and never logged; the
  debug line records only whether a key is set.
* **Availability and cost move with the provider.** `agent serve` builds the
  connectors once; a rejected key or an unknown model surfaces as the
  provider's error on the first answer, not at startup.

## Changing the embedding width

`chunks_vec` is created at the width in `migrations/0001_init.sql` (768). When
`EMBED_DIMS` differs, the next **read-write** open (`agent ingest`,
`agent embed`) rebuilds `chunks_vec` at the configured width inside one
transaction:

1. drop the old `chunks_vec` (vectors are not comparable across widths),
2. create it at `EMBED_DIMS`,
3. clear `embedded_with` / `embedded_at` on every chunk.

Then `agent embed` re-embeds the whole corpus, and the tag stamps the new
model. Read-only commands (`query`, `ask`, `serve`, `stats`, `backup`) never
rebuild: the first two fail with `index vector dimensions do not match
EMBED_DIMS` until the re-embed runs. `stats` and `backup` read the width from
the index and work regardless.

`EMBED_DIMS` accepts a blank value or the default `768`; a non-numeric value
fails at startup naming the variable, and a width above 16384 is refused
rather than creating a table whose every KNN would be enormous.

## Related

* [Environment configuration and .env loading](/environment.md) — the full
  variable inventory and the `example.env` rule.
* [Embedding pass and the vector index](/embeddings.md) — the tag, batching,
  and `--full`.
* [Self-hosted LocalAI endpoint and model catalog](/localai.md) — the default
  local models.
* [The in-service agent loop and the A2A surface](/agent-architecture.md) —
  what the chat model is asked to do.
