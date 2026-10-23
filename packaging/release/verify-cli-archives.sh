#!/usr/bin/env bash
# Verify GoReleaser CLI archives before they are published.
set -euo pipefail
unset CDPATH

usage() {
  echo "usage: $0 --dist DIR --version VERSION" >&2
  exit 64
}

dist_dir=""
expected_version=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dist)
      [ "$#" -ge 2 ] || usage
      dist_dir=$2
      shift 2
      ;;
    --version)
      [ "$#" -ge 2 ] || usage
      expected_version=$2
      shift 2
      ;;
    *) usage ;;
  esac
done

[ -n "$dist_dir" ] && [ -d "$dist_dir" ] && [ -n "$expected_version" ] || usage
case "$expected_version" in
  *-dev*)
    echo "refusing development CLI version ${expected_version}" >&2
    exit 1
    ;;
esac

dist_dir=$(cd -- "$dist_dir" && pwd)
checksum_name="midnight_${expected_version}_SHA256SUMS"
checksum_path="${dist_dir}/${checksum_name}"
[ -s "$checksum_path" ] || {
  echo "missing checksum manifest ${checksum_path}" >&2
  exit 1
}

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$dist_dir" && sha256sum --check "$checksum_name")
elif command -v shasum >/dev/null 2>&1; then
  (cd "$dist_dir" && shasum -a 256 --check "$checksum_name")
else
  echo "sha256sum or shasum is required" >&2
  exit 1
fi

expected_archives=(
  "midnight_${expected_version}_darwin_amd64.tar.gz"
  "midnight_${expected_version}_darwin_arm64.tar.gz"
  "midnight_${expected_version}_linux_amd64.tar.gz"
  "midnight_${expected_version}_linux_arm64.tar.gz"
  "midnight_${expected_version}_windows_amd64.zip"
)
shopt -s nullglob
actual_archives=(
  "${dist_dir}"/midnight_"${expected_version}"_*.tar.gz
  "${dist_dir}"/midnight_"${expected_version}"_*.zip
)
if [ "${#actual_archives[@]}" -ne "${#expected_archives[@]}" ]; then
  echo "found ${#actual_archives[@]} CLI archives; expected ${#expected_archives[@]}" >&2
  exit 1
fi
manifest_entries=$(wc -l < "$checksum_path" | tr -d '[:space:]')
if [ "$manifest_entries" -ne "${#expected_archives[@]}" ]; then
  echo "checksum manifest has ${manifest_entries} entries; expected ${#expected_archives[@]}" >&2
  exit 1
fi

scratch_dir=$(mktemp -d "${TMPDIR:-/tmp}/midnight-cli-archives.XXXXXX")
cleanup() {
  rm -rf -- "$scratch_dir"
}
trap cleanup EXIT INT TERM

completion_files=(midnight.bash _midnight midnight.fish midnight.ps1)
native_os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64|amd64) native_arch=amd64 ;;
  arm64|aarch64) native_arch=arm64 ;;
  *) native_arch=unsupported ;;
esac
native_verified=0

for archive_name in "${expected_archives[@]}"; do
  archive_path="${dist_dir}/${archive_name}"
  [ -s "$archive_path" ] || {
    echo "missing CLI archive ${archive_path}" >&2
    exit 1
  }
  extract_dir="${scratch_dir}/${archive_name}"
  mkdir -p -- "$extract_dir"
  case "$archive_name" in
    *.tar.gz) tar -xzf "$archive_path" -C "$extract_dir" ;;
    *.zip) unzip -q "$archive_path" -d "$extract_dir" ;;
    *) echo "unsupported archive ${archive_name}" >&2; exit 1 ;;
  esac

  binary="${extract_dir}/midnight"
  case "$archive_name" in
    *_windows_*) binary="${extract_dir}/midnight.exe" ;;
  esac
  [ -f "$binary" ] || {
    echo "${archive_name} is missing the midnight binary" >&2
    exit 1
  }
  for required in LICENSE README.md README-midnight.md; do
    [ -s "${extract_dir}/${required}" ] || {
      echo "${archive_name} is missing ${required}" >&2
      exit 1
    }
  done
  for completion in "${completion_files[@]}"; do
    completion_path="${extract_dir}/completions/${completion}"
    [ -s "$completion_path" ] || {
      echo "${archive_name} is missing completions/${completion}" >&2
      exit 1
    }
  done
  grep -q '__start_midnight' "${extract_dir}/completions/midnight.bash"
  grep -q '#compdef midnight' "${extract_dir}/completions/_midnight"
  grep -q 'complete -c midnight' "${extract_dir}/completions/midnight.fish"
  grep -q 'Register-ArgumentCompleter' "${extract_dir}/completions/midnight.ps1"

  archive_os=""
  archive_arch=""
  case "$archive_name" in
    *_darwin_amd64.*) archive_os=darwin; archive_arch=amd64 ;;
    *_darwin_arm64.*) archive_os=darwin; archive_arch=arm64 ;;
    *_linux_amd64.*) archive_os=linux; archive_arch=amd64 ;;
    *_linux_arm64.*) archive_os=linux; archive_arch=arm64 ;;
    *_windows_amd64.*) archive_os=windows; archive_arch=amd64 ;;
  esac
  if [ "$archive_os" = "$native_os" ] && [ "$archive_arch" = "$native_arch" ]; then
    chmod +x "$binary"
    reported_version=$($binary version)
    if [ "$reported_version" != "$expected_version" ]; then
      echo "${archive_name} reports ${reported_version}; expected ${expected_version}" >&2
      exit 1
    fi
    case "$reported_version" in
      *-dev*)
        echo "${archive_name} reports development metadata ${reported_version}" >&2
        exit 1
        ;;
    esac

    generated_dir="${scratch_dir}/generated"
    mkdir -p -- "$generated_dir"
    "$binary" completion bash > "${generated_dir}/midnight.bash"
    "$binary" completion zsh > "${generated_dir}/_midnight"
    "$binary" completion fish > "${generated_dir}/midnight.fish"
    "$binary" completion powershell > "${generated_dir}/midnight.ps1"
    for completion in "${completion_files[@]}"; do
      cmp "${extract_dir}/completions/${completion}" "${generated_dir}/${completion}"
    done
    native_verified=1
  fi
done

if [ "$native_arch" != unsupported ] && { [ "$native_os" = linux ] || [ "$native_os" = darwin ]; }; then
  if [ "$native_verified" -ne 1 ]; then
    echo "no archive matched native platform ${native_os}/${native_arch}" >&2
    exit 1
  fi
fi

echo "verified ${#expected_archives[@]} Midnight CLI archives at version ${expected_version}"
