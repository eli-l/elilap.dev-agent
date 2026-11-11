#!/usr/bin/env bash
# ingest-all.sh — cron-friendly ingest runner.
#
# Calls the built binary from the repo root. The binary loads .env and
# resolves ./index.db relative to the working directory, so run from here
# rather than the caller's directory.

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [ ! -x "$root/agent" ]; then
	echo "ingest-all: $root/agent not found; build it with 'go build -tags sqlite_fts5 -o agent ./cmd/agent'" >&2
	exit 1
fi

cd "$root"
exec "$root/agent" ingest "$@"
