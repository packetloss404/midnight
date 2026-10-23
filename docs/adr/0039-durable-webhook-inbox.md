# ADR-0039: Verified webhooks use a durable at-least-once inbox

- Status: accepted
- Date: 2026-07-29

## Context

Midnight previously reserved a provider delivery identifier and then called
the GitHub dispatcher synchronously. A process exit, database error, or
dispatcher failure after reservation but before durable build intake left no
payload to retry. The provider's next delivery was treated as an idempotent
replay, so valid autodeploy work could be lost.

Retaining a signed body is necessary but is not sufficient. A delayed worker
must not discover a source link created after the webhook arrived, a stale
worker must not complete work after losing its lease, and one failing service
must not force already-completed targets to run again. Recovery also needs an
operator surface that does not expose source payloads or signing metadata.

## Decision

Migration 0050 replaces the dedupe-only delivery table with a durable inbox
and immutable per-service target rows. Historical migration-0005 rows are
renamed and retained as inert evidence; they cannot be redriven because their
payloads were never stored.

The production receiver follows this order:

1. bound the body to 25 MiB and verify the configured endpoint and signature;
2. derive the payload and event hashes;
3. commit the complete verified payload, immutable envelope metadata, and
   acceptance audit row in one PostgreSQL transaction; and
4. acknowledge only after that transaction commits.

For a GitHub push, the acceptance transaction first acquires the same
installation advisory lock used by grant/link mutation and revocation. A
mutation that commits first receives an authority timestamp before the
database receipt; a webhook that commits first forces the later mutation to
receive a timestamp after the receipt. Temporal target discovery therefore
follows transaction commit order rather than an application timestamp assigned
inside an uncommitted transaction.

The durable identity is
`(provider, endpoint_id, provider_delivery_id)`. An exact immutable-envelope
replay returns the original inbox identity with HTTP 200. Reuse with a
different body hash, length, event hash, event ID, or event type is a security
conflict and returns HTTP 409. Signing-key rotation does not turn an otherwise
exact replay into a conflict. Invalid signatures are never retained.

Workers claim deliveries and targets with expiring random lease tokens. Every
state mutation checks the current token and lease expiry, and long-running
delivery ownership is renewed. Exhausted planning or target attempts become
dead-letter state instead of remaining unclaimable. Retained delivery and
target payload hashes are checked before they reach a planner or dispatcher;
corruption is terminal and is not silently retried.

A persisted plan must contain unique target keys. The target inserts and
delivery routing-complete marker share one transaction; any duplicate or
database conflict rolls the entire plan back instead of silently collapsing
different snapshots.

Durable GitHub planning uses the database receipt timestamp. It selects only
installation grants, repository grants, repository verification, and source
links whose relevant creation, update, and verification timestamps are no
later than that receipt. The resulting grant/link versions and timestamps are
stored with each immutable service target. A service linked after receipt is
therefore never added by a later retry.

Target dispatch revalidates the exact installation-grant,
repository-grant, and link resource versions inside the same installation lock
used by revocation. It then commits canonical operation, deployment, staged
outbox, and build work through the existing idempotent intake transaction.
A process may die after that downstream commit and before marking the inbox
target complete; repeating the target is safe because canonical trigger and
staged-intake keys return the existing result.

The delivery contract is **at least once**, not exactly once. Lease expiry can
cause a target to be attempted again, and every downstream consumer must
remain idempotent.

Recovery operators can list delivery metadata, inspect target metadata, and
redrive a dead letter:

```text
GET  /api/v1alpha/webhook-deliveries
GET  /api/v1alpha/webhook-deliveries/{delivery_id}
POST /api/v1alpha/webhook-deliveries/{delivery_id}/redrive
```

The redrive body is exactly empty. Inbox state, the exact idempotency response,
and audit evidence commit in one serializable transaction. A redrive is
rejected when retained payload integrity fails. These responses never include
the verified body, target dispatch payload, signature, signing-key identifier,
or lease token.

Terminal delivery bodies and target dispatch payloads are retained for 30
days. A boot-and-daily bounded maintenance pass then nulls only those bytes
while preserving provider identity, hashes, attempts, outcomes, target
metadata, result IDs, and audit history. Pending, retrying, or leased work is
never purged. A dead letter whose payload has aged out remains inspectable but
cannot be redriven.

Memory mode deliberately retains the historical synchronous dispatcher for
local compatibility and reports durable recovery as unavailable. It is not
evidence for durable autodeploy.

## Consequences

- HTTP 202 now means a verified delivery is durable, not that planning,
  building, or deployment has succeeded.
- Exact provider replays do not create duplicate targets or deployments.
- Source authority is frozen at database receipt for target discovery and
  revalidated at durable build intake for current revocation safety.
- One target can retry or dead-letter independently while completed targets
  remain terminal.
- PostgreSQL stores signed source payloads for retry. Terminal payload bytes
  are purged after the 30-day recovery window; metadata and hashes remain.
  Operator APIs and audit rows expose only bounded metadata.
- Historical pre-0050 rows remain non-redrivable by design.
- Migration 0050 refuses to roll back while any v50 delivery exists rather
  than silently deleting acknowledged work. Draining or archiving it requires
  an explicit destructive recovery procedure outside schema rollback.
- A live private-repository delivery and installed process-death matrix remain
  external evidence. This ADR does not claim exactly-once delivery or hosted
  GitHub availability.
