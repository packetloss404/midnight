#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
set -euo pipefail
wrapper="$(git rev-parse --show-toplevel)/scripts/ci/integration-test-exec.sh"
export TESTCONTAINERS_SESSION_ID=inherited-expired-session
first=$("$wrapper" bash -c 'printf "%s\n" "$TESTCONTAINERS_SESSION_ID" "$1" "$2"' -- 'argument with spaces' '-test.run=TestReal')
second=$("$wrapper" bash -c 'printf "%s\n" "$TESTCONTAINERS_SESSION_ID"')
first_session=${first%%$'\n'*}
[[ "$first_session" =~ ^[a-f0-9]{32}$ && "$second" =~ ^[a-f0-9]{32}$ ]]
[[ "$first_session" != "$second" && "$second" != "$TESTCONTAINERS_SESSION_ID" ]]
[[ "$first" == "$first_session"$'\nargument with spaces\n-test.run=TestReal' ]]
if "$wrapper" bash -c 'exit 17'; then
  echo 'integration wrapper swallowed child failure' >&2
  exit 1
else
  [[ "$?" == 17 ]]
fi
echo 'integration executable isolation: PASS'
