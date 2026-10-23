# Multi-Region Routing Foundations

This document describes pure routing contracts for warm-standby planning. These contracts are intentionally offline: they do not update DNS, program proxies, move traffic, provision regions, replicate data, or create an HA control plane.

## Route Candidate Selection

- `internal/networking.RegionRouteCandidate` models a region endpoint with residency label, health state, observed latency, operator weight, and optional failover order.
- `PlanRegionRoute` normalizes candidates, rejects duplicate region IDs, filters incompatible or unhealthy regions, and returns a deterministic primary plus standby list.
- Healthy candidates outrank degraded candidates; within the same health tier, higher weight and lower latency win.

## Data Residency Compatibility

- `RegionRoutePolicy.RequiredResidency` pins routing to one residency label such as `eu` or `us`.
- `RegionRoutePolicy.AllowedResidency` permits a bounded set of residency labels when no single residency is required.
- Regions outside the residency scope are rejected with an explicit reason and are never used as primary or standby candidates.

## Failover Planning

- `RegionRoutePlan.FailoverSteps` lists ordered primary-to-standby transitions for compatible non-unhealthy regions.
- `MinimumStandbys` marks the plan degraded when the candidate set cannot satisfy the required standby count.
- If no compatible healthy/degraded region exists, the plan has no primary and is degraded with a reason.

## Local API Surface

- The OpenAPI contract documents `POST /api/v1alpha/networking/region-routing/plan`; the router registers the validation-only route only when `MIDNIGHT_EXPERIMENTAL=1`.
- The handler validates request shape with strict JSON decoding and calls the pure planner only.
- It does not update DNS, program proxies, move traffic, provision regions, replicate data, call health endpoints, or touch filesystems/databases.
- The product dashboard does not expose planner sample endpoints. Experimental local review can enable and call the planning route; tests and tooling can call the planner helper directly.

## Out Of Scope

This slice does not include billing, public hosted multi-tenancy, build isolation, HA control plane, enterprise identity/audit, BYOK, disconnected bundles, LTS, SLA, DPA/subprocessor workflows, database replication, active-active routing, DNS updates, proxy configuration, or live health probing.
