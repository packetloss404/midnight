#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
# Real Docker/Postgres checks must not turn into a green, skipped-only run.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
for required in docker go jq; do
  command -v "$required" >/dev/null || {
    echo "integration gate: required command not found: $required" >&2
    exit 2
  }
done
docker info >/dev/null
# Keep cold pulls outside individual test startup budgets.
docker pull testcontainers/ryuk:0.14.0
docker pull postgres:16-alpine
docker build -t midnight-test-minio:7aac2a2c5b7c test/integration/minio
mkdir -p artifacts/ci-integration
results=artifacts/ci-integration/results.jsonl
set +e
# Testcontainers packages share a Docker reaper; serialize package startup so
# concurrent first clients cannot race its handshake and lose cleanup ownership.
GOMAXPROCS="${GOMAXPROCS:-4}" go test -p 1 -tags=integration -json -count=1 -timeout=20m ./... | tee "$results"
pipeline_status=("${PIPESTATUS[@]}")
test_exit=${pipeline_status[0]}
evidence_exit=${pipeline_status[1]}
set -e

jq -r 'select(.Action == "skip" and .Test != null) | .Package + " " + .Test' "$results" \
  > artifacts/ci-integration/skipped-tests.txt
skipped=$(wc -l < artifacts/ci-integration/skipped-tests.txt)
echo "integration gate: $skipped tests skipped (see artifacts/ci-integration/skipped-tests.txt)"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo '### Docker/Postgres integration'
    echo "Go test exit code: $test_exit. Skipped tests: $skipped. Skips are not end-to-end acceptance evidence."
    echo '```text'
    cat artifacts/ci-integration/skipped-tests.txt
    echo '```'
  } >> "$GITHUB_STEP_SUMMARY"
fi
if ((test_exit != 0)); then
  exit "$test_exit"
fi
if ((evidence_exit != 0)); then
  echo 'integration gate: could not retain complete test evidence' >&2
  exit "$evidence_exit"
fi

required_tests=(
  'github.com/midnight/midnight/internal/api TestPostgresBootPersistsAcrossPoolRouterRebuild'
  'github.com/midnight/midnight/cmd/midnight-buildkit TestTenantSourceUpHTTPOutboxWorkerMaterializationAndRelease'
  'github.com/midnight/midnight/internal/volume TestManagerPersistsBytesAcrossRealDockerContainerReplacement'
  'github.com/midnight/midnight/internal/runtime/docker TestRunOneOffAgainstDockerSurvivesAdapterRestartWithoutRerun'
  'github.com/midnight/midnight/internal/backup TestS3DestinationAgainstMinIO'
  'github.com/midnight/midnight/internal/backup TestSealedCredentialEndToEndAgainstMinIO'
)
if [[ -n "${MIDNIGHT_IDENTITY_TEST_DSN:-}" ]]; then
  required_tests+=(
    'github.com/midnight/midnight/internal/identity TestPostgresAdministratorRecoveryResetsCredentialsAndClosesSessions'
  )
fi
passed=$(jq -r 'select(.Action == "pass" and .Test != null) | .Package + " " + .Test' "$results")
for required_test in "${required_tests[@]}"; do
  if ! grep -Fxq -- "$required_test" <<<"$passed"; then
    echo "integration gate: required durable journey did not pass: $required_test" >&2
    exit 1
  fi
done

echo 'integration gate: PASS; required durable canaries executed successfully'
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo 'Durable boot/restart, HTTP source-to-worker, and real volume replacement canaries passed.'
  } >> "$GITHUB_STEP_SUMMARY"
fi
