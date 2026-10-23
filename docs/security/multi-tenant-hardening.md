# Multi-Tenant Hardening Foundations

This area starts with pure contracts before live enforcement paths change.

## Tenant Scope

`internal/tenancy.Scope` is the shared tenant boundary for public resources. It requires `workspace_id`, `project_id`, and `environment_id` values to be non-empty safe identifiers and provides canonical labels:

- `midnight.workspace`
- `midnight.project`
- `midnight.environment`

Runtime and build planners should call `ValidateLabels` before accepting caller-supplied labels. A missing or mismatched tenant label fails closed to prevent cross-tenant resource reuse. Build cache namespaces use explicit field prefixes rather than ambiguous concatenation, so dotted safe identifiers cannot collide across tenant boundaries.

Billing, metering, limits, and abuse planning also uses `tenancy.Scope` on every usage event, plan assignment, bucket, snapshot, violation, activity, and decision. Aggregation keys include workspace, project, and environment so matching project IDs in different workspaces remain separate in the local contract.

## Build Isolation Planning

`internal/isolation.PlanBuild` produces a side-effect-free isolation plan for future BuildKit workers. It does not start BuildKit, containers, gVisor, Kata, or microVMs.

The current defaults are:

- `trusted` builds use the `container` backend with private tenant cache.
- `untrusted` builds use the `gvisor` backend, private tenant cache, and disabled network egress.
- `isolated` builds use the `microvm` backend, private tenant cache, disabled network egress, and plan-only warnings until a VM launcher is wired.

All build isolation plans reject privileged execution and host socket access. Isolated builds cannot request the plain container backend.

The OpenAPI contract documents `POST /api/v1alpha/isolation/build/plan`. The
router registers this validation-only route only when
`MIDNIGHT_EXPERIMENTAL=1`; it is absent from the normal product API. The
handler exposes the same planner with local provenance/SBOM evidence
references and secret fingerprints. It is still metadata-only: no BuildKit
worker, container, gVisor, Kata, microVM, registry, network, database, or
filesystem evidence operation is performed.

## Intentional Gaps

This slice does not implement payment collection, live rate-limit enforcement, scheduler/runtime admission hooks, multi-region routing, HA control plane behavior, enterprise identity, audit export, BYOK, disconnected bundles, data residency, or runtime launcher integration.
