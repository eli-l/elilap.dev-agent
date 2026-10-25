# syntax=docker/dockerfile:1

# Stage 1 - build.
#
# The build never needs the network: dependencies are vendored, so the
# builder only needs go.mod, go.sum, vendor/ and the packages. The SQLite
# driver is pure Go, so CGO is off and the result is a static binary that
# runs in a runtime image without a libc.
FROM golang:1.27 AS builder

WORKDIR /src

COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY migrations/ ./migrations/

ENV CGO_ENABLED=0
RUN go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/agent ./cmd/agent

# The runtime stage owns /data, so it has to exist with the right ownership
# before the image is assembled. Distroless has no shell, so the directory
# is prepared here and copied across.
RUN mkdir -p /out/data

# Stage 2 - runtime.
#
# Contents, and nothing else: the binary, the CA bundle for HTTPS calls to
# the model server, the static import configuration, and a writable /data.
# No shell, no package manager, no source tree.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

WORKDIR /app

COPY --from=builder /out/agent /agent
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --chown=65532:65532 sources.yaml /app/sources.yaml
COPY --from=builder --chown=65532:65532 /out/data /data

# 65532 is the "nonroot" user of the distroless base image. It owns /data,
# so the SQLite index (and its WAL/SHM siblings) can be created there.
USER 65532:65532

# The index lives on a volume; the path is a single knob (PLAN.md Decision 11).
ENV AGENT_DB_PATH=/data/index.db

# This is a CLI, not a server: nothing listens on a port, so nothing is
# exposed and there is no healthcheck to fake.
ENTRYPOINT ["/agent"]
