# ADR-0036: Tenant cancellation fences durable work and converges at worker safe points

- Status: accepted
- Date: 2026-07-29

## Context

The legacy operation cancel handler changes an operation record, but a
deployment can already exist in a durable outbox, a pending queue job, or a
worker lease. Changing only the operation leaves runnable work behind and can
produce a canceled operation whose canonical deployment later becomes active.
Admitting that path to a human session would also bypass the current-authority,
scoped-idempotency, and audit rules in ADR-0032.

Cancellation is not the same as the recovery-only operation fail endpoint. A
user asks Midnight to stop owned deployment work. A recovery operator records
an externally diagnosed failure. The latter remains outside the tenant product
surface.

## Decision

The exact empty-body
`POST /api/v1alpha/operations/{operation_id}/cancel` route is admitted to a
human session only through `PostgresTenantOperationCancelCoordinator`.
The coordinator requires `deployment.trigger.cli` on the canonical
deployment's current project and environment. Project automation credentials,
GraphQL, MCP, memory stores, and custom stores do not inherit this authority.

One retryable `SERIALIZABLE` transaction:

1. resolves exactly one canonical deployment lineage;
2. revalidates the human session and current typed capability;
3. reserves actor/project/concrete-route idempotency for the exact empty body;
4. changes pending or delivering deploy and staged-work outboxes to
   `canceled`;
5. cancels pending build/deploy jobs and locks leased jobs;
6. terminalizes the canonical deployment and operation immediately when all
   work was withdrawn before a lease;
7. otherwise appends a durable cancel-request event while leaving leased work
   in its truthful queued/running state;
8. stores the exact `202` response; and
9. appends capability-, session-, tenant-, and work-attributed audit evidence.

Migration 0047 adds the terminal `canceled` state to both durable outbox
constraints. Dispatchers do not select that state. Their job insertion and
delivered marker already share one transaction: if cancellation wins the
outbox row, a stale delivery rolls its job back; if delivery wins, cancellation
sees and cancels the pending job.

Build and deploy workers observe the durable request at named safe points.
They atomically terminalize the canonical deployment and operation before
deploy handoff or traffic promotion. A request received during an in-progress
BuildKit solve does not interrupt the solve; it is honored after the solve and
before deploy handoff. Midnight therefore claims safe-point cancellation, not
instant process interruption.

The recovery-only `POST /operations/{id}/fail` route remains operator-only.

## Lock order

The accepting transaction acquires:

1. current session/grant and scoped-idempotency locks;
2. deploy/staged outbox rows;
3. pending and leased job rows;
4. the operation row;
5. the canonical deployment row; and
6. the audit-chain head.

Worker completion acquires operation then deployment. Dispatcher delivery
acquires its outbox row before committing a queue job, and queue workers no
longer hold a job transaction while invoking the canonical completion seam.

## Consequences

- A pre-lease cancellation returns an operation already in `CANCELED`.
- A leased cancellation returns the truthful queued/running operation stored
  at acceptance and advertises a worker safe point in its audit record.
- Idempotent replay returns the exact stored response only after current
  authorization is revalidated.
- A distinct cancel key after terminal convergence receives the normal invalid
  transition conflict.
- Runtime-action operations without their own canonical deployment lineage are
  not admitted by this route.
- Cancellation does not promise interruption of an active BuildKit solve,
  removal of an already promoted deployment, or multi-host process signaling.

## Verification

Disposable PostgreSQL coverage runs cancellation against real migrations,
sessions, RBAC, idempotency, outboxes, jobs, operation/deployment FSMs, and the
audit chain. It proves concurrent same-key replay across sessions, viewer and
cross-tenant denial with zero artifacts, demoted replay denial, dispatcher
races, immediate pre-lease convergence, leased-worker request fencing,
worker-side canonical convergence, and audit-failure rollback. Worker unit
tests separately prove build safe-point recognition and deploy-worker use of
the atomic canonical completion seam.
