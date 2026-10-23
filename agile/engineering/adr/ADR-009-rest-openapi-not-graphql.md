# ADR-009: REST + OpenAPI for v1alpha; GraphQL deferred

- Status: Accepted (from panel review)
- Date: 2026-04-25
- Deciders: Eight-Specialist Panel
- Tags: [phase-0, api, openapi, contract, dx, deferred-graphql]

## Context

Midnight's API is the contract for dashboard, CLI, SDKs, MCP, IaC providers (Terraform/Pulumi), Git integrations, and webhooks. Railway's public API is GraphQL (line 251); Coolify, Dokku, and most self-hosted PaaS systems are REST. The panel reviewed both and resolved REST as the v1alpha contract, with explicit reasoning:

> "MVP: REST with OpenAPI and explicit domain actions, plus Server-Sent Events or WebSocket event streams. Later: GraphQL query layer can be added if dashboard graph traversal becomes painful. Avoid generic CRUD endpoints for critical operations. Use semantic mutations such as `applyChangeSet`, `deployService`, `rollbackDeployment`, `verifyDomain`, and `restoreBackup`. The REST API is the contract for dashboard, CLI, SDKs, MCP, IaC providers, Git integrations, and webhooks." (lines 411–415)

> "REST/OpenAPI must be provider-grade from day one: stable resource IDs, cursor pagination, idempotency keys, operation resources, scoped tokens, rate limits, versioned JSON, generated SDK readiness, and contract tests." (line 47)

> "REST/OpenAPI is the MVP automation contract; GraphQL remains optional later." (line 2381)

> "API versioning is URL-path only (`/api/v1alpha`, `/api/v1`). Media-type and header versioning are explicitly out of scope. Field naming: API JSON = snake_case; YAML config = camelCase. SDKs translate at the boundary." (line 179)

The panel locked normative artifacts that REST + OpenAPI handle directly: resource ID format with reserved prefixes (line 169), cursor pagination scheme (line 170), v1 graduation rubric requiring `oasdiff` clean diff and Schemathesis property tests (line 171), idempotency-key semantics (line 172), `202 Accepted` + `Location: /api/v1/operations/{op_id}` for async actions (line 173), error code registry (line 174), `Authorization: Bearer` only (line 175), CloudEvents binding (line 177), default rate-limit budgets (line 178). Every one of these is a REST/OpenAPI primitive.

GraphQL handles graph traversal elegantly but does not handle async operations, idempotency keys, semantic mutations, rate-limit envelopes, or contract testing as ergonomically. Schemathesis-style property testing on a GraphQL schema is also weaker than on OpenAPI. The mid-level/senior panel and the eight-specialist panel both landed on "REST first; GraphQL only if the dashboard genuinely needs it."

## Decision

Midnight's public API in Phase 0 Alpha is **REST under `/api/v1alpha`** with **OpenAPI 3.1** as the source-of-record schema. Stable v1 graduates to `/api/v1` only after the v1 graduation rubric (line 171) is satisfied.

Locked properties (panel-pinned):

- **Resource IDs:** `<prefix>_<26-char Crockford base32 ULID>` with reserved prefixes (`prj`, `env`, `svc`, `dep`, `op`, `job`, …); total length ≤ 32 (line 169).
- **Cursor pagination:** opaque base64url, server-signed (HMAC); tampering returns `400 INVALID_CURSOR`; `next_cursor: null` denotes end; `limit` defaults 50, max 200; cursors stable ≥ 1 hour; forward-only in v1 (line 170).
- **Idempotency-Key:** scope `(token_id, method, path_template)`; TTL 24h; required on `POST/PATCH/DELETE`; body hash via SHA-256 over RFC 8785 canonical JSON; replay returns identical body and `Idempotent-Replayed: true` (line 172).
- **Async mutations:** `202 Accepted` with `Location: /api/v1/operations/{op_id}` and the full Operation in the body. Polling at `GET /operations/{id}` with integer-seconds `Retry-After` and adaptive backoff hint (1s → 30s). Streaming default is **SSE** at `GET /operations/{id}/events`; reconnect via `Last-Event-ID`. WebSocket is documented as alternate at `/v1/ws` for log/exec multiplexing only (line 173).
- **Auth:** `Authorization: Bearer <midn_*>` only; never `X-Midnight-Token`. CLI uses OAuth 2.0 device authorization grant (RFC 8628) for headless and PKCE for workstations. Tokens go to OS keychain, not `.midnight/` (line 175).
- **Versioning:** URL path only (`/api/v1alpha`, `/api/v1`). Media-type and header versioning explicitly out of scope (line 179).
- **Error envelope:** `code`, `message`, `request_id`, `docs_url`, optional `details`; codes from a flat `SCREAMING_SNAKE_CASE` registry at `docs/error-codes.md` (line 174, line 602).
- **Rate-limit budgets:** pinned (line 178): user 600 req/min, service-account 1200, CLI 600, MCP 300, webhook receiver 6000, Git provider 1200, agent 12000. Two-tier `min(per-tenant, per-source-IP)`. `Retry-After` always integer seconds. Headers `RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset`.
- **Field naming:** API JSON = snake_case; YAML config = camelCase. SDKs translate at the boundary (line 179).
- **Semantic mutations** for critical operations: `applyChangeSet`, `deployService`, `rollbackDeployment`, `verifyDomain`, `restoreBackup`. No generic CRUD on critical paths (line 414).

GraphQL is **deferred**. It will be reconsidered only if the dashboard's graph traversal demonstrably outgrows REST + judicious eager-loading. No GraphQL endpoint ships in Alpha or v1.

## Alternatives Considered

- **GraphQL at v1alpha (Railway-style).** Rejected: panel explicit (lines 411–415, 2381). Railway's GraphQL works for them, but our consumer set (CLI, SDKs across N languages, MCP, Terraform, IaC) is primarily REST-native. GraphQL also fights:
  - Idempotency-Key semantics (no idiomatic place to attach one).
  - `202 + Location` async pattern (GraphQL has no equivalent, requires a side-channel subscription).
  - Standard rate-limit headers (per-query cost is harder to bound).
  - Schemathesis property testing (OpenAPI tooling is more mature).
  - HTTP caching primitives.
- **gRPC.** Rejected at v1alpha: gRPC clients are required across every language we plan to support; the curl/`midnight` CLI path becomes harder; webhook-style outbound calls and the SSRF-safe HTTP client (line 124) speak HTTP. gRPC is reasonable for the agent-to-control-plane channel internally, but is not the public surface.
- **REST + GraphQL parallel from day one.** Rejected: doubles the contract-test surface, doubles the SDK generation, and forces every endpoint to live twice. Panel explicitly suggested GraphQL as a **later** query layer (line 412), not a parallel surface.
- **JSON-RPC.** Rejected: weaker IaC/SDK ergonomics; no idiomatic OpenAPI equivalent; no first-class HTTP semantics for async operations.
- **Webhook-only / event-driven.** Rejected: read paths still need request/response; CLI debugging needs synchronous queries.

## Consequences

Positive:

- One contract source (OpenAPI 3.1) drives SDK generation, contract tests (Schemathesis, Pact), provider documentation (Terraform Provider Resource Matrix, line 176), and the dashboard's HTTP client.
- v1 graduation rubric is concrete (line 171): zero `oasdiff` breaking-change diff between minors for ≥ 90 days; ≥ 95% endpoint coverage by Schemathesis property tests; green Pact contracts; deprecation header machinery (RFC 8594 `Sunset`, `Deprecation`); two consecutive SDK releases without manual patches; ≥ 1 external IaC consumer.
- REST + idempotency keys + operations + cursor pagination are well-trodden; no novelty risk on the contract substrate.
- HTTP caching, rate-limit headers, status-code semantics, and SSE/WebSocket paths are stable.

Negative:

- Dashboard "graph traversal" use cases (e.g., "all deployments → variables → references in this project") may need careful endpoint design or BFF aggregation. Acceptable cost; if it becomes painful, a GraphQL query layer can be added later (panel: line 412).
- Endpoint count grows; we mitigate with rigorous OpenAPI lint (operation IDs, auth scopes, schemas, examples, rate-limit class, idempotency behavior, error schema — line 691).
- Generic `GET` listing endpoints risk becoming over-fetched; we use cursor pagination (line 170) and explicit `?expand=` when warranted, instead of GraphQL-style field selection.

Follow-up work:

- OpenAPI 3.1 generated from Go types via `oapi-codegen` (or equivalent); spec lives at `schemas/openapi/v1alpha.yaml` and is the source of record (`SPRINT_03_CONTROL_PLANE_API.md`).
- Schemathesis property test suite stood up as a CI gate (panel: line 171: ≥ 95% endpoint coverage required at v1).
- Pact contracts for CLI, dashboard, MCP, Terraform provider added in MVP Beta.
- `oasdiff` runs on every PR; breaking-change diff against the released v1alpha schema fails the build.

## Compliance / Verification

Concrete gates a reviewer or auditor checks:

- **Test name `TestOpenAPILint`** in `tooling/openapi`: the OpenAPI document passes panel-locked lint rules — every operation has a stable `operationId`, auth scope, schema, examples, rate-limit class, idempotency behavior, and error schema (line 691).
- **Test name `TestErrorEnvelope`** in `pkg/api/errors`: golden tests for `400`, `401`, `403`, `404`, `409`, `422`, `429`, `500` produce the panel-locked error envelope (`code`, `message`, `request_id`, `docs_url`, `details`).
- **Test name `TestIdempotencyKeyContract`** in `pkg/api/idempotency`: same key/body returns same operation; same key/different body returns `409 IDEMPOTENCY_KEY_REUSED`; concurrent in-flight returns `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` with `Retry-After` (line 172).
- **Test name `TestAsyncOperationLocation`** in `pkg/api`: every mutating async endpoint returns `202` with `Location: /api/v1alpha/operations/{op_id}` and the full Operation body.
- **Test name `TestCursorTamperingFails`** in `pkg/api/pagination`: a tampered cursor returns `400 INVALID_CURSOR`; HMAC over server secret verifies cursors.
- **Test name `TestNoXMidnightToken`** in `pkg/api/auth`: requests with `X-Midnight-Token` are rejected; only `Authorization: Bearer` is accepted (panel: line 175).
- **Test name `TestNoGraphQLEndpoint`** in `pkg/api`: an HTTP probe for `/graphql` returns 404; the absence is asserted as part of the v1alpha contract.
- **CI gate `gate.oasdiff-no-break`:** `oasdiff` against the previously released schema produces zero breaking changes; merging breaking changes requires a documented version bump.
- **CI gate `gate.schemathesis-coverage`:** Schemathesis property tests cover ≥ 80% of endpoints in alpha (≥ 95% required at v1, panel: line 171).
- **Runbook check `OR-CHK-API-CONTRACT-VERSION`** in `midnight doctor`: reports the running server's API version and confirms it matches the published schema for that release.
- **Code review checklist item `CR-API-01`:** any new endpoint requires an OpenAPI entry, an `operationId`, an idempotency note, a rate-limit class, and an error catalog.

## References

- spec: lines 47, 169–180, 411–418, 600–620, 686–691, 2381, 2384, 2409
- features: `FEATURE_DEPLOYMENT_STATE_MACHINE.md`, `FEATURE_ROLLBACK_BY_DIGEST.md`, `FEATURE_ACME_DNS01_WILDCARD.md`, `FEATURE_AUDIT_HASH_CHAIN.md`
- sprints: `SPRINT_03_CONTROL_PLANE_API.md`, `SPRINT_07_OBSERVABILITY_SUPPORT.md`, `SPRINT_09_VARS_SECRETS_AUDIT.md`
- handoff: `QUALITY_AND_TEST_STRATEGY.md`, `DEFINITION_OF_READY_AND_DONE.md`
