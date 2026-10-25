#!/usr/bin/env bash
# ingest-all.sh — cron-friendly ingest runner.
#
# Real implementation lands in Phase 1.
# See ../PLAN.md for the build plan.

set -euo pipefail
cd "$(dirname "$0")/.."
exec go run -mod=vendor ./cmd/agent ingest "$@"
