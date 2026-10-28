#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
# Linux-only disposable BuildKit/registry plus a real Traefik HTTP journey.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
for required in docker go jq python3 curl; do
  command -v "$required" >/dev/null || exit 2
done
docker info >/dev/null
docker pull testcontainers/ryuk:0.14.0
docker pull postgres:16-alpine
mkdir -p artifacts/ci-live-runtime
scratch=$(mktemp -d)
suffix="$$-$RANDOM"
builder="midnight-ci-builder-$suffix"
registry="midnight-ci-registry-$suffix"
traefik="midnight-ci-traefik-$suffix"
cleanup() {
  docker logs "$builder" > artifacts/ci-live-runtime/buildkit.log 2>&1 || true
  docker logs "$registry" > artifacts/ci-live-runtime/registry.log 2>&1 || true
  docker rm -f "$builder" "$registry" "$traefik" >/dev/null 2>&1 || true
  rm -rf -- "$scratch"
}
trap cleanup EXIT INT TERM

docker run -d --name "$registry" -p 127.0.0.1::5000 registry:3.0.0
registry_port=$(docker port "$registry" 5000/tcp)
export MIDNIGHT_BUILDKIT_REGISTRY="$registry_port"
registry_ready=false
for _ in $(seq 1 60); do
  if curl -fsS "http://$registry_port/v2/" >/dev/null 2>&1; then
    registry_ready=true
    break
  fi
  sleep 1
done
"$registry_ready" || { echo 'live runtime: registry did not become ready' >&2; exit 1; }
builder_port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
export BUILDKIT_HOST="tcp://127.0.0.1:$builder_port"
docker run -d --name "$builder" --privileged --network host moby/buildkit:v0.28.1 \
  --addr "$BUILDKIT_HOST"
docker cp "$builder:/usr/bin/buildctl" "$scratch/buildctl"
docker create --name "$traefik" traefik:v3.1.5 >/dev/null
docker cp "$traefik:/usr/local/bin/traefik" "$scratch/traefik"
chmod +x "$scratch/buildctl" "$scratch/traefik"
export MIDNIGHT_BUILDCTL="$scratch/buildctl" MIDNIGHT_TRAEFIK_BIN="$scratch/traefik"

ready=false
for _ in $(seq 1 60); do
  if "$MIDNIGHT_BUILDCTL" debug workers >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
"$ready" || { echo 'live runtime: BuildKit did not become ready' >&2; exit 1; }

GOMAXPROCS="${GOMAXPROCS:-4}" go test -p 1 -exec "$PWD/scripts/ci/integration-test-exec.sh" \
  -tags=integration -count=1 -timeout=15m -json \
  -run '^(TestBuildctlIntegrationProducesDeterministicDigest|TestTraefikDynamicPublicationServesYAML|TestTenantAgentSourceChatBuildKitRuntimeHTTP)$' \
  ./internal/build ./internal/router ./cmd/midnight-buildkit | tee artifacts/ci-live-runtime/results.jsonl
passed=$(jq -r 'select(.Action == "pass" and .Test != null) | .Package + " " + .Test' artifacts/ci-live-runtime/results.jsonl)
for required_test in \
  'github.com/midnight/midnight/internal/build TestBuildctlIntegrationProducesDeterministicDigest' \
  'github.com/midnight/midnight/internal/router TestTraefikDynamicPublicationServesYAML/LiveHTTP' \
  'github.com/midnight/midnight/cmd/midnight-buildkit TestTenantAgentSourceChatBuildKitRuntimeHTTP'; do
  grep -Fxq -- "$required_test" <<<"$passed" || {
    echo "live runtime: required test did not execute successfully: $required_test" >&2
    exit 1
  }
done
echo 'live runtime: PASS (real build/registry push, routed HTTP replacement, tenant chat source build to Docker HTTP)'
