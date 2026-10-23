# API Target Design and Delivery Order

Status: target foundation implemented; generated stable-v1 candidate is
machine-ready but is not served before the formal promotion procedure.

This document records the target architecture selected after inspecting the
router, stores, OpenAPI contract, event catalog, authentication boundaries,
idempotency implementation, pagination, and release checks. The normative HTTP
source remains `api/openapi/v1alpha/midnight.yaml`; generated audience views
must never become independent sources of truth.

## Target architecture

1. **One canonical HTTP contract, generated audience views.** OpenAPI 3.1.2 is
   the canonical description. Product, operator, runtime-agent, and UI views
   are deterministic filters produced by `tools/api-contract-views`.
2. **Separate trust domains.** Human sessions, project automation tokens,
   operator recovery credentials, inbound webhook signatures, and per-agent
   credentials are distinct OpenAPI schemes and runtime admission paths. No
   broad operator token authenticates a runtime agent.
3. **Uniform HTTP semantics.** Errors use RFC 9457
   `application/problem+json` fields plus the v1alpha nested `error`
   compatibility extension. Unsupported methods return 405 with `Allow`.
   Request IDs accept only a bounded visible-safe alphabet. JSON control
   documents are bounded, strict, and contain exactly one value.
4. **Safe mutation boundary.** Idempotency reserves a request identity and
   commits the exact terminal status, response body, and stable headers.
   Post-reservation validation and persistence errors are replayable terminal
   results. Tenant coordinators should commit domain state, audit evidence,
   outbox work, and idempotency in one database transaction.
5. **Bounded collections.** Collection reads use deterministic keyset order,
   `LIMIT n+1`, and expiring authenticated cursors bound to actor, tenant,
   collection, filters, order, and page size. Operation order is
   `(created_at,id)` with matching PostgreSQL indexes.
6. **Observable asynchronous work.** Mutations that enqueue durable work return
   an Operation. Operations expose immutable creation/update timestamps,
   lifecycle events, backed logs, cancellation where safe, and bounded SSE.
7. **Explicit event contract.** AsyncAPI 3.0 describes structured CloudEvents
   and is served at `/api/v1alpha/asyncapi.yaml`. Event types are checked
   against the runtime catalog. New outbound signatures bind delivery ID,
   timestamp, and raw bytes; inbound v1 verification remains a migration path.
8. **Measured admission.** Authenticated HTTP traffic has real request budgets,
   accurate `RateLimit-*` fields, `429` plus `Retry-After`, and separate stream
   concurrency leases. Keys use server-derived actor/tenant and direct peer IP;
   forwarded addresses require a future trusted-proxy configuration.
9. **Contract-driven delivery.** CI checks error-code registration, both
   directions of route ownership, generated-view freshness, event-catalog
   parity, generated model compilation, and stateful idempotency/operation
   invariants.

## Delivery order

The implementation order is intentionally dependency-aware:

1. Safety and correctness: trust boundaries, exact idempotency, transactional
   creation, bounded JSON, safe request IDs, keyset pagination, and admission.
2. Contract consolidation: RFC 9457, method semantics, security schemes,
   OpenAPI 3.1.2, audience views, and AsyncAPI.
3. Conformance: spec-to-router and router-to-spec checks, generated model
   compilation, and stateful property tests.
4. Stable-v1 graduation: remove placeholders and compatibility envelopes,
   require examples/tags, prove conditional request semantics, freeze a
   baseline, and run a breaking-change diff before promotion.
5. Client migration and retirement: publish generated clients, advertise
   deprecation and sunset dates, measure alpha usage, then remove alpha only
   after the documented support window.

## Promotion status and remaining exclusions

`tools/api-v1-graduation --require-ready` currently passes with 173 canonical
operations, 158 promoted operations, 15 explicitly justified alpha
exclusions, and zero mechanical blockers. The generated
`api/openapi/v1/midnight.yaml` contains only that promoted inventory. It is not
served at `/api/v1` until the release owner freezes a baseline, reviews the
compatibility diff, and completes the documented promotion procedure.

The following graduation foundations are complete:

- no response whose normal deployment contract is `501 Not Implemented`;
- operation tags and representative request/success/error examples;
- RFC 9457 errors without the nested v1alpha compatibility envelope;
- strong ETags and RFC 9110 `If-Match` preconditions for replace, update, and
  delete operations (body `expected_version` is alpha compatibility debt);
- a deterministic stable projection and compatibility checks with no
  unapproved breaking change on the current source revision;
- bidirectional live-route conformance, generated-client compilation, and
  stateful conformance checks in the release gate;
- atomic PostgreSQL identity claiming, including audit evidence and encrypted
  exact success/conflict replay;
- atomic production GraphQL deploy intake, including operation, deployment,
  initial events, outbox, audit, tenant-scoped idempotency, and the exact
  selected GraphQL result used to reconstruct the single-field HTTP response;
- authoritative strong ETags and atomic CAS for promoted environment,
  configuration, domain, variable, membership, invitation, and automation
  token mutations.

The 15 alpha exclusions remain visible in the canonical contract. Twelve need
an authoritative item validator plus store-level CAS before promotion: session
revocation, conversation deletion, recovery-domain deletion, TCP-proxy
withdrawal, volume resize/deletion, backup-policy deletion, monitor deletion,
service GitHub-link replacement/deletion, and installation workspace-grant
replacement/deletion. Three require product/runtime work rather than a
contract relabel: real backup execution, scoped configuration synchronization,
and durable dashboard template deployment.

Release-owner work that remains outside the mechanical graduation gate:

- distributed admission or an explicit, tested deployment constraint that a
  single process owns each budget, plus trusted-proxy configuration where
  proxies are supported;
- removal of remaining crash-consistency gaps in GraphQL template
  materialization and legacy durable volume, domain, TCP-proxy, monitor,
  recovery, and configuration mutations that still mutate a standalone store
  before idempotency completion;
- removal of the operator-compatibility crash gap where project and
  default-environment creation are atomic but audit/idempotency completion are
  still separate commits;
- PostgreSQL and concurrent in-flight conformance coverage for the stateful
  idempotency harness, which currently proves the contract deterministically
  against the in-process implementation.

## Compatibility policy

V1alpha may add fields and operations without notice, but it must not silently
change authentication, replay, or persistence guarantees. Stable v1 follows
semantic versioning at the contract level: additive compatible changes remain
within v1; removals, type narrowing, required-field additions, and security
weakening require a new major API version. Deprecation includes machine-readable
headers, documentation, telemetry, a published sunset, and a supported
migration path before removal.
