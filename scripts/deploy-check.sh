#!/usr/bin/env bash
#
# scripts/deploy-check.sh - prove that this machine can run the agent in a
# container, and say exactly what is missing when it cannot.
#
# Checks, in order:
#   1. the docker CLI is on PATH
#   2. the docker daemon is reachable
#   3. the compose plugin is available
#   4. LOCALAI_BASE_URL is set and answers GET {base}/models
#   5. EMBED_MODEL and CHAT_MODEL are served by that model server
#   6. the image builds
#   7. the image runs "agent doctor" successfully
#   8. "docker compose config" renders the stack
#
# Every check fails with one specific, actionable message. Nothing is
# printed except the names of variables and the ids the server serves -
# never a credential value.
#
# Usage: scripts/deploy-check.sh [--skip-build] [--image NAME]

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${AGENT_IMAGE:-elilap-dev-agent:local}"
SKIP_BUILD=0
MODEL_TIMEOUT="${MODEL_TIMEOUT:-10}"
EMBED_MODEL="${EMBED_MODEL:-embeddinggemma-300m}"
CHAT_MODEL="${CHAT_MODEL:-spark-x2.5-4b}"

usage() {
  cat <<'USAGE'
usage: scripts/deploy-check.sh [--skip-build] [--image NAME]

  --skip-build   reuse the existing image instead of rebuilding it
  --image NAME   image tag to build and run (default: elilap-dev-agent:local)

Environment:
  LOCALAI_BASE_URL   required - the OpenAI-compatible endpoint (must end in /v1)
  AGENT_IMAGE        image tag to build and run (default: elilap-dev-agent:local)
  EMBED_MODEL        embedding model id to require (default: embeddinggemma-300m)
  CHAT_MODEL         chat model id to require (default: spark-x2.5-4b)
USAGE
}

step() {
  printf '==> %s
' "$1"
}

fail() {
  printf 'deploy-check: %s
' "$1" >&2
  exit 1
}

while [ $# -gt 0 ]; do
  case "$1" in
    --skip-build)
      SKIP_BUILD=1
      ;;
    --image)
      [ $# -ge 2 ] || fail "--image needs a value"
      IMAGE="$2"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      fail "unknown argument '$1'"
      ;;
  esac
  shift
done

# 1. docker CLI.
step "docker CLI"
command -v docker >/dev/null 2>&1 ||
  fail "docker is not on PATH. Install Docker Engine (or any OCI runtime) and re-run this script."
docker --version

# 2. daemon.
step "docker daemon"
docker info >/dev/null 2>&1 ||
  fail "the docker daemon is not reachable. Start it (systemctl start docker) or point DOCKER_HOST at a reachable daemon."
echo "daemon is reachable"

# 3. compose plugin.
step "docker compose"
docker compose version >/dev/null 2>&1 ||
  fail "this docker installation has no compose plugin. Install docker-compose-plugin (or use a daemon that ships it)."
docker compose version

# 4. model server reachability.
step "model server"
[ -n "${LOCALAI_BASE_URL:-}" ] ||
  fail "LOCALAI_BASE_URL is not set. Export the OpenAI-compatible endpoint, e.g. LOCALAI_BASE_URL=http://host:8080/v1"
MODEL_BODY="$(mktemp)"
trap 'rm -f "$MODEL_BODY"' EXIT
HTTP_CODE="$(curl -sS -o "$MODEL_BODY" -w '%{http_code}' --max-time "$MODEL_TIMEOUT" "${LOCALAI_BASE_URL%/}/models" 2>/dev/null)" ||
  fail "the model server at $LOCALAI_BASE_URL cannot be reached. Check that it is running and routable from this host."
[ "$HTTP_CODE" = "200" ] ||
  fail "the model server at $LOCALAI_BASE_URL answered HTTP $HTTP_CODE for GET ${LOCALAI_BASE_URL%/}/models. The base URL must end in /v1 - a wrong path answers 404."
echo "reachable: $LOCALAI_BASE_URL"

# 5. served model catalog.
step "model catalog"
MODEL_IDS="$(grep -o '"id"[[:space:]]*:[[:space:]]*"[^"]*"' "$MODEL_BODY" | sed -E 's/.*"([^"]*)"$/\1/' | sort -u)"
[ -n "$MODEL_IDS" ] ||
  fail "the model server at $LOCALAI_BASE_URL served no model ids. The response must be an OpenAI-compatible {\"data\":[{\"id\":\"...\"}]} catalog."
echo "served models: $(printf '%s' "$MODEL_IDS" | paste -sd, -)"
for pair in "EMBED_MODEL:$EMBED_MODEL" "CHAT_MODEL:$CHAT_MODEL"; do
  VAR="${pair%%:*}"
  WANT="${pair#*:}"
  printf '%s\n' "$MODEL_IDS" | grep -Fqx "$WANT" ||
    fail "the model server does not serve $VAR=$WANT. Set $VAR to one of the served ids, or load that model on the server."
  echo "$VAR=$WANT is served"
done

# 6. build.
if [ "$SKIP_BUILD" = "1" ]; then
  step "image build (skipped)"
  docker image inspect "$IMAGE" >/dev/null 2>&1 ||
    fail "--skip-build was given but the image $IMAGE does not exist locally. Build it once with: docker build -t $IMAGE ."
else
  step "image build"
  docker build -t "$IMAGE" "$REPO_ROOT" ||
    fail "the image did not build. The build output above names the failing step."
fi

# 7. the image runs the preflight.
step "agent doctor in the image"
DOCTOR_OUT="$(docker run --rm \
  -e LOCALAI_BASE_URL="$LOCALAI_BASE_URL" \
  -e EMBED_MODEL="$EMBED_MODEL" \
  -e CHAT_MODEL="$CHAT_MODEL" \
  "$IMAGE" doctor 2>&1)" ||
  {
    printf '%s\n' "$DOCTOR_OUT" >&2
    fail "the image $IMAGE did not pass \"agent doctor\". The output above names the failing check - usually the model server is not routable from inside the container."
  }
printf '%s\n' "$DOCTOR_OUT"

# 8. compose renders.
step "docker compose config"
(cd "$REPO_ROOT" && LOCALAI_BASE_URL="$LOCALAI_BASE_URL" docker compose config >/dev/null) ||
  fail "docker compose config failed. Run it in $REPO_ROOT to see the error - LOCALAI_BASE_URL must be set and the compose file must be valid."
echo "compose config renders"

printf '\ndeploy-check: all checks passed (image %s)\n' "$IMAGE"
