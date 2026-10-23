# ADR-0032: Human product mutations advance through one atomic coordinator

- Status: accepted
- Date: 2026-07-28

## Context

ADR-0031 established durable sessions, tenant ownership, and RBAC, then kept
human product writes closed. The legacy mutation handlers reserve
idempotency, authorize, write product rows, and emit audit evidence through
separate calls. That ordering has time-of-check/time-of-use, partial-write,
cross-actor replay, and audit-loss windows.

Adding a human bearer to those handlers would turn an authenticated session
into installation-wide write access. Namespacing a client key by actor alone
would also leave cross-tenant ambiguity and an ABA race when an expired key is
reused while a late request still holds the old attempt.

## Decision

Human product writes are enabled one REST route at a time through a
Postgres-only `TenantMutationCoordinator`. A route is admitted only when one
retryable `SERIALIZABLE` transaction:

1. revalidates and locks the current session and user generation;
2. resolves the canonical tenant and serializes with membership changes;
3. locks the active membership and evaluates a typed `identity.Capability`;
4. reserves or replays idempotency under
   `(actor, tenant, method, route template, client key)`;
5. writes every product row and durable work intent;
6. stores the exact terminal HTTP response; and
7. appends actor- and grant-attributed evidence to the audit hash chain.

Every reservation attempt has a cryptographically random generation. State
transitions present the complete immutable reference and use compare-and-set
predicates. Expired attempts become retained `EXPIRED` history; a late request
cannot mutate a replacement generation. Terminal responses are absorbing.

Replay is an authorization decision, not possession of a client key. The
coordinator revalidates the current session and capability before returning a
stored response. Different sessions for the same user share the actor
namespace; different actors and tenants remain independent.

Migration 0039 backfills legacy rows into explicit installation scope, adds
tenant and generation columns, preserves unsafe legacy rows as expired
history, and refuses a downgrade that would collapse scoped attempts.

The first admitted route was `POST /api/v1alpha/projects`. Its transaction
creates the project, protected default production environment, creator owner
grant, exact `201` response, and audit row together. The same coordinator now
admits `POST /api/v1alpha/services`, committing service, exact response,
idempotency, and audit state together.

Loop 3.1 admits ordinary human
`POST /api/v1alpha/environments` through the same coordinator. The transaction
resolves and locks the exact project/workspace lineage, revalidates
`environment.write`, reserves project-scoped idempotency, creates one
unprotected environment, stores the exact `201` or terminal slug-conflict
response, and appends audit evidence. Protected creation is denied after
current authority is revalidated but before any reservation or product row.
Human delete and duplicate routes remain closed.

Loop 3.3 separately admits exact human
`PATCH /api/v1alpha/environments/{environment_id}` metadata updates. The
request is a strict bounded object with positive `expected_version` and at
least one explicitly present `slug`, `name`, or `tier`; it cannot name a
project, protection flag, duplication/copy option, delete action, or runtime
change. The transaction resolves and locks the canonical
environment/project/workspace lineage, revalidates `environment.write` plus
the restricted-environment overlay, reserves actor/project/concrete-route
idempotency, and compare-and-swaps `resource_version`. It commits the metadata,
exact `200` or terminal version/slug-conflict response, idempotency state, and
audit evidence together. The recovery operator retains its legacy wider PATCH
body as a compatibility surface; that does not widen HumanSession authority.

The first admitted deployment route is the narrower
`POST /api/v1alpha/services/{service_id}/deployments`. For human sessions it
accepts exactly `{environment_id,image}`, requires an immutable lowercase
SHA-256 digest, and commits the operation, deployment, initial events, exact
`202` response, deploy-outbox intent, and audit entry in one transaction.
Replay is scoped to the actor, project, concrete service route, exact accepted
body, and client key. Authorization denials leave no idempotency, product,
outbox, or audit artifact.

The exact
`POST /api/v1alpha/services/{service_id}/deployments/{deployment_id}/actions/{action}`
route now admits `redeploy`, `rollback`, `restart`, `stop`, and `remove` for
human sessions through a separate PostgreSQL coordinator. The request body is
empty. The coordinator resolves the target environment from the canonical
deployment, revalidates `deployment.trigger.cli` for replay actions or
`service.write` for runtime actions, reserves actor/project/concrete-route
idempotency, commits the existing durable action/outbox machinery, attaches
exact operation lineage, stores the `202` response, and appends audit evidence
in one serializable transaction. Runtime actions are serialized per deployment
and a second distinct action conflicts while one is queued or running. CLI and
GUI callers use this REST boundary; GraphQL does not inherit the capability.

ADR-0036 separately admits the exact empty-body
`POST /api/v1alpha/operations/{operation_id}/cancel` route. Its transaction
fences deploy and staged-work outboxes plus pending jobs, records a durable
request for leased workers, and converges canonical operation/deployment state
at a named worker safe point. It does not admit the recovery-only failure
endpoint or promise interruption of an active BuildKit solve.

## Lock order

The project-create path acquires:

1. `workspace-members:<workspace_id>` advisory lock;
2. session and user rows;
3. workspace membership and workspace rows;
4. the scoped idempotency advisory lock; and
5. the audit-chain head lock.

Membership mutations already use the first lock. A demotion committed before
the mutation lock is released is therefore visible to the authorization
decision; a mutation that linearizes first may finish before the demotion.
Service and environment creation acquire the workspace-members lock followed
by the project-members lock before revalidating session and membership rows,
then take idempotency and audit locks. Environment metadata update resolves the
immutable lineage, takes those membership locks, revalidates current session
and grants, locks the exact environment row including restriction and version,
then takes the scoped idempotency and audit locks. Restricted non-admin access
is concealed before replay.

## Consequences

- Memory-mode and custom-store human writes remain unavailable.
- Operator/developer compatibility routes continue to use the generic store,
  but their state transitions now carry the same generation-fenced reference.
- Deployment and outbox trigger keys are derived from the server-minted
  accepted operation, not the client key. Distinct actors, routes, and
  generations can therefore use the same client key without colliding in the
  globally unique durable-work namespace.
- The durable deploy outbox, official-template saga, and environment duplicate
  transaction use the transaction-aware idempotency primitives instead of
  raw-key SQL.
- GraphQL, MCP, tenant failure marking, custom domains/TLS, shared variables,
  volumes, and every other unlisted human mutation remain closed until
  individually migrated and tested through a coordinator. Content-addressed
  `up` is separately admitted by ADR-0035, operation cancellation by ADR-0036,
  the first-deploy generated-route slice by ADR-0037, GitHub source linking by
  ADR-0038, ordinary environment metadata/config/variable coordinators by
  Loops 3.2–3.4, and the service-managed generated-domain lifecycle by
  ADR-0040.
- Raw request bytes are still the body digest input. RFC 8785 canonical JSON
  is a remaining contract gap and must land before semantically equivalent
  JSON bodies are claimed to share a replay fingerprint.

## Verification gate

Each admitted route requires durable tests for:

- same-user replay across sessions;
- different actors and tenants using the same client key independently;
- server work triggers remaining distinct from client replay keys;
- viewer and cross-tenant denials leaving zero artifacts;
- membership demotion racing the request;
- revocation and loss of capability denying replay; and
- one project, protected production environment, owner grant, terminal
  response, and audit row after restart-capable Postgres execution.

The immutable-image route additionally requires live PostgreSQL proof that
concurrent identical requests produce one operation, deployment, outbox
intent, and reservation; both callers receive the exact same stored response;
only one response is marked replayed; and revocation, grant removal,
cross-tenant scope, mutable images, unknown fields, and unsupported source or
route intent all fail closed.

The deployment-action route additionally requires live PostgreSQL proof for
concurrent same-key replay across sessions, runtime and image action lineage,
current-role denial before replay, cross-service concealment, queued/running
runtime conflict fencing, and audit-failure rollback with zero partial state.

The environment-create route additionally requires live PostgreSQL proof for
same-user cross-session replay, project-editor admission, viewer and
cross-tenant denial, revoked-session and removed-grant denial before replay,
terminal slug-conflict replay, audit-failure rollback, protected-create
zero-artifact denial, and one pre-minted identity across serializable/unique
conflict retry.
