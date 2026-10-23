#!/usr/bin/env bash
# Generate the shell completion files embedded in Midnight CLI archives.
set -euo pipefail
unset CDPATH

script_dir=$(cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(cd -- "${script_dir}/../.." && pwd)
output_dir=${1:-"${repo_dir}/dist/completions"}

case "$output_dir" in
  /*) ;;
  *) output_dir="${repo_dir}/${output_dir}" ;;
esac

scratch_dir=$(mktemp -d "${TMPDIR:-/tmp}/midnight-cli-completions.XXXXXX")
cleanup() {
  rm -rf -- "$scratch_dir"
}
trap cleanup EXIT INT TERM

cd "$repo_dir"
go build -trimpath -o "${scratch_dir}/midnight" ./cmd/midnight

mkdir -p -- "$output_dir"
"${scratch_dir}/midnight" completion bash > "${scratch_dir}/midnight.bash"
"${scratch_dir}/midnight" completion zsh > "${scratch_dir}/_midnight"
"${scratch_dir}/midnight" completion fish > "${scratch_dir}/midnight.fish"
"${scratch_dir}/midnight" completion powershell > "${scratch_dir}/midnight.ps1"

install -m 0644 "${scratch_dir}/midnight.bash" "${output_dir}/midnight.bash"
install -m 0644 "${scratch_dir}/_midnight" "${output_dir}/_midnight"
install -m 0644 "${scratch_dir}/midnight.fish" "${output_dir}/midnight.fish"
install -m 0644 "${scratch_dir}/midnight.ps1" "${output_dir}/midnight.ps1"

for completion in midnight.bash _midnight midnight.fish midnight.ps1; do
  if [ ! -s "${output_dir}/${completion}" ]; then
    echo "completion generation produced an empty ${completion}" >&2
    exit 1
  fi
done
