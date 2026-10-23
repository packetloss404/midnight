#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
#
# Builds the production console, serves it through midnight-server, and runs
# the security-header browser contract plus the login/MFA browser flow.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

for command_name in curl go npm python3; do
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "security headers browser test: required command '$command_name' was not found" >&2
    exit 78
  }
done
[[ -x "$ROOT_DIR/web-next/node_modules/.bin/playwright" ]] || {
  echo "security headers browser test: Playwright is not installed; run npm --prefix web-next install" >&2
  exit 78
}

free_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

server_port="$(free_port)"
server_base="http://127.0.0.1:$server_port"
server_log="$(mktemp -t midnight-security-headers.XXXXXX)"
server_binary="$(mktemp -t midnight-server.XXXXXX)"
server_pid=""

cleanup() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" >/dev/null 2>&1 || true
    wait "$server_pid" >/dev/null 2>&1 || true
  fi
  rm -f -- "$server_log" "$server_binary"
}
trap cleanup EXIT INT TERM

echo "==> Build production console"
npm --prefix "$ROOT_DIR/web-next" run build

echo "==> Build midnight-server"
(
  cd "$ROOT_DIR"
  go build -o "$server_binary" ./cmd/midnight-server
)

echo "==> Start midnight-server with production console assets"
(
  cd "$ROOT_DIR"
  export MIDNIGHT_WEB_DIST_DIR="$ROOT_DIR/web-next/dist"
  exec "$server_binary" serve \
    --storage=memory \
    --listen "127.0.0.1:$server_port"
) >"$server_log" 2>&1 &
server_pid="$!"

for attempt in $(seq 1 60); do
  if curl -fsS "$server_base/api/v1alpha/compat" >/dev/null 2>&1; then
    break
  fi
  if [[ "$attempt" == "60" ]]; then
    echo "security headers browser test: server did not become ready" >&2
    sed -n '1,200p' "$server_log" >&2
    exit 1
  fi
  sleep 1
done

echo "==> Exercise header, CSP, login, and MFA browser contracts"
(
  cd "$ROOT_DIR/web-next"
  MIDNIGHT_E2E_BASE_URL="$server_base" \
  MIDNIGHT_E2E_EXPECT_SECURITY_HEADERS=1 \
    ./node_modules/.bin/playwright test \
      e2e/security-headers.e2e.ts \
      e2e/login.e2e.ts
)

echo "security headers browser test: PASS"
