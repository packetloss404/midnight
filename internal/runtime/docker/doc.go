// Package docker implements the Phase 1 RuntimeAdapter against a real Docker
// Engine via the Engine API client (github.com/moby/moby/client).
//
// The adapter enforces Phase 0 / Phase 1 contracts before it accepts any
// scheduling work:
//
//   - cgroup v2 unified hierarchy (ADR-005). Init() refuses with
//     failurecodes.CGroupV2Required on cgroup v1.
//   - Docker Engine API floor v1.43 (Engine ≥ 24.0) (ADR-004 + line 90).
//     Init() refuses with failurecodes.DockerEngineUnsupported below the
//     floor.
//   - daemon.json baseline (FEATURE_DAEMON_JSON_BASELINE / line 89).
//     Init() refuses with failurecodes.DockerDaemonBaselineConflict when the
//     live daemon configuration violates the locked baseline (overlay2,
//     live-restore=true, json-file log driver). The agent does NOT auto-edit
//     daemon.json — that is installer territory.
//
// Container hardening defaults (line 91, line 93, ADR-004 Compliance) are
// applied at submit time:
//
//   - HostConfig.Init = true (tini, P0-D006).
//   - HostConfig.SecurityOpt contains "no-new-privileges:true".
//   - HostConfig.CapDrop = [ALL] and CapAdd is restricted to the panel-locked
//     allowlist (CHOWN, DAC_OVERRIDE, FOWNER, FSETID, KILL, SETGID, SETUID,
//     NET_BIND_SERVICE, SETPCAP, SYS_CHROOT).
//
// Container names and labels follow the Sprint 4 deterministic schema
// documented in names.go.
//
// The adapter is constructed via New(...) which returns a configured Adapter
// plus a verified BaselineReport. New(...) accepts an EngineClient seam so
// unit tests can drive the adapter without a live Docker daemon. The
// production wiring uses NewEngineClient(...) which builds a Docker SDK
// client with Engine API version negotiation pinned to the v1.43 floor.
//
// The adapter is registered against the conformance suite under build tag
// `integration`; default `go test ./...` MUST NOT require Docker.
package docker
