---
type: Run
title: Vector retrieval and the /v1/retrieve dev endpoint
description: First live run of the vector-only retrieval slice - unit tests for the KNN query, retrieval engine, and HTTP handler, then a loopback POST /v1/retrieve against the 886-chunk index that returned the expected Ghost chunk in 91ms.
status: stable
generated:
  by: deepseek/deepseek-v4.1-flash
  at: 2026-11-02T21:30:00Z
run:
  type: agent-serve-retrieve
  cmd: ./agent serve --addr 127.0.0.1:18080
output: |
  $ go test -tags sqlite_fts5 -mod=vendor -count=1 ./internal/store/ ./internal/retrieval/ ./cmd/agent/
  ok  github.com/eli-l/elilap.dev-agent/internal/store      0.152s
  ok  github.com/eli-l/elilap.dev-agent/internal/retrieval  0.136s
  ok  github.com/eli-l/elilap.dev-agent/cmd/agent           0.120s

  $ curl -sS -X POST http://127.0.0.1:18080/v1/retrieve \
      -d '{"query":"harness as a new form of vendor lock-in","top_n":1}'
  [{"chunk_id":"ccdecc4556e1e05b5566bcbd9df58a5fce347e491dbd72437db41ea8d73e97c7",
    "title":"I gave up on zero harness and here is why",
    "source_url":"https://read.elilap.dev/i-gave-up-on-zero-harness-and-here-is-why/",
    "score":0.6642,"distance":0.8196, ...}]
  http=200 total=0.091s

  $ ./agent serve --addr 0.0.0.0:18080
  agent serve: --addr "0.0.0.0:18080" is not loopback; the dev server is token-less and must not be exposed
---

# Result

The endpoint returned the expected Ghost post as the top hit for a phrase
from its own intro, ranked by vec0 L2 distance with a cosine score of 0.66.
End to end - one embeddings request to LocalAI plus the KNN join - took about
91ms, comfortably inside the Phase 2 p50 target of 300ms for retrieval.

Automated coverage:

* `internal/store` — KNN ordering (self, scaled, orthogonal vectors), `k`
  bounds, metadata hydration from `chunks`, an empty index, and rejection of
  wrong-dimension or non-positive arguments.
* `internal/retrieval` — trimmed query passed to the embedder once with the
  configured model, ranked hits with cosine score, default and maximum
  `top_n`, non-nil empty results, `ErrInvalidQuery` classification, backend
  failure propagation, and dimension checking.
* `cmd/agent` — loopback address validation (including `:8080` and LAN
  addresses), the handler's success/empty/error paths, unknown-field and
  oversized-body rejection, and startup guards: a non-loopback bind fails
  before the index is opened, an index with no embeddings is refused, and an
  index stamped for a different model names the tag mismatch and the
  `agent embed` recovery command.

The dev server bound `127.0.0.1` only and logged one request line; the
non-loopback attempt exited before opening the database. No response body or
query text appears in the logs.
