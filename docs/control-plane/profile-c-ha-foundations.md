# Profile C HA Foundations

Profile C HA starts with pure control-plane planning in `internal/controlplane/ha`. The package does not elect leaders, move traffic, execute migrations, or mutate database state.

Current checks:

- Quorum readiness validates expected voter membership and requires a fresh ready majority before quorum is considered ready.
- Migration leader eligibility requires the candidate to be a ready fresh quorum voter, migration-capable, and backed by ready schema, migration-plan, and optional backup-readiness evidence.
- Failover decision evidence records `none` when no failover is requested. Requested failovers require a reason code, operator approval, attached health evidence, ready quorum evidence, and a ready fresh target voter.
- Region routing evidence reuses `internal/networking.PlanRegionRoute` and requires a non-degraded plan with a primary and at least one residency-compatible standby.
- Profile C HA claims are allowed only when quorum, migration leader, failover, and region routing evidence all report `ready`.

Local API surface:

- The OpenAPI contract documents `POST /api/v1alpha/control-plane/ha/profile-c/readiness`; the router registers the validation-only route only when `MIDNIGHT_EXPERIMENTAL=1`.
- The handler composes pure planning helpers and rejects invalid region-routing inputs with the stable validation envelope.
- It does not elect leaders, move traffic, execute migrations, perform backup I/O, replicate databases, mutate control-plane state, touch filesystems, or make network calls.
- The product dashboard does not expose planner sample endpoints. Experimental local review can enable and call the planning route; tests and tooling can call the planner helper directly.

Unsafe claim boundary:

- If `claimed_ha_profile` is `profile-c` and any component is not ready, the summary is blocked with `UNSAFE_HA_CLAIM` and `ha_claim_allowed: false`.
- A non-ready summary is planning evidence only and must not be presented as high availability support.

Out of scope for this slice:

- Build isolation, billing, enterprise identity or audit, BYOK, disconnected bundles, LTS, SLA, DPA, and data residency enforcement beyond route-policy compatibility.
- Runtime leader election, traffic cutover, database writes, migration execution, backup I/O, and automated failover.
