# Phase 0 Beta Sprint B6 Host Drill Candidate Catalog

Owner: Worker 6

Updated: 2026-05-01

## Purpose

Sprint B6 is the candidate-catalog follow-up to Sprint B5. B5 landed the first host-scoped Phase 3 metadata slice in the existing Phase 0 drill manifest; B6 documents the next candidate set and the handoff paths without changing code or claiming host execution.

B6 reconciles the B5 implementation with earlier planning notes:

| Topic | Current B6 position |
| --- | --- |
| B5 carrier | `D-08` (`Conntrack pressure alert drill`) is the implemented carrier for `phase3HostDrillMetadata`. |
| Older D-19 language | Treat as superseded planning language. `D-19` remains the QA gate release-readiness placeholder unless the manifest/gate owner intentionally remaps a future slice. |
| Manifest shape | Keep the existing manifest envelope. Candidate rows use `metadata.tags`, `metadata.notes`, and `metadata.subcases`; no new schema is required for B6. |
| Evidence model | Candidate evidence must be workspace-relative and portable under the B4 proof-packet rules. |

## Readiness Domains

B6 uses the same four Phase 3 readiness domains as B5:

| Domain | Candidate evidence intent |
| --- | --- |
| `networking` | Route promotion, WireGuard peer churn, WireGuard key rotation overlap, PMTU regression, IPAM quarantine, and conntrack flush correctness. |
| `runtime_adapter` | Scheduler reschedule, adapter conformance, heartbeat, workload placement, and host capability planning. |
| `registry_cache` | Host-local registry/cache status, digest pinning, cache promotion, cache recovery, and cache integrity planning. |
| `volume_attach` | Attach/detach planning, source and destination host identity, volume identity, recovery state, and workload impact. |

## Candidate Catalog

| Candidate | Domain | Suggested reason code | Suggested event | Required host-scoped data | Return owner |
| --- | --- | --- | --- | --- | --- |
| Conntrack flush correctness | `networking` | `phase3.dataplane.conntrack-flush` | `dataplane.conntrack_flush` | Removed peer, allowed IPs, affected hosts, planner evidence ref. | Phase 3 networking owners |
| WireGuard peer churn | `networking` | `phase3.dataplane.peer-churn` | `dataplane.peer_churn` | Added or removed host ID, peer FSM state, allowed IP changes, planner evidence ref. | Phase 3 networking owners |
| WireGuard key rotation overlap | `networking` | `phase3.dataplane.wg-key-rotation` | `dataplane.wg_key_rotation` | Host pair, old key ID, new key ID, overlap-window state, planner evidence ref. | Phase 3 networking owners |
| PMTU regression | `networking` | `phase3.dataplane.pmtu-regression` | `dataplane.pmtu_regression` | Source host, destination host, attempted MTUs, selected per-link MTU, planner evidence ref. | Phase 3 networking owners |
| IPAM quarantine | `networking` | `phase3.dataplane.ipam-quarantine` | `dataplane.ipam_quarantine` | Quarantined host, conflicting allocation, release owner, planner evidence ref. | Phase 3 networking owners |
| Scheduler reschedule | `runtime_adapter` | `phase3.scheduler.host-reschedule` | `scheduler.host_reschedule` | Source host, destination host, workload identity, stateless/stateful handling, planner evidence ref. | Phase 3 runtime/scheduler owners |
| Runtime adapter conformance | `runtime_adapter` | `phase3.runtime.adapter-conformance` | `runtime.adapter_conformance` | Host ID, adapter name, conformance subset, failed capability if any, planner evidence ref. | Phase 3 runtime/scheduler owners |
| Registry cache recovery | `registry_cache` | `phase3.registry.cache-recovery` | `registry.cache_recovery` | Host ID, cache role, digest set, recovery action, planner evidence ref. | Phase 3 registry/storage owners |
| Volume attach transfer | `volume_attach` | `phase3.storage.volume-attach-transfer` | `storage.volume_attach_transfer` | Workload ID, volume ID, source host, destination host, attach/detach state, planner evidence ref. | Phase 3 registry/storage owners |

## Local-Only Boundary

B6 may document candidates, reconcile B5 handoff language, read the manifest, and run local gate validation. It may not execute live host drills or create artifacts that imply live execution.

B6 must not:

- Query Docker, Traefik, DNS, public routes, registries, or host networking state.
- Create WireGuard interfaces, rotate keys, mutate IPAM state, flush conntrack, or probe routes.
- Mount, attach, detach, or inspect host volumes.
- Install packages, start services, stop services, or inspect systemd host state.
- Create placeholder host artifacts to make a release evidence index pass.
- Claim that planner-only metadata proves Phase 3 host execution.

## Validation

Run from the repository root:

```sh
go run ./tools/phase0-gate --format=json --execute-drills --require-executed-drills
```

This command validates the local Phase 0 gate and confirms the current `D-08` B5 metadata contract is still present. It does not execute live host drills.

## Return Paths

- Return to B4 when candidate evidence cannot be represented as workspace-relative proof-packet refs.
- Return to B5/manifest-gate ownership when `D-08` is missing `phase3.host`, `phase3.domain`, `phase3.planner`, or `phase3.evidence`; when the gate omits `phase3HostDrillMetadata`; or when future Phase 3 rows need accepted ID-range work.
- Return to Phase 3 networking owners for executable route promotion, WireGuard peer churn, key rotation, PMTU, IPAM quarantine, and conntrack flush semantics.
- Return to Phase 3 runtime/scheduler owners for executable reschedule, adapter conformance, heartbeat, and placement semantics.
- Return to Phase 3 registry/storage owners for registry cache and volume attach/detach planner evidence.

## Acceptance Criteria

1. B6 names `D-08` as the implemented B5 carrier and identifies old `D-19` wording as superseded planning language.
2. B6 lists the four readiness domains: `networking`, `runtime_adapter`, `registry_cache`, and `volume_attach`.
3. B6 states the local-only boundary and explicitly says no live host execution is performed.
4. B6 defines return paths for proof-packet, manifest/gate, networking, runtime/scheduler, and registry/storage ownership.
5. B6 provides the local validation command without adding or editing gate code.
