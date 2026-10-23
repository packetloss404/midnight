#!/usr/bin/env bash
# doctor-vm-ec2-positive.sh — disposable-VM CI harness step body for
# the 0.1-EC2 (Debian 12) positive-path job. Invoked from the
# .github/workflows/doctor-vm-ec2.yml matrix entry case=positive.
#
# Runs inside a `debian:12-slim` container that mounts the workspace
# at /work and shares the runner host's cgroup v2 hierarchy via
# --cgroupns=host. Required env: ARTIFACT_DIR, DOCTOR_MAX_DURATION.
#
# Steps:
#   1. Install ca-certificates / curl / gnupg / iproute2 / jq.
#   2. Add the Docker upstream APT repo + key (matches
#      deploy/debian/control:Depends).
#   3. Install docker-ce + docker-ce-cli + the produced midnight .deb.
#   4. Capture host fixtures (mountinfo, daemon.json, ss-ltn).
#   5. Run vm-harness against the system midnight-doctor.
#
# We do NOT start dockerd inside the container — the doctor probe
# reads `docker version` text from a fixture file we synthesize so
# the OR-CHK-DOC-003 (Docker Engine API >= v1.43) check has
# something to compare against without booting the daemon. The
# alpha-floor harness is satisfying a static-fact contract; live
# `dockerd` smoke is targeted for v0.2.0-alpha (see
# phase1-blocker1-vm-ci-followup tracker).

set -euxo pipefail

: "${ARTIFACT_DIR:?ARTIFACT_DIR must be set}"
: "${DOCTOR_MAX_DURATION:?DOCTOR_MAX_DURATION must be set}"

start=$(date +%s)

apt-get update
apt-get install -y --no-install-recommends \
    ca-certificates curl gnupg jq adduser systemd \
    iproute2 procps

install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/debian/gpg \
    | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
chmod a+r /etc/apt/keyrings/docker.gpg

cat > /etc/apt/sources.list.d/docker.list <<EOF
deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/debian bookworm stable
EOF

apt-get update
apt-get install -y --no-install-recommends docker-ce docker-ce-cli
apt-get install -y ./dist/midnight_*.deb

# Synthesize a docker-version fixture. The cli reports an
# API-compatible client version; the OR-CHK-DOC-003 probe parses
# major.minor numbers >= 1.43.
echo "1.45" > /tmp/docker-version.txt

# `ss -ltn` is provided by iproute2; capture into a fixture so the
# OR-CHK-DOC-004 ingress-port probe has something to read.
ss -ltn > /tmp/ss-ltn.txt 2>/dev/null \
    || ss --listening --tcp --numeric > /tmp/ss-ltn.txt 2>/dev/null \
    || true

mkdir -p "${ARTIFACT_DIR}"
elapsed=$(( $(date +%s) - start ))
echo "host-prep elapsed: ${elapsed}s"

./dist/vm-harness \
    --doctor-binary=midnight-doctor \
    --distro=debian-12-container \
    --max-duration="${DOCTOR_MAX_DURATION}" \
    --json-out="${ARTIFACT_DIR}/doctor.json" \
    --junit-out="${ARTIFACT_DIR}/junit.xml" \
    --doctor-args=--mountinfo-fixture=/proc/self/mountinfo,--daemon-json-fixture=/etc/docker/daemon.json,--docker-version-fixture=/tmp/docker-version.txt,--ss-ltn-fixture=/tmp/ss-ltn.txt,--firewall-mode=none,--mac-mode=apparmor-enforce,--time-sync=synchronized,--hostname-fqdn=ec2.midnight.local,--docker-packages=docker-ce
