#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)
cd "$repo_root"

generator='github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0'
spec='api/openapi/v1alpha/midnight.yaml'
types_config='api/openapi/v1alpha/oapi-codegen.types.yaml'
client_config='api/openapi/v1alpha/oapi-codegen.client.yaml'

generate() {
  go run "$generator" --config "$types_config" "$spec"
  go run "$generator" --config "$client_config" "$spec"
}

check() {
  temporary_dir=$(mktemp -d "${TMPDIR:-/tmp}/midnightctl-generate.XXXXXX")
  trap 'rm -rf -- "$temporary_dir"' EXIT HUP INT TERM
  mkdir -p "$temporary_dir/pkg/midnightctl"

  (
    cd "$temporary_dir"
    go run "$generator" --config "$repo_root/$types_config" "$repo_root/$spec"
    go run "$generator" --config "$repo_root/$client_config" "$repo_root/$spec"
  )

  status=0
  for output in types.gen.go client.gen.go; do
    temporary_output="$temporary_dir/pkg/midnightctl/$output"
    if ! cmp -s "pkg/midnightctl/$output" "$temporary_output"; then
      echo "generated Midnight client is stale: pkg/midnightctl/$output" >&2
      diff -u "pkg/midnightctl/$output" "$temporary_output" || true
      status=1
    fi
  done
  return "$status"
}

case "${1:-generate}" in
  generate)
    generate
    ;;
  check)
    check
    ;;
  *)
    echo "usage: $0 [generate|check]" >&2
    exit 64
    ;;
esac
