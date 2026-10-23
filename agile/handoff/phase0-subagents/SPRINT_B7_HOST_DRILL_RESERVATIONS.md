# Phase 0 Beta Sprint B7 Host Drill Reservations

Owner: Worker 6

Updated: 2026-05-01

## Purpose

Sprint B7 is the local-only future drill ID reservation/index slice after Sprint B6. B6 cataloged host-drill candidates; B7 reserves the next ID block for future Phase 3 executable rows without adding those rows to the current executable manifest.

B7 does not claim execution. It is a handoff map so the manifest/gate owner and Phase 3 owners can later promote the reserved IDs when an executable multi-host rig, accepted ID range, and harness assertions exist.

## Reservation Index

`D-20` through `D-23` are reserved outside `test/drills/manifest.yaml` for now:

| Reserved ID | Future drill intent | Readiness domain | Future evidence shape | Return owner |
| --- | --- | --- | --- | --- |
| `D-20` | Route promotion / dataplane reachability. | `networking` | Source host, destination host, route state, planner/execution evidence ref. | Phase 3 networking owners |
| `D-21` | Host reschedule / runtime adapter. | `runtime_adapter` | Workload ID, source host, destination host, adapter capability state, planner/execution evidence ref. | Phase 3 runtime/scheduler owners |
| `D-22` | Registry cache recovery / digest availability. | `registry_cache` | Host ID, cache role, digest set, recovery action, planner/execution evidence ref. | Phase 3 registry/storage owners |
| `D-23` | Volume attach transfer / recovery. | `volume_attach` | Workload ID, volume ID, source host, destination host, attach/detach state, planner/execution evidence ref. | Phase 3 registry/storage owners |

The reservation block is an index only. The IDs are intentionally absent from the executable manifest until the manifest/gate owner accepts the range and wires the matching harness behavior.

## Local-Only Boundary

B7 may:

- Document the reserved ID block and return paths.
- Read `test/drills/manifest.yaml` and the B4/B5/B6 handoff docs.
- Run local Phase 0 gate validation that uses the current manifest.
- Keep evidence naming compatible with workspace-relative B4 proof-packet refs.

B7 must not:

- Add `D-20`, `D-21`, `D-22`, or `D-23` to the executable manifest.
- Add or require harness assertions for the reserved IDs.
- Execute live host drills or create placeholder evidence that implies execution.
- Query Docker, Traefik, DNS, routes, registries, volumes, or host networking state.
- Create WireGuard interfaces, rotate keys, mutate IPAM, flush conntrack, mount volumes, install packages, or start services.

## Validation

Run from the repository root:

```sh
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills
```

This validates the current local Phase 0 gate and manifest. It does not validate `D-20` through `D-23`, because those IDs are reserved outside the executable manifest and have no harness assertion requirement yet.

## Return Paths

- Return to B4 when a future `D-20` through `D-23` evidence ref cannot be represented as a workspace-relative proof-packet ref.
- Return to B5/B6 manifest-gate ownership when the reservation needs accepted ID-range changes, executable manifest rows, or harness assertion requirements.
- Return to Phase 3 networking owners for executable route promotion, peer churn, key rotation, PMTU, IPAM quarantine, and conntrack semantics.
- Return to Phase 3 runtime/scheduler owners for executable host reschedule, adapter conformance, heartbeat, and placement semantics.
- Return to Phase 3 registry/storage owners for executable registry cache and volume attach/detach evidence.

## Acceptance Criteria

1. B7 reserves `D-20` through `D-23` as a local-only future drill ID index after B6.
2. The reserved IDs remain outside `test/drills/manifest.yaml` and outside executable gate assertions.
3. B7 states that no live host execution, host mutation, or placeholder execution evidence is required or produced.
4. B7 maps each reserved ID to one Phase 3 readiness domain and return owner.
5. B7 provides the local validation command for the current gate without adding code or manifest rows.
