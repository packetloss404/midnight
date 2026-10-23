# Component Compatibility

Compatibility rows are populated as implementation lands. The `/api/v1alpha/compat` payload uses the same component names for server-enforced skew checks.

| Component | Version floor | Current skew window | Area |
|---|---|---|---|
| Go | 1.23 | Toolchain must match repository `go.mod` | Tooling |
| Node.js | 20 | UI/tooling only; not part of runtime skew checks | Dashboard/tooling |
| npm | 10 | UI/tooling only; not part of runtime skew checks | Dashboard/tooling |
| Docker Engine API | 1.43 | Hosts below the floor fail preflight | Install/runtime |
| server | `0.0.0-dev` | Exact version in current compatibility slice | Control plane |
| agent | `0.0.0-dev` | Exact version; canary planner stages updates before widening | Runtime/Agent |
| worker | `0.0.0-dev` | Exact version in current compatibility slice | Worker |
| scheduler | `0.0.0-dev` | In-process with server; no independent rollout yet | Scheduler |

## Capability Matrix

| Capability | Component | Version window | Status |
|---|---|---|---|
| Stateless multi-host scheduling | scheduler | `0.0.0-dev` only | Planning-only; no dataplane or live host calls |
| Agent canary rollout | agent | `0.0.0-dev` only | Planning-only; batches ready hosts before widening |

## Scheduler Notes

- Stateless multi-host placement is supported by the scheduler planning API only. It places replicas on ready hosts with matching labels and spare capacity, and refuses stateful service placement.
- Agent canary rollout planning selects deterministic ready-host batches while respecting `max_unavailable`. Offline, draining, or unknown-status hosts consume unavailable budget and are held out.
- This slice does not add dataplane networking, TCP proxying, volume movement, Kubernetes adapters, registry caching, workload identity, or CI adapters.
