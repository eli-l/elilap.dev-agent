# syntax=docker/dockerfile:1

# Build the single Go binary with CGO (mattn/go-sqlite3 + sqlite-vec). The
# module cache is populated before the source copy so a source-only change
# does not re-download dependencies. The builder tag intentionally floats to
# the newest 1.27 patch (PLAN.md Decision 9).
FROM golang:1.27-bookworm AS build
WORKDIR /src
# sqlite-vec's cgo header includes sqlite3.h; the runtime does not need the
# development package after the binary is linked.
RUN apt-get update \
    && apt-get install -y --no-install-recommends libsqlite3-dev \
    && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=1 go build -tags sqlite_fts5 -trimpath -ldflags="-s -w" -o /out/agent ./cmd/agent

# Runtime: the binary plus CA certificates (HTTPS sources), tzdata (UTC
# timestamps), migrations, and sources.yaml. LocalAI stays external.
#
# The image runs as root so a bind-mounted ./data works on any host. Compose
# drops all capabilities, sets no-new-privileges, and makes the root filesystem
# read-only; /data remains the only writable mount.
# The runtime is digest-pinned so the shipped image is reproducible; bump the
# digest deliberately when updating the base image.
FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/agent /usr/local/bin/agent
COPY migrations /app/migrations
COPY sources.yaml /app/sources.yaml
RUN mkdir -p /data
ENV AGENT_DB_PATH=/data/index.db \
    AGENT_A2A_TOKENS_FILE=/data/a2a-tokens.txt
VOLUME ["/data"]
EXPOSE 8080

# Compose overrides the command for one-shot ingest/embed/backup runs. The
# public bind is required because a container listens on 0.0.0.0; it needs a
# token in /data/a2a-tokens.txt (or AGENT_A2A_TOKENS) and AGENT_A2A_PUBLIC_URL.
CMD ["agent", "serve", "--bind-public", "--addr", "0.0.0.0:8080"]
