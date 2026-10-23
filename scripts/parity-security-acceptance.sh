#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
#
# Live single-host network-boundary acceptance. Contract tests cover routing,
# auth, uploads, redaction, and idempotency; this script adds a real Docker
# proof that project/environment aliases are reachable only from their
# intended network and that no candidate silently joins the default bridge.

set -euo pipefail

BUSYBOX_IMAGE="${MIDNIGHT_PARITY_BUSYBOX_IMAGE:-busybox:1.37.0@sha256:9532d8c39891ca2ecde4d30d7710e01fb739c87a8b9299685c63704296b16028}"
suffix="$$"
network_a="midnight-security-project-a-production-$suffix"
network_b="midnight-security-project-b-production-$suffix"
app_a="midnight-security-app-a-$suffix"
app_b="midnight-security-app-b-$suffix"

command -v docker >/dev/null 2>&1 || {
  echo "security acceptance: required command 'docker' was not found" >&2
  exit 78
}
docker info >/dev/null 2>&1 || {
  echo "security acceptance: Docker daemon is not reachable" >&2
  exit 78
}

cleanup() {
  docker stop "$app_a" "$app_b" >/dev/null 2>&1 || true
  docker network rm "$network_a" "$network_b" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker network create \
  --label midnight.managed=true \
  --label midnight.project=project-a \
  --label midnight.environment=production \
  "$network_a" >/dev/null
docker network create \
  --label midnight.managed=true \
  --label midnight.project=project-b \
  --label midnight.environment=production \
  "$network_b" >/dev/null

docker run -d --rm \
  --name "$app_a" \
  --network "$network_a" \
  --network-alias project-a-api \
  "$BUSYBOX_IMAGE" \
  sh -ec 'mkdir -p /www; printf project-a > /www/index.html; exec httpd -f -p 8080 -h /www' >/dev/null
docker run -d --rm \
  --name "$app_b" \
  --network "$network_b" \
  --network-alias project-b-api \
  "$BUSYBOX_IMAGE" \
  sh -ec 'mkdir -p /www; printf project-b > /www/index.html; exec httpd -f -p 8080 -h /www' >/dev/null

for attempt in $(seq 1 20); do
  if same_scope="$(
    docker run --rm --network "$network_a" "$BUSYBOX_IMAGE" \
      wget -qO- http://project-a-api:8080/ 2>/dev/null
  )"; then
    break
  fi
  if [[ "$attempt" == "20" ]]; then
    echo "security acceptance: same-scope service discovery did not become ready" >&2
    exit 1
  fi
  sleep 1
done
[[ "$same_scope" == "project-a" ]] || {
  echo "security acceptance: same-scope response was not project-a" >&2
  exit 1
}

if docker run --rm --network "$network_b" "$BUSYBOX_IMAGE" \
  wget -qO- -T 2 http://project-a-api:8080/ >/dev/null 2>&1; then
  echo "security acceptance: cross-project DNS/network access unexpectedly succeeded" >&2
  exit 1
fi
if docker run --rm --network "$network_a" "$BUSYBOX_IMAGE" \
  wget -qO- -T 2 http://project-b-api:8080/ >/dev/null 2>&1; then
  echo "security acceptance: reverse cross-project access unexpectedly succeeded" >&2
  exit 1
fi

for container_name in "$app_a" "$app_b"; do
  attached_networks="$(
    docker inspect "$container_name" --format '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{"\n"}}{{end}}'
  )"
  if grep -Fxq bridge <<<"$attached_networks"; then
    echo "security acceptance: $container_name silently joined the default bridge" >&2
    exit 1
  fi
  network_count="$(grep -c . <<<"$attached_networks")"
  [[ "$network_count" == "1" ]] || {
    echo "security acceptance: $container_name joined $network_count networks, want exactly one" >&2
    exit 1
  }
done

echo "security acceptance: PASS"
echo "  same-scope DNS/HTTP succeeded"
echo "  cross-project DNS/HTTP failed closed in both directions"
echo "  candidates were absent from the default bridge"
