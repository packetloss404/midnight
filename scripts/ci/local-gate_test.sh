#!/usr/bin/env bash

set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
gate="$repo_root/scripts/ci/local-gate.sh"

expected=$'release-check\tmake release-check\nrace\tgo test ./... -race -count=1\nbrowser\tweb-next gate0 plus security-header browser acceptance\nworkflow-lint\tactionlint v1.7.7\ngovulncheck\tgovulncheck v1.3.0 with integration tests\nosv-scanner\tOSV Scanner v2.3.3 against Go and the dashboard npm lockfile\nlinux-cross-build\tCGO-free Linux amd64 and arm64 release binary builds'
actual=$("$gate" --list)
if [[ "$actual" != "$expected" ]]; then
  echo "local-gate test: ordered gate manifest changed unexpectedly" >&2
  diff -u <(printf '%s\n' "$expected") <(printf '%s\n' "$actual") >&2 || true
  exit 1
fi

if "$gate" --evidence-dir /tmp/midnight-ci-evidence --list >/dev/null 2>&1; then
  echo "local-gate test: accepted an absolute evidence directory" >&2
  exit 1
fi

if "$gate" --evidence-dir artifacts/../outside --list >/dev/null 2>&1; then
  echo "local-gate test: accepted a parent-traversing evidence directory" >&2
  exit 1
fi

if "$gate" --unknown >/dev/null 2>&1; then
  echo "local-gate test: accepted an unknown option" >&2
  exit 1
fi

echo "local-gate test: PASS"
