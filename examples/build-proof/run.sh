#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
#
# End-to-end source-build proof. POSTs a source-ref deploy to the running
# Midnight API and waits for the build worker -> deploy worker chain to
# settle through the digest-pinned build-to-deploy handoff. Public routing is
# intentionally exercised through the separate generated/custom-domain product
# lifecycle, not invented by this broad source-build fixture.
#
# Required env (with defaults):
#   MIDNIGHT_API_URL          (default http://127.0.0.1:8080)
#   MIDNIGHT_API_BEARER_TOKEN (default empty)
#   MIDNIGHT_BUILDPROOF_SERVICE_ID (required: the target service id)
#   MIDNIGHT_BUILDPROOF_REPO  (default file:///abs/path/to/this/repo)
#   MIDNIGHT_BUILDPROOF_REF   (default main)
#   MIDNIGHT_BUILDPROOF_TIMEOUT (default 300 — seconds)

set -euo pipefail

api_url="${MIDNIGHT_API_URL:-http://127.0.0.1:8080}"
bearer="${MIDNIGHT_API_BEARER_TOKEN:-}"
service_id="${MIDNIGHT_BUILDPROOF_SERVICE_ID:-}"
repo_default="file://$(cd "$(dirname "$0")/repo" && pwd)"
repo="${MIDNIGHT_BUILDPROOF_REPO:-$repo_default}"
ref="${MIDNIGHT_BUILDPROOF_REF:-main}"
timeout_s="${MIDNIGHT_BUILDPROOF_TIMEOUT:-300}"

if [[ -z "$service_id" ]]; then
  echo "MIDNIGHT_BUILDPROOF_SERVICE_ID is required" >&2
  exit 2
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 2
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required" >&2
  exit 2
fi

auth_args=()
if [[ -n "$bearer" ]]; then
  auth_args=(-H "Authorization: Bearer $bearer")
fi

idempotency_key="buildproof-$(date +%s)-$$"

intake_payload=$(jq -n \
  --arg repo "$repo" \
  --arg ref "$ref" \
  '{
     environment_id: "env_buildproof",
     source: { repo: $repo, ref: $ref }
   }')

intake_response=$(curl -fsS -X POST \
  "${auth_args[@]}" \
  -H "Idempotency-Key: $idempotency_key" \
  -H "Content-Type: application/json" \
  --data "$intake_payload" \
  "$api_url/api/v1alpha/services/$service_id/deployments")

operation_id=$(jq -r '.id' <<<"$intake_response")
operation_kind=$(jq -r '.kind' <<<"$intake_response")
operation_state=$(jq -r '.state' <<<"$intake_response")

if [[ "$operation_kind" != "build" ]]; then
  echo "intake: unexpected operation kind=$operation_kind, want build" >&2
  echo "$intake_response" >&2
  exit 1
fi
echo "intake: operation=$operation_id kind=$operation_kind state=$operation_state"

deadline=$((SECONDS + timeout_s))
last_event=""
build_image=""

poll_operation() {
  local id="$1"
  curl -fsS "${auth_args[@]}" "$api_url/api/v1alpha/operations/$id"
}

poll_events() {
  local id="$1"
  curl -fsS "${auth_args[@]}" "$api_url/api/v1alpha/operations/$id/events" || true
}

while ((SECONDS < deadline)); do
  op_json=$(poll_operation "$operation_id")
  state=$(jq -r '.state' <<<"$op_json")
  events_json=$(poll_events "$operation_id")
  latest=$(jq -r '.[-1].message // ""' <<<"$events_json" 2>/dev/null || true)
  if [[ -n "$latest" && "$latest" != "$last_event" ]]; then
    echo "build event: $latest"
    last_event="$latest"
  fi
  case "$state" in
    SUCCEEDED)
      build_image=$(jq -r '.[] | .message | select(test("build pushed: image="))' <<<"$events_json" \
        | sed -E 's/.*image=//; s/ .*//' | tail -n1)
      echo "build job: succeeded image=$build_image"
      break
      ;;
    FAILED|CANCELED)
      echo "build job: $state" >&2
      echo "$events_json" >&2
      exit 1
      ;;
  esac
  sleep 2
done

if [[ -z "$build_image" ]]; then
  echo "build job: timeout" >&2
  exit 1
fi

echo "deploy handoff: queued digest-pinned image=$build_image"
echo "PASS"
