#!/usr/bin/env bash
# Run the pinned promptfoo with the suite's local-only settings in one place.
#
# - the promptfoo database, cache, and logs stay under tests-ai/.promptfoo/
# - nothing is uploaded: sharing and telemetry are off
# - attack generation and grading use the configured local provider; the
#   promptfoo-hosted inference is only reachable for plugins promptfoo marks
#   remote-only (see README.md). The smoke script opts into a fully local run.
set -euo pipefail
cd "$(dirname "$0")"
export PROMPTFOO_CONFIG_DIR=.promptfoo
export PROMPTFOO_DISABLE_SHARING=true
export PROMPTFOO_DISABLE_TELEMETRY=true
# promptfoo gates red-team evals behind an interactive work-email prompt. These
# runs are scripted and local, so use its non-interactive CI mode; nothing is
# uploaded (sharing and telemetry are disabled above).
export CI=true
PF=./node_modules/.bin/promptfoo
if [ ! -x "$PF" ]; then
	echo "run-promptfoo: promptfoo is not installed; run 'npm ci' in tests-ai/ first" >&2
	exit 1
fi
exec "$PF" "$@"
