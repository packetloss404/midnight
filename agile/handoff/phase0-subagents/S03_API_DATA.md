# Phase 0 Subagent Handoff: S03 API/Data

Owner lane: Phase 0 Worker S03, API/Data.
Primary sprint source: `agile/sprints/SPRINT_03_CONTROL_PLANE_API.md`.
Reviewed contract sources:

- `agile/engineering/schemas/SCHEMA_PHASE_0_ALPHA.sql`
- `agile/engineering/schemas/SCHEMA_QUEUE_CONTRACT.md`
- `agile/engineering/contracts/OPENAPI_V1ALPHA.yaml`
- `agile/engineering/contracts/OPERATION_AND_IDEMPOTENCY_CONTRACT.md`
- `agile/engineering/contracts/API_ERROR_CODE_REGISTRY.md`
- `agile/engineering/schemas/SCHEMA_NOTES.md`
- `agile/engineering/skeletons/REPO_LAYOUT.md`
- `agile/engineering/skeletons/MODULE_DEPENDENCY_MATRIX.md`
- `agile/engineering/skeletons/INTERFACES_CONTROL_PLANE.md`

Status: contract handoff complete. No shared sprint, schema, or OpenAPI
contract files were edited.

## Scope

S03 owns the control-plane API and data substrate for Phase 0 Alpha:

- `/api/v1alpha` HTTP server skeleton and OpenAPI 3.1 release artifact.
- Bearer auth for `Authorization: Bearer midn_*` tokens, token mint/revoke,
  token hashing, token prefix audit display, and per-class rate-limit headers.
- Stable error envelope and additive-only error-code registry integration.
- `X-Request-Id` correlation and W3C `Traceparent`/`Tracestate` ingestion.
- `Idempotency-Key` middleware, RFC 8785 canonical body hashing, replay cache,
  concurrency handling, and 24h TTL.
- Public Operation resources, `202 Accepted` responses, `Location` headers,
  polling headers, and SSE operation streams.
- Server-signed opaque cursor pagination.
- Phase 0 Alpha Postgres migration, schema comments, grants, partition strategy,
  schema docs generation, and migration/upgrade compatibility hooks.
- Postgres job queue package and SQL contract implementation for `build`,
  `deploy`, `reconcile`, `webhook`, and `cleanup` queues.
- Contract-test scaffolding for Schemathesis and Pact consumers.
- Bootstrap-admin API endpoint integration with the Sprint 2 bootstrap token
  file and the first normal admin token flow.

Out of S03 scope, but consuming S03 contracts:

- Host agent runtime behavior and Docker/Traefik local work are S04/S06 lanes.
- BuildKit, image build, and deploy worker internals are S05/S06 lanes.
- Variables/secrets implementation depth and audit hardening continue in S09+,
  though S03 creates the table/API seams.
- Dashboard UX, CLI workflows, Terraform provider, and MCP consumer coverage use
  the generated OpenAPI/Pact contracts but do not own the server contracts.

## Owned Future Implementation Paths

Implement these paths exactly as described by `REPO_LAYOUT.md`; adding or moving
top-level package boundaries requires an ADR.

| Path | S03 ownership |
|---|---|
| `cmd/midnight-server/` | HTTP listener, composition root, `/api/v1alpha`, OpenAPI serving, middleware wiring. |
| `cmd/midnight-worker/` | Queue pool host for build/deploy/reconcile/webhook/cleanup classes. |
| `internal/api/` | Generated or hand-written handlers, route registration, response encoders, operation endpoints. |
| `internal/api/middleware/` | Auth, rate limit, request id, trace context, idempotency, error envelope, cursor guards. |
| `internal/db/` | Migrations driver, sqlc queries, repository implementations, transaction helpers. |
| `internal/queue/` | Postgres queue, dequeue SQL, heartbeat, reclaim, fairness, metrics. |
| `internal/auth/` | Token parser, token generator, sha256 verifier, bootstrap auth support, actor model. |
| `internal/idempotency/` | Key reservation, state matrix, canonical JSON body hash, replay storage. |
| `internal/failurecodes/` | Generated registry from `docs/error-codes.md` or the contract source. |
| `internal/version/` | `/api/v1alpha/compat`, schema version, supported client/agent ranges. |
| `internal/cloudevents/` | Event envelope primitives consumed by operations SSE and webhooks. |
| `internal/audit/` | Interface dependency only in S03 paths; append/verify implementation can continue in S09/S14. |
| `pkg/orailtypes/` | Resource IDs, resource versions, public enums, API DTOs shared with CLI/agent/client. |
| `pkg/midnightctl/` | Generated Go client from OpenAPI for CLI, Terraform, MCP, and integrations. |
| `api/openapi/v1alpha/midnight.yaml` | Generated/released OpenAPI artifact. |
| `migrations/` | `0001_alpha_core`, seed data, table tuning, partition bootstrap, migration manifest. |
| `docs/error-codes.md` | Published copy of the additive-only error-code registry. |
| `docs/api/` | Auth, idempotency, operations, pagination, OpenAPI, correlation docs. |
| `tools/contract-tests/schemathesis/` | Stateful OpenAPI property tests and coverage artifact. |
| `tools/contract-tests/pact/{cli,dashboard,terraform,mcp}/` | Consumer contract scaffolds and provider verification. |

## Contracts Exported To Other Workers

| Consumer | Exported S03 contract |
|---|---|
| S04 host agent | Authenticated agent token shape, `hosts`/`host_heartbeats` schema, `AGENT_CLOCK_SKEW`, `VERSION_SKEW_REJECTED`, heartbeat/request-id correlation, and API-only database boundary. |
| S05 BuildKit/build | `jobs` queue contract, `operations` progress/events contract, `builds`/`deployment_snapshots` schema, failure-code registry, and idempotent enqueue/outbox requirement. |
| S06 ingress/logs/routes | `domains`, `routes`, `deployment_instances`, `events`, SSE stream shape, `CANCEL_NOT_SAFE`, route readiness fields, and cursor pagination for timelines/logs. |
| S07/S08 ops and drills | Migration commands, partition pruning worker contract, queue metrics, rate-limit headers, error-code CI gates, OpenAPI signing/oasdiff, Schemathesis/Pact gates. |
| S09 variables/secrets/audit | `variables`, `variable_references`, `api_tokens`, `bootstrap_tokens`, audit row shape, redaction-safe error envelope, and no-plaintext sealed-variable API rule. |
| CLI/dashboard/Terraform/MCP | OpenAPI 3.1 file, stable error envelope, auth header, idempotency header, cursor list shape, Operation polling/SSE, Pact consumer expectations. |
| Reconciler/deploy workers | Operation-to-job relation, queue lease loss behavior, cooperative cancel safe-point signaling, and single-flight `(service, environment)` lock semantics. |

## Schema Count Source Of Truth

There is a table-count inconsistency across the sprint story and the schema
contracts. Do not edit the shared sprint or schema contracts just to reconcile
wording.

Source of truth for implementation: `agile/engineering/schemas/SCHEMA_PHASE_0_ALPHA.sql`
plus `agile/engineering/schemas/SCHEMA_NOTES.md`.

Observed counts:

- Sprint story S03-001 description lists 25 parent tables.
- Sprint story S03-001 acceptance says "all 25 tables exist".
- `SCHEMA_PHASE_0_ALPHA.sql` declares 29 parent tables.
- The SQL file also declares two starter partition children for each of the 5
  high-write partitioned tables, so the file has 39 `CREATE TABLE` statements.

The 4 parent tables present in the schema-of-record but absent from the sprint
story's 25-table list are:

- `bootstrap_tokens`
- `service_environment_state`
- `deployment_image_pins`
- `audit_checkpoints`

Normalized implementation rule:

- Build the migration from the schema-of-record's 29 parent tables, plus the
  starter partition children required by the DDL.
- Treat the sprint's "25 tables" as the panel-pinned core table list, not as a
  removal instruction for the 4 additional schema-of-record tables.
- Keep `jobs_recent_served` out of `0001_alpha_core` unless the queue library
  adds a later operational migration; `SCHEMA_QUEUE_CONTRACT.md` explicitly
  treats it as a derived worker-library table, not a base schema table.

## Additional Contract Drifts To Resolve Before Codegen

These should be resolved by documenting the chosen source of truth in the first
implementation PR. Avoid silent code decisions.

| Drift | Current sources | Recommended S03 source of truth |
|---|---|---|
| Token class enum | Schema `api_tokens.token_class` uses `pat`, `sa`, `agent`, `ci`; Sprint/OpenAPI token regex uses `admin`, `user`, `sa`, `agent`, `mcp`. | OpenAPI/Sprint for wire token classes. Migration should be adjusted in implementation or a compatibility mapping explicitly documented. |
| Operation/idempotency TTL direction | Operation contract says `idempotency_key.expires_at >= operation.expires_at`; one `operations` table comment says the reverse. | `OPERATION_AND_IDEMPOTENCY_CONTRACT.md` section 1.3 and the `idempotency_keys` table comment: key expiry is greater than or equal to operation expiry. |
| Resource prefixes | OpenAPI reserved list omits Alpha helper prefixes documented in `SCHEMA_NOTES.md`, such as `ws`, `btok`, `src`, `vrf`, `snp`, `bld`, `hst`, `rte`. | `SCHEMA_NOTES.md` for internal Alpha resource IDs; public OpenAPI prefix list must be expanded or helper resources hidden before v1. |
| Environment tier spelling | Schema uses `development`; OpenAPI enum uses `dev`. | Choose one before DTO generation; prefer API spelling stability if clients already use `dev`, or map `dev` to `development` at the repository boundary. |
| Operation URL examples | Some panel quotes mention `/api/v1/operations/{op_id}`; Sprint/OpenAPI Phase 0 surface is `/api/v1alpha/operations/{op_id}`. | Use `/api/v1alpha` for all Phase 0 code, docs, tests, and Location headers. |

## Acceptance Checklist

Schema and persistence:

- [ ] `migrations/0001_alpha_core.reversible.sql` creates the schema-of-record
  29 parent tables, partition children, FKs, indexes, triggers, comments, and
  grants.
- [ ] High-write parents `events`, `audit_logs`, `jobs`,
  `host_heartbeats`, and `deployment_instances` are monthly partitioned.
- [ ] `jobs` includes `lease_owner`, `lease_expires_at`, `heartbeat_at`,
  `attempt`, `available_at`, `priority`, `queue`, and `partition_key`.
- [ ] Queue and high-write table tuning migrations include `jobs`
  `autovacuum_vacuum_scale_factor=0.02` and `fillfactor=80`.
- [ ] Resource ID format checks are enforced where feasible and documented
  where a public/internal prefix mismatch remains.
- [ ] `pg_dump --schema-only` snapshot test exists and is CI-gated.

API skeleton:

- [ ] `cmd/midnight-server` starts, binds, emits `/api/v1alpha/compat`, and
  serves `/api/v1alpha/openapi.yaml` byte-for-byte from the release artifact.
- [ ] Every `/api/v1alpha/*` response emits `X-Request-Id`, and JSON bodies
  with `request_id` match the header.
- [ ] All non-2xx responses use exactly `{code, message, request_id, docs_url}`
  plus optional `details`.
- [ ] `Authorization: Bearer midn_*` is the only token transport; any
  `X-Midnight-Token` use returns `401 AUTH_TOKEN_HEADER_INVALID`.
- [ ] Rate-limited responses carry `RateLimit-Limit`,
  `RateLimit-Remaining`, `RateLimit-Reset`, and integer `Retry-After`.

Idempotency and operations:

- [ ] `Idempotency-Key` is required on every `POST`, `PATCH`, and `DELETE`.
- [ ] Scope is `(token_id, method, path_template)` and body hash is SHA-256
  over RFC 8785 canonical JSON.
- [ ] The full 5 x 2 idempotency state matrix has unit tests and stateful
  contract tests.
- [ ] Replays return byte-identical stored responses with
  `Idempotent-Replayed: true`.
- [ ] Concurrent duplicates return `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` with
  integer `Retry-After`.
- [ ] Async mutations return `202 Accepted`, `Location:
  /api/v1alpha/operations/{op_id}`, and the full Operation body.
- [ ] `GET /operations/{id}` returns polling headers and terminal/non-terminal
  states correctly.
- [ ] `GET /operations/{id}/events` supports SSE and `Last-Event-ID` replay.
- [ ] Operation GC cannot delete an operation before its idempotency key is
  expired/deleted.

Queue:

- [ ] Dequeue uses one statement with `FOR UPDATE SKIP LOCKED`, ordered by
  `priority DESC, available_at ASC`, filtered by queue and lease availability.
- [ ] Competing workers receive disjoint job sets under integration load.
- [ ] Heartbeats run at `lease_ttl / 3`; lease loss aborts side effects.
- [ ] Reaper reclaims expired leases and increments attempts according to the
  contract.
- [ ] Independent pool configs exist for `build`, `deploy`, `reconcile`,
  `webhook`, and `cleanup`.
- [ ] Weighted-fair dequeue prevents one `partition_key` from starving others.
- [ ] Metrics exist for in-flight jobs, dequeue latency, reclaimed jobs, and
  dead letters.

Contracts and tests:

- [ ] OpenAPI generated artifact lives at `api/openapi/v1alpha/midnight.yaml`
  and published release copy is signed.
- [ ] Runtime `/api/v1alpha/openapi.yaml` equals the artifact byte-for-byte.
- [ ] `oasdiff breaking` and no-`total_count` OpenAPI lint are CI gates.
- [ ] Error-code additive-only CI guard fails removed/renamed codes.
- [ ] Schemathesis suite runs against an ephemeral server and publishes
  `schemathesis-coverage.json`.
- [ ] Pact scaffolds exist for CLI, dashboard, Terraform, and MCP, with provider
  verification against the live server.

Bootstrap and tokens:

- [ ] Bootstrap endpoint consumes the Sprint 2 one-shot token file flow and
  creates the first admin with password plus TOTP.
- [ ] Bootstrap path auto-disables after a normal admin exists and returns
  `404 BOOTSTRAP_DISABLED` thereafter.
- [ ] Token minting uses 256 bits of entropy and the `midn_<class>_<26-base32>`
  wire format.
- [ ] Plaintext tokens are never stored; only `sha256(token)` and public
  6-character prefix are persisted.
- [ ] Service-account rotation allows two simultaneously valid tokens for at
  least 24 hours.

## Dependency Blockers

- Sprint 1 repo skeleton must exist before implementation. The current
  workspace is documentation-heavy and does not expose a Go module or `.git`
  repository at `D:/projects/midnight`; code tasks assume the locked layout
  will be instantiated.
- S03-001 depends on Sprint 2 role/provisioning work (`MIDNIGHT-S02-012`) and
  Sprint 1 repository foundations (`MIDNIGHT-S01-001`).
- S03-003 and S03-013 depend on the Sprint 2 bootstrap token file path and
  service unit (`MIDNIGHT-S02-010`).
- Token enum drift blocks clean schema/API codegen until resolved.
- Prefix taxonomy drift blocks strict ID validation unless API helper resources
  are hidden or the OpenAPI reserved-prefix list expands.
- Environment tier spelling drift blocks generated DTO reuse without a mapper.
- Idempotency TTL direction drift must be settled in migration tests before any
  GC/reaper code lands.
- `pg_partman` is recommended but not a hard dependency; the daily cleanup job
  must cover partition creation/pruning when `pg_partman` is absent.
- Contract-test work depends on a bootable ephemeral server and OpenAPI artifact
  generation.

## Exact First Implementation Tasks

1. Instantiate the locked repo skeleton from `REPO_LAYOUT.md` if it is not
   already present: `go.mod`, `cmd/`, `internal/`, `pkg/`, `api/`,
   `migrations/`, `tools/`, and `docs/`.
2. Add `pkg/orailtypes` with `ID`, `ResourceVersion`, public state enums,
   error-code type aliases, cursor/page DTOs, and Operation/Job DTO shells.
3. Add `internal/clock` and require all S03 packages to inject time through it.
4. Create `migrations/0001_alpha_core.reversible.sql` from
   `SCHEMA_PHASE_0_ALPHA.sql`, preserving 29 parent tables and starter
   partitions. Add goose markers and a down migration only where reversible.
5. Create follow-up migrations for failure-code seed rows and table tuning
   (`jobs` autovacuum/fillfactor at minimum).
6. Add `internal/db` migration runner and repository interfaces matching
   `INTERFACES_CONTROL_PLANE.md`; wire testcontainers for integration tests.
7. Add schema tests: apply migrations to Postgres 15+, verify table count,
   partition parents/children, grants, indexes, and schema-only dump snapshot.
8. Add `internal/failurecodes` generator from the registry source and copy the
   published registry to `docs/error-codes.md`.
9. Add `internal/api` response encoder and `internal/api/middleware/requestid`
   with `X-Request-Id` and trace context tests.
10. Add `internal/api/middleware/apierr` or equivalent error envelope package
    that only emits registered codes.
11. Add `internal/auth/token` parser/generator/verifier, sha256 lookup, prefix
    extraction, token class mapping, and rejection tests for malformed headers.
12. Add rate-limit middleware with the pinned class budgets and required
    headers.
13. Add `cmd/midnight-server` composition root with health/compat endpoints,
    middleware chain, and OpenAPI serving.
14. Move or generate the OpenAPI artifact at `api/openapi/v1alpha/midnight.yaml`
    from `agile/engineering/contracts/OPENAPI_V1ALPHA.yaml`; add a byte-for-byte
    runtime serving test.
15. Add `internal/idempotency`: key reservation, canonical JSON hashing, state
    matrix, replay storage, and middleware integration.
16. Add `internal/api` demo async endpoint `POST /api/v1alpha/_demo/long` to
    exercise idempotency plus operations without depending on build/deploy.
17. Add `internal/api` Operation handlers for `GET /operations/{id}` and
    `GET /operations/{id}/events`, including polling headers and SSE replay.
18. Add `internal/queue` with the exact dequeue, heartbeat, fail, complete, and
    reclaim SQL from `SCHEMA_QUEUE_CONTRACT.md`.
19. Add queue integration tests with 8 workers, 10k jobs, lease-crash reclaim,
    and fairness by `partition_key`.
20. Add bootstrap endpoint skeleton tied to `bootstrap_tokens`, password hash,
    TOTP enrollment, auto-disable, and token issuance.
21. Add OpenAPI lint checks: no `total_count`, all errors reference registry
    codes, all mutating operations require `Idempotency-Key`.
22. Add Schemathesis scaffold for `_demo/long`, error envelope, idempotency
    replay, cursor tampering, and coverage artifact output.
23. Add Pact scaffolds for CLI, dashboard, Terraform, and MCP with the first
    interaction: list projects returns cursor-paginated response.
24. Add CI tasks for migrations, tests, OpenAPI generation, oasdiff breaking
    checks, additive-only error codes, Schemathesis, and Pact provider verify.

## Ready-To-Implement Notes

- Prefer OpenAPI/Sprint contract for public wire behavior; prefer
  `SCHEMA_PHASE_0_ALPHA.sql` plus `SCHEMA_NOTES.md` for physical schema shape.
- Do not let handlers execute business side effects before idempotency reserves
  the key.
- Keep `internal/api/middleware` free of database imports except through
  injected auth/idempotency/rate-limit interfaces where needed.
- The API may enqueue work, but workers do not import the API.
- Agent and worker packages must not read tables directly unless their owning
  lane explicitly exposes a repository; the host agent never gets SQL access.
- Treat every mutating endpoint as audit-eligible even if S03 only wires the
  interface and later lanes complete audit-chain internals.
