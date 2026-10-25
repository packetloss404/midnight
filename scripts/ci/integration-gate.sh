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
mkdir -p artifacts/ci-integration
results=artifacts/ci-integration/results.jsonl
GOMAXPROCS="${GOMAXPROCS:-4}" go test -p 2 -tags=integration -json -count=1 -timeout=20m ./... | tee "$results"

required_tests=(
  'github.com/midnight/midnight/internal/api TestPostgresBootPersistsAcrossPoolRouterRebuild'
  'github.com/midnight/midnight/cmd/midnight-buildkit TestTenantSourceUpHTTPOutboxWorkerMaterializationAndRelease'
  'github.com/midnight/midnight/internal/volume TestManagerPersistsBytesAcrossRealDockerContainerReplacement'
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

jq -r 'select(.Action == "skip" and .Test != null) | .Package + " " + .Test' "$results" \
  > artifacts/ci-integration/skipped-tests.txt
skipped=$(wc -l < artifacts/ci-integration/skipped-tests.txt)
echo "integration gate: PASS; $skipped tests skipped (see artifacts/ci-integration/skipped-tests.txt)"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo '### Docker/Postgres integration'
    echo 'Durable boot/restart, HTTP source-to-worker, and real volume replacement canaries passed.'
    echo "Skipped tests: $skipped. These are not end-to-end acceptance evidence."
    echo '```text'
    cat artifacts/ci-integration/skipped-tests.txt
    echo '```'
  } >> "$GITHUB_STEP_SUMMARY"
fi
