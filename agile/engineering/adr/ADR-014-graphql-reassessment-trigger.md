# ADR-014: GraphQL Reassessment Trigger

- Status: Accepted (Parity Review supplement to ADR-009)
- Date: 2026-04-25
- Deciders: Parity Review
- Tags: [api, dx, ecosystem, deferral-policy]

## Context

ADR-009 chose REST + OpenAPI over GraphQL for `/api/v1alpha`. Railway's public API is GraphQL, so every external Railway-ecosystem tool (community SDKs, CLIs, third-party automation) breaks when migrating to Midnight. The deferral was correct (idempotency keys, async operations, cursor pagination, contract testing all fit REST better) but it was open-ended: nothing told the team *when* to revisit.

Without a trigger, the GraphQL question reopens randomly with each design-partner conversation and either (a) burns capacity defending the same decision repeatedly or (b) silently lands a half-baked GraphQL surface that we cannot maintain.

## Decision

Add an explicit re-evaluation trigger to ADR-009. When **at least three** of the following are true, the team drafts a GraphQL adapter ADR (proposed `ADR-024`) and either accepts a v2 GraphQL adapter or formally rejects the request with refreshed reasoning:

(a) Dashboard project page exceeds **10 REST round-trips** to render the project graph. Measured by the dashboard performance budget.

(b) **Two or more design partners** specifically request GraphQL for tooling integration (not "would be nice", but with a tooling commitment).

(c) **External Railway-ecosystem tools** (community CLIs, IDE extensions, third-party SaaS adapters) require GraphQL adapters that the SDK pipeline cannot satisfy with REST alone.

(d) The SDK generator produces **> 4 hand-patches per release** for graph-shaped queries (e.g., "list deployments with their builds with their failed steps"), measured over 3 consecutive releases.

Until the trigger fires, REST + cursor pagination + operation resources is the only public contract. The decision to re-evaluate is not the same as the decision to ship — re-evaluation may end with "still no."

## Alternatives Considered

- **Time-based trigger** (e.g., "revisit at v2"). Times don't measure pain; we should react to evidence, not the calendar.
- **Single-condition trigger** (e.g., "any design partner asks"). Too loose; would reopen the decision constantly.
- **Hard NO permanently.** Eliminates optionality; ignores the legitimate use case for graph-shaped queries.
- **Ship GraphQL alongside REST in v1alpha.** Doubles the contract surface; doubles SDK work; doubles documentation; doubles security review. Not worth it without evidence of demand.

## Consequences

**Positive**: discharges the open-ended deferral; gives the team a clear "we will look at this when…" answer to every design-partner conversation; ties the decision to measurable evidence; protects the v1 graduation rubric (clause e — clean SDK regenerations) by tying GraphQL adoption to its impact on that rubric.

**Negative**: someone has to actually measure the four conditions; without instrumentation, the trigger never fires.

**Follow-ups**:
- Sprint 20 (SDK pipeline) instruments the SDK-generator hand-patch count per release.
- Dashboard performance budget tracks REST round-trips per page in v1.
- Design-partner intake form (operational doc, not in this ADR) asks the GraphQL question explicitly.

## Compliance / Verification

- A monthly automated report (in v1) emits the four metrics to the program team. The trigger fires automatically and creates a planning ticket.
- ADR template for `ADR-024` (or successor) is pre-stubbed in `engineering/adr/_TEMPLATES/ADR-graphql-adapter.md` so the response is ready when the trigger fires.
- Code-review checklist item `CR-API-09`: any feature work that adds a REST endpoint specifically to compensate for graph-shaped reads must check whether condition (a) is now true.

## References

- Spec: `Parity Review Locked Decisions` → GraphQL re-evaluation trigger
- ADR-009 (REST + OpenAPI for v1alpha)
- Reviewer source: `agile/parity-review/PARITY_04_ECOSYSTEM_DX.md`
- Railway public GraphQL API: https://docs.railway.com/reference/public-api
