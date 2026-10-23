#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
#
# Resolve the immutable source and version identity used by Debian builds.
# The output is shell-escaped KEY=VALUE data intended to be evaluated by the
# package Makefile after this script has verified an exact detached source
# checkout and rejected Vite inputs that Git provenance cannot account for.

set -euo pipefail

if [[ "$#" -ne 1 ]]; then
  echo "usage: $0 REPOSITORY_ROOT" >&2
  exit 64
fi

requested_root="$1"
[[ -d "$requested_root" ]] || {
  echo "debian build metadata: repository root is not a directory: $requested_root" >&2
  exit 66
}
repo_root="$(cd -- "$requested_root" && pwd -P)"
changelog="$repo_root/deploy/debian/changelog"
web_root="$repo_root/web-next"

for command_name in find git sed tr; do
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "debian build metadata: required command '$command_name' was not found" >&2
    exit 69
  }
done

[[ -f "$changelog" ]] || {
  echo "debian build metadata: changelog not found: $changelog" >&2
  exit 66
}

git -C "$repo_root" rev-parse --is-inside-work-tree >/dev/null 2>&1 || {
  echo "debian build metadata: repository root is not a git worktree: $repo_root" >&2
  exit 65
}

worktree_root="$(git -C "$repo_root" rev-parse --show-toplevel)"
worktree_root="$(cd -- "$worktree_root" && pwd -P)"
[[ "$worktree_root" == "$repo_root" ]] || {
  echo "debian build metadata: repository root must be the exact git worktree root" >&2
  exit 65
}
if git -C "$repo_root" symbolic-ref -q HEAD >/dev/null 2>&1; then
  echo "debian build metadata: package builds require a detached HEAD at the reviewed source commit" >&2
  exit 73
fi
if [[ -n "$(git -C "$repo_root" status --porcelain=v1 --untracked-files=all --ignore-submodules=none)" ]]; then
  echo "debian build metadata: source worktree is not clean; package from a fresh detached worktree" >&2
  exit 73
fi
[[ -d "$web_root" ]] || {
  echo "debian build metadata: web-next source directory is missing" >&2
  exit 66
}

# Vite reads ignored .env files and inherited VITE_* values during production
# builds. Either input could change shipped JavaScript while Go still reports
# vcs.modified=false, so untracked/ignored files and ambient variables are a
# hard provenance failure. A deliberately tracked .env file remains part of
# the reviewed source commit and is therefore auditable.
while IFS= read -r -d '' vite_env_file; do
  relative_path="${vite_env_file#"$repo_root"/}"
  if ! git -C "$repo_root" ls-files --error-unmatch -- "$relative_path" >/dev/null 2>&1; then
    echo "debian build metadata: untracked or ignored web-next/.env input is forbidden" >&2
    exit 73
  fi
done < <(find "$web_root" -maxdepth 1 \( -type f -o -type l \) \( -name .env -o -name '.env.*' \) -print0)

vite_environment_present=false
while IFS= read -r environment_name; do
  if [[ "$environment_name" == VITE_* ]]; then
    vite_environment_present=true
    break
  fi
done < <(compgen -e)
if [[ "$vite_environment_present" == true ]]; then
  echo "debian build metadata: inherited VITE_* variables are forbidden during package builds" >&2
  exit 73
fi

source_commit="$(git -C "$repo_root" rev-parse HEAD)"
source_date="$(git -C "$repo_root" show -s --format=%cI HEAD)"
source_epoch="$(git -C "$repo_root" show -s --format=%ct HEAD)"

[[ "$source_commit" =~ ^[0-9a-fA-F]{40,64}$ ]] || {
  echo "debian build metadata: source commit is not a full object id: $source_commit" >&2
  exit 65
}
[[ -n "$source_date" && "$source_epoch" =~ ^[0-9]+$ ]] || {
  echo "debian build metadata: source commit date is invalid" >&2
  exit 65
}

first_line="$(LC_ALL=C sed -n '1p' "$changelog")"
case "$first_line" in
  'midnight ('*') '*) ;;
  *)
    echo "debian build metadata: malformed first changelog line: $first_line" >&2
    exit 65
    ;;
esac

debian_version="${first_line#midnight (}"
debian_version="${debian_version%%)*}"
if [[ "$debian_version" != *-* ]]; then
  echo "debian build metadata: version must carry an explicit Debian revision: $debian_version" >&2
  exit 65
fi

upstream_version="${debian_version%-*}"
debian_revision="${debian_version##*-}"
product_version="$(printf '%s' "$upstream_version" | tr '~' '-')"

expected_debian_version="${MIDNIGHT_EXPECTED_DEB_VERSION:-0.1.0~alpha-6}"
expected_product_version="${MIDNIGHT_EXPECTED_PRODUCT_VERSION:-0.1.0-alpha}"
[[ "$debian_version" == "$expected_debian_version" ]] || {
  echo "debian build metadata: package version $debian_version does not match expected $expected_debian_version" >&2
  exit 65
}
[[ "$product_version" == "$expected_product_version" ]] || {
  echo "debian build metadata: product version $product_version does not match expected $expected_product_version" >&2
  exit 65
}
[[ "$debian_revision" == "6" ]] || {
  echo "debian build metadata: expected package revision 6, got $debian_revision" >&2
  exit 65
}

printf 'MIDNIGHT_SOURCE_COMMIT=%q\n' "$source_commit"
printf 'MIDNIGHT_SOURCE_DATE=%q\n' "$source_date"
printf 'MIDNIGHT_SOURCE_EPOCH=%q\n' "$source_epoch"
printf 'MIDNIGHT_DEBIAN_VERSION=%q\n' "$debian_version"
printf 'MIDNIGHT_PRODUCT_VERSION=%q\n' "$product_version"
