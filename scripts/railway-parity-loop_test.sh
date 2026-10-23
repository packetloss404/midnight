#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOOP="$ROOT_DIR/scripts/railway-parity-loop.sh"
MODES="fast toolchain dataplane fault security observability ui restore vm local integration all live"

fail() {
  echo "railway-parity-loop contract test: $*" >&2
  exit 1
}

list_output="$(bash "$LOOP" --list)"
for mode in $MODES; do
  printf '%s\n' "$list_output" | grep -Eq "^${mode}[[:space:]]" ||
    fail "--list omitted mode $mode"

  dry_output="$(
    MIDNIGHT_PARITY_REPEAT=2 bash "$LOOP" --dry-run "$mode"
  )"
  printf '%s\n' "$dry_output" | grep -Fq "mode: $mode" ||
    fail "--dry-run dispatched the wrong mode for $mode"
  printf '%s\n' "$dry_output" | grep -Fq "repeat: 2" ||
    fail "--dry-run omitted repeat count for $mode"
  printf '%s\n' "$dry_output" | grep -Fq "steps:" ||
    fail "--dry-run omitted steps for $mode"
done

alternate_output="$(bash "$LOOP" fast --dry-run)"
printf '%s\n' "$alternate_output" | grep -Fq "mode: fast" ||
  fail "MODE --dry-run syntax did not dispatch fast"

unknown_stderr="$(mktemp)"
missing_stderr="$(mktemp)"
invalid_manifest="$(mktemp)"
valid_snapshot="$(mktemp)"
evidence_root="$(mktemp -d)"
history_root="$(mktemp -d)"
valid_manifest="$evidence_root/manifest.json"
trap 'rm -f "$unknown_stderr" "$missing_stderr" "$invalid_manifest" "$valid_snapshot"; rm -rf "$evidence_root" "$history_root"' EXIT
if bash "$LOOP" definitely-not-a-mode --dry-run 2>"$unknown_stderr"; then
  fail "unknown mode returned success"
fi
grep -Fq "unknown mode 'definitely-not-a-mode'" "$unknown_stderr" ||
  fail "unknown mode did not explain the dispatch error"

if (
  unset MIDNIGHT_PARITY_FAULT_RUNNER
  bash "$LOOP" fault
) > /dev/null 2>"$missing_stderr"; then
  fail "fault mode passed without its external runner"
fi
grep -Fq "MIDNIGHT_PARITY_FAULT_RUNNER" "$missing_stderr" ||
  fail "fault mode did not name its missing runner"

if MIDNIGHT_PARITY_REPEAT=0 bash "$LOOP" --dry-run fast > /dev/null 2>"$missing_stderr"; then
  fail "zero repeat count returned success"
fi
grep -Fq "positive integer" "$missing_stderr" ||
  fail "invalid repeat count did not explain the contract"

fast_block="$(sed -n '/^run_fast()/,/^}/p' "$LOOP")"
for package in ./internal/identity ./internal/graphql ./internal/agent/mcp ./internal/cliconfig ./internal/sourceartifact; do
  printf '%s\n' "$fast_block" | grep -Fq "$package" ||
    fail "fast mode omitted security-critical package $package"
done
security_block="$(sed -n '/^run_security()/,/^}/p' "$LOOP")"
for package in ./internal/identity ./internal/graphql ./internal/agent/mcp ./internal/api ./internal/sourceartifact; do
  printf '%s\n' "$security_block" | grep -Fq "$package" ||
    fail "security mode omitted tenant package $package"
done
printf '%s\n' "$security_block" |
  grep -Fq "TenantImageDeployIntakeAtomicAuthorizationAndReplay" ||
  fail "security mode omitted tenant deployment-action transaction proof"
printf '%s\n' "$security_block" |
  grep -Fq "TenantOperationCancelAtomicAuthorizationAndWorkerFence" ||
  fail "security mode omitted tenant operation-cancellation transaction proof"
integration_block="$(sed -n '/^run_integration()/,/^}/p' "$LOOP")"
for package in ./cmd/midnight-buildkit ./internal/sourceartifact; do
  printf '%s\n' "$integration_block" | grep -Fq "$package" ||
    fail "integration mode omitted source-upload package $package"
done

printf '{}\n' >"$invalid_manifest"
if python3 "$ROOT_DIR/scripts/validate-parity-runner-evidence.py" \
  "$invalid_manifest" fault "$(git -C "$ROOT_DIR" rev-parse HEAD)" \
  "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" test-nonce "$evidence_root" "$history_root" \
  > /dev/null 2>"$missing_stderr"; then
  fail "empty external-runner manifest passed validation"
fi

evidence_time="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
evidence_commit="$(git -C "$ROOT_DIR" rev-parse HEAD)"
mkdir -p "$evidence_root/artifacts"
write_fault_proof() {
  local assertion_name="$1"
  local observations="$2"
  local proof_path="$evidence_root/artifacts/$assertion_name.json"
  printf '%s\n' \
    "{\"schema_version\":\"midnight.parity.assertion.v1\",\"mode\":\"fault\",\"assertion\":\"$assertion_name\",\"commit_sha\":\"$evidence_commit\",\"run_nonce\":\"test-nonce\",\"fresh_instance_id\":\"test-instance\",\"observations\":$observations,\"evidence\":[\"contract-test machine observation\"]}" \
    >"$proof_path"
  python3 -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest())' "$proof_path"
}
fresh_hash="$(write_fault_proof fresh_install "{\"installation_id\":\"test-instance\",\"provisioned_at\":\"$evidence_time\",\"empty_state_verified\":true}")"
matrix_hash="$(write_fault_proof process_death_matrix '{"fault_points_total":4,"fault_points_passed":4,"max_recovery_seconds":3}')"
recovery_hash="$(write_fault_proof durable_recovery '{"operations_recovered":2,"terminal_state_verified":true}')"
duplicate_hash="$(write_fault_proof duplicate_suppression '{"duplicates_injected":2,"duplicate_side_effects":0}')"
redaction_hash="$(write_fault_proof redaction '{"files_scanned":6,"credential_findings":0}')"
cleanup_hash="$(write_fault_proof cleanup '{"resources_created":4,"residual_resources":0}')"
printf '%s\n' \
  "{\"schema_version\":\"midnight.parity.runner.v1\",\"mode\":\"fault\",\"commit_sha\":\"$evidence_commit\",\"run_nonce\":\"test-nonce\",\"fresh_instance_id\":\"test-instance\",\"started_at\":\"$evidence_time\",\"completed_at\":\"$evidence_time\",\"assertions\":[{\"name\":\"fresh_install\",\"passed\":true,\"artifact\":\"artifacts/fresh_install.json\"},{\"name\":\"process_death_matrix\",\"passed\":true,\"artifact\":\"artifacts/process_death_matrix.json\"},{\"name\":\"durable_recovery\",\"passed\":true,\"artifact\":\"artifacts/durable_recovery.json\"},{\"name\":\"duplicate_suppression\",\"passed\":true,\"artifact\":\"artifacts/duplicate_suppression.json\"},{\"name\":\"redaction\",\"passed\":true,\"artifact\":\"artifacts/redaction.json\"},{\"name\":\"cleanup\",\"passed\":true,\"artifact\":\"artifacts/cleanup.json\"}],\"components\":{\"midnight\":\"test\"},\"redaction_passed\":true,\"cleanup_passed\":true,\"artifacts\":[{\"path\":\"artifacts/fresh_install.json\",\"sha256\":\"$fresh_hash\",\"media_type\":\"application/json\"},{\"path\":\"artifacts/process_death_matrix.json\",\"sha256\":\"$matrix_hash\",\"media_type\":\"application/json\"},{\"path\":\"artifacts/durable_recovery.json\",\"sha256\":\"$recovery_hash\",\"media_type\":\"application/json\"},{\"path\":\"artifacts/duplicate_suppression.json\",\"sha256\":\"$duplicate_hash\",\"media_type\":\"application/json\"},{\"path\":\"artifacts/redaction.json\",\"sha256\":\"$redaction_hash\",\"media_type\":\"application/json\"},{\"path\":\"artifacts/cleanup.json\",\"sha256\":\"$cleanup_hash\",\"media_type\":\"application/json\"}]}" \
  >"$valid_manifest"
python3 "$ROOT_DIR/scripts/validate-parity-runner-evidence.py" \
  "$valid_manifest" fault "$evidence_commit" "$evidence_time" test-nonce \
  "$evidence_root" "$history_root" > /dev/null ||
  fail "valid external-runner manifest failed validation"
cp "$valid_manifest" "$valid_snapshot"

future_time="$(python3 -c 'import datetime as d; print((d.datetime.now(d.timezone.utc)+d.timedelta(days=1)).isoformat().replace("+00:00","Z"))')"
sed "s/$evidence_time/$future_time/g" "$valid_snapshot" >"$valid_manifest"
if python3 "$ROOT_DIR/scripts/validate-parity-runner-evidence.py" \
  "$valid_manifest" fault "$evidence_commit" "$evidence_time" test-nonce \
  "$evidence_root" "$history_root" > /dev/null 2>"$missing_stderr"; then
  fail "future-dated external-runner manifest passed validation"
fi

sed "s/$fresh_hash/0000000000000000000000000000000000000000000000000000000000000000/" \
  "$valid_snapshot" >"$valid_manifest"
if python3 "$ROOT_DIR/scripts/validate-parity-runner-evidence.py" \
  "$valid_manifest" fault "$evidence_commit" "$evidence_time" test-nonce \
  "$evidence_root" "$history_root" > /dev/null 2>"$missing_stderr"; then
  fail "external-runner manifest with a false artifact hash passed validation"
fi

cp "$valid_snapshot" "$valid_manifest"
printf '%s\n' "Bearer undeclared-secret" >"$evidence_root/artifacts/undeclared.log"
if python3 "$ROOT_DIR/scripts/validate-parity-runner-evidence.py" \
  "$valid_manifest" fault "$evidence_commit" "$evidence_time" test-nonce \
  "$evidence_root" "$history_root" > /dev/null 2>"$missing_stderr"; then
  fail "external-runner evidence with an undeclared file passed validation"
fi
rm -f "$evidence_root/artifacts/undeclared.log"

python3 -c '
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    document = json.load(source)
document["midn_admin_manifestkey"] = "non-secret value"
with open(sys.argv[2], "w", encoding="utf-8") as target:
    json.dump(document, target, separators=(",", ":"))
    target.write("\n")
' "$valid_snapshot" "$valid_manifest"
if python3 "$ROOT_DIR/scripts/validate-parity-runner-evidence.py" \
  "$valid_manifest" fault "$evidence_commit" "$evidence_time" test-nonce \
  "$evidence_root" "$history_root" > /dev/null 2>"$missing_stderr"; then
  fail "credential-shaped manifest key passed retained-evidence redaction"
fi
grep -Fq "credential-like material" "$missing_stderr" ||
  fail "credential-shaped manifest key failed for the wrong reason"
cp "$valid_snapshot" "$valid_manifest"

cleanup_path="$evidence_root/artifacts/cleanup.json"
for credential in \
  midn_user_example midn_boot_example midn_admin_example midn_agent_example \
  midn_change_example midn_mcp_example midn_root_example midn_sa_example \
  midn_secret_example "Bearer example"; do
  printf '%s\n' "$credential" >"$cleanup_path"
  credential_hash="$(python3 -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest())' "$cleanup_path")"
  sed "s/$cleanup_hash/$credential_hash/" "$valid_snapshot" >"$valid_manifest"
  if python3 "$ROOT_DIR/scripts/validate-parity-runner-evidence.py" \
    "$valid_manifest" fault "$evidence_commit" "$evidence_time" test-nonce \
    "$evidence_root" "$history_root" > /dev/null 2>"$missing_stderr"; then
    fail "credential class '$credential' passed retained-evidence redaction"
  fi
  grep -Fq "credential-like material" "$missing_stderr" ||
    fail "credential class '$credential' failed for the wrong reason"
done

echo "railway-parity-loop contract test: passed"
