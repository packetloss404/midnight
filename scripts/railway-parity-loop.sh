#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="local"
REPEAT="${MIDNIGHT_PARITY_REPEAT:-1}"
DRY_RUN=0
# Keep the loop on the repository's reviewed, vulnerability-fixed Go patch
# release even when the host has a newer but unpatched release line.
export GOTOOLCHAIN="${MIDNIGHT_PARITY_GO_TOOLCHAIN:-go1.26.9}"

usage() {
  cat <<'USAGE'
Usage:
  scripts/railway-parity-loop.sh [MODE] [--dry-run]
  scripts/railway-parity-loop.sh --dry-run [MODE]
  scripts/railway-parity-loop.sh --list

Modes:
  fast           High-risk Go and modern-UI contract tests.
  toolchain      Live Railpack, BuildKit, registry, and emitted-app proof.
  dataplane      Integration tests plus a disposable full-data-plane runner.
  fault          Recovery contract tests plus a process-fault runner.
  security       Security contracts plus live Docker isolation checks.
  observability  Log/telemetry contract tests plus a real-origin runner.
  ui             Browser Gate 0 plus a seeded real-API browser flow.
  restore        Recovery contracts plus disposable destructive restore.
  vm             VM harness tests plus a clean supported-VM runner.
  local          Full repository local gate and memory-mode acceptance.
  integration    Database/testcontainer suites (Docker required).
  all            Local and integration compatibility gates.
  live           Installed-instance package, app, audit, and backup checks.

Full-system modes require an executable runner named in --list output. Each
runner must write the requested `midnight.parity.runner.v1` evidence manifest;
exit status alone is never accepted as proof. The repository does not silently
replace those runners with planning/unit tests.
See docs/railway-parity-runner-evidence.md for the exact runner protocol.

Set MIDNIGHT_PARITY_REPEAT=N to repeat the selected loop. Every pass must be
green; the script exits on the first regression. --dry-run prints dispatch,
steps, and prerequisites without checking tools, reading credentials, or
starting an external stack.
USAGE
}

list_modes() {
  cat <<'MODES'
MODE           COVERAGE                                  EXTERNAL REQUIREMENT
fast           built-in targeted contracts               none
toolchain      live source-build toolchain                Docker daemon + network
dataplane      contracts + disposable full data plane    MIDNIGHT_PARITY_DATAPLANE_RUNNER
fault          contracts + process fault injection       MIDNIGHT_PARITY_FAULT_RUNNER
security       contracts + live isolation/leak scans     Docker daemon
observability  contracts + real telemetry origins        MIDNIGHT_PARITY_OBSERVABILITY_RUNNER
ui             browser Gate 0 + seeded real API flows     none
restore        contracts + destructive platform restore  Docker daemon
vm             vm-harness tests + clean supported VM      MIDNIGHT_PARITY_VM_RUNNER
local          built-in full local gate                   none
integration    built-in database testcontainers           Docker daemon
all            local + integration                        Docker daemon
live           installed-instance acceptance              MIDNIGHT_API_BASE, MIDNIGHT_API_TOKEN
MODES
}

mode_steps() {
  case "$1" in
    fast)
      printf '%s\n' \
        "repository diff/format/lint checks" \
        "OpenAPI/router and parity-loop contract checks" \
        "targeted deployment/API/runtime Go tests" \
        "web-next typecheck and component tests"
      ;;
    toolchain)
      printf '%s\n' \
        "build production build-worker image" \
        "Railpack prepare + pinned BuildKit gateway solve" \
        "registry digest comparison + emitted application probes"
      ;;
    dataplane)
      printf '%s\n' \
        "database/testcontainer integration suites" \
        "MIDNIGHT_PARITY_DATAPLANE_RUNNER (fresh PostgreSQL/registry/BuildKit/Traefik/API/workers)"
      ;;
    fault)
      printf '%s\n' \
        "deploy/queue/reconciler/worker recovery tests" \
        "MIDNIGHT_PARITY_FAULT_RUNNER (real process-death convergence matrix)"
      ;;
    security)
      printf '%s\n' \
        "auth/idempotency/log/network/router/build security tests" \
        "live project/environment Docker network isolation proof"
      ;;
    observability)
      printf '%s\n' \
        "logs/observability/notifications/timeline tests" \
        "built-in disposable PostgreSQL, Docker-log/stats, and Traefik access-log proofs" \
        "MIDNIGHT_PARITY_OBSERVABILITY_RUNNER (real build/runtime/HTTP origins and delivery)"
      ;;
    ui)
      printf '%s\n' \
        "web-next Gate 0 typecheck/unit/build/Chromium suite" \
        "seeded real API browser flow without request interception"
      ;;
    restore)
      printf '%s\n' \
        "backup/volume/ACME recovery contract tests" \
        "built-in PostgreSQL + platform filesystem destroy/restore proof" \
        "optional MIDNIGHT_PARITY_RESTORE_RUNNER for installed off-host recovery"
      ;;
    vm)
      printf '%s\n' \
        "tools/vm-harness tests" \
        "MIDNIGHT_PARITY_VM_RUNNER (clean supported VM install/restart/reinstall)"
      ;;
    local)
      printf '%s\n' \
        "repository diff/format/lint" \
        "all Go tests, vet, and builds" \
        "legacy and modern UI lint/test/build/audit" \
        "memory-mode MVP acceptance"
      ;;
    integration)
      printf '%s\n' \
        "database/testcontainer integration suites" \
        "real Docker TCP publication, redeploy, withdrawal, and network-detachment proof"
      ;;
    all)
      mode_steps local
      mode_steps integration
      ;;
    live)
      printf '%s\n' \
        "package-template acceptance" \
        "real-app/HTTPS acceptance" \
        "audit acceptance" \
        "backup API acceptance (not destructive restore)"
      ;;
  esac
}

mode_requirements() {
  case "$1" in
    fast) printf '%s\n' "tools: git, gofmt, go, npm" ;;
    toolchain) printf '%s\n' "tools: docker, curl, python3; running Docker daemon and image network access" ;;
    dataplane) printf '%s\n' "tools: docker, go, git, python3; clean worktree; env: MIDNIGHT_PARITY_DATAPLANE_RUNNER" ;;
    fault) printf '%s\n' "tools: go, git, python3; clean worktree; env: MIDNIGHT_PARITY_FAULT_RUNNER" ;;
    security) printf '%s\n' "tools: go, docker; running Docker daemon" ;;
    observability) printf '%s\n' "tools: go, docker, git, python3; running Docker daemon; clean worktree; env: MIDNIGHT_PARITY_OBSERVABILITY_RUNNER" ;;
    ui) printf '%s\n' "tools: go, npm, curl, python3, Playwright Chromium" ;;
    restore) printf '%s\n' "tools: go, docker, python3; running Docker daemon; optional external extension also needs git and a clean worktree" ;;
    vm) printf '%s\n' "tools: go, git, python3; clean worktree; env: MIDNIGHT_PARITY_VM_RUNNER" ;;
    local) printf '%s\n' "tools: git, gofmt, go, npm, python3, curl, jq" ;;
    integration) printf '%s\n' "tools: docker, go; running Docker daemon" ;;
    all) printf '%s\n' "local + integration requirements" ;;
    live) printf '%s\n' "tools: bash, curl, jq; env: MIDNIGHT_API_BASE, MIDNIGHT_API_TOKEN" ;;
  esac
}

fail_prerequisite() {
  echo "railway-parity-loop: mode=$MODE prerequisite missing: $*" >&2
  exit 78
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 ||
    fail_prerequisite "required command '$1' was not found"
}

require_env() {
  local variable_name="$1"
  local guidance="$2"
  if [[ -z "${!variable_name:-}" ]]; then
    fail_prerequisite "set $variable_name ($guidance)"
  fi
}

require_runner() {
  local variable_name="$1"
  local guidance="$2"
  require_env "$variable_name" "$guidance"
  local runner="${!variable_name}"
  if [[ "$runner" == */* ]]; then
    [[ -f "$runner" && -x "$runner" ]] ||
      fail_prerequisite "$variable_name must name an executable file; got '$runner'"
  else
    command -v "$runner" >/dev/null 2>&1 ||
      fail_prerequisite "$variable_name must name an executable command; got '$runner'"
  fi
}

require_clean_external_source() {
  [[ -z "$(git -C "$ROOT_DIR" status --porcelain --untracked-files=all)" ]] ||
    fail_prerequisite "external runner evidence requires a clean worktree so commit_sha identifies the tested source"
}

external_source_matches() {
  local expected_commit="$1"
  [[ "$(git -C "$ROOT_DIR" rev-parse HEAD)" == "$expected_commit" ]] &&
    [[ -z "$(git -C "$ROOT_DIR" status --porcelain --untracked-files=all)" ]]
}

run_runner() (
  local variable_name="$1"
  local evidence_mode="$2"
  local runner="${!variable_name}"
  require_clean_external_source
  local evidence_parent="$ROOT_DIR/.midnight/parity-evidence/external/$evidence_mode"
  mkdir -p "$evidence_parent"
  local workspace
  workspace="$(mktemp -d "${TMPDIR:-/tmp}/midnight-parity-${evidence_mode}.XXXXXX")"
  cleanup_runner_workspace() {
    if [[ -n "${workspace:-}" && -d "$workspace" ]]; then
      rm -rf -- "$workspace"
    fi
  }
  trap cleanup_runner_workspace EXIT
  trap 'exit 130' INT TERM
  local manifest="$workspace/manifest.json"
  local artifact_dir="$workspace/artifacts"
  mkdir -p "$artifact_dir"
  local run_nonce
  run_nonce="$(basename "$workspace")"
  local started_at
  started_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  local commit_sha
  commit_sha="$(git -C "$ROOT_DIR" rev-parse HEAD)"
  echo "==> external runner: $variable_name=$runner"
  if ! MIDNIGHT_PARITY_EVIDENCE_MANIFEST="$manifest" \
    MIDNIGHT_PARITY_EVIDENCE_ROOT="$workspace" \
    MIDNIGHT_PARITY_EVIDENCE_ARTIFACT_DIR="$artifact_dir" \
    MIDNIGHT_PARITY_EVIDENCE_MODE="$evidence_mode" \
    MIDNIGHT_PARITY_COMMIT_SHA="$commit_sha" \
    MIDNIGHT_PARITY_RUN_STARTED_AT="$started_at" \
    MIDNIGHT_PARITY_RUN_NONCE="$run_nonce" \
    "$runner"; then
    return 1
  fi
  if ! external_source_matches "$commit_sha"; then
    echo "railway-parity-loop: external runner changed HEAD or the worktree; evidence rejected" >&2
    return 1
  fi
  if ! python3 "$ROOT_DIR/scripts/validate-parity-runner-evidence.py" \
    "$manifest" "$evidence_mode" "$commit_sha" "$started_at" "$run_nonce" \
    "$workspace" "$evidence_parent"; then
    return 1
  fi
  if ! external_source_matches "$commit_sha"; then
    echo "railway-parity-loop: source changed before evidence retention; evidence rejected" >&2
    return 1
  fi
  local evidence_dir
  if ! evidence_dir="$(mktemp -d "$evidence_parent/$(date -u '+%Y%m%dT%H%M%SZ')-XXXXXX")"; then
    return 1
  fi
  if ! cp -R "$workspace"/. "$evidence_dir"/; then
    rm -rf -- "$evidence_dir"
    return 1
  fi
  rm -rf -- "$workspace"
  workspace=""
  echo "  validated evidence=$evidence_dir/manifest.json"
)

preflight_mode() {
  case "$1" in
    fast)
      require_cmd git
      require_cmd gofmt
      require_cmd go
      require_cmd npm
      ;;
    toolchain)
      require_cmd docker
      require_cmd curl
      require_cmd python3
      docker info >/dev/null 2>&1 ||
        fail_prerequisite "Docker daemon is not reachable"
      ;;
    dataplane)
      require_runner MIDNIGHT_PARITY_DATAPLANE_RUNNER "a wrapper that provisions and destroys the documented disposable data plane"
      require_cmd docker
      docker info >/dev/null 2>&1 ||
        fail_prerequisite "Docker daemon is not reachable"
      require_cmd go
      require_cmd git
      require_cmd python3
      require_clean_external_source
      ;;
    fault)
      require_runner MIDNIGHT_PARITY_FAULT_RUNNER "a wrapper that injects the documented process-death matrix and asserts convergence"
      require_cmd go
      require_cmd git
      require_cmd python3
      require_clean_external_source
      ;;
    security)
      require_cmd go
      require_cmd docker
      docker info >/dev/null 2>&1 ||
        fail_prerequisite "Docker daemon is not reachable"
      ;;
    observability)
      require_runner MIDNIGHT_PARITY_OBSERVABILITY_RUNNER "a wrapper backed by real build, runtime, HTTP, and notification origins"
      require_cmd go
      require_cmd git
      require_cmd python3
      require_clean_external_source
      require_cmd docker
      docker info >/dev/null 2>&1 ||
        fail_prerequisite "Docker daemon is not reachable"
      ;;
    ui)
      require_cmd go
      require_cmd npm
      require_cmd curl
      require_cmd python3
      [[ -x "$ROOT_DIR/web-next/node_modules/.bin/playwright" ]] ||
        fail_prerequisite "Playwright is not installed; run 'npm --prefix web-next install' and 'npm --prefix web-next run test:e2e:install'"
      ;;
    restore)
      require_cmd go
      require_cmd docker
      require_cmd python3
      docker info >/dev/null 2>&1 ||
        fail_prerequisite "Docker daemon is not reachable"
      if [[ -n "${MIDNIGHT_PARITY_RESTORE_RUNNER:-}" ]]; then
        require_runner MIDNIGHT_PARITY_RESTORE_RUNNER "an optional installed-instance off-host recovery extension"
        require_cmd git
        require_clean_external_source
      fi
      ;;
    vm)
      require_runner MIDNIGHT_PARITY_VM_RUNNER "a wrapper that provisions a clean supported Debian/Ubuntu VM"
      require_cmd go
      require_cmd git
      require_cmd python3
      require_clean_external_source
      ;;
    local)
      require_cmd git
      require_cmd gofmt
      require_cmd go
      require_cmd npm
      require_cmd python3
      require_cmd curl
      require_cmd jq
      ;;
    integration)
      require_cmd docker
      docker info >/dev/null 2>&1 ||
        fail_prerequisite "Docker daemon is not reachable"
      require_cmd go
      ;;
    all)
      preflight_mode local
      preflight_mode integration
      ;;
    live)
      require_cmd bash
      require_cmd curl
      require_cmd jq
      require_env MIDNIGHT_API_BASE "the installed Midnight API URL"
      require_env MIDNIGHT_API_TOKEN "the installed Midnight API bearer token"
      ;;
  esac
}

run_fast() {
  cd "$ROOT_DIR"
  git diff --check
  test -z "$(gofmt -l cmd internal pkg tools)"
  go run ./tools/lint
  go run ./tools/contract-tests
  bash scripts/railway-parity-loop_test.sh
  go test -count=1 \
    ./cmd/midnight \
    ./cmd/midnight-buildkit \
    ./cmd/midnight-worker \
    ./cmd/midnight-agent/... \
    ./cmd/midnight-server \
    ./internal/agent/mcp \
    ./internal/api \
    ./internal/backup \
    ./internal/build \
    ./internal/cliconfig \
    ./internal/deploy \
    ./internal/graphql \
    ./internal/identity \
    ./internal/runtime/docker \
    ./internal/scheduler \
    ./internal/serviceconfig \
    ./internal/sourceartifact \
    ./internal/tcpproxy \
    ./internal/templates \
    ./pkg/agentapi
  npm --prefix web-next run typecheck
  npm --prefix web-next test -- --run
}

run_local() {
  cd "$ROOT_DIR"
  git diff --check
  test -z "$(gofmt -l cmd internal pkg tools)"
  go run ./tools/lint
  go test ./... -count=1
  go vet ./...
  go build ./...

  npm --prefix web run lint
  npm --prefix web test
  npm --prefix web run build
  npm --prefix web audit --omit=dev

  npm --prefix web-next run typecheck
  npm --prefix web-next test -- --run
  npm --prefix web-next run build
  npm --prefix web-next audit --omit=dev

  local acceptance_base="${MIDNIGHT_API_BASE:-}"
  if [[ -z "$acceptance_base" ]]; then
    local acceptance_port
    acceptance_port="$(
      python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
    )"
    acceptance_base="http://127.0.0.1:${acceptance_port}"
  fi
  MIDNIGHT_API_BASE="$acceptance_base" bash scripts/mvp-acceptance.sh local
}

run_toolchain() {
  cd "$ROOT_DIR"
  bash scripts/parity-toolchain-acceptance.sh
}

run_integration() {
  cd "$ROOT_DIR"
  # The integration stack is pure Go. Disabling cgo avoids host-CPU probe
  # crashes in transitive testcontainer dependencies on Apple Silicon.
  CGO_ENABLED=0 go test -tags=integration -count=1 -timeout=15m \
    ./cmd/midnight-buildkit \
    ./internal/db/... \
    ./internal/api/... \
    ./internal/deploy/... \
    ./internal/sourceartifact \
    ./internal/tcpproxy
}

run_dataplane() {
  run_integration
  run_runner MIDNIGHT_PARITY_DATAPLANE_RUNNER dataplane
}

run_fault() {
  cd "$ROOT_DIR"
  go test -count=1 \
    ./internal/deploy/... \
    ./internal/queue/... \
    ./internal/reconciler/... \
    ./cmd/midnight-worker
  run_runner MIDNIGHT_PARITY_FAULT_RUNNER fault
}

run_security() {
  cd "$ROOT_DIR"
  go test -count=1 \
    ./internal/agent/mcp/... \
    ./internal/api \
    ./internal/auth/... \
    ./internal/graphql \
    ./internal/idempotency/... \
    ./internal/identity \
    ./internal/logs/... \
    ./internal/network/... \
    ./internal/networking/... \
    ./internal/router/... \
    ./internal/runtime/docker/... \
    ./internal/tcpproxy/... \
    ./cmd/midnight-buildkit
  CGO_ENABLED=0 go test -tags=integration -count=1 -timeout=15m \
    ./internal/api ./internal/sourceartifact ./internal/tcpproxy \
    -run 'Test(PostgresIdentityClaimSessionRestartAndTenantConcealment|TenantImageDeployIntakeAtomicAuthorizationAndReplay|TenantOperationCancelAtomicAuthorizationAndWorkerFence|QuotaLedgerSurvivesCascadeAndConvergesAfterFilesystemProof|QuotaConcurrentAdmissionFairnessAndPublicationLockOrder|DockerPublishedPortEnrollsRoutesRedeploysWithdrawsAndDetaches)'
  bash scripts/parity-security-acceptance.sh
}

run_observability() {
  cd "$ROOT_DIR"
  go test -count=1 \
    ./internal/logs/... \
    ./internal/observability/... \
    ./internal/notifications/... \
    ./internal/timeline/...
  CGO_ENABLED=0 go test -tags=integration -count=1 -timeout=10m ./internal/api \
    -run 'Test(PostgresObservabilitySurvivesRestartAndSSEReconnect|DockerObservabilityCollectorCapturesRealLogsAndStats|TraefikFileCollectorResolvesDurableRouteAndSurvivesRestart)'
  run_runner MIDNIGHT_PARITY_OBSERVABILITY_RUNNER observability
}

run_ui() {
  cd "$ROOT_DIR"
  npm --prefix web-next run test:gate0
  bash scripts/parity-ui-acceptance.sh
}

run_restore() {
  cd "$ROOT_DIR"
  go test -count=1 \
    ./internal/backup/... \
    ./internal/volume/... \
    ./internal/acme/...
  bash scripts/parity-restore-acceptance.sh
  if [[ -n "${MIDNIGHT_PARITY_RESTORE_RUNNER:-}" ]]; then
    require_runner MIDNIGHT_PARITY_RESTORE_RUNNER "an optional installed-instance off-host recovery extension"
    require_cmd git
    require_cmd python3
    run_runner MIDNIGHT_PARITY_RESTORE_RUNNER restore
  fi
}

run_vm() {
  cd "$ROOT_DIR"
  go test -count=1 ./tools/vm-harness
  run_runner MIDNIGHT_PARITY_VM_RUNNER vm
}

run_live() {
  cd "$ROOT_DIR"
  bash scripts/mvp-acceptance.sh package-template
  bash scripts/mvp-acceptance.sh real-app
  bash scripts/mvp-acceptance.sh audit
  # This checks the current backup API contract. Destructive recovery belongs
  # to the restore mode and its required external runner.
  bash scripts/mvp-acceptance.sh backup
}

run_mode() {
  case "$1" in
    fast) run_fast ;;
    toolchain) run_toolchain ;;
    dataplane) run_dataplane ;;
    fault) run_fault ;;
    security) run_security ;;
    observability) run_observability ;;
    ui) run_ui ;;
    restore) run_restore ;;
    vm) run_vm ;;
    local) run_local ;;
    integration) run_integration ;;
    all)
      run_local
      run_integration
      ;;
    live) run_live ;;
  esac
}

case "${1:-}" in
  --list)
    [[ $# -eq 1 ]] || {
      usage >&2
      exit 64
    }
    list_modes
    exit 0
    ;;
  --dry-run)
    DRY_RUN=1
    MODE="${2:-local}"
    [[ $# -le 2 ]] || {
      usage >&2
      exit 64
    }
    ;;
  help|-h|--help)
    usage
    exit 0
    ;;
  "")
    MODE="local"
    ;;
  *)
    MODE="$1"
    if [[ "${2:-}" == "--dry-run" ]]; then
      DRY_RUN=1
      [[ $# -eq 2 ]] || {
        usage >&2
        exit 64
      }
    elif [[ $# -gt 1 ]]; then
      usage >&2
      exit 64
    fi
    ;;
esac

case "$MODE" in
  fast|toolchain|dataplane|fault|security|observability|ui|restore|vm|local|integration|all|live) ;;
  *)
    echo "railway-parity-loop: unknown mode '$MODE'" >&2
    usage >&2
    exit 64
    ;;
esac

if ! [[ "$REPEAT" =~ ^[1-9][0-9]*$ ]]; then
  echo "MIDNIGHT_PARITY_REPEAT must be a positive integer" >&2
  exit 64
fi

if [[ "$DRY_RUN" == "1" ]]; then
  echo "Railway-parity dry run"
  echo "mode: $MODE"
  echo "repeat: $REPEAT"
  echo "requirements:"
  mode_requirements "$MODE" | sed 's/^/  /'
  echo "steps:"
  mode_steps "$MODE" | sed 's/^/  - /'
  exit 0
fi

preflight_mode "$MODE"

for ((pass = 1; pass <= REPEAT; pass++)); do
  echo "==> Railway-parity loop: mode=$MODE pass=$pass/$REPEAT"
  run_mode "$MODE"
done

echo "Railway-parity loop completed: mode=$MODE passes=$REPEAT all-declared-steps=passed"
