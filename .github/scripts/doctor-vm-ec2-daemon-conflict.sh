#!/usr/bin/env bash
# doctor-vm-ec2-daemon-conflict.sh — 0.1-EC2 negative-path
# (daemon.json conflict, mirroring 0.1-EC4) harness body. Invoked
# from .github/workflows/doctor-vm-ec2.yml.
#
# The container is freshly minted; we pre-pollute
# /etc/docker/daemon.json with a conflicting log-driver +
# storage-driver, then run vm-harness against the host-built
# midnight-doctor binary with --check=OR-CHK-DOC-002 and assert the
# panel-locked exit code 2.
#
# We do NOT install the .deb here. The .deb's postinst would refuse
# install on conflict (which is the 0.1-EC4 dpkg-side contract); the
# doctor-side contract is what we are testing here. Both surfaces
# are panel-locked.

set -euxo pipefail

: "${ARTIFACT_DIR:?ARTIFACT_DIR must be set}"
: "${DOCTOR_MAX_DURATION:?DOCTOR_MAX_DURATION must be set}"

mkdir -p /etc/docker
echo '{"log-driver":"syslog","storage-driver":"devicemapper"}' \
    > /etc/docker/daemon.json
cat /etc/docker/daemon.json

mkdir -p "${ARTIFACT_DIR}"

./dist/vm-harness \
    --doctor-binary=./dist/midnight-doctor \
    --distro=debian-12-daemon-conflict \
    --max-duration="${DOCTOR_MAX_DURATION}" \
    --json-out="${ARTIFACT_DIR}/doctor.json" \
    --junit-out="${ARTIFACT_DIR}/junit.xml" \
    --negative-path \
    --expect-exit=2 \
    --expect-fail-id=OR-CHK-DOC-002 \
    --doctor-args=--check=OR-CHK-DOC-002,--daemon-json-fixture=/etc/docker/daemon.json
