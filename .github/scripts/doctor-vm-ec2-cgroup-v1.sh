#!/usr/bin/env bash
# doctor-vm-ec2-cgroup-v1.sh — 0.1-EC2 negative-path (cgroup v1
# refusal, mirroring 0.1-EC3) harness body. Invoked from
# .github/workflows/doctor-vm-ec2.yml.
#
# The runner host is cgroup v2 unified, so we drive the cgroup-v1
# refusal through the `--hostsource=fixture:` seam added by Phase 1
# Blocker 1. The fixture lives at internal/doctor/testdata/cgroup-v1
# and supplies a /proc/self/mountinfo describing a v1 hierarchy.
# vm-harness asserts:
#
#   * doctor exits with code 2 (panel-locked,
#     doctor.ExitCodeForResults table)
#   * OR-CHK-DOC-001 reports status="fail"
#
# This is the same assertion shape as the live-host story would
# produce on a real cgroup-v1 box; the v0.2.0-alpha follow-up
# (phase1-blocker1-vm-ci-followup) lifts the test onto a real VM.

set -euxo pipefail

: "${ARTIFACT_DIR:?ARTIFACT_DIR must be set}"
: "${DOCTOR_MAX_DURATION:?DOCTOR_MAX_DURATION must be set}"

mkdir -p "${ARTIFACT_DIR}"

./dist/vm-harness \
    --doctor-binary=./dist/midnight-doctor \
    --distro=debian-12-cgroup-v1 \
    --max-duration="${DOCTOR_MAX_DURATION}" \
    --json-out="${ARTIFACT_DIR}/doctor.json" \
    --junit-out="${ARTIFACT_DIR}/junit.xml" \
    --negative-path \
    --expect-exit=2 \
    --expect-fail-id=OR-CHK-DOC-001 \
    --hostsource=fixture:internal/doctor/testdata/cgroup-v1 \
    --doctor-args=--check=OR-CHK-DOC-001
