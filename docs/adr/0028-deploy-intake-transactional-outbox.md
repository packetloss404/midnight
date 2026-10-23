# ADR-0028: Deployment work uses transactional Postgres outboxes

- Status: accepted
- Date: 2026-07-27

## Context

The REST deploy and `midnight up` handlers previously wrote five independent
records in order: idempotency attachment, operation, canonical deployment,
queue job, and events. A process failure after the operation/deployment writes
but before queue insertion left accepted-looking product truth with no worker
work capable of advancing it.

## Decision

Postgres-backed REST deploy/up intake commits the following in one database
transaction:

1. the queued operation;
2. the queued canonical deployment with its unique trigger key;
3. initial operation and deployment events;
4. the idempotency operation linkage and 202 response state; and
5. one `deploy_intake_outbox` row containing the complete build/deploy job
   payload.

The API returns after this transaction; delivery belongs to the background
dispatcher. A 202 means the durable outbox owns the intent and does not claim
that a `jobs` row already exists. `X-Midnight-Delivery-State: pending` makes
that boundary explicit.

Dispatchers claim outbox rows with `FOR UPDATE SKIP LOCKED` and an expiring
lease. Job insertion uses `jobs.outbox_id`, which is unique. Job insertion,
outbox delivery state, and the delivered operation event commit in one
transaction. A crash before that transaction leaves an expiring claim; a crash
after it leaves both the job and delivered marker. Either case is safe to retry.
Exhausted delivery attempts fail the operation and queued deployment instead of
leaving false progress.

Memory-mode routers retain the direct in-process queue path. They are not
durability evidence.

The production GraphQL `serviceInstanceDeployV2` mutation uses the same
Postgres authority through a transport-neutral coordinator. One SERIALIZABLE
transaction now commits the operation, canonical deployment, initial events,
actor/tenant-scoped idempotency result, selected GraphQL service-result bytes,
audit row, and deploy outbox. The authenticated recovery actor comes from the
HTTP handler; the tenant comes from locked service/project/workspace lineage.
The mutation must be the only side-effecting top-level field, so replay can
reconstruct the full GraphQL HTTP response byte-for-byte without re-running a
sibling mutation. Memory mode deliberately retains the legacy direct-queue
compatibility path and is still not durability evidence.

Migration 0025 extends the same delivery contract with a generic
`staged_work_outbox`. Its stage-specific dedupe keys cover:

- GitHub push intake, where operation, canonical deployment, initial events,
  and build work are committed together; and
- build-to-deploy handoff, where the immutable source/image result and deploy
  work are committed together.

The build worker and API server may both dispatch staged work. Expiring leases
and `jobs.outbox_id` uniqueness make that multi-process delivery safe.

## Residual boundaries

- Idempotency reservation intentionally occurs before the intake transaction so
  concurrent requests are serialized. A crash between reservation and commit
  returns `IDEMPOTENCY_REQUEST_IN_PROGRESS` until the reservation's 24-hour TTL;
  it cannot return a false accepted response.
- `midnight up` stores its extracted checkout on the API host and places that
  path in the outbox payload. A process restart on the same persistent
  filesystem is recoverable. Loss of the host/filesystem is not; content-addressed
  upload storage is still required for host-independent source durability.
- Audit-chain emission is post-commit and best effort. The operation and
  deployment event ledgers are the transactional intake evidence.
- GitHub/build records created by releases before migration 0025 are not
  retroactively reconstructed when their historical queue evidence is absent.
- Delivery failure details are durable in `deploy_intake_outbox.last_error` and
  operation events. A first-class administrative outbox API is not yet exposed.
