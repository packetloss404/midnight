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
  package-template    Prove PostgreSQL provisioning, connectivity, and persistence.
  human-domain        Converge and prove one human-session generated HTTP domain.
  real-app            Check real app deploy, runtime inventory, route, and HTTPS.
  audit               Check audit API/evidence and optional JSONL verification.
  backup              Check experimental backup manifest/restore-plan APIs only.
  local               Run local-smoke and surface-agreement.
  help                Show this help.

Set MIDNIGHT_API_BASE, MIDNIGHT_API_TOKEN, MVP_EVIDENCE_DIR, and MIDNIGHT_CLI
to point at an existing server, bearer token, evidence directory, and CLI
binary. local-smoke starts its own memory-mode server unless
MIDNIGHT_ACCEPTANCE_EXTERNAL_SERVER=1 is set.

human-domain additionally requires MIDNIGHT_HUMAN_SESSION_TOKEN,
MVP_PROJECT_ID, MVP_SERVICE_ID, MVP_ENVIRONMENT_ID, and MVP_INGRESS_URL. It
converges the singleton generated domain but never deletes it.

package-template uses MVP_PROJECT_ID when supplied; otherwise it creates a
project in MVP_WORKSPACE_ID (default wrk_default). It requires the durable
Postgres API and Docker worker. MVP_TEMPLATE_POLL_ATTEMPTS defaults to 120.
backup requires an experimental API and a destination; it does not copy or
restore application data and cannot satisfy the recovery acceptance gate.

real-app requires MIDNIGHT_API_TOKEN, MVP_PROJECT_ID, MVP_SERVICE_ID,
MVP_ENVIRONMENT_ID, MVP_IMAGE, MVP_PUBLIC_HOST, and MVP_EXPECTED_RESPONSE.
Create the service/environment and its managed public domain first. Human
sessions use the existing generated HTTP domain; recovery credentials may
select an existing generated or ownership-verified custom domain. Configure
external DNS/HTTPS separately. MVP_APP_POLL_ATTEMPTS defaults to 90.
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
  run_step "$NPM_BIN" --prefix web-next ci
  run_step "$NPM_BIN" --prefix web-next run typecheck
  run_step "$NPM_BIN" --prefix web-next test
  run_step "$NPM_BIN" --prefix web-next run build
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
      -H "Idempotency-Key: memory-template-${MVP_RUN_ID}" \
      --data '{"project_id":"prj_memory_probe","name":"Postgres 16 MVP"}' \
      "$MIDNIGHT_API_BASE/api/v1alpha/templates/postgres/instances"
  )"
  printf "%s" "$status" >"$MVP_EVIDENCE_DIR/template-deploy-memory.status"
  test "$status" = "503"
  jq -e '.error.code == "CAPABILITY_UNAVAILABLE" and .error.details.capability == "one-click-template-deploy"' "$MVP_EVIDENCE_DIR/template-deploy-memory.json"
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
  local attempts="${MVP_TEMPLATE_POLL_ATTEMPTS:-120}"
  if [[ ! "$attempts" =~ ^[1-9][0-9]*$ ]]; then
    echo "package-template: MVP_TEMPLATE_POLL_ATTEMPTS must be a positive integer" >&2
    return 64
  fi
  mkdir -p "$MVP_EVIDENCE_DIR"
  local project_id="${MVP_PROJECT_ID:-}" request status_url state
  if [[ -z "$project_id" ]]; then
    local suffix
    suffix="$(printf '%s' "$MVP_RUN_ID" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9-' | cut -c1-32)"
    request="$(jq -n --arg workspace "${MVP_WORKSPACE_ID:-wrk_default}" --arg slug "mvp-template-${suffix}" \
      '{workspace_id:$workspace,slug:$slug,display_name:"MVP Template"}')"
    api_curl -fsS -H "Content-Type: application/json" \
      -H "Idempotency-Key: mvp-template-project-${MVP_RUN_ID}" \
      --data "$request" "$MIDNIGHT_API_BASE/api/v1alpha/projects" |
      tee "$MVP_EVIDENCE_DIR/template-project.json"
    project_id="$(jq_text -e '.id | select(type == "string" and length > 0)' "$MVP_EVIDENCE_DIR/template-project.json")"
  fi
  request="$(jq -n --arg project "$project_id" '{project_id:$project,name:"Postgres 16 MVP"}')"
  api_curl -fsS \
    -H "Content-Type: application/json" \
    -H "Idempotency-Key: mvp-template-${MVP_RUN_ID}" \
    --data "$request" \
    "$MIDNIGHT_API_BASE/api/v1alpha/templates/postgres/instances" |
    tee "$MVP_EVIDENCE_DIR/template-deploy.json"

  jq -e '
    .template_slug == "postgres" and
    (.state == "queued" or .state == "running" or .state == "succeeded") and
    (.provision_run_id | type == "string" and length > 0) and
    (.operation_id | type == "string" and length > 0) and
    (.deployment_id | type == "string" and length > 0) and
    (.created_services | length) == 1 and
    (.created_volumes | length) == 1
  ' "$MVP_EVIDENCE_DIR/template-deploy.json"
  status_url="$(jq_text -e '.status_url | select(type == "string")' "$MVP_EVIDENCE_DIR/template-deploy.json")"
  # Follow only the canonical local status route; never forward an operator
  # bearer to an arbitrary Location supplied by a mismatched server.
  if [[ ! "$status_url" =~ ^/api/v1alpha/templates/postgres/instances/[a-zA-Z0-9_-]+$ ]]; then
    echo "package-template: invalid provision status URL" >&2
    return 1
  fi
  for ((attempt=1; attempt<=attempts; attempt++)); do
    api_curl -fsS "$MIDNIGHT_API_BASE$status_url" |
      tee "$MVP_EVIDENCE_DIR/template-status.json"
    state="$(jq_text -e '.state | select(type == "string")' "$MVP_EVIDENCE_DIR/template-status.json")"
    case "$state" in
      succeeded) break ;;
      failed)
        echo "package-template: provisioning failed; inspect template-status.json" >&2
        return 1
        ;;
      queued|running) ;;
      *) echo "package-template: unknown provision state: $state" >&2; return 1 ;;
    esac
    if ((attempt < attempts)); then sleep 1; fi
  done
  jq -e '
    .state == "succeeded" and
    .connectivity_verified == true and
    .persistence_verified == true and
    .cleanup_completed == true
  ' "$MVP_EVIDENCE_DIR/template-status.json"
  if grep -E 'postgres://[^[:space:]]+:[^*@[:space:]]+@|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY|MIDNIGHT_.*SECRET=' "$MVP_EVIDENCE_DIR/template-deploy.json" "$MVP_EVIDENCE_DIR/template-status.json"; then
    echo "secret-like material found in template evidence" >&2
    return 1
  fi
  echo "package-template: PASS (connectivity, replacement persistence, and probe cleanup)"
}

real_app() {
  choose_curl
  choose_tool JQ_BIN jq jq.exe
  require_cmd openssl
  require_cmd timeout
  : "${MIDNIGHT_API_TOKEN:?real-app requires an explicit API credential}"
  : "${MVP_PROJECT_ID:?set MVP_PROJECT_ID to the destination project}"
  : "${MVP_SERVICE_ID:?set MVP_SERVICE_ID to the destination service}"
  : "${MVP_ENVIRONMENT_ID:?set MVP_ENVIRONMENT_ID to the destination environment}"
  : "${MVP_IMAGE:?set MVP_IMAGE to an immutable image digest}"
  : "${MVP_PUBLIC_HOST:?set MVP_PUBLIC_HOST to an existing managed domain}"
  : "${MVP_PUBLIC_PATH:=/}"
  : "${MVP_EXPECTED_RESPONSE:?set MVP_EXPECTED_RESPONSE to a literal application response marker}"
  local attempts="${MVP_APP_POLL_ATTEMPTS:-90}"
  local identifier
  for identifier in "$MVP_PROJECT_ID" "$MVP_SERVICE_ID" "$MVP_ENVIRONMENT_ID"; do
    [[ "$identifier" =~ ^[a-zA-Z0-9_-]+$ ]] || {
      echo "real-app: project, service, and environment IDs must be canonical identifiers" >&2
      return 64
    }
  done
  if [[ ! "$attempts" =~ ^[1-9][0-9]*$ || ! "$MVP_IMAGE" =~ @sha256:[a-fA-F0-9]{64}$ ||
    ! "$MVP_PUBLIC_HOST" =~ ^[a-zA-Z0-9.-]+$ || "$MVP_PUBLIC_PATH" != /* ]]; then
    echo "real-app: invalid poll budget, image digest, hostname, or path" >&2
    return 64
  fi
  mkdir -p "$MVP_EVIDENCE_DIR"

  # Resolve exact scope before a mutation. Slugs alone can collide across
  # projects, and deploy intake requires an explicit environment.
  api_curl -fsS --max-time 30 "$MIDNIGHT_API_BASE/api/v1alpha/services?project_id=$MVP_PROJECT_ID" >"$MVP_EVIDENCE_DIR/services.json"
  jq -e --arg service "$MVP_SERVICE_ID" --arg project "$MVP_PROJECT_ID" \
    '[.[] | select(.id == $service and .project_id == $project)] | length == 1' "$MVP_EVIDENCE_DIR/services.json"
  api_curl -fsS --max-time 30 "$MIDNIGHT_API_BASE/api/v1alpha/environments?project_id=$MVP_PROJECT_ID" >"$MVP_EVIDENCE_DIR/environments.json"
  jq -e --arg environment "$MVP_ENVIRONMENT_ID" --arg project "$MVP_PROJECT_ID" \
    '[.[] | select(.id == $environment and .project_id == $project)] | length == 1' "$MVP_EVIDENCE_DIR/environments.json"

  local domains_url human=false
  case "$MIDNIGHT_API_TOKEN" in
    midn_user_*)
      human=true
      domains_url="$MIDNIGHT_API_BASE/api/v1alpha/services/$MVP_SERVICE_ID/environments/$MVP_ENVIRONMENT_ID/domains"
      ;;
    *) domains_url="$MIDNIGHT_API_BASE/api/v1alpha/domains?project_id=$MVP_PROJECT_ID&environment_id=$MVP_ENVIRONMENT_ID&service_id=$MVP_SERVICE_ID" ;;
  esac
  api_curl -fsS --max-time 30 "$domains_url" >"$MVP_EVIDENCE_DIR/domains-before.json"
  real_app_select_domain "$MVP_EVIDENCE_DIR/domains-before.json" >"$MVP_EVIDENCE_DIR/domain-before.json"
  # Human image intake admits only public opt-in. It keeps the existing
  # generated domain's server-owned route. Recovery intake selects the saved
  # route with its exact TLS intent; external edge TLS is a separate proof.
  jq --arg image "$MVP_IMAGE" --arg environment "$MVP_ENVIRONMENT_ID" --argjson human "$human" '
    {image:$image,environment_id:$environment,route:
      (if $human then {public:true}
       else {public:true,name:.route_name,path:.path,tls:.tls} +
         (if .kind == "custom" then {host:.hostname} else {} end)
       end)}' "$MVP_EVIDENCE_DIR/domain-before.json" >"$MVP_EVIDENCE_DIR/deploy-request.json"

  api_curl -fsS --max-time 30 -D "$MVP_EVIDENCE_DIR/deploy-response.headers" \
    -H "Content-Type: application/json" \
    -H "Idempotency-Key: mvp-deploy-${MVP_RUN_ID}" \
    --data @"$MVP_EVIDENCE_DIR/deploy-request.json" \
    "$MIDNIGHT_API_BASE/api/v1alpha/services/$MVP_SERVICE_ID/deployments" |
    tee "$MVP_EVIDENCE_DIR/deploy-response.json"
  jq -e '.kind == "deploy" and (.state == "QUEUED" or .state == "RUNNING" or .state == "SUCCEEDED")' "$MVP_EVIDENCE_DIR/deploy-response.json"
  local operation_id state attempt
  operation_id="$(jq_text -e '.id | select(type == "string" and test("^[a-zA-Z0-9_-]+$"))' "$MVP_EVIDENCE_DIR/deploy-response.json")"
  for ((attempt=1; attempt<=attempts; attempt++)); do
    api_curl -fsS --max-time 30 "$MIDNIGHT_API_BASE/api/v1alpha/operations/$operation_id" |
      tee "$MVP_EVIDENCE_DIR/operation-status.json"
    jq -e --arg id "$operation_id" '.id == $id and .kind == "deploy"' "$MVP_EVIDENCE_DIR/operation-status.json"
    state="$(jq_text -e '.state' "$MVP_EVIDENCE_DIR/operation-status.json")"
    case "$state" in
      SUCCEEDED) break ;;
      FAILED|CANCELLED|CANCELED)
        echo "real-app: deployment $state; inspect operation-status.json" >&2
        return 1
        ;;
      QUEUED|RUNNING) ;;
      *) echo "real-app: unknown operation state: $state" >&2; return 1 ;;
    esac
    if ((attempt < attempts)); then sleep 2; fi
  done
  [[ "$state" == SUCCEEDED ]] || { echo "real-app: deployment timed out" >&2; return 1; }
  api_curl -fsS --max-time 30 "$MIDNIGHT_API_BASE/api/v1alpha/services/$MVP_SERVICE_ID/deployments/latest-active?environment_id=$MVP_ENVIRONMENT_ID" |
    tee "$MVP_EVIDENCE_DIR/active-deployment.json"
  jq -e --arg operation "$operation_id" --arg service "$MVP_SERVICE_ID" \
    --arg project "$MVP_PROJECT_ID" --arg environment "$MVP_ENVIRONMENT_ID" --arg image "$MVP_IMAGE" '
      .operation_id == $operation and .service_id == $service and .project_id == $project and
      .environment_id == $environment and .image == $image and .active == true and .status == "ACTIVE"
    ' "$MVP_EVIDENCE_DIR/active-deployment.json"
  api_curl -fsS --max-time 30 "$domains_url" >"$MVP_EVIDENCE_DIR/domains-after.json"
  real_app_select_domain "$MVP_EVIDENCE_DIR/domains-after.json" >"$MVP_EVIDENCE_DIR/domain-after.json"
  jq -e --slurpfile before "$MVP_EVIDENCE_DIR/domain-before.json" \
    '.id == $before[0].id and .apply_state == "ready" and .reachable == true' "$MVP_EVIDENCE_DIR/domain-after.json"
  curl -fsS --max-time 30 -D "$MVP_EVIDENCE_DIR/public-response.headers" \
    "https://${MVP_PUBLIC_HOST}${MVP_PUBLIC_PATH}" \
    -o "$MVP_EVIDENCE_DIR/public-response.body"
  grep -Fq -- "$MVP_EXPECTED_RESPONSE" "$MVP_EVIDENCE_DIR/public-response.body"
  timeout 30 openssl s_client -servername "$MVP_PUBLIC_HOST" -connect "${MVP_PUBLIC_HOST}:443" </dev/null 2>/dev/null |
    openssl x509 -noout -subject -issuer -dates |
    tee "$MVP_EVIDENCE_DIR/public-certificate.txt"
  echo "real-app: PASS (exact active deployment, ready managed domain, and matching public HTTPS response)"
}

real_app_select_domain() {
  # Git Bash otherwise rewrites the literal route prefix '/' when invoking
  # native jq. File arguments still need its normal filesystem conversion.
  MSYS2_ENV_CONV_EXCL="${MSYS2_ENV_CONV_EXCL:-};MVP_DOMAIN_ROUTE_PREFIX" \
    MVP_DOMAIN_ROUTE_PREFIX="$MVP_PUBLIC_PATH" jq -e --arg project "$MVP_PROJECT_ID" --arg service "$MVP_SERVICE_ID" \
    --arg environment "$MVP_ENVIRONMENT_ID" --arg host "$MVP_PUBLIC_HOST" '
      [ .[] | select(.project_id == $project and .service_id == $service and
          .environment_id == $environment and .hostname == $host) |
        if has("routes") then . as $domain | .routes[] |
          {id:$domain.id,kind:$domain.kind,hostname:$domain.hostname,path:.path_prefix,
           public:.public,tls:$domain.tls_desired,route_name:.name,
           apply_state:$domain.apply_state,reachable:$domain.reachable}
        else . + {apply_state:.route_apply_state,reachable:.route_reachable} end |
        select(.path == env.MVP_DOMAIN_ROUTE_PREFIX and .public == true)
      ] | if length == 1 then .[0] else error("expected exactly one existing managed public domain for this scope/host/path") end
    ' "$1"
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
  echo "backup: metadata/restore-plan diagnostic only; no application bytes are copied or restored" >&2
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
