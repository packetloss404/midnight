#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
#
# Verify the package identity and the complete artifact set emitted by
# collect-debian-artifacts.sh. Run this on the Linux build host before an
# artifact is transferred or installed.

set -euo pipefail

if [[ "$#" -ne 8 ]]; then
  echo "usage: $0 ARTIFACT_DIR DEBIAN_VERSION PRODUCT_VERSION COMMIT COMMIT_DATE COMMIT_EPOCH GO_VERSION ARCHITECTURE" >&2
  exit 64
fi

artifact_dir="$1"
debian_version="$2"
product_version="$3"
expected_commit="$4"
expected_date="$5"
expected_epoch="$6"
expected_go_version="$7"
architecture="$8"

for command_name in awk date dpkg-deb go grep mkdir sha256sum; do
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "verify Debian artifacts: required command '$command_name' was not found" >&2
    exit 69
  }
done

lifecycle_verifier="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/verify-debian-systemd-lifecycle.sh"
[[ -x "$lifecycle_verifier" ]] || {
  echo "verify Debian artifacts: systemd lifecycle verifier is missing or not executable" >&2
  exit 69
}

expected_vcs_time="$(date -u --date="@$expected_epoch" +%Y-%m-%dT%H:%M:%SZ)"

deb="$artifact_dir/midnight_${debian_version}_${architecture}.deb"
dbgsym_stem="$artifact_dir/midnight-dbgsym_${debian_version}_${architecture}"
dbgsym_candidates=("$dbgsym_stem.ddeb" "$dbgsym_stem.deb")
dbgsym=""
for candidate in "${dbgsym_candidates[@]}"; do
  if [[ -e "$candidate" ]]; then
    [[ -z "$dbgsym" ]] || {
      echo "verify Debian artifacts: found both .ddeb and .deb dbgsym packages" >&2
      exit 65
    }
    dbgsym="$candidate"
  fi
done
[[ -n "$dbgsym" ]] || {
  echo "verify Debian artifacts: required dbgsym package is missing: $dbgsym_stem.{ddeb,deb}" >&2
  exit 66
}
changes="$artifact_dir/midnight_${debian_version}_${architecture}.changes"
buildinfo="$artifact_dir/midnight_${debian_version}_${architecture}.buildinfo"

for artifact in "$deb" "$dbgsym" "$changes" "$buildinfo"; do
  [[ -s "$artifact" ]] || {
    echo "verify Debian artifacts: required artifact is missing or empty: $artifact" >&2
    exit 66
  }
done

verify_manifest_checksum() {
  local manifest="$1"
  local payload="$2"
  local payload_name recorded actual
  payload_name="$(basename "$payload")"
  recorded="$(awk -v file="$payload_name" '
    $0 == "Checksums-Sha256:" { in_sha256 = 1; next }
    in_sha256 && /^[^ ]/ { exit }
    in_sha256 && $3 == file { print $1; exit }
  ' "$manifest")"
  actual="$(sha256sum "$payload" | awk '{print $1}')"
  [[ -n "$recorded" && "$recorded" == "$actual" ]] || {
    echo "verify Debian artifacts: SHA-256 mismatch for $payload_name in $(basename "$manifest")" >&2
    exit 65
  }
}

for manifest in "$changes" "$buildinfo"; do
  verify_manifest_checksum "$manifest" "$deb"
  verify_manifest_checksum "$manifest" "$dbgsym"
done

[[ "$(dpkg-deb -f "$deb" Package)" == "midnight" ]] || {
  echo "verify Debian artifacts: installable package name is not midnight" >&2
  exit 65
}
[[ "$(dpkg-deb -f "$dbgsym" Package)" == "midnight-dbgsym" ]] || {
  echo "verify Debian artifacts: debug package name is not midnight-dbgsym" >&2
  exit 65
}
for package in "$deb" "$dbgsym"; do
  [[ "$(dpkg-deb -f "$package" Version)" == "$debian_version" ]] || {
    echo "verify Debian artifacts: wrong Debian version in $(basename "$package")" >&2
    exit 65
  }
  [[ "$(dpkg-deb -f "$package" Architecture)" == "$architecture" ]] || {
    echo "verify Debian artifacts: wrong architecture in $(basename "$package")" >&2
    exit 65
  }
done

extract_root="$(mktemp -d "${TMPDIR:-/tmp}/midnight-debian-verify.XXXXXX")"
cleanup() {
  rm -rf -- "$extract_root"
}
trap cleanup EXIT INT TERM
dpkg-deb --extract "$deb" "$extract_root"
control_root="$extract_root/DEBIAN"
mkdir -p "$control_root"
dpkg-deb --control "$deb" "$control_root"

for maintainer_script in prerm postinst; do
  [[ -x "$control_root/$maintainer_script" ]] || {
    echo "verify Debian artifacts: package is missing executable $maintainer_script" >&2
    exit 65
  }
  grep -Fq 'UPGRADE_ACTIVE_UNITS=' "$control_root/$maintainer_script" || {
    echo "verify Debian artifacts: $maintainer_script is missing the exact-active upgrade lifecycle" >&2
    exit 65
  }
done
"$lifecycle_verifier" "$control_root/postinst" "$control_root/prerm" >/dev/null
for doctor_argument in --json --install-readiness --live; do
  grep -Fq -- "$doctor_argument" "$control_root/postinst" || {
    echo "verify Debian artifacts: installed postinst omits doctor argument $doctor_argument" >&2
    exit 65
  }
done

binaries=(
  midnight
  midnight-doctor
  midnight-server
  midnight-worker
  midnight-agent
  midnight-buildkit
  midnight-registry
  midnight-recovery
  midnight-github-status-worker
)
for name in "${binaries[@]}"; do
  binary="$extract_root/usr/bin/$name"
  [[ -x "$binary" ]] || {
    echo "verify Debian artifacts: package is missing executable $name" >&2
    exit 65
  }
  reported_version="$("$binary" version)"
  [[ "$reported_version" == "$product_version" ]] || {
    echo "verify Debian artifacts: $name reports $reported_version, expected $product_version" >&2
    exit 65
  }
  go_metadata="$(go version -m "$binary")"
  [[ "$go_metadata" == *": $expected_go_version"$'\n'* || "$go_metadata" == *": $expected_go_version" ]] || {
    echo "verify Debian artifacts: $name was not built with $expected_go_version" >&2
    exit 65
  }
  grep -Fq $'\tbuild\tvcs.revision='"$expected_commit" <<<"$go_metadata" || {
    echo "verify Debian artifacts: $name does not record the full VCS commit" >&2
    exit 65
  }
  grep -Fq $'\tbuild\tvcs.time='"$expected_vcs_time" <<<"$go_metadata" || {
    echo "verify Debian artifacts: $name does not record the VCS commit time" >&2
    exit 65
  }
  grep -Fq $'\tbuild\tvcs.modified=false' <<<"$go_metadata" || {
    echo "verify Debian artifacts: $name reports a modified source tree" >&2
    exit 65
  }
done

# Exercise the packaged binary through the same scoped live profile invoked by
# postinst. All paths are fixtures inside the extracted package so this proves
# installed wiring without inspecting or mutating the verifier host.
doctor_fixture_root="$extract_root/doctor-package-fixture"
doctor_fixture_env="$doctor_fixture_root/midnight.env"
doctor_fixture_dsn="$doctor_fixture_root/db.dsn"
doctor_fixture_routes="$doctor_fixture_root/traefik/dynamic"
doctor_fixture_report="$doctor_fixture_root/report.json"
mkdir -p "$doctor_fixture_routes"
printf 'MIDNIGHT_STORAGE=postgres\n' >"$doctor_fixture_env"
printf 'postgres://midnight:fixture@127.0.0.1/midnight\n' >"$doctor_fixture_dsn"
set +e
"$extract_root/usr/bin/midnight-doctor" \
  --json \
  --install-readiness \
  --live \
  --package-env-fixture="$doctor_fixture_env" \
  --db-dsn-fixture="$doctor_fixture_dsn" \
  --runtime-binaries="$extract_root/usr/bin/midnight,$extract_root/usr/bin/midnight-buildkit,$extract_root/usr/bin/midnight-registry" \
  --route-config-dir="$doctor_fixture_routes" \
  --systemd-unit-dir="$extract_root/lib/systemd/system" \
  >"$doctor_fixture_report"
doctor_fixture_exit=$?
set -e
[[ "$doctor_fixture_exit" -eq 0 ]] || {
  echo "verify Debian artifacts: packaged install-readiness doctor exited $doctor_fixture_exit" >&2
  cat "$doctor_fixture_report" >&2
  exit 65
}
grep -Fq '"live": true' "$doctor_fixture_report" || {
  echo "verify Debian artifacts: packaged doctor report is not labeled live" >&2
  exit 65
}
grep -Fq '"profile": "install-readiness"' "$doctor_fixture_report" || {
  echo "verify Debian artifacts: packaged doctor report omits the install-readiness profile" >&2
  exit 65
}
[[ "$(grep -c '"id": "OR-CHK-DOC-' "$doctor_fixture_report")" -eq 5 ]] || {
  echo "verify Debian artifacts: packaged doctor did not emit exactly five install-readiness checks" >&2
  exit 65
}
for check_id in OR-CHK-DOC-010 OR-CHK-DOC-013 OR-CHK-DOC-015 OR-CHK-DOC-018 OR-CHK-DOC-019; do
  grep -Fq '"id": "'"$check_id"'"' "$doctor_fixture_report" || {
    echo "verify Debian artifacts: packaged doctor omitted $check_id" >&2
    exit 65
  }
done


server_binary="$extract_root/usr/bin/midnight-server"
grep -aFq "$expected_commit" "$server_binary" || {
  echo "verify Debian artifacts: server compatibility identity is missing the full source commit" >&2
  exit 65
}
grep -aFq "$expected_date" "$server_binary" || {
  echo "verify Debian artifacts: server compatibility identity is missing the source commit date" >&2
  exit 65
}

[[ -s "$extract_root/usr/share/midnight/web-next/index.html" ]] || {
  echo "verify Debian artifacts: production console assets are missing" >&2
  exit 65
}

echo "verify Debian artifacts: PASS ($debian_version, $product_version, $expected_commit)"
