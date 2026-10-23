# Phase 0 Beta Sprint B8 Drill Promotion Readiness

Owner: Worker 6

Updated: 2026-05-01

## Purpose

Sprint B8 is the local-only promotion-readiness follow-up to Sprint B7. B7 reserved `D-20` through `D-23` as future Phase 3 host drill IDs; B8 defines the criteria that must be true before those reserved IDs can become executable manifest rows.

B8 does not claim execution. It does not add `D-20` through `D-23` to `test/drills/manifest.yaml`, does not expand the accepted drill ID range, and does not create harness assertion requirements. It is a readiness handoff for the manifest/gate owner and Phase 3 owners.

## Promotion Criteria

Before a reserved ID can enter the executable manifest, the promotion owner must provide:

| Criterion | Required shape |
| --- | --- |
| Accepted ID range | Manifest/gate ownership explicitly accepts `D-20` through `D-23` or the individual ID being promoted. |
| Executable semantics | The relevant Phase 3 owner defines the runner-safe fixture or multi-host rig behavior. |
| Deterministic assertions | The harness can evaluate pass/fail locally from declared evidence instead of inferring host state. |
| Host-scoped evidence | Evidence includes host or host-pair identity inside the payload. |
| Portable refs | Evidence refs are redacted, workspace-relative, and compatible with the B4 proof-packet/index rules. |
| Return path | Missing, unsafe, or live-only evidence has a named owner and rollback path before manifest promotion. |

## Reserved ID Readiness Map

| Reserved ID | Future drill intent | Readiness domain | Required promotion evidence | Return owner |
| --- | --- | --- | --- | --- |
| `D-20` | Route promotion / dataplane reachability. | `networking` | Source host, destination host, route state, expected reachability assertion, fixture or rig contract, and workspace-relative evidence ref. | Phase 3 networking owners |
| `D-21` | Host reschedule / runtime adapter. | `runtime_adapter` | Workload ID, source host, destination host, adapter capability state, scheduler expectation, and workspace-relative evidence ref. | Phase 3 runtime/scheduler owners |
| `D-22` | Registry cache recovery / digest availability. | `registry_cache` | Host ID, cache role, digest set, recovery action, cache integrity assertion, and workspace-relative evidence ref. | Phase 3 registry/storage owners |
| `D-23` | Volume attach transfer / recovery. | `volume_attach` | Workload ID, volume ID, source host, destination host, attach/detach state, recovery assertion, and workspace-relative evidence ref. | Phase 3 registry/storage owners |

## Local-Only Boundary

B8 may:

- Document the criteria for promoting reserved IDs into executable manifest rows.
- Read `test/drills/manifest.yaml` and the B4/B5/B6/B7 handoff docs.
- Run local Phase 0 gate validation that uses the current manifest.
- Keep future evidence naming compatible with workspace-relative B4 proof-packet refs.

B8 must not:

- Add `D-20`, `D-21`, `D-22`, or `D-23` to the executable manifest.
- Change the accepted drill ID range.
- Add or require harness assertions for the reserved IDs.
- Execute live host drills or create placeholder evidence that implies execution.
- Query Docker, Traefik, DNS, routes, registries, volumes, or host networking state.
- Create WireGuard interfaces, rotate keys, mutate IPAM, flush conntrack, mount volumes, install packages, or start services.

## Validation

Run from the repository root:

```sh
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills
```

This validates the current local Phase 0 gate and manifest. It does not validate execution for `D-20` through `D-23`, because those IDs are still reserved outside the executable manifest and the accepted ID range has not changed.

## Return Paths

- Return to B4 when a future `D-20` through `D-23` evidence ref cannot be represented as a redacted, workspace-relative proof-packet ref.
- Return to B5/B6/B7 manifest-gate ownership when promotion needs accepted ID-range changes, executable manifest rows, or harness assertion requirements.
- Return to Phase 3 networking owners for executable route promotion, peer churn, key rotation, PMTU, IPAM quarantine, and conntrack semantics.
- Return to Phase 3 runtime/scheduler owners for executable host reschedule, adapter conformance, heartbeat, and placement semantics.
- Return to Phase 3 registry/storage owners for executable registry cache recovery, digest availability, and volume attach/detach transfer semantics.

## Acceptance Criteria

1. B8 defines the criteria required before `D-20` through `D-23` can enter the executable manifest.
2. The reserved IDs remain outside `test/drills/manifest.yaml` and outside executable gate assertions.
3. B8 states that no live host execution, host mutation, ID range change, or placeholder execution evidence is required or produced.
4. B8 maps each reserved ID to one Phase 3 readiness domain, promotion evidence shape, and return owner.
5. B8 provides the local validation command for the current gate without adding code or manifest rows.
