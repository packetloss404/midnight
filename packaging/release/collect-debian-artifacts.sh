#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
#
# Move one complete Debian binary artifact set into the release directory.
# The dbgsym package is required because dpkg-buildpackage records it in both
# the .changes and .buildinfo manifests.

set -euo pipefail

if [[ "$#" -ne 4 ]]; then
  echo "usage: $0 SOURCE_DIR DESTINATION_DIR DEBIAN_VERSION ARCHITECTURE" >&2
  exit 64
fi

source_dir="$1"
destination_dir="$2"
debian_version="$3"
architecture="$4"

case "$debian_version:$architecture" in
  *[!A-Za-z0-9.+:~_-]*)
    echo "collect Debian artifacts: unsafe version or architecture" >&2
    exit 65
    ;;
esac

deb="$source_dir/midnight_${debian_version}_${architecture}.deb"
dbgsym_stem="$source_dir/midnight-dbgsym_${debian_version}_${architecture}"
dbgsym_candidates=("$dbgsym_stem.ddeb" "$dbgsym_stem.deb")
dbgsym=""
for candidate in "${dbgsym_candidates[@]}"; do
  if [[ -e "$candidate" ]]; then
    [[ -z "$dbgsym" ]] || {
      echo "collect Debian artifacts: found both .ddeb and .deb dbgsym packages" >&2
      exit 65
    }
    dbgsym="$candidate"
  fi
done
[[ -n "$dbgsym" ]] || {
  echo "collect Debian artifacts: required dbgsym package is missing: $dbgsym_stem.{ddeb,deb}" >&2
  exit 66
}
changes="$source_dir/midnight_${debian_version}_${architecture}.changes"
buildinfo="$source_dir/midnight_${debian_version}_${architecture}.buildinfo"
artifacts=("$deb" "$dbgsym" "$changes" "$buildinfo")

for artifact in "${artifacts[@]}"; do
  [[ -s "$artifact" ]] || {
    echo "collect Debian artifacts: required artifact is missing or empty: $artifact" >&2
    exit 66
  }
done

for manifest in "$changes" "$buildinfo"; do
  for payload in "$deb" "$dbgsym"; do
    grep -Fq " $(basename "$payload")" "$manifest" || {
      echo "collect Debian artifacts: $(basename "$manifest") does not reference $(basename "$payload")" >&2
      exit 65
    }
  done
done

mkdir -p "$destination_dir"
for artifact in "${artifacts[@]}"; do
  destination="$destination_dir/$(basename "$artifact")"
  [[ ! -e "$destination" ]] || {
    echo "collect Debian artifacts: refusing to overwrite existing artifact: $destination" >&2
    exit 73
  }
done

mv -- "${artifacts[@]}" "$destination_dir/"

printf 'collect Debian artifacts: retained package, dbgsym, changes, and buildinfo for %s/%s\n' \
  "$debian_version" "$architecture"
