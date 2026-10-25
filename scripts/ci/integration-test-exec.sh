#!/usr/bin/env bash
# Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
set -euo pipefail
# Go test binaries share a parent process by default. Its previous package's
# Ryuk can expire before the next package starts; give each executable its own
# reaper session while retaining cleanup and child-process session inheritance.
TESTCONTAINERS_SESSION_ID=$(python3 -c 'import uuid; print(uuid.uuid4().hex)')
export TESTCONTAINERS_SESSION_ID
exec "$@"
