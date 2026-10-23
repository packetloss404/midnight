# Kubernetes Runtime Adapter Foundation

The Kubernetes adapter starts as a planning-only implementation in
`internal/runtime/kubernetes`. It uses the existing runtime boundary:

```text
BuildArtifact -> ScheduleRequest -> InstanceStatus -> RoutingTarget
```

The package does not import client-go and does not contact a Kubernetes API
server. `PlanSchedule` converts a `runtime.ScheduleRequest` into dependency-free
planning structs for a pod and service. The in-memory adapter stores those plans
and implements the `runtime.RuntimeAdapter` interface so conformance behavior can
be tested before live Kubernetes reconciliation exists.

Current covered behavior:

- `Schedule` is idempotent for the same deployment, instance, and idempotency key.
- Planned pod and service objects preserve the canonical `midnight.*` labels.
- `Start` advances observed instance state to `RUNNING` without mutating public deployment state.
- `ResolveRoutingTarget` returns a logical service DNS target for planned instances.
- `Remove` clears planned status and routing membership.
- Tenant-scope network policy names reject cross-tenant reuse.

Explicit gaps left out of this slice:

- No live Kubernetes API client or controller loop.
- No multi-host scheduling beyond the adapter boundary.
- No WireGuard, internal DNS, TCP proxy, registry cache, workload identity, or CI adapter work.
- No Kubernetes volume attach/detach implementation.
- No Pulumi provider integration.
