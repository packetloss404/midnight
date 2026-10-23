#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
#
# Verify the generated systemd portions of an extracted Debian package. The
# custom maintainer scripts own runtime stop/start policy; debhelper may only
# preserve a unit's pre-existing enabled state and stop units on removal.

set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: $0 POSTINST PRERM" >&2
  exit 64
fi

postinst="$1"
prerm="$2"

for script in "$postinst" "$prerm"; do
  [[ -f "$script" ]] || {
    echo "verify Debian systemd lifecycle: missing maintainer script: $script" >&2
    exit 66
  }
done

fail() {
  echo "verify Debian systemd lifecycle: $*" >&2
  exit 65
}

# There must be no alternate activation path in postinst. In particular, a
# verb stored in a variable can evade a grep for `start`, so deb-systemd-invoke
# and invoke-rc.d are forbidden as commands regardless of their arguments.
# Direct systemctl usage is restricted to the custom, literal lifecycle lines.
if ! activation_error="$({
  awk '
    function trim(value) {
      sub(/^[[:space:]]+/, "", value)
      sub(/[[:space:]]+$/, "", value)
      return value
    }
    function reject(message) {
      print message
      bad = 1
      exit 1
    }
    {
      line = trim($0)
      if (line == "" || line ~ /^#/) {
        next
      }
      if (line ~ /(^|[;&|()[:space:]])deb-systemd-invoke([;&|()[:space:]]|$)/) {
        reject("postinst contains deb-systemd-invoke")
      }
      if (line ~ /(^|[;&|()[:space:]])invoke-rc[.]d([;&|()[:space:]]|$)/) {
        reject("postinst contains invoke-rc.d")
      }
      if (line !~ /(^|[;&|()[:space:]])systemctl([;&|()[:space:]]|$)/) {
        next
      }
      if (line == "systemctl stop \\") {
        next
      }
      if (line == "if command -v systemctl >/dev/null; then") {
        next
      }
      if (line == "if ! command -v systemctl >/dev/null; then") {
        next
      }
      if (line == "if ! systemctl daemon-reload; then") {
        next
      }
      if (line == "if ! command -v systemctl >/dev/null || ! systemctl start \"$@\"; then") {
        exact_start++
        next
      }
      reject("postinst contains an unrecognized direct systemctl command: " line)
    }
    END {
      if (bad) {
        exit 1
      }
      if (exact_start != 1) {
        print "postinst must contain exactly one exact-active systemctl start"
        exit 1
      }
    }
  ' "$postinst"
} 2>&1)"; then
  fail "$activation_error"
fi

# --no-enable intentionally uses Debian's dont-enable autoscript. It may
# recreate symlinks only when deb-systemd-helper has state proving the package
# was installed before and the same unit was previously enabled. Ignore only
# comments and whitespace; every executable line, nesting boundary, unit name,
# and marker must otherwise match the reviewed template exactly.
if ! postinst_error="$({
  awk '
    function trim(value) {
      sub(/^[[:space:]]+/, "", value)
      sub(/[[:space:]]+$/, "", value)
      return value
    }
    function quote(value) {
      return sprintf("%c%s%c", 39, value, 39)
    }
    function reject(message) {
      print message
      bad = 1
      exit 1
    }
    BEGIN {
      expected["midnight-agent.service"] = 1
      expected["midnight-buildkit-worker.service"] = 1
      expected["midnight-buildkit.service"] = 1
      expected["midnight-github-status-worker.service"] = 1
      expected["midnight-registry.service"] = 1
      expected["midnight-server.service"] = 1
      expected["midnight-worker.service"] = 1
      expected_count = 7
      outer = "if [ \"$1\" = \"configure\" ] || [ \"$1\" = \"abort-upgrade\" ] || [ \"$1\" = \"abort-deconfigure\" ] || [ \"$1\" = \"abort-remove\" ] ; then"
    }
    /^# Automatically added by dh_installsystemd/ {
      if (inside) {
        reject("nested dh_installsystemd marker in postinst")
      }
      if ($0 !~ /^# Automatically added by dh_installsystemd\/[[:alnum:].+~:_-]+$/) {
        reject("malformed dh_installsystemd marker in postinst")
      }
      inside = 1
      stage = 0
      unit = ""
      blocks++
      next
    }
    /^# End automatically added section$/ {
      if (inside) {
        if (stage != 9) {
          reject("incomplete or expanded dh_installsystemd block in postinst")
        }
        inside = 0
        next
      }
    }
    {
      line = trim($0)
      if (!inside) {
        if (line !~ /^#/ && line ~ /(^|[;&|()[:space:]])deb-systemd-helper([;&|()[:space:]]|$)/) {
          reject("deb-systemd-helper appears outside a reviewed dh_installsystemd block")
        }
        next
      }
      if (line == "" || line ~ /^#/) {
        next
      }
      if (stage == 0) {
        if (line != outer) {
          reject("dh_installsystemd block has a noncanonical outer guard")
        }
      } else if (stage == 1) {
        matched = ""
        for (candidate in expected) {
          if (line == "if deb-systemd-helper debian-installed " quote(candidate) "; then") {
            matched = candidate
          }
        }
        if (matched == "" || seen[matched]) {
          reject("dh_installsystemd block has an unknown or duplicate debian-installed unit")
        }
        unit = matched
        seen[unit] = 1
      } else if (stage == 2) {
        if (line != "deb-systemd-helper unmask " quote(unit) " >/dev/null || true") {
          reject("dh_installsystemd block has a noncanonical unmask command")
        }
      } else if (stage == 3) {
        if (line != "if deb-systemd-helper --quiet was-enabled " quote(unit) "; then") {
          reject("dh_installsystemd block has a mismatched was-enabled guard")
        }
      } else if (stage == 4) {
        if (line != "deb-systemd-helper enable " quote(unit) " >/dev/null || true") {
          reject("dh_installsystemd block has a mismatched enable command")
        }
      } else if (stage == 5 || stage == 6 || stage == 8) {
        if (line != "fi") {
          reject("dh_installsystemd block has noncanonical guard nesting")
        }
      } else if (stage == 7) {
        if (line != "deb-systemd-helper update-state " quote(unit) " >/dev/null || true") {
          reject("dh_installsystemd block has a mismatched update-state command")
        }
      } else {
        reject("dh_installsystemd block contains extra executable statements")
      }
      stage++
    }
    END {
      if (bad) {
        exit 1
      }
      if (inside) {
        print "unterminated dh_installsystemd block in postinst"
        exit 1
      }
      if (blocks != expected_count) {
        print "postinst must contain exactly six reviewed dh_installsystemd blocks"
        exit 1
      }
      for (unit in expected) {
        if (!seen[unit]) {
          print "postinst is missing reviewed dh_installsystemd block for " unit
          exit 1
        }
      }
    }
  ' "$postinst"
} 2>&1)"; then
  fail "$postinst_error"
fi

# On the Jammy/Bookworm debhelper 13 release hosts, --no-start suppresses the
# generated prerm fragment entirely. Custom prerm owns removal and upgrade
# stops, and it must snapshot the exact-active set before an upgrade stop.
if ! prerm_error="$({
  awk '
    function trim(value) {
      sub(/^[[:space:]]+/, "", value)
      sub(/[[:space:]]+$/, "", value)
      return value
    }
    function reject(message) {
      print message
      bad = 1
      exit 1
    }
    /^# Automatically added by dh_installsystemd/ {
      reject("prerm contains a generated dh_installsystemd block despite --no-start")
    }
    {
      line = trim($0)
      if (line == "" || line ~ /^#/) {
        next
      }
      if (line ~ /(^|[;&|()[:space:]])(deb-systemd-invoke|invoke-rc[.]d)([;&|()[:space:]]|$)/) {
        reject("prerm contains a generated or alternate service invocation")
      }
      if (line ~ /(^|[;&|()[:space:]])systemctl[[:space:]]+is-enabled([;&|()[:space:]]|$)/) {
        reject("prerm infers runtime activity from systemd enablement")
      }
      if (line ~ /(^|[;&|()[:space:]])deb-systemd-helper([;&|()[:space:]]|$)/) {
        reject("prerm infers runtime activity from deb-systemd-helper state")
      }
    }
    END {
      if (bad) {
        exit 1
      }
    }
  ' "$prerm"
} 2>&1)"; then
  fail "$prerm_error"
fi

echo "verify Debian systemd lifecycle: PASS"
