# Phase 0 Beta Sprint B5 Host Drill Metadata Handoff

Owner: Worker 6

Updated: 2026-05-01

## Purpose

Sprint B5 documents the first host-scoped Phase 3 drill metadata slice while Phase 0 remains local-only. It follows the Sprint B4 proof packet by keeping evidence portable, workspace-relative, and explicit about source ownership. B5 does not execute host drills or claim that any host networking action happened.

The implementation intent is to use the existing manifest envelope, not a new schema. The first B5 slice is carried on the current `D-19` release readiness drill row with key/value entries under `metadata.subcases`.

## First Slice

| Field | B5 value or rule |
| --- | --- |
| Manifest row | `D-19` (`QA gate release readiness drill`) |
| B5 contract | `phase3HostDrillMetadata` in the gate report |
| Evidence packet item | `b5-phase3-host-drill-metadata-contract` |
| Source | `test/drills/manifest.yaml` |
| Host scope | Stable symbolic host identity such as `control-plane-01` |
| Readiness domain | One of `networking`, `runtime_adapter`, `registry_cache`, or `volume_attach` |
| Planner | Stable symbolic planner name such as `route-promotion` |
| Evidence | Stable symbolic evidence name such as `local-planner-summary` |

Expected metadata shape:

```yaml
metadata:
  subcases:
    - phase3.host=control-plane-01
    - phase3.domain=networking
    - phase3.planner=route-promotion
    - phase3.evidence=local-planner-summary
```

The `phase3.evidence` value is a symbolic local planner evidence name in the gate report, not proof that a host drill ran. B5 does not create host artifacts and does not stub them to make verification pass.

## Candidate Expansion

Once the manifest/gate owner accepts more Phase 3 drill rows or intentionally maps additional slices onto existing rows, the same field pattern can cover:

| Candidate | Reason code | Event | Host-scoped diagnostic intent | Owner path |
| --- | --- | --- | --- | --- |
| Scheduler reschedule host drill | `phase3.scheduler.host-reschedule` | `scheduler.host_reschedule` | Source host, destination host, workload identity, and stateless/stateful handling are recorded as data. | Phase 3 runtime/scheduler owners |
| WireGuard peer churn drill | `phase3.dataplane.peer-churn` | `dataplane.peer_churn` | Added/removed host IDs, peer FSM state, and impacted allowed IPs are explicit. | Phase 3 networking owners |
| WireGuard key rotation overlap drill | `phase3.dataplane.wg-key-rotation` | `dataplane.wg_key_rotation` | Host pair, old/new public key IDs, and overlap-window state are explicit. | Phase 3 networking owners |
| PMTU regression drill | `phase3.dataplane.pmtu-regression` | `dataplane.pmtu_regression` | Source host, destination host, attempted MTUs, and selected per-link MTU are explicit. | Phase 3 networking owners |
| IPAM quarantine drill | `phase3.dataplane.ipam-quarantine` | `dataplane.ipam_quarantine` | Quarantined host, conflicting allocation, and release owner are explicit. | Phase 3 networking owners |
| Conntrack flush drill | `phase3.dataplane.conntrack-flush` | `dataplane.conntrack_flush` | Removed peer, peer allowed IPs, and affected host list are explicit. | Phase 3 networking owners |

## Acceptance Criteria

1. `D-19` carries the first B5 host-scoped metadata slice without requiring a new manifest schema.
2. `metadata.subcases` includes `phase3.host`, `phase3.domain`, `phase3.planner`, and `phase3.evidence` key/value entries.
3. The Phase 3 domain is one of the required operator-readiness domains: `networking`, `runtime_adapter`, `registry_cache`, or `volume_attach`.
4. Host identity is explicit in metadata and later evidence payloads; it is not inferred from the runner.
5. The slice does not claim live host execution, WireGuard mutation, conntrack mutation, route probing, Docker inspection, or Traefik inspection.
6. Handoff paths are clear for B4 packet issues, manifest/gate metadata completion, Phase 3 networking implementation, and Phase 3 runtime/scheduler implementation.

## How B5 Follows B4

B4 proved the portable packet shape: release evidence, release summary, release evidence index, and extra evidence refs must be workspace-relative and verifiable without live infrastructure. B5 applies the same portability rule to future Phase 3 host drills. A drill may be host-scoped, but its archived evidence still needs to be a local file referenced by the proof packet, with host identity recorded in the evidence payload.

B5 therefore extends the release-readiness story forward without changing the local-only boundary. It is a metadata contract that can be attached to a later B4-style proof packet as documentation/planner evidence, not a replacement for a real Phase 3 integration rig.

## Local-Only Boundary

B5 may:

- Read `test/drills/manifest.yaml`, `test/drills/README.md`, B4 handoff notes, ADR-016, and the multi-host dataplane dossier.
- Validate that manifest metadata uses existing fields and workspace-relative refs.
- Update the owned documentation files that describe B5.
- Run local gate checks that do not require host services.

B5 must not:

- Start, stop, install, or inspect Midnight host services.
- Query Docker, Traefik, DNS, public routes, or host networking state.
- Create WireGuard interfaces, rotate keys, mutate IPAM state, or flush conntrack.
- Create placeholder host artifacts to make a release evidence index pass.
- Claim that a planner-only metadata row proves Phase 3 host execution.

## Return Paths

- Return to B4 if the B5 evidence ref cannot be represented as a workspace-relative ref in the canonical proof packet.
- Return to the manifest/gate owner when `D-19` is missing required B5 `phase3.*` subcases; when the gate report omits `phase3HostDrillMetadata`; or when future Phase 3 drill IDs need to be accepted.
- Return to Phase 3 networking owners for WireGuard peer churn, key rotation, PMTU, IPAM quarantine, and conntrack flush execution semantics.
- Return to Phase 3 runtime/scheduler owners for reschedule host evidence, adapter conformance, and stateless/stateful placement semantics.

## Current Validation

Documentation-only validation for this handoff should include:

```sh
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills
```

This command validates the current local gate and manifest metadata contract. It still does not execute any live host drill.
