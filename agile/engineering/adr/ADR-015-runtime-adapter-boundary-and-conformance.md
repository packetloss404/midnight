# ADR-015: Runtime Adapter Boundary and Conformance Suite

- Status: Accepted (Parity Review)
- Date: 2026-04-25
- Deciders: Parity Review (top-3 must-fix from Reviewer 3)
- Tags: [runtime, architecture, phase-1, phase-3, conformance]

## Context

The spec asserts that `BuildArtifact → ScheduleRequest → InstanceStatus → RoutingTarget` is the runtime adapter boundary, and that "Kubernetes becomes a later adapter without changing user-facing resources." ADR-004 locked Docker Engine API for v1 but never defined the abstract `RuntimeAdapter` interface, never wrote a conformance suite, and never said how a future containerd/Kubernetes/Nomad adapter proves equivalence. Phase 3's multi-runtime promise is structurally at risk: by the time Phase 3 starts, a team would either (a) reverse-engineer the boundary from Docker code, (b) write a 3000-line ADR from scratch, or (c) silently couple new code to Docker semantics.

Spec references:

- `Runtime Plane / Runtime adapter boundary` (~L884-L890)
- ADR-004 (Docker Engine API runtime)
- `Parity Review Locked Decisions` → Runtime adapter boundary.

## Decision

1. **Define the `RuntimeAdapter` Go interface** in `internal/runtime/adapter.go` (skeleton in `agile/engineering/skeletons/INTERFACES_CONTROL_PLANE.md`). Required methods cover: lifecycle (`Schedule`, `Start`, `Stop`, `Remove`, `Drain`), observation (`StreamInstanceStatus`, `Reconcile`), networking (`ApplyNetworkPolicy`, `ResolveRoutingTarget`), volumes (`AttachVolume`, `DetachVolume`), and supply chain (`PullVerify` — image signature verified before pull).

2. **Ship a runtime conformance suite** in `tests/conformance/runtime/` (Phase 1). The suite is a Go test package that any adapter must pass. It verifies:
   - Idempotent `Schedule` (running twice with the same `ScheduleRequest` produces one instance).
   - State transitions match the deployment FSM transition matrix.
   - Observed-state reporting through `StreamInstanceStatus` is monotonic and includes all panel-locked instance states.
   - `PullVerify` rejects unsigned images by default.
   - Label hygiene: every Midnight-managed object carries the deterministic `midnight.<resource>=<id>` label set.
   - Clean detach: `Stop` followed by `Remove` leaves no orphan resources.
   - Resource limits enforced (CPU, memory, PIDs) — verified by stress fixtures.
   - Network policy enforcement (egress denial reaches the SMTP-blocked panel rule).

3. **Docker Engine API adapter passes the conformance suite as a release gate** for v1. Conformance is run in CI on every merge to `main`.

4. **Containerd, Kubernetes, and Nomad adapters** (Phase 3+) must pass the same suite before being accepted as supported runtimes. Documentation must list adapters and their conformance run timestamps.

5. The boundary is **versioned**. Conformance suite has a `RuntimeAdapter v1` major; breaking changes to the interface bump the major and require all adapters to re-conform. Adapter authors implement a `Capabilities()` method declaring which optional features they support (e.g., GPU scheduling, microVM isolation, replicas with shared volumes).

## Alternatives Considered

- **Define the interface only when adding the second adapter.** Misses every Docker-specific assumption that has accumulated by then; guarantees a rewrite of the Docker adapter when the second one ships.
- **Define a thin interface with no conformance suite.** "Conformance by hope" — every adapter diverges in subtle ways; breaks debugging and runbook portability.
- **Adopt Kubernetes CRI as the boundary.** CRI is too narrow (no scheduling, no networking); we need a higher-level abstraction.
- **Adopt OCI runtime spec as the boundary.** Even narrower; same problem as CRI.
- **Outsource to libpod / nerdctl as a single library.** Adds an opinionated layer; harms the Kubernetes adapter story which doesn't go through libpod.

## Consequences

**Positive**: Phase 3 multi-runtime adapter work is a project of weeks, not quarters. Bug reports become portable across runtimes. The `RuntimeAdapter` interface is testable in isolation.

**Negative**: writing the conformance suite up front costs 1–2 sprint-weeks in Phase 1 even though the second adapter is a year away. The suite needs maintenance as the interface evolves.

**Follow-ups**:
- Phase 1 sprint slot for conformance suite drafting (assign in `CAPACITY_AND_TEAMS.md`).
- Phase 3 spike for containerd adapter as the first non-Docker conformer (validates the boundary).
- Conformance suite report published in v1 release notes.

## Compliance / Verification

- CI gate `gate.runtime-conformance` runs the suite against the Docker adapter on every PR; failure blocks merge.
- Test report includes a per-method coverage matrix; gaps fail the gate.
- Code-review checklist `CR-RUNTIME-02`: any new field in `ScheduleRequest` / `InstanceStatus` requires a conformance test before merge.
- Sprint 13 (Upgrade Transaction) integration tests use the conformance suite to verify version-skew behavior.

## References

- Spec: `Runtime Plane`, `Parity Review Locked Decisions`
- ADR-004 (Docker Engine API)
- Skeleton: `agile/engineering/skeletons/INTERFACES_CONTROL_PLANE.md`
- Reviewer source: `agile/parity-review/PARITY_03_PLATFORM_INFRA.md`
