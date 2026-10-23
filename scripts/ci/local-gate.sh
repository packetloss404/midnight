#!/usr/bin/env bash
# Provider-independent CI gate for reviewed Midnight commits.
#
# The gate deliberately mirrors the expensive GitHub-hosted checks so a
# billing or runner outage cannot turn an unverified commit into a release.
# Evidence is local and commit-bound; it does not impersonate a GitHub check.

set -euo pipefail

usage() {
  cat <<'EOF'
usage: scripts/ci/local-gate.sh [--list] [--allow-dirty] [--evidence-dir DIR]

Runs the release, race, browser, workflow-lint, vulnerability, and Linux
cross-build gates sequentially. By default the worktree must be clean.

  --list          print the ordered gates without running them
  --allow-dirty   permit changes, but mark evidence release-ineligible
  --evidence-dir  repository-relative output directory (default: artifacts/ci-local)
EOF
}

list_only=false
allow_dirty=false
evidence_dir=artifacts/ci-local

while (($# > 0)); do
  case "$1" in
    --list)
      list_only=true
      shift
      ;;
    --allow-dirty)
      allow_dirty=true
      shift
      ;;
    --evidence-dir)
      if (($# < 2)); then
        echo "local-gate: --evidence-dir requires a value" >&2
        exit 64
      fi
      evidence_dir=$2
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "local-gate: unknown argument $1" >&2
      usage >&2
      exit 64
      ;;
  esac
done

case "/$evidence_dir/" in
  //*|*/../*|*/./*)
    echo "local-gate: --evidence-dir must be a normalized repository-relative path" >&2
    exit 64
    ;;
esac

source_root=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "local-gate: run inside a Git worktree" >&2
  exit 2
}
cd "$source_root"

step_ids=(
  release-check
  race
  browser
  workflow-lint
  govulncheck
  osv-scanner
  linux-cross-build
)
step_descriptions=(
  "make release-check"
  "go test ./... -race -count=1"
  "web-next gate0 plus security-header browser acceptance"
  "actionlint v1.7.7"
  "govulncheck v1.3.0 with integration tests"
  "OSV Scanner v2.3.3 against Go and both npm lockfiles"
  "CGO-free Linux amd64 and arm64 release binary builds"
)

if "$list_only"; then
  for index in "${!step_ids[@]}"; do
    printf '%s\t%s\n' "${step_ids[$index]}" "${step_descriptions[$index]}"
  done
  exit 0
fi

for required in awk git go node npm jq; do
  command -v "$required" >/dev/null 2>&1 || {
    echo "local-gate: required command not found: $required" >&2
    exit 2
  }
done

module_go_version=$(awk '$1 == "go" { print $2; exit }' go.mod)
if [[ ! "$module_go_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "local-gate: go.mod must pin an exact Go patch version" >&2
  exit 2
fi
export GOTOOLCHAIN="${MIDNIGHT_GO_TOOLCHAIN:-go$module_go_version}"

worktree_entries=()
while IFS= read -r -d '' entry; do
  status=${entry:0:2}
  path=${entry:3}
  if [[ "$status" == "??" ]]; then
    candidate=${path%/}
    if [[ -d "$candidate" ]]; then
      nested_root=$(git -C "$candidate" rev-parse --show-toplevel 2>/dev/null || true)
      if [[ "$nested_root" == "$source_root/$candidate" ]]; then
        # Clean commits execute in an isolated worktree below, so independent
        # sibling repositories cannot enter the build graph.
        continue
      fi
    fi
  fi
  worktree_entries+=("$entry")
done < <(git status --porcelain=v1 -z --untracked-files=normal)

dirty=false
release_eligible=true
if ((${#worktree_entries[@]} > 0)); then
  dirty=true
  release_eligible=false
  if ! "$allow_dirty"; then
    echo "local-gate: worktree changes are present; commit them or use --allow-dirty for non-release development evidence" >&2
    exit 2
  fi
fi

commit=$(git rev-parse HEAD)
short_commit=$(git rev-parse --short=12 HEAD)
started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
evidence_root="$source_root/$evidence_dir"
mkdir -p "$evidence_root"
scratch_dir=$(mktemp -d "$evidence_root/.local-gate.XXXXXX")
log_file="$scratch_dir/local-ci-$short_commit.log"
result_file="$scratch_dir/local-ci-$short_commit.json"
checkout_parent=""
checkout_dir=""

cleanup() {
  if [[ -n "${checkout_dir:-}" && -e "$checkout_dir/.git" ]]; then
    git -C "$source_root" worktree remove --force "$checkout_dir" >/dev/null 2>&1 || true
  fi
  if [[ -n "${checkout_parent:-}" && -d "$checkout_parent" ]]; then
    rm -rf -- "$checkout_parent"
  fi
  if [[ -n "${scratch_dir:-}" && -d "$scratch_dir" ]]; then
    rm -rf -- "$scratch_dir"
  fi
}
trap cleanup EXIT HUP INT TERM

execution_root="$source_root"
if ! "$dirty"; then
  checkout_parent=$(mktemp -d "${TMPDIR:-/tmp}/midnight-ci-worktree.XXXXXX")
  checkout_dir="$checkout_parent/source"
  git worktree add --detach --quiet "$checkout_dir" "$commit"
  execution_root="$checkout_dir"
fi
cd "$execution_root"

run_release_check() {
  make release-check
}

run_race() {
  go test ./... -race -count=1
}

run_browser() {
  npm --prefix web-next run test:gate0 || return
  scripts/security-headers-browser-test.sh
}

run_workflow_lint() {
  go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7
}

run_govulncheck() {
  go run golang.org/x/vuln/cmd/govulncheck@v1.3.0 -test -tags=integration ./...
}

run_osv_scanner() {
  go run github.com/google/osv-scanner/v2/cmd/osv-scanner@v2.3.3 scan source --lockfile=go.mod || return
  go run github.com/google/osv-scanner/v2/cmd/osv-scanner@v2.3.3 scan source --allow-no-lockfiles --lockfile=web/package-lock.json || return
  go run github.com/google/osv-scanner/v2/cmd/osv-scanner@v2.3.3 scan source --lockfile=web-next/package-lock.json
}

run_linux_cross_build() {
  local build_root
  local architecture
  local command_name
  build_root="$scratch_dir/cross-build"
  for architecture in amd64 arm64; do
    mkdir -p "$build_root/linux-$architecture"
    for command_name in midnight midnight-server midnight-worker midnight-agent midnight-buildkit midnight-registry midnight-recovery midnight-github-status-worker; do
      CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build -trimpath -buildvcs=false \
        -o "$build_root/linux-$architecture/$command_name" "./cmd/$command_name" || return
    done
  done
}

run_step() {
  case "$1" in
    release-check) run_release_check ;;
    race) run_race ;;
    browser) run_browser ;;
    workflow-lint) run_workflow_lint ;;
    govulncheck) run_govulncheck ;;
    osv-scanner) run_osv_scanner ;;
    linux-cross-build) run_linux_cross_build ;;
    *) return 64 ;;
  esac
}

steps_json=$(printf '%s\n' "${step_ids[@]}" | jq -R . | jq -s .)
failed_step=""

for index in "${!step_ids[@]}"; do
  step_id=${step_ids[$index]}
  description=${step_descriptions[$index]}
  printf '\n==> %s: %s\n' "$step_id" "$description" | tee -a "$log_file"
  set +e
  run_step "$step_id" 2>&1 | tee -a "$log_file"
  step_exit=${PIPESTATUS[0]}
  set -e
  if ((step_exit != 0)); then
    failed_step=$step_id
    break
  fi
done

finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
result=passed
if [[ -n "$failed_step" ]]; then
  result=failed
  release_eligible=false
fi

jq -n \
  --arg schema_version "midnight.dev/local-ci-evidence/v1" \
  --arg commit "$commit" \
  --arg started_at "$started_at" \
  --arg finished_at "$finished_at" \
  --arg result "$result" \
  --arg failed_step "$failed_step" \
  --arg go_version "$(go version)" \
  --arg node_version "$(node --version)" \
  --arg npm_version "$(npm --version)" \
  --argjson dirty "$dirty" \
  --argjson release_eligible "$release_eligible" \
  --argjson steps "$steps_json" \
  '{
    schema_version: $schema_version,
    commit: $commit,
    worktree_dirty: $dirty,
    release_eligible: $release_eligible,
    started_at: $started_at,
    finished_at: $finished_at,
    result: $result,
    failed_step: (if $failed_step == "" then null else $failed_step end),
    toolchain: {go: $go_version, node: $node_version, npm: $npm_version},
    steps: $steps
  }' >"$result_file"

final_log="$evidence_root/local-ci-$short_commit.log"
final_result="$evidence_root/local-ci-$short_commit.json"
mv -f -- "$log_file" "$final_log"
mv -f -- "$result_file" "$final_result"

if [[ "$result" != passed ]]; then
  echo "local-gate: failed at $failed_step; evidence: $final_result" >&2
  exit 1
fi

echo "local-gate: PASS for $commit"
echo "local-gate: evidence: $evidence_dir/local-ci-$short_commit.json"
