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
url="" output="" data="" next=""
for argument in "$@"; do
  if [[ "$next" == output ]]; then output="$argument"; next=""; continue; fi
  if [[ "$next" == data ]]; then data="$argument"; next=""; continue; fi
  case "$argument" in
    -o) next=output ;;
    --data) next=data ;;
    http://*|https://*) url="$argument" ;;
  esac
done
case "$url" in
  */audit/events\?limit=20)
    if [[ "${STUB_AUDIT_SCENARIO:-success}" == empty ]]; then printf '{"events":[]}\n'; else printf '{"events":[{"id":1}]}\n'; fi
    ;;
  */dashboard/audit-evidence)
    status=ready valid=true signatures=verified rows=1 rows_checked=1 checked=1 checkpoints=1 signed=1 unsigned=0
    case "${STUB_AUDIT_SCENARIO:-success}" in
      unsigned) signatures=unsigned; checkpoints=0; checked=0; signed=0; unsigned=1 ;;
      tampered) valid=false; signatures=failed ;;
      untrusted) valid=false; signatures=failed; status=needs-review ;;
      empty) status=empty; rows=0; rows_checked=0; checked=0; checkpoints=0; signed=0; signatures=unsigned ;;
      partial) signatures=partial; rows=2; rows_checked=2; unsigned=1 ;;
      mismatched_snapshot) rows=2 ;;
    esac
    printf '{"status":"%s","event_count":%s,"checkpoint_count":%s,"verification":{"valid":%s,"rows_checked":%s,"checkpoints_checked":%s,"signature_status":"%s","signed_through_sequence":%s,"unsigned_rows":%s}}\n' "$status" "$rows" "$checkpoints" "$valid" "$rows_checked" "$checked" "$signatures" "$signed" "$unsigned"
    ;;
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
  */services\?project_id=prj_app)
    printf '[{"id":"svc_app","project_id":"%s"}]\n' "${STUB_APP_PROJECT:-prj_app}"
    ;;
  */environments\?project_id=prj_app) printf '[{"id":"env_app","project_id":"prj_app"}]\n' ;;
  */services/svc_app/environments/env_app/domains|*/domains\?project_id=prj_app\&environment_id=env_app\&service_id=svc_app)
    host="${STUB_APP_HOST:-app.example.test}"
    if [[ "$url" == *environments/env_app/domains ]]; then
      printf '[{"id":"dom_app","project_id":"prj_app","service_id":"svc_app","environment_id":"env_app","kind":"generated","hostname":"%s","path":"/","public":true,"tls":false,"route_apply_state":"ready","route_reachable":true}]\n' "$host"
    else
      printf '[{"id":"dom_app","project_id":"prj_app","service_id":"svc_app","environment_id":"env_app","kind":"%s","hostname":"%s","tls_desired":false,"apply_state":"ready","reachable":true,"routes":[{"name":"saved-route","path_prefix":"/","public":true}]}]\n' "${STUB_APP_DOMAIN_KIND:-generated}" "$host"
    fi
    ;;
  */services/svc_app/deployments)
    jq -e '.environment_id == "env_app" and .route.public == true and .route.tls != true and (.image | endswith("@sha256:" + ("a" * 64)))' "${data#@}" >/dev/null
    if [[ "${STUB_APP_HUMAN:-false}" == true ]]; then
      jq -e '.route == {public:true}' "${data#@}" >/dev/null
    elif [[ "${STUB_APP_DOMAIN_KIND:-generated}" == custom ]]; then
      jq -e '.route.name == "saved-route" and .route.host == "app.example.test"' "${data#@}" >/dev/null
    else
      jq -e '.route.name == "saved-route" and (.route | has("host") | not)' "${data#@}" >/dev/null
    fi
    printf '{"id":"op_app","kind":"deploy","state":"QUEUED"}\n'
    ;;
  */operations/op_app)
    case "${STUB_APP_SCENARIO:-success}" in
      failed) state=FAILED ;;
      timeout) state=RUNNING ;;
      *) state=SUCCEEDED ;;
    esac
    printf '{"id":"op_app","kind":"deploy","state":"%s","message":"old event said SUCCEEDED"}\n' "$state"
    ;;
  */services/svc_app/deployments/latest-active\?environment_id=env_app)
    printf '{"id":"dep_app","operation_id":"%s","project_id":"prj_app","service_id":"svc_app","environment_id":"env_app","image":"registry.example/app@sha256:%s","status":"ACTIVE","active":true}\n' "${STUB_APP_OPERATION:-op_app}" "$(printf '%064d' 0 | tr 0 a)"
    ;;
  https://app.example.test/)
    printf '%s\n' "${STUB_APP_BODY:-expected app marker}" >"$output"
    ;;
  *) echo "unexpected acceptance route: $url" >&2; exit 22 ;;
esac
STUB
printf '#!/usr/bin/env bash\nexit 0\n' >"$scratch/bin/sleep"
printf '#!/usr/bin/env bash\nif [[ "$1" == x509 ]]; then cat >/dev/null; fi\nprintf "certificate metadata\\n"\n' >"$scratch/bin/openssl"
printf '#!/usr/bin/env bash\nshift\nexec "$@"\n' >"$scratch/bin/timeout"
chmod +x "$scratch/bin/curl" "$scratch/bin/sleep" "$scratch/bin/openssl" "$scratch/bin/timeout"
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

export MVP_PROJECT_ID=prj_app MVP_SERVICE_ID=svc_app MVP_ENVIRONMENT_ID=env_app
export MVP_PUBLIC_HOST=app.example.test MVP_PUBLIC_PATH=/ MVP_APP_POLL_ATTEMPTS=2
MVP_IMAGE="registry.example/app@sha256:$(printf '%064d' 0 | tr 0 a)"
export MVP_IMAGE
export MVP_EXPECTED_RESPONSE='expected app marker'
run_app_case() {
  rm -f "$STUB_LOG"
  bash "$ROOT_DIR/scripts/mvp-acceptance.sh" real-app >"$scratch/output" 2>&1
}
run_app_case || { cat "$scratch/output" >&2; fail "exact scoped real-app contract did not pass"; }
grep -Fq 'real-app: PASS' "$scratch/output" || fail "missing real-app verdict"
if grep -Fq '/events' "$STUB_LOG"; then fail "real-app still regex-polls event text"; fi
export STUB_APP_DOMAIN_KIND=custom
run_app_case || fail "saved custom route selector did not pass"
unset STUB_APP_DOMAIN_KIND
export MIDNIGHT_API_TOKEN=midn_user_contract STUB_APP_HUMAN=true
run_app_case || fail "human generated-domain contract did not pass"
export MIDNIGHT_API_TOKEN=local-contract-token
unset STUB_APP_HUMAN

for scenario in failed timeout; do
  export STUB_APP_SCENARIO="$scenario"
  if run_app_case; then fail "real-app $scenario incorrectly passed despite event text"; fi
done
unset STUB_APP_SCENARIO
export STUB_APP_PROJECT=prj_other
if run_app_case; then fail "real-app cross-project service passed"; fi
if grep -Fq 'Idempotency-Key: mvp-deploy-' "$STUB_LOG"; then fail "real-app mutated a mismatched scope"; fi
unset STUB_APP_PROJECT
export STUB_APP_HOST=other.example.test
if run_app_case; then fail "real-app missing managed hostname passed"; fi
if grep -Fq 'Idempotency-Key: mvp-deploy-' "$STUB_LOG"; then fail "real-app invented an unmanaged route"; fi
unset STUB_APP_HOST
export STUB_APP_OPERATION=op_old
if run_app_case; then fail "real-app unrelated active deployment passed"; fi
unset STUB_APP_OPERATION
export STUB_APP_BODY='different application'
if run_app_case; then fail "real-app wrong public response passed"; fi
unset STUB_APP_BODY
export MVP_APP_POLL_ATTEMPTS=0
if run_app_case; then fail "real-app zero poll budget passed"; fi
[[ ! -s "$STUB_LOG" ]] || fail "invalid real-app budget requested API data"
run_audit_case() {
  bash "$ROOT_DIR/scripts/mvp-acceptance.sh" audit >"$scratch/output" 2>&1
}
export STUB_AUDIT_SCENARIO=success
run_audit_case || { cat "$scratch/output" >&2; fail "trusted checkpoint audit was refused"; }
export STUB_AUDIT_SCENARIO=partial
run_audit_case || fail "explicit verified prefix plus disclosed unsigned tail was refused"
for scenario in unsigned tampered untrusted empty mismatched_snapshot; do
  export STUB_AUDIT_SCENARIO="$scenario"
  if run_audit_case; then fail "$scenario audit evidence incorrectly passed"; fi
done
echo "mvp-acceptance contract test: PASS"
