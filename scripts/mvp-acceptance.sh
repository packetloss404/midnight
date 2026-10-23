#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-help}"
MIDNIGHT_API_BASE="${MIDNIGHT_API_BASE:-http://127.0.0.1:8080}"
MIDNIGHT_API_TOKEN="${MIDNIGHT_API_TOKEN:-}"
MVP_RUN_ID="${MVP_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
MVP_EVIDENCE_DIR="${MVP_EVIDENCE_DIR:-$ROOT_DIR/artifacts/mvp-acceptance/$MVP_RUN_ID}"
MIDNIGHT_CLI="${MIDNIGHT_CLI:-midnight}"
GO_BIN="${GO_BIN:-go}"
NPM_BIN="${NPM_BIN:-npm}"
JQ_BIN="${JQ_BIN:-jq}"
CURL_BIN="${CURL_BIN:-curl}"

SERVER_PID=""

usage() {
  cat <<'USAGE'
Usage: scripts/mvp-acceptance.sh <mode>

Modes:
  local-gate          Run local build/test/release commands.
  local-smoke         Start memory-mode server and run dashboard/API checks.
  surface-agreement   Check API/CLI/dashboard surface agreement locally.
  memory-blockers     Record owned outside-scope memory-mode diagnostics.
  package-template    Check package/Postgres template deploy queue wiring.
  human-domain        Converge and prove one human-session generated HTTP domain.
  real-app            Check real app deploy, runtime inventory, route, and HTTPS.
  audit               Check audit API/evidence and optional JSONL verification.
  backup              Check backup/restore APIs.
  local               Run local-smoke and surface-agreement.
  help                Show this help.

Set MIDNIGHT_API_BASE, MIDNIGHT_API_TOKEN, MVP_EVIDENCE_DIR, and MIDNIGHT_CLI
to point at an existing server, bearer token, evidence directory, and CLI
binary. local-smoke starts its own memory-mode server unless
MIDNIGHT_ACCEPTANCE_EXTERNAL_SERVER=1 is set.

human-domain additionally requires MIDNIGHT_HUMAN_SESSION_TOKEN,
MVP_PROJECT_ID, MVP_SERVICE_ID, MVP_ENVIRONMENT_ID, and MVP_INGRESS_URL. It
converges the singleton generated domain but never deletes it.
USAGE
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 127
  }
}

choose_tool() {
  local var_name="$1"
  local primary="$2"
  local fallback="$3"
  if type -P "$primary" >/dev/null 2>&1; then
    printf -v "$var_name" '%s' "$primary"
  elif type -P "$fallback" >/dev/null 2>&1; then
    printf -v "$var_name" '%s' "$fallback"
  else
    echo "missing required command: $primary" >&2
    exit 127
  fi
}

jq() {
  if [[ "$JQ_BIN" == *.exe ]]; then
    local converted=()
    local arg
    for arg in "$@"; do
      if [[ "$arg" == /* && "$arg" != -* && "$(type -P wslpath >/dev/null 2>&1; echo $?)" == "0" ]]; then
        converted+=("$(wslpath -w "$arg")")
      else
        converted+=("$arg")
      fi
    done
    command "$JQ_BIN" "${converted[@]}"
  else
    command "$JQ_BIN" "$@"
  fi
}

jq_text() {
  jq -r "$@" | tr -d '\r'
}

curl() {
  command "$CURL_BIN" "$@"
}

api_curl() {
  if [[ -n "$MIDNIGHT_API_TOKEN" ]]; then
    curl -H "Authorization: Bearer $MIDNIGHT_API_TOKEN" "$@"
  else
    curl "$@"
  fi
}

choose_curl() {
  if [[ "$GO_BIN" == *.exe && "$(type -P curl.exe >/dev/null 2>&1; echo $?)" == "0" ]]; then
    CURL_BIN="curl.exe"
  else
    choose_tool CURL_BIN curl curl.exe
  fi
}

tool_path() {
  local tool="$1"
  local path="$2"
  if [[ "$tool" == *.exe && "$(type -P wslpath >/dev/null 2>&1; echo $?)" == "0" ]]; then
    wslpath -w "$path"
  else
    printf '%s' "$path"
  fi
}

run_step() {
  echo
  echo "==> $*"
  "$@"
}

cleanup() {
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" >/dev/null 2>&1 || true
    wait "$SERVER_PID" >/dev/null 2>&1 || true
    if [[ "$GO_BIN" == *.exe && "$(type -P powershell.exe >/dev/null 2>&1; echo $?)" == "0" ]]; then
      local port
      port="${MIDNIGHT_API_BASE##*:}"
      port="${port%%/*}"
      if [[ "$port" =~ ^[0-9]+$ ]]; then
        powershell.exe -NoProfile -Command "Get-NetTCPConnection -LocalAddress 127.0.0.1 -LocalPort $port -ErrorAction SilentlyContinue | ForEach-Object { Stop-Process -Id \$_.OwningProcess -Force -ErrorAction SilentlyContinue }" >/dev/null 2>&1 || true
      fi
    fi
  fi
}
trap cleanup EXIT

wait_for_api() {
  for _ in $(seq 1 60); do
    if curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/compat" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "API did not become ready at $MIDNIGHT_API_BASE" >&2
  return 1
}

api_listen_addr() {
  local addr="${MIDNIGHT_API_BASE#http://}"
  addr="${addr#https://}"
  addr="${addr%%/*}"
  if [[ "$addr" == "$MIDNIGHT_API_BASE" || -z "$addr" ]]; then
    addr="127.0.0.1:8080"
  elif [[ "$addr" != *:* ]]; then
    addr="${addr}:8080"
  fi
  printf '%s' "$addr"
}

start_memory_server() {
  if [[ "${MIDNIGHT_ACCEPTANCE_EXTERNAL_SERVER:-0}" == "1" ]]; then
    wait_for_api
    return
  fi
  if curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/compat" >/dev/null 2>&1; then
    echo "==> using existing API at $MIDNIGHT_API_BASE"
    return
  fi
  local listen_addr
  listen_addr="$(api_listen_addr)"
  # The CLI deliberately requires an explicit credential even in developer
  # mode. Give the self-started acceptance server the same deterministic
  # local-only bearer so REST, CLI, and GUI probes exercise one auth contract.
  if [[ -z "$MIDNIGHT_API_TOKEN" ]]; then
    MIDNIGHT_API_TOKEN="midnight-local-acceptance"
  fi
  echo "==> starting memory-mode midnight-server on $MIDNIGHT_API_BASE ($listen_addr)"
  (cd "$ROOT_DIR" && "$GO_BIN" run ./cmd/midnight-server serve --storage=memory --listen "$listen_addr" --api-bearer-token "$MIDNIGHT_API_TOKEN") &
  SERVER_PID="$!"
  wait_for_api
}

local_gate() {
  cd "$ROOT_DIR"
  choose_tool GO_BIN go go.exe
  choose_tool NPM_BIN npm npm.cmd
  require_cmd make
  run_step make bootstrap
  run_step make build
  run_step make test
  run_step "$GO_BIN" test ./tools/contract-tests
  run_step "$GO_BIN" run ./tools/contract-tests
  run_step "$NPM_BIN" --prefix web test
  run_step "$NPM_BIN" --prefix web run build
  run_step make release-check
}

local_smoke() {
  cd "$ROOT_DIR"
  choose_tool GO_BIN go go.exe
  choose_curl
  choose_tool JQ_BIN jq jq.exe
  mkdir -p "$MVP_EVIDENCE_DIR"
  start_memory_server

  curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/compat" |
    tee "$MVP_EVIDENCE_DIR/compat.json" |
    jq -e '.api_version == "v1alpha"'
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/summary" |
    tee "$MVP_EVIDENCE_DIR/dashboard-summary.json" |
    jq -e '.status == "ready"'
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/template-marketplace" |
    tee "$MVP_EVIDENCE_DIR/template-marketplace.json" |
    jq -e '.cards | length >= 1'
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/runtime-inventory" |
    tee "$MVP_EVIDENCE_DIR/runtime-inventory-memory.json" |
    jq -e '(.services | length) == 0 and (.routes | length) == 0 and (.hosts | length) == 0'
  api_curl -fsS -o "$(tool_path "$CURL_BIN" "$MVP_EVIDENCE_DIR/dashboard-memory.html")" "$MIDNIGHT_API_BASE/dashboard/"
  test -s "$MVP_EVIDENCE_DIR/dashboard-memory.html"

  generated_evidence_check
}

record_blocker() {
  local name="$1"
  local owner="$2"
  local rationale="$3"
  shift 3
  if "$@" >/dev/null 2>&1; then
    printf 'PRESENT\t%s\t%s\t%s\n' "$name" "$owner" "$rationale" | tee -a "$MVP_EVIDENCE_DIR/blockers.tsv"
  else
    printf 'CLEARED\t%s\t%s\t%s\n' "$name" "$owner" "$rationale" | tee -a "$MVP_EVIDENCE_DIR/blockers.tsv"
  fi
}

memory_template_queue_probe() {
  local status
  status="$(
    api_curl -sS -o "$(tool_path "$CURL_BIN" "$MVP_EVIDENCE_DIR/template-deploy-memory.json")" -w "%{http_code}" \
      -H "Content-Type: application/json" \
      --data '{"template_id":"postgres-16","service_name":"Postgres 16 MVP","database_name":"mvp"}' \
      "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/template-deploy"
  )"
  printf "%s" "$status" >"$MVP_EVIDENCE_DIR/template-deploy-memory.status"
  test "$status" = "501"
  jq -e '.error.code == "NOT_IMPLEMENTED" and (.error.message | test("queue is not wired"))' "$MVP_EVIDENCE_DIR/template-deploy-memory.json"
}

memory_blockers() {
  cd "$ROOT_DIR"
  choose_tool GO_BIN go go.exe
  choose_curl
  choose_tool JQ_BIN jq jq.exe
  local backup_runs_memory_path
  backup_runs_memory_path="$(tool_path "$CURL_BIN" "$MVP_EVIDENCE_DIR/backup-runs-memory.json")"
  mkdir -p "$MVP_EVIDENCE_DIR"
  start_memory_server
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/runtime-inventory" >"$MVP_EVIDENCE_DIR/runtime-inventory-memory.json"
  printf 'state\tname\towner\trationale\n' >"$MVP_EVIDENCE_DIR/blockers.tsv"
  record_blocker "memory template deploy has no queue store" "control-plane-api" "memory mode is diagnostic; MVP package mode must use the Postgres queue store" \
    memory_template_queue_probe
  record_blocker "no generated route exists before runtime reports one" "runtime-agent" "local smoke does not run Docker/Traefik; surface-agreement and real-app modes assert route surfaces" \
    bash -c "'$CURL_BIN' -fsS -H 'Authorization: Bearer $MIDNIGHT_API_TOKEN' '$MIDNIGHT_API_BASE/api/v1alpha/dashboard/runtime-inventory' | '$JQ_BIN' -e '(.routes | length) == 0'"
  record_blocker "no runtime host is registered before agent registration" "runtime-agent" "local smoke starts only the API; surface-agreement registers an agent and checks host inventory" \
    jq -e '(.hosts | length) == 0 and (.agents | length) == 0' "$MVP_EVIDENCE_DIR/runtime-inventory-memory.json"
  record_blocker "backup run API has no configured destination in unconfigured memory mode" "operator-config" "backup mode requires a real configured destination; missing config must stay a typed degraded state" \
    bash -c "test \"\$('$CURL_BIN' -sS -H 'Authorization: Bearer $MIDNIGHT_API_TOKEN' -o '$backup_runs_memory_path' -w '%{http_code}' '$MIDNIGHT_API_BASE/api/v1alpha/backup/runs')\" = '400' && '$JQ_BIN' -e '.error.code == \"BACKUP_OFFHOST_MISSING\"' '$backup_runs_memory_path'"
}

generated_evidence_check() {
  choose_curl
  choose_tool JQ_BIN jq jq.exe
  mkdir -p "$MVP_EVIDENCE_DIR"
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/_demo/schema-evidence" |
    tee "$MVP_EVIDENCE_DIR/schema-evidence.json" |
    jq -e '.status == "ready" and (.source_files | length) >= 1'
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/_demo/migration-plan" |
    tee "$MVP_EVIDENCE_DIR/migration-plan.json" |
    jq -e '.status == "ready" or .status == "needs-review" or .status == "blocked"'
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/_demo/release-evidence" |
    tee "$MVP_EVIDENCE_DIR/release-evidence.json" |
    jq -e '(.issue_codes | type == "array") and (.status == "ready" or .status == "needs-review" or .status == "blocked")'
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/_demo/evidence-index" |
    tee "$MVP_EVIDENCE_DIR/evidence-index.json" |
    jq -e '(.references | length) == 4 and (.status == "ready" or .status == "needs-review" or .status == "blocked")'
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/_demo/api-data-readiness" |
    tee "$MVP_EVIDENCE_DIR/api-data-readiness.json" |
    jq -e '.openapi.available == true and (.status == "ready" or .status == "needs-review" or .status == "blocked")'
}

surface_agreement() {
  cd "$ROOT_DIR"
  choose_tool GO_BIN go go.exe
  choose_curl
  choose_tool JQ_BIN jq jq.exe
  mkdir -p "$MVP_EVIDENCE_DIR"
  start_memory_server
  run_step "$GO_BIN" run ./tools/contract-tests

  local suffix project_slug service_slug agent_id project_id service_id operation_id
  suffix="$(printf '%s' "$MVP_RUN_ID" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9-' | cut -c1-32)"
  project_slug="mvp-surface-${suffix}"
  service_slug="web-${suffix}"
  agent_id="agent-${suffix}"
  local cli_auth=()
  if [[ -n "$MIDNIGHT_API_TOKEN" ]]; then
    cli_auth=(--token "$MIDNIGHT_API_TOKEN")
  elif [[ "${MIDNIGHT_ACCEPTANCE_EXTERNAL_SERVER:-0}" != "1" ]]; then
    # The CLI intentionally refuses to select an unauthenticated credential
    # implicitly, even when the developer-mode API accepts requests without
    # auth. Supply a non-secret test credential so the local acceptance flow
    # exercises that explicit resolver contract.
    cli_auth=(--token "midnight-local-acceptance")
  fi

  api_curl -fsS \
    -H "Content-Type: application/json" \
    -H "Idempotency-Key: surface-project-${suffix}" \
    --data "{\"workspace_id\":\"wrk_default\",\"slug\":\"${project_slug}\",\"display_name\":\"MVP Surface ${suffix}\"}" \
    "$MIDNIGHT_API_BASE/api/v1alpha/projects" |
    tee "$MVP_EVIDENCE_DIR/surface-project.json"
  project_id="$(jq_text '.id' "$MVP_EVIDENCE_DIR/surface-project.json")"
  test -n "$project_id"

  api_curl -fsS \
    -H "Content-Type: application/json" \
    -H "Idempotency-Key: surface-service-${suffix}" \
    --data "{\"project_id\":\"${project_id}\",\"slug\":\"${service_slug}\",\"display_name\":\"MVP Web ${suffix}\"}" \
    "$MIDNIGHT_API_BASE/api/v1alpha/services" |
    tee "$MVP_EVIDENCE_DIR/surface-service.json"
  service_id="$(jq_text '.id' "$MVP_EVIDENCE_DIR/surface-service.json")"
  test -n "$service_id"

  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/projects" |
    tee "$MVP_EVIDENCE_DIR/projects-api.json"
  jq -e --arg id "$project_id" --arg slug "$project_slug" '.[] | select(.id == $id and .slug == $slug)' "$MVP_EVIDENCE_DIR/projects-api.json"
  "$GO_BIN" run ./cmd/midnight project list --api-base "$MIDNIGHT_API_BASE" ${cli_auth[@]+"${cli_auth[@]}"} --json |
    tee "$MVP_EVIDENCE_DIR/projects-cli.json"
  jq -e --arg id "$project_id" --arg slug "$project_slug" '.[] | select(.id == $id and .slug == $slug)' "$MVP_EVIDENCE_DIR/projects-cli.json"

  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/services" |
    tee "$MVP_EVIDENCE_DIR/services-api.json"
  jq -e --arg id "$service_id" --arg slug "$service_slug" --arg project "$project_id" '.[] | select(.id == $id and .slug == $slug and .project_id == $project)' "$MVP_EVIDENCE_DIR/services-api.json"
  "$GO_BIN" run ./cmd/midnight service list --api-base "$MIDNIGHT_API_BASE" ${cli_auth[@]+"${cli_auth[@]}"} --json |
    tee "$MVP_EVIDENCE_DIR/services-cli.json"
  jq -e --arg id "$service_id" --arg slug "$service_slug" --arg project "$project_id" '.[] | select(.id == $id and .slug == $slug and .project_id == $project)' "$MVP_EVIDENCE_DIR/services-cli.json"

  api_curl -fsS \
    -H "Content-Type: application/json" \
    --data "{\"agent_id\":\"${agent_id}\",\"version\":\"0.0.0-dev\",\"hostname\":\"host-${suffix}\"}" \
    "$MIDNIGHT_API_BASE/api/v1alpha/agents/register" |
    tee "$MVP_EVIDENCE_DIR/agent-register.json"
  jq -e --arg id "$agent_id" '.agent_id == $id' "$MVP_EVIDENCE_DIR/agent-register.json"
  api_curl -fsS \
    -H "Content-Type: application/json" \
    --data "{\"agent_id\":\"${agent_id}\",\"route_targets\":[{\"route_name\":\"${service_slug}\",\"deployment_id\":\"dep-${suffix}\",\"instance_id\":\"inst-${suffix}\",\"container_ip\":\"10.0.0.2\",\"internal_service_port\":8080,\"readiness_status\":\"ready\"}],\"drift\":{\"engine_version\":\"docker-test\",\"cgroup_version\":\"v2\"}}" \
    "$MIDNIGHT_API_BASE/api/v1alpha/agents/${agent_id}/status" |
    tee "$MVP_EVIDENCE_DIR/agent-status.json"

  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/runtime-inventory" |
    tee "$MVP_EVIDENCE_DIR/runtime-inventory-surface.json"
  jq -e --arg id "$project_id" '.projects[] | select(.id == $id)' "$MVP_EVIDENCE_DIR/runtime-inventory-surface.json"
  jq -e --arg id "$service_id" '.services[] | select(.id == $id)' "$MVP_EVIDENCE_DIR/runtime-inventory-surface.json"
  jq -e --arg id "$agent_id" '.hosts[] | select(.agent_id == $id and (.doctor == "registered" or .doctor == "stale" or .doctor == "ok"))' "$MVP_EVIDENCE_DIR/runtime-inventory-surface.json"
  jq -e --arg id "$service_id" --arg route "$service_slug" '.routes[] | select(.service_id == $id or .route_name == $route or .route == $route)' "$MVP_EVIDENCE_DIR/runtime-inventory-surface.json"

  api_curl -fsS \
    -X POST \
    -H "Idempotency-Key: surface-operation-${suffix}" \
    "$MIDNIGHT_API_BASE/api/v1alpha/_demo/long" |
    tee "$MVP_EVIDENCE_DIR/operation-surface.json"
  operation_id="$(jq_text '.id' "$MVP_EVIDENCE_DIR/operation-surface.json")"
  test -n "$operation_id"
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/operations/${operation_id}/logs" |
    tee "$MVP_EVIDENCE_DIR/operation-logs-api.json"
  jq -e --arg id "$operation_id" '.operation_id == $id and .operation_events.available == true and ([.app_logs[] | select(.available == false)] | length) >= 3' "$MVP_EVIDENCE_DIR/operation-logs-api.json"
  "$GO_BIN" run ./cmd/midnight deploy status --api-base "$MIDNIGHT_API_BASE" ${cli_auth[@]+"${cli_auth[@]}"} --operation-id "$operation_id" --json |
    tee "$MVP_EVIDENCE_DIR/operation-status-cli.json"
  jq -e --arg id "$operation_id" '.id == $id' "$MVP_EVIDENCE_DIR/operation-status-cli.json"
  "$GO_BIN" run ./cmd/midnight deploy logs --api-base "$MIDNIGHT_API_BASE" ${cli_auth[@]+"${cli_auth[@]}"} --operation-id "$operation_id" --json |
    tee "$MVP_EVIDENCE_DIR/operation-logs-cli.json"
  jq -e --arg id "$operation_id" '.operation_id == $id and .operation_events.available == true' "$MVP_EVIDENCE_DIR/operation-logs-cli.json"

  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/audit/events?limit=20" |
    tee "$MVP_EVIDENCE_DIR/audit-events-surface.json"
  jq -e '.events | length >= 2' "$MVP_EVIDENCE_DIR/audit-events-surface.json"
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/audit/evidence" |
    tee "$MVP_EVIDENCE_DIR/audit-evidence-api.json"
  jq -e '.status == "ready" or .status == "pass" or .status == "verified" or .status == "empty"' "$MVP_EVIDENCE_DIR/audit-evidence-api.json"
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/audit-evidence" |
    tee "$MVP_EVIDENCE_DIR/audit-evidence-dashboard.json"
  jq -e '.status == "ready" or .status == "pass" or .status == "verified" or .status == "empty"' "$MVP_EVIDENCE_DIR/audit-evidence-dashboard.json"
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/audit/export" |
    tee "$MVP_EVIDENCE_DIR/audit-export.ndjson"
  grep -q '"record_kind":"manifest"' "$MVP_EVIDENCE_DIR/audit-export.ndjson"

  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/backup/restore/status" |
    tee "$MVP_EVIDENCE_DIR/restore-status-surface.json"
  jq -e '(.status == "available" or .status == "unavailable") and (.restore_execution.available == false or .offhost.configured == true or .offhost.reason_code != "")' "$MVP_EVIDENCE_DIR/restore-status-surface.json"

  generated_evidence_check
}

human_domain() {
  choose_curl
  choose_tool JQ_BIN jq jq.exe
  : "${MIDNIGHT_HUMAN_SESSION_TOKEN:?human-domain requires MIDNIGHT_HUMAN_SESSION_TOKEN}"
  : "${MVP_PROJECT_ID:?human-domain requires MVP_PROJECT_ID}"
  : "${MVP_SERVICE_ID:?human-domain requires MVP_SERVICE_ID}"
  : "${MVP_ENVIRONMENT_ID:?human-domain requires MVP_ENVIRONMENT_ID}"
  : "${MVP_INGRESS_URL:?human-domain requires MVP_INGRESS_URL}"

  case "$MIDNIGHT_HUMAN_SESSION_TOKEN" in
    midn_user_*) ;;
    *)
      echo "human-domain requires a human session token (midn_user_*), not a recovery or automation credential" >&2
      exit 64
      ;;
  esac

  local collection_url idempotency_suffix idempotency_key status domain_id hostname state
  collection_url="${MIDNIGHT_API_BASE%/}/api/v1alpha/services/${MVP_SERVICE_ID}/environments/${MVP_ENVIRONMENT_ID}/domains"
  idempotency_suffix="$(printf '%s' "$MVP_RUN_ID" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9-' | cut -c1-64)"
  if [[ -z "$idempotency_suffix" ]]; then
    idempotency_suffix="$(date -u +%Y%m%d%H%M%S)"
  fi
  idempotency_key="mvp-human-domain-${idempotency_suffix}"

  mkdir -p "$MVP_EVIDENCE_DIR"
  umask 077
  status="$(
    curl -sS \
      -X POST \
      -D "$MVP_EVIDENCE_DIR/generated-domain-create.headers" \
      -o "$MVP_EVIDENCE_DIR/generated-domain-create.json" \
      -w '%{http_code}' \
      -H "Authorization: Bearer $MIDNIGHT_HUMAN_SESSION_TOKEN" \
      -H "Idempotency-Key: $idempotency_key" \
      -H "Content-Type: application/json" \
      --data '{}' \
      "$collection_url"
  )"
  if [[ "$status" != "200" && "$status" != "201" ]]; then
    echo "human-domain create/converge returned HTTP $status" >&2
    jq -r '.error.code // .code // "UNKNOWN_ERROR"' "$MVP_EVIDENCE_DIR/generated-domain-create.json" >&2 || true
    exit 1
  fi

  jq -e \
    --arg project "$MVP_PROJECT_ID" \
    --arg service "$MVP_SERVICE_ID" \
    --arg environment "$MVP_ENVIRONMENT_ID" \
    '.project_id == $project and
     .service_id == $service and
     .environment_id == $environment and
     .kind == "generated" and
     .path == "/" and
     .public == true and
     .tls == false and
     (.version | numbers) >= 1 and
     (.id | startswith("dom_")) and
     (.hostname | type == "string" and length > 0)' \
    "$MVP_EVIDENCE_DIR/generated-domain-create.json" >/dev/null

  domain_id="$(jq_text '.id' "$MVP_EVIDENCE_DIR/generated-domain-create.json")"
  hostname="$(jq_text '.hostname' "$MVP_EVIDENCE_DIR/generated-domain-create.json")"
  state=""
  for _ in $(seq 1 60); do
    curl -fsS \
      -H "Authorization: Bearer $MIDNIGHT_HUMAN_SESSION_TOKEN" \
      "$collection_url/$domain_id" \
      -o "$MVP_EVIDENCE_DIR/generated-domain-current.json"
    state="$(jq_text '.route_apply_state' "$MVP_EVIDENCE_DIR/generated-domain-current.json")"
    if jq -e '.route_apply_state == "ready" and .route_reachable == true' \
      "$MVP_EVIDENCE_DIR/generated-domain-current.json" >/dev/null; then
      break
    fi
    if [[ "$state" == "failed" ]]; then
      echo "human-domain route reconciliation entered failed state" >&2
      exit 1
    fi
    sleep 2
  done

  jq -e \
    --arg id "$domain_id" \
    --arg hostname "$hostname" \
    '.id == $id and .hostname == $hostname and
     .route_apply_state == "ready" and .route_reachable == true' \
    "$MVP_EVIDENCE_DIR/generated-domain-current.json" >/dev/null

  curl -fsS \
    -D "$MVP_EVIDENCE_DIR/generated-domain-ingress.headers" \
    -H "Host: $hostname" \
    "${MVP_INGRESS_URL%/}/" \
    -o "$MVP_EVIDENCE_DIR/generated-domain-ingress.body"

  jq -n \
    --arg schema_version "midnight.dev/human-generated-domain-acceptance/v1" \
    --arg api_base "${MIDNIGHT_API_BASE%/}" \
    --arg project_id "$MVP_PROJECT_ID" \
    --arg service_id "$MVP_SERVICE_ID" \
    --arg environment_id "$MVP_ENVIRONMENT_ID" \
    --arg domain_id "$domain_id" \
    --arg hostname "$hostname" \
    --arg route_apply_state "$state" \
    '{
      schema_version: $schema_version,
      api_base: $api_base,
      project_id: $project_id,
      service_id: $service_id,
      environment_id: $environment_id,
      domain_id: $domain_id,
      hostname: $hostname,
      route_apply_state: $route_apply_state,
      route_reachable: true,
      host_header_ingress_probe: "passed"
    }' >"$MVP_EVIDENCE_DIR/generated-domain-acceptance.json"

  echo "human-domain: PASS for $hostname"
  echo "human-domain: evidence: $MVP_EVIDENCE_DIR/generated-domain-acceptance.json"
}

package_template() {
  choose_curl
  choose_tool JQ_BIN jq jq.exe
  mkdir -p "$MVP_EVIDENCE_DIR"
  api_curl -fsS \
    -H "Content-Type: application/json" \
    --data '{"template_id":"postgres-16","service_name":"Postgres 16 MVP","database_name":"mvp"}' \
    "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/template-deploy" |
    tee "$MVP_EVIDENCE_DIR/template-deploy.json"

  jq -e '
    .schema_version == "midnight.dashboard.template-deploy.v1" and
    .service.template_id == "postgres-16" and
    .service.engine == "postgres" and
    .service.runtime_state == "waiting-for-worker" and
    .service.health == "queued" and
    .service.queue == "deploy" and
    (.service.job_id | length > 0) and
    .plan.template_id == "postgres-16" and
    .plan.engine == "postgres" and
    .plan.volume.attach_required == true and
    .plan.volume.status == "planned" and
    .plan.backup.status == "needs-runtime" and
    (.plan.backup.restore_command | length > 0) and
    (.plan.required_secrets | index("database-password")) and
    (.plan.runtime_env[] | select(.name == "DATABASE_PASSWORD" and .secret == true and .source == "generated-credential")) and
    .plan.expose_tcp == false and
    .operation.kind == "deploy" and
    .operation.state == "QUEUED" and
    .queue == "deploy" and
    (.job_id | length > 0) and
    .job_state == "QUEUED" and
    .plan.durable == true and
    .plan.connection_guidance.visibility == "private" and
    .plan.connection_guidance.internal == "postgres://${SERVICE_HOST}:${SERVICE_PORT}/mvp?user=${DATABASE_USER}" and
    (.plan.credential_refs | map(select(.name == "database-password" and .generated == true and .scope == "sealed")) | length) == 1 and
    .plan.volume.attach_required == true and
    .plan.volume.status == "planned" and
    .plan.backup.status == "needs-runtime" and
    .plan.restore_plan_guidance.status == "operator-plan-required" and
    .plan.networking.private_by_default == true and
    .plan.networking.external_tcp.status == "disabled" and
    .plan.networking.external_tcp.source_allow_list_required == true and
    .plan.expose_tcp == false
  ' "$MVP_EVIDENCE_DIR/template-deploy.json"

  local operation_id
  operation_id="$(jq_text '.operation.id' "$MVP_EVIDENCE_DIR/template-deploy.json")"
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/operations?kind=deploy&status=queued" |
    tee "$MVP_EVIDENCE_DIR/deploy-operations.json"
  jq -e --arg id "$operation_id" '.data[] | select(.id == $id)' "$MVP_EVIDENCE_DIR/deploy-operations.json"
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/operations/$operation_id/events" |
    tee "$MVP_EVIDENCE_DIR/template-operation-events.sse"
  grep -q "template_id=postgres-16" "$MVP_EVIDENCE_DIR/template-operation-events.sse"
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/runtime-inventory" |
    tee "$MVP_EVIDENCE_DIR/runtime-inventory-after-template.json"
  jq -e '.services[] | select(.template_id == "postgres-16" and .runtime_state == "waiting-for-worker" and .queue == "deploy" and (.job_id | length > 0) and .connection.visibility == "private" and .volume.attach_required == true and .backup.status == "needs-runtime" and .restore_plan_guidance.status == "operator-plan-required" and .networking.private_by_default == true)' "$MVP_EVIDENCE_DIR/runtime-inventory-after-template.json"
  if grep -E 'postgres://[^[:space:]]+:[^*@[:space:]]+@|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY|MIDNIGHT_.*SECRET=' "$MVP_EVIDENCE_DIR/template-deploy.json" "$MVP_EVIDENCE_DIR/runtime-inventory-after-template.json"; then
    echo "secret-like material found in template evidence" >&2
    return 1
  fi
}

real_app() {
  choose_curl
  choose_tool JQ_BIN jq jq.exe
  require_cmd openssl
  : "${MVP_PROJECT_SLUG:=mvp-acceptance}"
  : "${MVP_SERVICE_SLUG:=web}"
  : "${MVP_IMAGE:?set MVP_IMAGE to an immutable image digest}"
  : "${MVP_PUBLIC_HOST:?set MVP_PUBLIC_HOST}"
  : "${MVP_PUBLIC_PATH:=/}"
  mkdir -p "$MVP_EVIDENCE_DIR"

  cat >"$MVP_EVIDENCE_DIR/midnight-app.json" <<EOF
{
  "api_version": "midnight/v1alpha",
  "projects": [{"slug": "${MVP_PROJECT_SLUG}", "display_name": "MVP Acceptance"}],
  "services": [{"project_slug": "${MVP_PROJECT_SLUG}", "slug": "${MVP_SERVICE_SLUG}", "display_name": "MVP Web"}]
}
EOF

  local cli_auth=()
  if [[ -n "$MIDNIGHT_API_TOKEN" ]]; then
    cli_auth=(--token "$MIDNIGHT_API_TOKEN")
  fi
  "$MIDNIGHT_CLI" config apply \
    --file "$MVP_EVIDENCE_DIR/midnight-app.json" \
    --endpoint "$MIDNIGHT_API_BASE" \
    --idempotency-key "mvp-config-${MVP_RUN_ID}" \
    ${cli_auth[@]+"${cli_auth[@]}"} |
    tee "$MVP_EVIDENCE_DIR/config-apply.json"

  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/services" | tee "$MVP_EVIDENCE_DIR/services.json"
  local service_id operation_id
  service_id="$(jq_text --arg slug "$MVP_SERVICE_SLUG" '(.data // .)[] | select(.slug == $slug) | .id' "$MVP_EVIDENCE_DIR/services.json")"
  test -n "$service_id"

  cat >"$MVP_EVIDENCE_DIR/deploy-request.json" <<EOF
{
  "image": "${MVP_IMAGE}",
  "route": {
    "name": "${MVP_SERVICE_SLUG}",
    "group": "mvp",
    "host": "${MVP_PUBLIC_HOST}",
    "path": "${MVP_PUBLIC_PATH}",
    "public": true,
    "tls": true
  }
}
EOF

  api_curl -fsS -D "$MVP_EVIDENCE_DIR/deploy-response.headers" \
    -H "Content-Type: application/json" \
    -H "Idempotency-Key: mvp-deploy-${MVP_RUN_ID}" \
    --data @"$MVP_EVIDENCE_DIR/deploy-request.json" \
    "$MIDNIGHT_API_BASE/api/v1alpha/services/$service_id/deployments" |
    tee "$MVP_EVIDENCE_DIR/deploy-response.json"
  jq -e '.kind == "deploy" and (.state == "QUEUED" or .state == "RUNNING" or .state == "SUCCEEDED")' "$MVP_EVIDENCE_DIR/deploy-response.json"
  operation_id="$(jq_text '.id // .operation.id' "$MVP_EVIDENCE_DIR/deploy-response.json")"
  test -n "$operation_id"

  for i in $(seq 1 90); do
    api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/operations/$operation_id/events" |
      tee "$MVP_EVIDENCE_DIR/operation-events-${i}.sse"
    if grep -Eq "SUCCEEDED|succeeded" "$MVP_EVIDENCE_DIR/operation-events-${i}.sse"; then
      break
    fi
    if grep -Eq "FAILED|failed|error" "$MVP_EVIDENCE_DIR/operation-events-${i}.sse"; then
      exit 1
    fi
    sleep 2
  done

  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/runtime-inventory" |
    tee "$MVP_EVIDENCE_DIR/runtime-inventory.json"
  jq -e --arg id "$service_id" '.services[] | select(.id == $id and (.health == "healthy" or .health == "ready"))' "$MVP_EVIDENCE_DIR/runtime-inventory.json"
  jq -e --arg id "$service_id" --arg host "$MVP_PUBLIC_HOST" '.routes[] | select(.service_id == $id or .service == $id or .host == $host)' "$MVP_EVIDENCE_DIR/runtime-inventory.json"
  curl -fsS -D "$MVP_EVIDENCE_DIR/public-response.headers" \
    "https://${MVP_PUBLIC_HOST}${MVP_PUBLIC_PATH}" \
    -o "$MVP_EVIDENCE_DIR/public-response.body"
  openssl s_client -servername "$MVP_PUBLIC_HOST" -connect "${MVP_PUBLIC_HOST}:443" </dev/null 2>/dev/null |
    openssl x509 -noout -subject -issuer -dates |
    tee "$MVP_EVIDENCE_DIR/public-certificate.txt"
}

audit_check() {
  choose_curl
  choose_tool JQ_BIN jq jq.exe
  mkdir -p "$MVP_EVIDENCE_DIR"
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/audit/events?limit=20" | tee "$MVP_EVIDENCE_DIR/audit-events.json"
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/audit-evidence" | tee "$MVP_EVIDENCE_DIR/audit-evidence.json"
  jq -e '.events | length >= 1' "$MVP_EVIDENCE_DIR/audit-events.json"
  jq -e '.status == "ready" or .status == "pass" or .status == "verified"' "$MVP_EVIDENCE_DIR/audit-evidence.json"
  if [[ -s "$MVP_EVIDENCE_DIR/audit-export.jsonl" ]]; then
    "$MIDNIGHT_CLI" audit verify --from-jsonl "$MVP_EVIDENCE_DIR/audit-export.jsonl" --json |
      tee "$MVP_EVIDENCE_DIR/audit-verify.json"
    jq -e '.status == "verified" or .valid == true' "$MVP_EVIDENCE_DIR/audit-verify.json"
  fi
  if grep -R -E 'postgres://[^[:space:]]+:[^*@[:space:]]+@|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY|MIDNIGHT_.*SECRET=' "$MVP_EVIDENCE_DIR"; then
    echo "secret-like material found in evidence" >&2
    exit 1
  fi
}

backup_check() {
  choose_curl
  choose_tool JQ_BIN jq jq.exe
  mkdir -p "$MVP_EVIDENCE_DIR"
  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/backup/restore/status" |
    tee "$MVP_EVIDENCE_DIR/restore-status.json"
  jq -e '.status == "available" or .status == "unavailable"' "$MVP_EVIDENCE_DIR/restore-status.json"

  : "${MVP_BACKUP_ID:=backup-${MVP_RUN_ID}}"
  api_curl -fsS -X POST \
    -H "Content-Type: application/json" \
    --data "{\"id\":\"${MVP_BACKUP_ID}\",\"database_dump_bytes\":1,\"registry_metadata_bytes\":1,\"config_snapshot_bytes\":1,\"support_reference_bytes\":1,\"support_reference_count\":1}" \
    "$MIDNIGHT_API_BASE/api/v1alpha/backup/runs" |
    tee "$MVP_EVIDENCE_DIR/backup-run.json"
  jq -e --arg id "$MVP_BACKUP_ID" '.id == $id and .manifest.id == $id and .object.key == ("runs/" + $id + "/manifest.json")' "$MVP_EVIDENCE_DIR/backup-run.json"

  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/backup/runs" |
    tee "$MVP_EVIDENCE_DIR/backup-runs.json"
  jq -e --arg id "$MVP_BACKUP_ID" '.data[] | select(.id == $id)' "$MVP_EVIDENCE_DIR/backup-runs.json"

  api_curl -fsS -X POST \
    -H "Content-Type: application/json" \
    --data "{\"backup_id\":\"${MVP_BACKUP_ID}\",\"operator\":\"mvp-acceptance\",\"rollback_owner\":\"platform-oncall\",\"notes\":\"MVP acceptance restore plan\"}" \
    "$MIDNIGHT_API_BASE/api/v1alpha/backup/restore/plans" |
    tee "$MVP_EVIDENCE_DIR/restore-plan.json"
  jq -e --arg id "$MVP_BACKUP_ID" '.backup_id == $id and .plan.backupId == $id and (.plan.steps | length) >= 1' "$MVP_EVIDENCE_DIR/restore-plan.json"

  api_curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/backup/restore/plans" |
    tee "$MVP_EVIDENCE_DIR/restore-plans.json"
  jq -e --arg id "$MVP_BACKUP_ID" '.data[] | select(.backup_id == $id)' "$MVP_EVIDENCE_DIR/restore-plans.json"
}

case "$MODE" in
  local-gate) local_gate ;;
  local-smoke) local_smoke ;;
  surface-agreement) surface_agreement ;;
  memory-blockers) memory_blockers ;;
  package-template) package_template ;;
  human-domain) human_domain ;;
  real-app) real_app ;;
  audit) audit_check ;;
  backup) backup_check ;;
  local)
    local_smoke
    surface_agreement
    ;;
  help|-h|--help) usage ;;
  *)
    usage >&2
    exit 64
    ;;
esac
