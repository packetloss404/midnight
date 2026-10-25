#!/usr/bin/env bash
# Acceptance-runner regression tests. HTTP responses here are contract doubles,
# not evidence that a database workload was provisioned.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir -p "$ROOT_DIR/artifacts"
scratch="$(mktemp -d "$ROOT_DIR/artifacts/mvp-runner-test.XXXXXX")"
cleanup() {
  case "$scratch" in
    "$ROOT_DIR"/artifacts/mvp-runner-test.*) rm -rf -- "$scratch" ;;
  esac
}
trap cleanup EXIT
mkdir -p "$scratch/bin"
cat >"$scratch/bin/curl" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$STUB_LOG"
url="${!#}"
case "$url" in
  */projects) printf '{"id":"prj_test"}\n' ;;
  */templates/postgres/instances)
    printf '{"template_slug":"postgres","state":"queued","provision_run_id":"tpr_test","operation_id":"op_test","deployment_id":"dep_test","created_services":[{"id":"svc_test"}],"created_volumes":[{"id":"vol_test"}],"status_url":"%s"}\n' "${STUB_STATUS_URL:-/api/v1alpha/templates/postgres/instances/tpr_test}"
    ;;
  */templates/postgres/instances/tpr_test)
    count=0
    if [[ -f "$STUB_COUNT" ]]; then read -r count <"$STUB_COUNT"; fi
    count=$((count + 1))
    printf '%s\n' "$count" >"$STUB_COUNT"
    state=succeeded
    verified=true
    case "${STUB_SCENARIO:-success}" in
      success) if ((count == 1)); then state=running; fi ;;
      failed) state=failed ;;
      timeout) state=queued ;;
      incomplete) verified=false ;;
      unknown) state=unexpected ;;
    esac
    printf '{"state":"%s","connectivity_verified":true,"persistence_verified":%s,"cleanup_completed":true}\n' "$state" "$verified"
    ;;
  *) echo "unexpected acceptance route: $url" >&2; exit 22 ;;
esac
STUB
printf '#!/usr/bin/env bash\nexit 0\n' >"$scratch/bin/sleep"
chmod +x "$scratch/bin/curl" "$scratch/bin/sleep"
export PATH="$scratch/bin:$PATH"
export STUB_LOG="$scratch/requests.log" STUB_COUNT="$scratch/polls"
export MIDNIGHT_API_BASE=http://127.0.0.1:18080
export MIDNIGHT_API_TOKEN=local-contract-token MVP_RUN_ID=runner-contract
export MVP_TEMPLATE_POLL_ATTEMPTS=2 MVP_EVIDENCE_DIR="$scratch/evidence"

fail() { echo "mvp-acceptance contract test: $*" >&2; exit 1; }
run_case() {
  rm -f "$STUB_LOG" "$STUB_COUNT"
  bash "$ROOT_DIR/scripts/mvp-acceptance.sh" package-template >"$scratch/output" 2>&1
}

export STUB_SCENARIO=success
unset MVP_PROJECT_ID STUB_STATUS_URL
run_case || fail "running-to-succeeded lifecycle did not pass"
grep -Fq 'package-template: PASS' "$scratch/output" || fail "success lacks lifecycle verdict"
grep -Fq '/api/v1alpha/projects' "$STUB_LOG" || fail "missing project bootstrap"
grep -Fq 'Idempotency-Key: mvp-template-runner-contract' "$STUB_LOG" || fail "missing template idempotency"
grep -Fq 'Authorization: Bearer local-contract-token' "$STUB_LOG" || fail "missing configured API authentication"
[[ "$(cat "$STUB_COUNT")" == 2 ]] || fail "did not wait for terminal state"
if grep -Fq 'dashboard/template-deploy' "$STUB_LOG"; then fail "used legacy planning route"; fi

export MVP_PROJECT_ID=prj_existing
run_case || fail "existing-project lifecycle did not pass"
if grep -Fq '/api/v1alpha/projects' "$STUB_LOG"; then fail "created an unwanted project"; fi
grep -Fq 'prj_existing' "$STUB_LOG" || fail "ignored existing project"

for scenario in failed timeout incomplete unknown; do
  export STUB_SCENARIO="$scenario"
  if run_case; then fail "$scenario provisioning incorrectly passed"; fi
done

export STUB_SCENARIO=success STUB_STATUS_URL=https://example.test/status
if run_case; then fail "external status URL was followed"; fi
if [[ -e "$STUB_COUNT" ]]; then fail "unsafe status URL was requested"; fi
grep -Fq 'invalid provision status URL' "$scratch/output" || fail "unsafe URL lacks diagnostic"

unset STUB_STATUS_URL
export MVP_TEMPLATE_POLL_ATTEMPTS=0
if run_case; then fail "zero poll budget passed"; fi
[[ ! -s "$STUB_LOG" ]] || fail "invalid budget mutated the server"
echo "mvp-acceptance contract test: PASS"
