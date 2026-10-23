#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
#
# Starts a real memory-mode Midnight API, seeds it through public REST
# endpoints, starts the production web-next development server with that API
# as its proxy, and executes a browser flow without request interception.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EVIDENCE_ROOT="${MIDNIGHT_PARITY_EVIDENCE_DIR:-$ROOT_DIR/.midnight/parity-evidence}"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
EVIDENCE_DIR="$EVIDENCE_ROOT/ui/$RUN_ID"
mkdir -p "$EVIDENCE_DIR"

for command_name in go npm curl python3; do
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "UI acceptance: required command '$command_name' was not found" >&2
    exit 78
  }
done
[[ -x "$ROOT_DIR/web-next/node_modules/.bin/playwright" ]] || {
  echo "UI acceptance: Playwright is not installed; run npm --prefix web-next install" >&2
  exit 78
}

free_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

api_port="$(free_port)"
web_port="$(free_port)"
api_base="http://127.0.0.1:$api_port"
web_base="http://127.0.0.1:$web_port"
api_pid=""
web_pid=""

cleanup() {
  if [[ -n "$web_pid" ]]; then
    kill "$web_pid" >/dev/null 2>&1 || true
    wait "$web_pid" >/dev/null 2>&1 || true
  fi
  if [[ -n "$api_pid" ]]; then
    kill "$api_pid" >/dev/null 2>&1 || true
    wait "$api_pid" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

echo "==> Start real memory-mode API"
(
  cd "$ROOT_DIR"
  exec go run ./cmd/midnight-server serve \
    --storage=memory \
    --listen "127.0.0.1:$api_port"
) >"$EVIDENCE_DIR/api.log" 2>&1 &
api_pid="$!"
for attempt in $(seq 1 60); do
  if curl -fsS "$api_base/api/v1alpha/compat" >/dev/null 2>&1; then
    break
  fi
  if [[ "$attempt" == "60" ]]; then
    echo "UI acceptance: API did not become ready" >&2
    exit 1
  fi
  sleep 1
done

suffix="$(date -u +%H%M%S)-$$"
project_json="$(
  curl -fsS \
    -H "Content-Type: application/json" \
    -H "Idempotency-Key: parity-ui-project-$suffix" \
    --data "{\"workspace_id\":\"wrk_default\",\"slug\":\"parity-ui-$suffix\",\"display_name\":\"Parity UI $suffix\"}" \
    "$api_base/api/v1alpha/projects"
)"
project_id="$(
  python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$project_json"
)"
service_json="$(
  curl -fsS \
    -H "Content-Type: application/json" \
    -H "Idempotency-Key: parity-ui-service-$suffix" \
    --data "{\"project_id\":\"$project_id\",\"slug\":\"api\",\"display_name\":\"Parity API\"}" \
    "$api_base/api/v1alpha/services"
)"
service_id="$(
  python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$service_json"
)"
environment_json="$(curl -fsS "$api_base/api/v1alpha/environments")"
environment_id="$(
  python3 -c '
import json, sys
project = sys.argv[1]
items = json.load(sys.stdin)
print(next(item["id"] for item in items if item["project_id"] == project and item["slug"] == "production"))
' "$project_id" <<<"$environment_json"
)"

echo "==> Start web-next against the real API"
(
  cd "$ROOT_DIR"
  export MIDNIGHT_API_PROXY_TARGET="$api_base"
  exec npm --prefix web-next run dev -- \
    --host 127.0.0.1 \
    --port "$web_port" \
    --strictPort
) >"$EVIDENCE_DIR/web.log" 2>&1 &
web_pid="$!"
for attempt in $(seq 1 60); do
  if curl -fsS "$web_base/" >/dev/null 2>&1; then
    break
  fi
  if [[ "$attempt" == "60" ]]; then
    echo "UI acceptance: web server did not become ready" >&2
    exit 1
  fi
  sleep 1
done

echo "==> Run browser flow without API interception"
(
  cd "$ROOT_DIR/web-next"
  MIDNIGHT_E2E_BASE_URL="$web_base" \
  MIDNIGHT_E2E_PROJECT_ID="$project_id" \
  MIDNIGHT_E2E_ENVIRONMENT_ID="$environment_id" \
  MIDNIGHT_E2E_SERVICE_ID="$service_id" \
  MIDNIGHT_E2E_SERVICE_NAME="Parity API" \
  MIDNIGHT_E2E_EVIDENCE_DIR="$EVIDENCE_DIR" \
    ./node_modules/.bin/playwright test e2e/live-api.e2e.ts
)

echo "UI acceptance: PASS"
echo "  project=$project_id environment=$environment_id service=$service_id"
echo "  evidence=$EVIDENCE_DIR"
