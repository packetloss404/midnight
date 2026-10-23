#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
#
# Disposable live acceptance for the source-build toolchain. This lane uses
# the production build-worker image, a real BuildKit daemon, the pinned
# Railpack gateway frontend, and the pinned Distribution registry. It then
# pulls and runs the emitted image and compares BuildKit's digest with the
# Docker Engine's registry digest.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSIONS_FILE="$ROOT_DIR/versions.yaml"
EVIDENCE_ROOT="${MIDNIGHT_PARITY_EVIDENCE_DIR:-$ROOT_DIR/.midnight/parity-evidence}"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
EVIDENCE_DIR="$EVIDENCE_ROOT/toolchain/$RUN_ID"

version_value() {
  local key="$1"
  sed -n "s/^${key}:[[:space:]]*[\"']\\{0,1\\}\\([^\"']*\\)[\"']\\{0,1\\}[[:space:]]*$/\\1/p" "$VERSIONS_FILE"
}

BUILDKIT_VERSION="$(version_value buildkit)"
RAILPACK_VERSION="$(version_value railpack)"
RAILPACK_FRONTEND="$(version_value railpack_frontend)"
REGISTRY_VERSION="$(version_value distribution)"
TRAEFIK_VERSION="$(version_value traefik)"

for command_name in docker curl go python3 sed; do
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "toolchain acceptance: required command '$command_name' was not found" >&2
    exit 78
  }
done
docker info >/dev/null 2>&1 || {
  echo "toolchain acceptance: Docker daemon is not reachable" >&2
  exit 78
}
for source_dir in \
  "$ROOT_DIR/examples/build-proof/repo" \
  "$ROOT_DIR/examples/build-proof/python" \
  "$ROOT_DIR/examples/build-proof/go" \
  "$ROOT_DIR/examples/build-proof/secret" \
  "$ROOT_DIR/examples/build-proof/dockerfile"; do
  [[ -d "$source_dir" ]] || {
    echo "toolchain acceptance: source directory does not exist: $source_dir" >&2
    exit 78
  }
done
[[ -n "$BUILDKIT_VERSION" && -n "$RAILPACK_VERSION" &&
  -n "$RAILPACK_FRONTEND" && -n "$REGISTRY_VERSION" &&
  -n "$TRAEFIK_VERSION" ]] || {
  echo "toolchain acceptance: versions.yaml is missing a required toolchain pin" >&2
  exit 1
}
[[ "$RAILPACK_FRONTEND" == *@sha256:* ]] || {
  echo "toolchain acceptance: Railpack frontend is not digest-pinned" >&2
  exit 1
}

mkdir -p "$EVIDENCE_DIR"
# The production image runs as uid 65532 and must be able to persist the
# plan/info/metadata proof into the host-owned evidence directory.
chmod 0777 "$EVIDENCE_DIR"

suffix="$$"
network_name="midnight-toolchain-$suffix"
registry_name="midnight-toolchain-registry-$suffix"
buildkit_name="midnight-toolchain-buildkit-$suffix"
worker_image="midnight-build-worker:parity-$suffix"
registry_port="$(
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
)"
active_app=""
built_images=()

cleanup() {
  if [[ -n "$active_app" ]]; then
    docker stop "$active_app" >/dev/null 2>&1 || true
  fi
  docker stop "$buildkit_name" "$registry_name" >/dev/null 2>&1 || true
  docker network rm "$network_name" >/dev/null 2>&1 || true
  if [[ "${#built_images[@]}" -gt 0 ]]; then
    docker image rm "${built_images[@]}" >/dev/null 2>&1 || true
  fi
  docker image rm "$worker_image" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

echo "==> Build production source-build worker"
docker build \
  --file "$ROOT_DIR/deploy/oci/build-worker/Dockerfile" \
  --tag "$worker_image" \
  --build-arg "BUILDKIT_VERSION=$BUILDKIT_VERSION" \
  --build-arg "RAILPACK_VERSION=$RAILPACK_VERSION" \
  "$ROOT_DIR"

echo "==> Start disposable registry and BuildKit"
docker network create "$network_name" >/dev/null
docker run -d --rm \
  --name "$registry_name" \
  --network "$network_name" \
  --network-alias registry \
  -p "127.0.0.1:$registry_port:5000" \
  "registry:${REGISTRY_VERSION#v}" >/dev/null
docker run -d --rm --privileged \
  --name "$buildkit_name" \
  --network "$network_name" \
  --network-alias buildkit \
  "moby/buildkit:${BUILDKIT_VERSION}-rootless" \
  --addr tcp://0.0.0.0:1234 >/dev/null

for attempt in $(seq 1 30); do
  if docker run --rm \
    --network "$network_name" \
    --entrypoint /usr/local/bin/buildctl \
    "$worker_image" \
    --addr tcp://buildkit:1234 debug workers >/dev/null 2>&1; then
    break
  fi
  if [[ "$attempt" == "30" ]]; then
    echo "toolchain acceptance: BuildKit did not become ready" >&2
    exit 1
  fi
  sleep 1
done

build_railpack_sample() {
  local sample="$1"
  local source_dir="$2"
  local expected_provider="$3"
  local expected_root="$4"
  local sample_evidence="$EVIDENCE_DIR/$sample"
  local image_tag="accept-$sample-$suffix"
  local host_image="127.0.0.1:$registry_port/midnight/railpack-$sample:$image_tag"
  local app_port
  app_port="$(
    python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
  )"
  active_app="midnight-toolchain-app-$sample-$suffix"
  mkdir -p "$sample_evidence"
  chmod 0777 "$sample_evidence"

  echo "==> Prepare $sample source with Railpack ${RAILPACK_VERSION}"
  docker run --rm \
    --entrypoint /usr/local/bin/railpack \
    -v "$source_dir:/workspace:ro" \
    -v "$sample_evidence:/evidence" \
    "$worker_image" \
    prepare /workspace \
    --plan-out /evidence/railpack-plan.json \
    --info-out /evidence/railpack-info.json

  python3 - "$sample_evidence/railpack-info.json" "${RAILPACK_VERSION#v}" "$expected_provider" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    info = json.load(handle)
assert info.get("success") is True, info
assert info.get("railpackVersion") == sys.argv[2], info
assert sys.argv[3] in info.get("detectedProviders", []), info
PY

  echo "==> Solve $sample through the pinned gateway and push to the real registry"
  docker run --rm \
    --network "$network_name" \
    --entrypoint /usr/local/bin/buildctl \
    -v "$source_dir:/workspace:ro" \
    -v "$sample_evidence:/evidence" \
    "$worker_image" \
    --addr tcp://buildkit:1234 \
    build \
    --frontend gateway.v0 \
    --local context=/workspace \
    --local dockerfile=/evidence \
    --opt "source=$RAILPACK_FRONTEND" \
    --output "type=image,name=registry:5000/midnight/railpack-$sample:$image_tag,push=true,registry.insecure=true" \
    --metadata-file /evidence/buildctl.metadata.json

  curl -fsS "http://127.0.0.1:$registry_port/v2/midnight/railpack-$sample/tags/list" >/dev/null
  docker pull "$host_image" >/dev/null
  built_images+=("$host_image")

  local metadata_digest
  local engine_digest
  metadata_digest="$(
    python3 - "$sample_evidence/buildctl.metadata.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    metadata = json.load(handle)
print(metadata.get("containerimage.digest", ""))
PY
  )"
  engine_digest="$(
    docker image inspect "$host_image" --format '{{index .RepoDigests 0}}' |
      sed -n 's/.*@//p'
  )"
  [[ "$metadata_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || {
    echo "toolchain acceptance: $sample BuildKit emitted an invalid digest: $metadata_digest" >&2
    exit 1
  }
  [[ "$metadata_digest" == "$engine_digest" ]] || {
    echo "toolchain acceptance: $sample registry digest $engine_digest does not match BuildKit $metadata_digest" >&2
    exit 1
  }

  echo "==> Run the emitted $sample image and probe real application endpoints"
  docker run -d --rm \
    --name "$active_app" \
    -p "127.0.0.1:$app_port:8080" \
    "$host_image" >/dev/null
  local health_body=""
  local root_body=""
  for attempt in $(seq 1 30); do
    if health_body="$(curl -fsS "http://127.0.0.1:$app_port/healthz" 2>/dev/null)"; then
      break
    fi
    if [[ "$attempt" == "30" ]]; then
      docker logs "$active_app" >&2 || true
      echo "toolchain acceptance: emitted $sample application did not become ready" >&2
      exit 1
    fi
    sleep 1
  done
  root_body="$(curl -fsS "http://127.0.0.1:$app_port/")"
  [[ "$health_body" == '{"status":"ok"}' ]] || {
    echo "toolchain acceptance: $sample returned unexpected health response: $health_body" >&2
    exit 1
  }
  [[ "$root_body" == "$expected_root" ]] || {
    echo "toolchain acceptance: $sample returned unexpected root response: $root_body" >&2
    exit 1
  }
  docker stop "$active_app" >/dev/null
  active_app=""
  chmod 0750 "$sample_evidence"
  echo "  $sample digest=$metadata_digest"
}

build_dockerfile_sample() {
  local source_dir="$ROOT_DIR/examples/build-proof/dockerfile"
  local sample_evidence="$EVIDENCE_DIR/dockerfile"
  local image_tag="accept-dockerfile-$suffix"
  local host_image="127.0.0.1:$registry_port/midnight/dockerfile:$image_tag"
  local app_port
  app_port="$(
    python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
  )"
  active_app="midnight-toolchain-app-dockerfile-$suffix"
  mkdir -p "$sample_evidence"
  chmod 0777 "$sample_evidence"

  echo "==> Solve explicit Dockerfile with the pinned BuildKit daemon"
  docker run --rm \
    --network "$network_name" \
    --entrypoint /usr/local/bin/buildctl \
    -v "$source_dir:/workspace:ro" \
    -v "$sample_evidence:/evidence" \
    "$worker_image" \
    --addr tcp://buildkit:1234 \
    build \
    --frontend dockerfile.v0 \
    --local context=/workspace \
    --local dockerfile=/workspace \
    --opt filename=Dockerfile \
    --output "type=image,name=registry:5000/midnight/dockerfile:$image_tag,push=true,registry.insecure=true" \
    --metadata-file /evidence/buildctl.metadata.json

  curl -fsS "http://127.0.0.1:$registry_port/v2/midnight/dockerfile/tags/list" >/dev/null
  docker pull "$host_image" >/dev/null
  built_images+=("$host_image")
  local metadata_digest
  local engine_digest
  metadata_digest="$(
    python3 - "$sample_evidence/buildctl.metadata.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    metadata = json.load(handle)
print(metadata.get("containerimage.digest", ""))
PY
  )"
  engine_digest="$(
    docker image inspect "$host_image" --format '{{index .RepoDigests 0}}' |
      sed -n 's/.*@//p'
  )"
  [[ "$metadata_digest" =~ ^sha256:[0-9a-f]{64}$ &&
    "$metadata_digest" == "$engine_digest" ]] || {
    echo "toolchain acceptance: Dockerfile digest mismatch ($metadata_digest vs $engine_digest)" >&2
    exit 1
  }

  echo "==> Run the emitted Dockerfile image and probe real endpoints"
  docker run -d --rm \
    --name "$active_app" \
    -p "127.0.0.1:$app_port:8080" \
    "$host_image" >/dev/null
  local health_body=""
  for attempt in $(seq 1 30); do
    if health_body="$(curl -fsS "http://127.0.0.1:$app_port/healthz" 2>/dev/null)"; then
      break
    fi
    if [[ "$attempt" == "30" ]]; then
      docker logs "$active_app" >&2 || true
      echo "toolchain acceptance: emitted Dockerfile application did not become ready" >&2
      exit 1
    fi
    sleep 1
  done
  local root_body
  root_body="$(curl -fsS "http://127.0.0.1:$app_port/")"
  [[ "$health_body" == '{"status":"ok"}' &&
    "$root_body" == "midnight dockerfile build proof" ]] || {
    echo "toolchain acceptance: Dockerfile endpoint response mismatch" >&2
    exit 1
  }
  docker stop "$active_app" >/dev/null
  active_app=""
  chmod 0750 "$sample_evidence"
  echo "  dockerfile digest=$metadata_digest"
}

build_railpack_secret_sample() {
  local source_dir="$ROOT_DIR/examples/build-proof/secret"
  local sample_evidence="$EVIDENCE_DIR/secret"
  local image_tag="accept-secret-$suffix"
  local host_image="127.0.0.1:$registry_port/midnight/railpack-secret:$image_tag"
  local docker_arch
  local go_arch
  local app_port
  local secret_value
  docker_arch="$(docker info --format '{{.Architecture}}')"
  case "$docker_arch" in
    arm64 | aarch64) go_arch=arm64 ;;
    amd64 | x86_64) go_arch=amd64 ;;
    *)
      echo "toolchain acceptance: unsupported Docker architecture for secret proof: $docker_arch" >&2
      exit 78
      ;;
  esac
  app_port="$(
    python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
  )"
  secret_value="midnight-live-secret-${RUN_ID}-never-persist"
  active_app="midnight-toolchain-app-secret-$suffix"
  mkdir -p "$sample_evidence/output"
  chmod 0777 "$sample_evidence" "$sample_evidence/output"

  echo "==> Prepare Railpack plan with secret IDs only"
  docker run --rm \
    --entrypoint /usr/local/bin/railpack \
    -v "$source_dir:/workspace:ro" \
    -v "$sample_evidence:/evidence" \
    "$worker_image" \
    prepare /workspace \
    --plan-out /evidence/railpack-plan.json \
    --info-out /evidence/railpack-info.json \
    --env PRIVATE_TOKEN=__MIDNIGHT_EPHEMERAL_BUILD_SECRET__

  if python3 - "$sample_evidence/railpack-plan.json" "$sample_evidence/railpack-info.json" <<'PY'
import pathlib
import sys

needle = b"midnight-live-secret-"
for raw in sys.argv[1:]:
    if needle in pathlib.Path(raw).read_bytes():
        raise SystemExit(1)
PY
  then
    :
  else
    echo "toolchain acceptance: Railpack prepare persisted a secret value" >&2
    exit 1
  fi

  echo "==> Solve with Midnight's env-backed BuildKit secret boundary"
  (
    cd "$ROOT_DIR"
    CGO_ENABLED=0 GOOS=linux GOARCH="$go_arch" \
      go test -c -o "$sample_evidence/build-secret-live.test" ./internal/build
  )
  chmod 0755 "$sample_evidence/build-secret-live.test"
  export MIDNIGHT_LIVE_BUILD_SECRET="$secret_value"
  docker run --rm \
    --network "$network_name" \
    --entrypoint /evidence/build-secret-live.test \
    -e MIDNIGHT_LIVE_BUILD_SECRET \
    -e MIDNIGHT_LIVE_BUILDKIT_ADDR=tcp://buildkit:1234 \
    -e MIDNIGHT_LIVE_BUILDCTL=/usr/local/bin/buildctl \
    -e MIDNIGHT_LIVE_BUILD_CONTEXT=/workspace \
    -e MIDNIGHT_LIVE_RAILPACK_PLAN=/evidence/railpack-plan.json \
    -e MIDNIGHT_LIVE_BUILD_OUTPUT=/evidence/output \
    -e "MIDNIGHT_LIVE_BUILD_IMAGE=registry:5000/midnight/railpack-secret:$image_tag" \
    -e MIDNIGHT_LIVE_REGISTRY=registry:5000 \
    -v "$source_dir:/workspace:ro" \
    -v "$sample_evidence:/evidence" \
    "$worker_image" \
    -test.run '^TestLiveRailpackBuildKitSecretMount$' \
    -test.v
  unset MIDNIGHT_LIVE_BUILD_SECRET
  rm -f "$sample_evidence/build-secret-live.test"

  if MIDNIGHT_SECRET_SCAN_VALUE="$secret_value" python3 - "$sample_evidence" <<'PY'
import os
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
needle = os.environ["MIDNIGHT_SECRET_SCAN_VALUE"].encode()
for path in root.rglob("*"):
    if path.is_file() and needle in path.read_bytes():
        print(path, file=sys.stderr)
        raise SystemExit(1)
PY
  then
    :
  else
    echo "toolchain acceptance: secret plaintext reached durable evidence" >&2
    exit 1
  fi

  curl -fsS "http://127.0.0.1:$registry_port/v2/midnight/railpack-secret/tags/list" >/dev/null
  docker pull "$host_image" >/dev/null
  built_images+=("$host_image")
  docker run -d --rm \
    --name "$active_app" \
    -p "127.0.0.1:$app_port:8080" \
    "$host_image" >/dev/null
  local root_body=""
  for attempt in $(seq 1 30); do
    if root_body="$(curl -fsS "http://127.0.0.1:$app_port/" 2>/dev/null)"; then
      break
    fi
    if [[ "$attempt" == "30" ]]; then
      docker logs "$active_app" >&2 || true
      echo "toolchain acceptance: emitted secret-proof application did not become ready" >&2
      exit 1
    fi
    sleep 1
  done
  [[ "$root_body" == "build secret was available without being persisted" ]] || {
    echo "toolchain acceptance: secret-proof response mismatch: $root_body" >&2
    exit 1
  }
  docker stop "$active_app" >/dev/null
  active_app=""
  chmod 0750 "$sample_evidence" "$sample_evidence/output"
  echo "  secret mount and leak scan passed"
}

build_railpack_sample \
  node "$ROOT_DIR/examples/build-proof/repo" node "midnight build proof"
build_railpack_sample \
  python "$ROOT_DIR/examples/build-proof/python" python "midnight python build proof"
build_railpack_sample \
  go "$ROOT_DIR/examples/build-proof/go" golang "midnight go build proof"
build_railpack_secret_sample
build_dockerfile_sample

chmod 0750 "$EVIDENCE_DIR"
echo "toolchain acceptance: PASS"
echo "  railpack=$RAILPACK_VERSION buildkit=$BUILDKIT_VERSION registry=$REGISTRY_VERSION traefik=$TRAEFIK_VERSION"
echo "  samples=node,python,go,railpack-secret,dockerfile"
echo "  evidence=$EVIDENCE_DIR"
