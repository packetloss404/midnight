#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
metadata_script="$root_dir/packaging/release/debian-build-metadata.sh"
collector_script="$root_dir/packaging/release/collect-debian-artifacts.sh"
verifier_script="$root_dir/packaging/release/verify-debian-artifacts.sh"
lifecycle_verifier="$root_dir/packaging/release/verify-debian-systemd-lifecycle.sh"
alpha2_bridge="$root_dir/deploy/debian/upgrade-alpha2-bridge"

test_root="$(mktemp -d "${TMPDIR:-/tmp}/midnight-debian-provenance.XXXXXX")"
cleanup() {
  rm -rf -- "$test_root"
}
trap cleanup EXIT INT TERM

fail() {
  echo "Debian package provenance test: $*" >&2
  exit 1
}

first_line="$(LC_ALL=C sed -n '1p' "$root_dir/deploy/debian/changelog")"
[[ "$first_line" == 'midnight (0.1.0~alpha-6) jammy bookworm; urgency=medium' ]] ||
  fail "repository changelog does not declare 0.1.0~alpha-6"

# Docker 29 publishes the supported client and API as separate Moby modules.
# Keep their selected versions and the patched archive security floor explicit
# so an unrelated tidy cannot reintroduce the retired root module or
# GO-2026-6253.
for unsupported_root_module in github.com/docker/docker github.com/moby/moby/v2; do
  if go -C "$root_dir" list -m "$unsupported_root_module" >/dev/null 2>&1; then
    fail "dependency contract selects unsupported root module $unsupported_root_module"
  fi
done
api_version="$(go -C "$root_dir" list -m -f '{{.Version}}' github.com/moby/moby/api)"
client_version="$(go -C "$root_dir" list -m -f '{{.Version}}' github.com/moby/moby/client)"
archive_version="$(go -C "$root_dir" list -m -f '{{.Version}}' github.com/moby/go-archive)"
[[ "$api_version" == 'v1.55.0' ]] ||
  fail "dependency contract expected moby/moby/api v1.55.0, got $api_version"
[[ "$client_version" == 'v0.5.1' ]] ||
  fail "dependency contract expected moby/moby/client v0.5.1, got $client_version"
[[ "$archive_version" == 'v0.3.3' ]] ||
  fail "dependency contract expected patched moby/go-archive v0.3.3, got $archive_version"

# The Make expression must be matched literally.
# shellcheck disable=SC2016
grep -Fq 'MIDNIGHT_COMMIT ?= $(shell git rev-parse HEAD' "$root_dir/deploy/debian/rules" ||
  fail "Debian rules do not default to the full git commit"
# The Make expression must be matched literally.
# shellcheck disable=SC2016
grep -Fq 'MIDNIGHT_DATE ?= $(shell git show -s --format=%cI HEAD' "$root_dir/deploy/debian/rules" ||
  fail "Debian rules do not default to the git commit date"
grep -Fq 'MIDNIGHT_GO_BUILD_FLAGS := -trimpath -buildvcs=true' "$root_dir/deploy/debian/rules" ||
  fail "Debian rules do not require clean VCS metadata in every Go binary"
grep -Fq 'dh_installsystemd --no-enable --no-start --no-stop-on-upgrade' "$root_dir/deploy/debian/rules" ||
  fail "Debian rules allow debhelper to duplicate the custom upgrade lifecycle"
grep -Fq 'build/midnight-github-status-worker' "$root_dir/deploy/debian/rules" ||
  fail "Debian rules do not build and install midnight-github-status-worker"
grep -Fq 'deploy/systemd/midnight-github-status-worker.service' "$root_dir/deploy/debian/rules" ||
  fail "Debian rules do not install the GitHub status worker unit"
grep -Fq 'packaging/release/verify-debian-artifacts.sh' "$root_dir/Makefile" ||
  fail "package targets do not invoke extracted-artifact verification"
[[ -x "$verifier_script" ]] ||
  fail "Debian artifact verifier is not executable"
[[ -x "$lifecycle_verifier" ]] ||
  fail "Debian systemd lifecycle verifier is not executable"
[[ -x "$alpha2_bridge" ]] ||
  fail "alpha-2 exact-active upgrade bridge is not executable"
# The bridge expression must be matched literally.
# shellcheck disable=SC2016
grep -Fq 'exec dpkg --install -- "$package"' "$alpha2_bridge" ||
  fail "alpha-2 bridge does not exec dpkg immediately after the exact-active snapshot"

systemd_fixture_root="$test_root/systemd-lifecycle"
mkdir -p "$systemd_fixture_root"
fixture_postinst="$systemd_fixture_root/postinst"
fixture_prerm="$systemd_fixture_root/prerm"

write_dont_enable_block() {
  local unit="$1"
  # Maintainer-script positional parameters must remain literal in the fixture.
  # shellcheck disable=SC2016
  printf '%s\n' \
    '# Automatically added by dh_installsystemd/13.11.4' \
    'if [ "$1" = "configure" ] || [ "$1" = "abort-upgrade" ] || [ "$1" = "abort-deconfigure" ] || [ "$1" = "abort-remove" ] ; then' \
    $'\t'"if deb-systemd-helper debian-installed '$unit'; then" \
    $'\t\t'"deb-systemd-helper unmask '$unit' >/dev/null || true" \
    $'\t\t'"if deb-systemd-helper --quiet was-enabled '$unit'; then" \
    $'\t\t\t'"deb-systemd-helper enable '$unit' >/dev/null || true" \
    $'\t\tfi' \
    $'\tfi' \
    $'\t'"deb-systemd-helper update-state '$unit' >/dev/null || true" \
    'fi' \
    '# End automatically added section'
}

{
  # Maintainer-script positional parameters must remain literal in the fixture.
  # shellcheck disable=SC2016
  printf '%s\n' \
    '#!/bin/sh' \
    'set -e' \
    'UPGRADE_ACTIVE_UNITS="${MIDNIGHT_TEST_ACTIVE_UNITS:-/nonexistent}"' \
    'if [ "$1" = configure ] && [ -n "${2:-}" ] && [ -f "$UPGRADE_ACTIVE_UNITS" ]; then' \
    $'\tset --' \
    $'\twhile IFS= read -r unit; do' \
    $'\t\tset -- "$@" "$unit"' \
    $'\tdone <"$UPGRADE_ACTIVE_UNITS"' \
    $'\tif [ "$#" -gt 0 ]; then' \
    $'\t\tif ! command -v systemctl >/dev/null || ! systemctl start "$@"; then' \
    $'\t\t\texit 1' \
    $'\t\tfi' \
    $'\tfi' \
    'fi'
  for unit in \
    midnight-agent.service \
    midnight-buildkit-worker.service \
    midnight-buildkit.service \
    midnight-github-status-worker.service \
    midnight-registry.service \
    midnight-server.service \
    midnight-worker.service; do
    write_dont_enable_block "$unit"
  done
  printf '%s\n' 'exit 0'
} >"$fixture_postinst"
chmod 0755 "$fixture_postinst"

printf '%s\n' \
  '#!/bin/sh' \
  'set -e' \
  'UPGRADE_ACTIVE_UNITS=/run/midnight-package-upgrade/active-units' \
  'exit 0' >"$fixture_prerm"
chmod 0755 "$fixture_prerm"

"$lifecycle_verifier" "$fixture_postinst" "$fixture_prerm" >/dev/null ||
  fail "systemd lifecycle verifier rejected the canonical guarded package fixture"

fake_bin="$systemd_fixture_root/bin"
lifecycle_log="$systemd_fixture_root/lifecycle.log"
was_enabled="$systemd_fixture_root/was-enabled"
active_snapshot="$systemd_fixture_root/active-units"
mkdir -p "$fake_bin"
# Fake-helper parameters are evaluated by the generated helper, not this test.
# shellcheck disable=SC2016
printf '%s\n' \
  '#!/bin/sh' \
  'if [ "${1:-}" = --quiet ]; then shift; fi' \
  'action="${1:-}"' \
  'shift || true' \
  'unit="${1:-}"' \
  'case "$action" in' \
  $'debian-installed)\t[ "${FAKE_DEBIAN_INSTALLED:-0}" -eq 1 ] ;;' \
  $'was-enabled)\tgrep -Fqx -- "$unit" "$FAKE_WAS_ENABLED" ;;' \
  "enable) printf 'enable %s\\n' \"\$unit\" >>\"\$FAKE_LIFECYCLE_LOG\" ;;" \
  $'unmask|update-state)\texit 0 ;;' \
  $'*)\texit 64 ;;' \
  'esac' >"$fake_bin/deb-systemd-helper"
# Fake systemctl parameters are evaluated when the fixture invokes the stub.
# shellcheck disable=SC2016
printf '%s\n' \
  '#!/bin/sh' \
  "printf 'systemctl %s\\n' \"\$*\" >>\"\$FAKE_LIFECYCLE_LOG\"" \
  'exit 0' >"$fake_bin/systemctl"
chmod 0755 "$fake_bin/deb-systemd-helper" "$fake_bin/systemctl"

: >"$was_enabled"
: >"$lifecycle_log"
PATH="$fake_bin:/usr/bin:/bin" \
FAKE_DEBIAN_INSTALLED=0 \
FAKE_WAS_ENABLED="$was_enabled" \
FAKE_LIFECYCLE_LOG="$lifecycle_log" \
MIDNIGHT_TEST_ACTIVE_UNITS="$active_snapshot" \
  sh "$fixture_postinst" configure
[[ ! -s "$lifecycle_log" ]] ||
  fail "fresh package configuration enabled or started a service"

printf '%s\n' midnight-agent.service midnight-server.service >"$was_enabled"
: >"$lifecycle_log"
PATH="$fake_bin:/usr/bin:/bin" \
FAKE_DEBIAN_INSTALLED=1 \
FAKE_WAS_ENABLED="$was_enabled" \
FAKE_LIFECYCLE_LOG="$lifecycle_log" \
MIDNIGHT_TEST_ACTIVE_UNITS="$active_snapshot" \
  sh "$fixture_postinst" configure 0.1.0~alpha-2
[[ "$(cat "$lifecycle_log")" == $'enable midnight-agent.service\nenable midnight-server.service' ]] ||
  fail "upgrade did not preserve only the previously enabled service set"

: >"$was_enabled"
: >"$lifecycle_log"
PATH="$fake_bin:/usr/bin:/bin" \
FAKE_DEBIAN_INSTALLED=1 \
FAKE_WAS_ENABLED="$was_enabled" \
FAKE_LIFECYCLE_LOG="$lifecycle_log" \
MIDNIGHT_TEST_ACTIVE_UNITS="$active_snapshot" \
  sh "$fixture_postinst" configure 0.1.0~alpha-2
[[ ! -s "$lifecycle_log" ]] ||
  fail "upgrade enabled a service whose prior state was disabled"

printf '%s\n' midnight-worker.service >"$active_snapshot"
: >"$lifecycle_log"
PATH="$fake_bin:/usr/bin:/bin" \
FAKE_DEBIAN_INSTALLED=1 \
FAKE_WAS_ENABLED="$was_enabled" \
FAKE_LIFECYCLE_LOG="$lifecycle_log" \
MIDNIGHT_TEST_ACTIVE_UNITS="$active_snapshot" \
  sh "$fixture_postinst" configure 0.1.0~alpha-2
[[ "$(cat "$lifecycle_log")" == 'systemctl start midnight-worker.service' ]] ||
  fail "upgrade restart did not use only the exact-active snapshot"

expect_lifecycle_rejection() {
  local candidate="$1"
  if "$lifecycle_verifier" "$candidate" "$fixture_prerm" >/dev/null 2>&1; then
    fail "systemd lifecycle verifier accepted unsafe fixture $(basename "$candidate")"
  fi
}

unguarded_fixture="$systemd_fixture_root/postinst-unguarded"
awk '
  !changed && /if deb-systemd-helper debian-installed/ {
    sub(/if deb-systemd-helper debian-installed [^;]+; then/, "if true; then")
    changed = 1
  }
  { print }
' "$fixture_postinst" >"$unguarded_fixture"
expect_lifecycle_rejection "$unguarded_fixture"

unconditional_fixture="$systemd_fixture_root/postinst-unconditional-enable"
awk '
  /^exit 0$/ && !added {
    print "deb-systemd-helper enable '\''midnight-server.service'\'' >/dev/null || true"
    added = 1
  }
  { print }
' "$fixture_postinst" >"$unconditional_fixture"
expect_lifecycle_rejection "$unconditional_fixture"

reenable_fixture="$systemd_fixture_root/postinst-reenable"
awk '
  /^exit 0$/ && !added {
    print "deb-systemd-helper reenable '\''midnight-server.service'\'' >/dev/null || true"
    added = 1
  }
  { print }
' "$fixture_postinst" >"$reenable_fixture"
expect_lifecycle_rejection "$reenable_fixture"

invoke_fixture="$systemd_fixture_root/postinst-dynamic-invoke"
awk '
  /^exit 0$/ && !added {
    print "deb-systemd-invoke \"$_dh_action\" '\''midnight-server.service'\''"
    added = 1
  }
  { print }
' "$fixture_postinst" >"$invoke_fixture"
expect_lifecycle_rejection "$invoke_fixture"

restart_fixture="$systemd_fixture_root/postinst-direct-restart"
sed 's/systemctl start "$@"/systemctl restart "$@"/' \
  "$fixture_postinst" >"$restart_fixture"
expect_lifecycle_rejection "$restart_fixture"

enabled_fallback_prerm="$systemd_fixture_root/prerm-enabled-fallback"
awk '
  /^exit 0$/ && !added {
    print "systemctl is-enabled --quiet '\''midnight-server.service'\''"
    added = 1
  }
  { print }
' "$fixture_prerm" >"$enabled_fallback_prerm"
if "$lifecycle_verifier" "$fixture_postinst" "$enabled_fallback_prerm" >/dev/null 2>&1; then
  fail "systemd lifecycle verifier accepted enabled-state restart inference"
fi

generated_prerm="$systemd_fixture_root/prerm-generated-stop"
awk '
  /^exit 0$/ && !added {
    print "# Automatically added by dh_installsystemd/13.11.4"
    print "deb-systemd-invoke stop '\''midnight-server.service'\'' >/dev/null || true"
    print "# End automatically added section"
    added = 1
  }
  { print }
' "$fixture_prerm" >"$generated_prerm"
if "$lifecycle_verifier" "$fixture_postinst" "$generated_prerm" >/dev/null 2>&1; then
  fail "systemd lifecycle verifier accepted a generated prerm service path"
fi

fixture_repo="$test_root/repository"
mkdir -p "$fixture_repo/deploy/debian" "$fixture_repo/web-next"
git -C "$fixture_repo" init -q
git -C "$fixture_repo" config user.name "Midnight Package Test"
git -C "$fixture_repo" config user.email "package-test@midnight.invalid"
printf '/build/\n/debian/\n' >"$fixture_repo/.gitignore"
printf 'module midnight.invalid/package-provenance\n\ngo 1.25\n' >"$fixture_repo/go.mod"
printf 'package main\n\nfunc main() {}\n' >"$fixture_repo/main.go"
printf '%s\n' \
  'midnight (0.1.0~alpha-6) jammy bookworm; urgency=medium' \
  '' \
  '  * Fixture.' \
  '' \
  ' -- Midnight Package Test <package-test@midnight.invalid>  Sat, 29 Aug 2026 16:00:00 +0000' \
  >"$fixture_repo/deploy/debian/changelog"
printf '.env\n.env.*\n!.env.example\n' >"$fixture_repo/web-next/.gitignore"
git -C "$fixture_repo" add .gitignore go.mod main.go deploy/debian/changelog web-next/.gitignore
GIT_AUTHOR_DATE='2026-08-29T16:00:00+00:00' \
GIT_COMMITTER_DATE='2026-08-29T16:00:00+00:00' \
  git -C "$fixture_repo" commit -q -m fixture

if "$metadata_script" "$fixture_repo" >/dev/null 2>&1; then
  fail "metadata helper accepted an attached branch checkout"
fi
git -C "$fixture_repo" checkout --detach -q HEAD

eval "$("$metadata_script" "$fixture_repo")"
[[ "$MIDNIGHT_SOURCE_COMMIT" == "$(git -C "$fixture_repo" rev-parse HEAD)" ]] ||
  fail "metadata helper did not return the full source commit"
[[ "$MIDNIGHT_SOURCE_DATE" == "$(git -C "$fixture_repo" show -s --format=%cI HEAD)" ]] ||
  fail "metadata helper did not return the source commit date"
[[ "$MIDNIGHT_SOURCE_EPOCH" == "$(git -C "$fixture_repo" show -s --format=%ct HEAD)" ]] ||
  fail "metadata helper did not return the source commit epoch"
[[ "$MIDNIGHT_DEBIAN_VERSION" == '0.1.0~alpha-6' ]] ||
  fail "metadata helper returned the wrong Debian version"
[[ "$MIDNIGHT_PRODUCT_VERSION" == '0.1.0-alpha' ]] ||
  fail "Debian revision leaked into the product version"

# package-deb creates only ignored staging/output paths after the clean-source
# check. Prove those paths do not flip the Go VCS stamp before the real build.
mkdir -p "$fixture_repo/debian" "$fixture_repo/build"
printf 'Source: midnight\n' >"$fixture_repo/debian/control"
"$metadata_script" "$fixture_repo" >/dev/null ||
  fail "metadata helper rejected ignored package staging paths"
go -C "$fixture_repo" build -buildvcs=true -o build/provenance-test .
go_metadata="$(go version -m "$fixture_repo/build/provenance-test")"
grep -Fq $'\tbuild\tvcs.revision='"$MIDNIGHT_SOURCE_COMMIT" <<<"$go_metadata" ||
  fail "ignored Debian staging lost the full Go VCS revision"
grep -Fq $'\tbuild\tvcs.modified=false' <<<"$go_metadata" ||
  fail "ignored Debian staging caused Go to report vcs.modified=true"

printf 'untracked input\n' >"$fixture_repo/untracked.txt"
if "$metadata_script" "$fixture_repo" >/dev/null 2>&1; then
  fail "metadata helper accepted an untracked source input"
fi
rm -f "$fixture_repo/untracked.txt"

printf 'VITE_AGENT_MOCK=1\n' >"$fixture_repo/web-next/.env.production"
if "$metadata_script" "$fixture_repo" >/dev/null 2>&1; then
  fail "metadata helper accepted an ignored Vite environment file"
fi
rm -f "$fixture_repo/web-next/.env.production"

if VITE_AGENT_MOCK=1 "$metadata_script" "$fixture_repo" >/dev/null 2>&1; then
  fail "metadata helper accepted an inherited VITE_* variable"
fi

printf '\n' >>"$fixture_repo/deploy/debian/changelog"
if "$metadata_script" "$fixture_repo" >/dev/null 2>&1; then
  fail "metadata helper accepted a dirty tracked source input"
fi

artifact_source="$test_root/artifact-source"
artifact_destination="$test_root/artifact-destination"
mkdir -p "$artifact_source"
deb_name='midnight_0.1.0~alpha-6_amd64.deb'
dbgsym_name='midnight-dbgsym_0.1.0~alpha-6_amd64.ddeb'
changes_name='midnight_0.1.0~alpha-6_amd64.changes'
buildinfo_name='midnight_0.1.0~alpha-6_amd64.buildinfo'
printf 'package\n' >"$artifact_source/$deb_name"
printf 'debug symbols\n' >"$artifact_source/$dbgsym_name"
for manifest_name in "$changes_name" "$buildinfo_name"; do
  printf 'Checksums-Sha256:\n hash 1 %s\n hash 1 %s\n' \
    "$deb_name" "$dbgsym_name" >"$artifact_source/$manifest_name"
done

"$collector_script" "$artifact_source" "$artifact_destination" \
  '0.1.0~alpha-6' amd64 >/dev/null
for artifact_name in "$deb_name" "$dbgsym_name" "$changes_name" "$buildinfo_name"; do
  [[ -s "$artifact_destination/$artifact_name" ]] ||
    fail "collector omitted $artifact_name"
done

# Debian emits automatic debug packages as .deb while Ubuntu commonly uses
# .ddeb. The retained artifact must follow the build host's manifest rather
# than assuming either distro convention.
debian_artifact_source="$test_root/debian-artifact-source"
debian_artifact_destination="$test_root/debian-artifact-destination"
debian_dbgsym_name='midnight-dbgsym_0.1.0~alpha-6_amd64.deb'
mkdir -p "$debian_artifact_source"
printf 'package\n' >"$debian_artifact_source/$deb_name"
printf 'debug symbols\n' >"$debian_artifact_source/$debian_dbgsym_name"
for manifest_name in "$changes_name" "$buildinfo_name"; do
  printf 'Checksums-Sha256:\n hash 1 %s\n hash 1 %s\n' \
    "$deb_name" "$debian_dbgsym_name" >"$debian_artifact_source/$manifest_name"
done
"$collector_script" "$debian_artifact_source" "$debian_artifact_destination" \
  '0.1.0~alpha-6' amd64 >/dev/null
[[ -s "$debian_artifact_destination/$debian_dbgsym_name" ]] ||
  fail "collector omitted Debian .deb dbgsym package"

ambiguous_source="$test_root/ambiguous-dbgsym-source"
mkdir -p "$ambiguous_source"
printf 'package\n' >"$ambiguous_source/$deb_name"
printf 'debug symbols\n' >"$ambiguous_source/$dbgsym_name"
printf 'debug symbols\n' >"$ambiguous_source/$debian_dbgsym_name"
for manifest_name in "$changes_name" "$buildinfo_name"; do
  printf 'Checksums-Sha256:\n hash 1 %s\n hash 1 %s\n hash 1 %s\n' \
    "$deb_name" "$dbgsym_name" "$debian_dbgsym_name" >"$ambiguous_source/$manifest_name"
done
if "$collector_script" "$ambiguous_source" "$test_root/ambiguous-destination" \
  '0.1.0~alpha-6' amd64 >/dev/null 2>&1; then
  fail "collector accepted ambiguous .ddeb and .deb dbgsym packages"
fi

missing_source="$test_root/missing-dbgsym-source"
mkdir -p "$missing_source"
printf 'package\n' >"$missing_source/$deb_name"
for manifest_name in "$changes_name" "$buildinfo_name"; do
  printf 'Checksums-Sha256:\n hash 1 %s\n hash 1 %s\n' \
    "$deb_name" "$dbgsym_name" >"$missing_source/$manifest_name"
done
if "$collector_script" "$missing_source" "$test_root/missing-destination" \
  '0.1.0~alpha-6' amd64 >/dev/null 2>&1; then
  fail "collector accepted an incomplete artifact set"
fi

echo "Debian package provenance test: PASS"
