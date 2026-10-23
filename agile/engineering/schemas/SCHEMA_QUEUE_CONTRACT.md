# SCHEMA_QUEUE_CONTRACT.md — Postgres-as-queue contract

Status: normative. Pairs with the `jobs` table in
`SCHEMA_PHASE_0_ALPHA.sql` and the panel decision:

> "Postgres job queue contract is normative: columns include
> `lease_owner`, `lease_expires_at`, `heartbeat_at`, `attempt`,
> `available_at`, `priority`, `queue`, `partition_key`. Dequeue uses
> `SELECT … FOR UPDATE SKIP LOCKED` ordered by `(priority,
> available_at)` filtered by `queue` and lease availability. Heartbeat
> cadence = `lease_ttl / 3`; expired leases are reclaimed automatically.
> Separate per-class queues with independent worker pools and bounded
> concurrency: `build`, `deploy`, `reconcile`, `webhook`, `cleanup`.
> Per-tenant fairness via weighted-fair dequeue."

This document pins the SQL fragments, lease lifecycle, fairness
algorithm, backpressure rules, dead-letter behavior, and cancellation
safe-points that every worker pool must implement. It supersedes any
informal description elsewhere.

---

## 1. The dequeue SQL (exact, normative)

The dequeue is one statement: a `WITH cte` that selects-and-locks the
single best candidate, followed by an `UPDATE` that takes the lease.
Both run inside the same statement so a worker that crashes after the
SELECT but before the UPDATE never holds a phantom lease.

```sql
-- Bind variables:
--   :worker_id  TEXT     -- stable id for this worker process
--   :queue      TEXT     -- one of 'build','deploy','reconcile','webhook','cleanup'
--   :lease_ttl  INTERVAL -- per-queue default; see "Lease TTL" below

WITH cte AS (
    SELECT id, created_at
    FROM   jobs
    WHERE  state = 'pending'
      AND  queue = :queue
      AND  available_at <= now()
      AND  (lease_expires_at IS NULL OR lease_expires_at < now())
    ORDER  BY priority DESC, available_at ASC
    FOR    UPDATE SKIP LOCKED
    LIMIT  1
)
UPDATE jobs j
SET    state            = 'leased',
       lease_owner      = :worker_id,
       lease_expires_at = now() + :lease_ttl,
       heartbeat_at     = now(),
       attempt          = j.attempt + 1,
       updated_at       = now()
FROM   cte
WHERE  j.id = cte.id
  AND  j.created_at = cte.created_at  -- partition-key match (jobs is partitioned)
RETURNING j.id, j.payload, j.attempt, j.max_attempts, j.partition_key,
          j.parent_operation_id, j.created_at;
```

Notes:

- The **runtime predicate** `lease_expires_at IS NULL OR lease_expires_at < now()`
  is duplicated in the `WHERE` and not pushed into the index predicate —
  Postgres rejects `now()` inside an index `WHERE`. The
  `jobs_dequeue_idx` partial index uses `state = 'pending'` instead, and
  the reaper (section 3) keeps the index accurate by transitioning
  expired-leased rows back to `'pending'`.
- The CTE's `LIMIT 1` is critical. `FOR UPDATE SKIP LOCKED` plus
  `LIMIT 1` is the textbook anti-thundering-herd shape: 100 workers see
  100 different rows.
- `created_at` is matched in the outer `UPDATE` because `jobs` is
  partitioned by `created_at`; the partition key must be present in the
  predicate so the planner can prune partitions during the update.

## 2. Lease / heartbeat lifecycle (state diagram)

```
                +-------------------------+
   enqueue -->  |        pending          |  <----------------+
                +-------------------------+                   |
                          |                                   |
                          | dequeue (acquire lease)           |
                          v                                   |
                +-------------------------+                   |
                |        leased           |                   |
                |  lease_owner = wid      |                   |
                |  lease_expires_at = …   |                   |
                +-------------------------+                   |
                  |        |        |                         |
   heartbeat ----+         |        +----- worker crash       |
   (extends             worker         (no heartbeat)         |
    lease)              succeeds /     => lease_expires_at    |
                        fails          is reached             |
                          |                                   |
                          v                                   |
                  +-----------------+   reaper requeues       |
                  | succeeded |     |   if attempt < max      |
                  | failed    | -- (terminal) ----+           |
                  | canceled  |                   |           |
                  +-----------------+             |           |
                                                  |           |
                  if attempt >= max_attempts ---> dead_letter |
                                                              |
                  reaper requeues (attempt < max) ------------+
```

Worker contract:

1. **Acquire lease.** Run the dequeue SQL. If the `RETURNING` set is
   empty, sleep with jittered backoff (default 1s ± 250ms) and retry.
2. **Heartbeat.** Every `lease_ttl / 3`, run:

   ```sql
   UPDATE jobs
   SET    lease_expires_at = now() + :lease_ttl,
          heartbeat_at     = now(),
          updated_at       = now()
   WHERE  id = :job_id
     AND  created_at = :job_created_at
     AND  lease_owner = :worker_id
     AND  state = 'leased';
   ```

   If `0 rows affected`, **the worker has lost its lease**. The worker
   MUST abort the in-flight unit of work, MUST NOT commit any further
   side effects, and MUST exit the lease loop. Side effects already
   committed before lease loss are the worker's responsibility to make
   idempotent (the deploy/build worker contract requires this).

3. **Complete (success).**

   ```sql
   UPDATE jobs
   SET    state       = 'succeeded',
          finished_at = now(),
          lease_owner = NULL,
          lease_expires_at = NULL,
          updated_at  = now()
   WHERE  id = :job_id
     AND  created_at = :job_created_at
     AND  lease_owner = :worker_id
     AND  state = 'leased';
   ```

4. **Complete (failure).** Same as success but with
   `state='failed'`, `reason_code=$code`. The reaper will requeue if
   the worker classified the failure as retriable (see step 5);
   otherwise the row stays terminal until manual replay or cleanup.

5. **Schedule retry (worker-classified retriable).**

   ```sql
   UPDATE jobs
   SET    state            = 'pending',
          lease_owner      = NULL,
          lease_expires_at = NULL,
          available_at     = now() + LEAST('1 hour'::interval,
                              ('30 seconds'::interval) * power(2, attempt)),
          reason_code      = $code,
          updated_at       = now()
   WHERE  id = :job_id
     AND  created_at = :job_created_at
     AND  lease_owner = :worker_id
     AND  state = 'leased';
   ```

   The exponential backoff is bounded at one hour. Jitter (±25%) is
   applied client-side before the SQL runs so a thundering reattempt is
   spread over a window.

## 3. Reclaim semantics on worker crash

A small reaper goroutine runs every 10 seconds (per process, with
jittered offset). It executes:

```sql
UPDATE jobs
SET    state =
         CASE WHEN attempt >= max_attempts THEN 'dead_letter' ELSE 'pending' END,
       lease_owner      = NULL,
       lease_expires_at = NULL,
       available_at     = now() + (
         CASE WHEN attempt >= max_attempts THEN '0 seconds'::interval
              ELSE LEAST('1 hour'::interval,
                         ('30 seconds'::interval) * power(2, attempt))
         END),
       reason_code      = COALESCE(reason_code, 'LEASE_EXPIRED'),
       dead_letter_reason = CASE WHEN attempt >= max_attempts
                                 THEN 'LEASE_EXPIRED_MAX_ATTEMPTS'
                                 ELSE NULL END,
       updated_at       = now()
WHERE  state = 'leased'
  AND  lease_expires_at < now();
```

Reclaim correctness depends on three invariants:

1. The worker's `heartbeat_at` and `lease_expires_at` are bumped
   atomically (section 2 step 2). A worker that survives must observe
   `lease_expires_at > now()` continuously; a worker that does not
   survive cannot bump these columns.
2. The dequeue SQL filters on `lease_expires_at IS NULL OR
   lease_expires_at < now()`, so a row mid-reclaim (still `'leased'`
   with expired lease) is invisible to dequeue until the reaper
   transitions it back to `'pending'`. There is no double-leasing
   window.
3. The worker's side effects must be idempotent. The build and deploy
   workers achieve this via image-digest pinning and the snapshot
   immutable-by-construction rule (see
   `FEATURE_DEPLOYMENT_STATE_MACHINE.md`); the reconcile worker via
   guarded `UPDATE … WHERE state=$from AND resource_version=$rv`; the
   webhook delivery worker via the delivery-id dedupe table (v1).

## 4. Per-class queue routing and worker pools

Per panel: separate per-class queues with independent worker pools.
Pinned values:

| Queue        | Default `lease_ttl` | Default pool size (per host)         | Max in-flight per queue (host) | Notes |
|--------------|---------------------|--------------------------------------|--------------------------------|-------|
| `build`      | `300s` (5min)       | `min(4, cores/2)`                    | pool size                      | Per panel: per-host build concurrency `min(4, cores/2)`; per-project concurrency 2 (enforced in worker, not in SQL). |
| `deploy`     | `60s`               | `4`                                  | pool size                      | The reconciler adds DEPLOYING -> ACTIVE on top; deploy workers do not promote. |
| `reconcile`  | `30s`               | `2`                                  | pool size                      | Polls at ~1Hz; rate-limited per `(service, environment)` to avoid hot-loops. |
| `webhook`    | `30s`               | `8`                                  | pool size                      | Outbound webhook delivery (signed). Per-endpoint token-bucket on top. |
| `cleanup`    | `120s` (2min)       | `2`                                  | pool size                      | Backup runs, partition pruning, audit checkpoint, GC sweeps, pin decrement. |

Each pool is independent: `build` saturating its pool does not block
`deploy` from making progress. The dequeue query is filtered by
`queue = :queue`, so the partial index `jobs_dequeue_idx` covers each
queue's hot path with the same shape.

OPEN QUESTION: `lease_ttl` defaults above match
`FEATURE_POSTGRES_JOB_QUEUE.md` proposals. The dossier flags these as
"confirm" items; the recommendation here is to ship with these values
and tune in the Alpha drills.

## 5. Per-tenant fairness algorithm

**Choice: weighted-fair dequeue, not token bucket.**

Rationale:

- **Token bucket** is an admission-control primitive — it is great for
  rate-limiting an external client, but it does not solve the
  starvation problem. A noisy tenant that fills its bucket every
  second does not yield to a quiet tenant whose bucket is full and
  whose work is otherwise eligible.
- **Weighted-fair dequeue** explicitly demotes partitions
  (`partition_key`s) that have been over-served in the recent window.
  This is exactly the property the panel asks for: every tenant gets
  bounded share, and a tenant with no work in the window does not lose
  its share to a tenant that submits a deluge.

The algorithm is implemented as an additional `AND` clause in the
dequeue CTE:

```sql
-- Demote partitions that have been served > fair_share times in the
-- last 60s window. The jobs_recent_served table is a small materialized
-- view maintained by the reaper at the bottom of every dequeue.
AND partition_key NOT IN (
    SELECT partition_key
    FROM   jobs_recent_served
    WHERE  served_at > now() - interval '60 seconds'
    GROUP  BY partition_key
    HAVING count(*) > :fair_share
)
```

Implementation rules:

- `fair_share = ceil(pool_size * 1.5)`. Pool size 4 -> fair share 6.
  This lets a tenant burst for one window-length but not starve others.
- The window is **60 seconds**. Shorter (30s) creates excessive churn
  on `jobs_recent_served`; longer (5min) reacts too slowly to a real
  starvation event.
- `jobs_recent_served` is a small append-only table with a per-row TTL
  enforced by the reaper: rows older than 5 minutes are pruned.
  Schema (kept out of `SCHEMA_PHASE_0_ALPHA.sql` because it is a
  derived/operational table, owned by the worker library):

  ```sql
  CREATE TABLE jobs_recent_served (
      partition_key text        NOT NULL,
      queue         text        NOT NULL,
      served_at     timestamptz NOT NULL DEFAULT now(),
      worker_id     text        NOT NULL
  );
  CREATE INDEX jobs_recent_served_window_idx
      ON jobs_recent_served (queue, partition_key, served_at);
  ```

- The fair filter is applied **per queue**: a noisy `build` tenant
  does not slow another tenant's `webhook` queue.
- If every eligible row is demoted (rare; happens when one tenant has
  all the queued work), the dequeue falls back to the unfiltered query
  after the window passes. Workers detect this by seeing zero rows for
  N seconds and re-trying without the fairness filter; the
  worker-library default N is 5 seconds.

OPEN QUESTION: 60s vs 5m window — `FEATURE_POSTGRES_JOB_QUEUE.md` open
question 5. We have committed to 60s here.

## 6. Backpressure rules

Three backpressure surfaces. All are configuration values; defaults
ship enabled.

### 6.1 Max in-flight per queue (per host)

Equal to the pool size for that queue (table in section 4). When all
workers in the pool are leased, the dequeue loop blocks; new arrivals
wait in `pending`. No additional admission control needed.

### 6.2 Max total queue depth

Per queue, total `pending` rows across the whole instance:

| Queue        | Soft limit | Hard limit |
|--------------|------------|------------|
| `build`      | 50         | 200        |
| `deploy`     | 100        | 500        |
| `reconcile`  | 1000       | 5000       |
| `webhook`    | 500        | 5000       |
| `cleanup`    | 100        | 1000       |

At soft limit, `doctor` raises an alert and the API returns
`Retry-After` on enqueue endpoints. At hard limit, enqueue returns
`429 RATE_LIMITED` for non-system actors; system enqueues
(reconciler-driven, audit checkpoint) bypass the hard limit because
they are bounded by their own pool size.

### 6.3 Per-tenant in-flight cap

Each tenant (`partition_key` = `workspace_id` for Alpha) is capped at
`fair_share * 4` simultaneous `pending`+`leased` rows per queue. This
is the long-window guard against a tenant filling the queue with weeks
of work. Beyond the cap, enqueue returns `429 RATE_LIMITED` on the API
boundary; the worker library refuses internal enqueue with
`OPERATION_SUPERSEDED` if the supersession path applies.

## 7. Dead-letter behavior

A row enters `state = 'dead_letter'` when:

- The reaper observes `attempt >= max_attempts` and `lease_expires_at <
  now()` (panel pin).
- The worker explicitly failed and classified the failure as terminal
  (e.g. `IMAGE_SIGNATURE_INVALID`).
- The cleanup worker observed an unrecoverable invariant violation
  (e.g. payload references a deleted resource).

Dead-letter rows are surfaced via:

- `GET /api/v1alpha/admin/queues` — per-queue dead-letter count.
- `GET /api/v1alpha/admin/jobs/{id}` — admin-only inspect (payload
  redacted via the standard pipeline).
- The Prometheus metric `midnight_jobs_dead_letter_total{queue}`.
- An `alerting` rule (panel: `DeployJobStuck` ships in the default
  Prometheus rules file).

A dead-lettered job can be replayed by an admin:

```
POST /api/v1alpha/admin/dead-letters/{id}/replay
```

Replay sets `state = 'pending'`, `attempt = 0`, `dead_letter_reason =
NULL`, `available_at = now()`. The action is **audit-logged**: an
audit row is inserted with `action='dead_letter.replay'`, the original
`reason_code`, and the operator's actor id. Replay is rate-limited to
prevent storms.

Dead-letter rows are not pruned by the partition-cleanup worker until
the operator either resolves them (replay or explicit dismiss) or
their parent partition's full retention period elapses. The dismiss
action is also audit-logged.

## 8. Cancellation cooperative safe-points

Per panel:

> "Cancellation is cooperative at named safe points only. Cancellation
> between route-add (step 6) and drain-completion (step 9) is rejected
> with `CANCEL_NOT_SAFE`; only rollback is permitted in that window.
> ACME challenges always run to completion or `revoke`."

A worker checks for cancellation at named safe points only. The points
are queue-class-specific. Outside these points, a cancellation request
on the parent operation is enqueued but not delivered until the worker
reaches the next safe point.

### 8.1 `build` queue safe points

1. **Pre-source-fetch.** Right after lease, before any clone/upload.
2. **Post-source, pre-builder-invocation.** After source is on disk;
   builder has not been invoked.
3. **Post-build-plan-resolved, pre-BuildKit.** After Railpack has
   returned a plan; before BuildKit starts work.
4. **Post-BuildKit-success, pre-image-push.** After image is built;
   before push to registry. (Cancel here drops the local image.)
5. **Post-snapshot-insert.** After `deployment_snapshots` row is
   committed. (Cancel here marks the deployment `CANCELED`; the
   snapshot row is retained as the panel pins
   "deployment_snapshots are immutable after insert".)

After step 5, the build hands the deployment off to the deploy queue;
build-side cancellation is no longer applicable.

### 8.2 `deploy` queue safe points

The deploy worker's safe points map to the rollout algorithm in the
deployment FSM dossier. Per the panel pin (step 6 = route-add, step 9
= drain-complete):

1. **Pre-image-pull.**
2. **Post-image-pull, pre-signature-verify.**
3. **Post-signature-verify, pre-secret-resolution.**
4. **Post-secret-resolution, pre-schedule.**
5. **Post-schedule, pre-container-start.** (rollout step 4)
6. **Post-readiness-check, pre-route-add.** (rollout step 5/6
   boundary). **THIS IS THE LAST SAFE POINT** before the cancel-not-
   safe window.

Steps 6.5 (route-add) through 9 (drain-complete) are the
**cancel-not-safe window**. Any cancel request received here is
rejected at the API with `409 CANCEL_NOT_SAFE`. The user is told
"roll back instead." The worker still receives the cancel signal but
does not act on it; if the deploy fails later the row transitions to
`FAILED` with the failure's reason code, not `CANCELED`.

7. **Post-drain-complete.** (rollout step 10) — only used for
   rollbacks of in-progress drains, not cancellations.

### 8.3 `reconcile` queue safe points

The reconciler is naturally cancel-friendly: each iteration is short
and idempotent. Safe points are at every iteration boundary. A cancel
request mid-iteration is honored by the next iteration boundary
(typically <1s).

### 8.4 `webhook` queue safe points

Webhook deliveries are at-most-once-in-flight per delivery row.
Cancellation safe points are:

1. **Pre-DNS-resolution.** Cancel before any outbound network call.
2. **Pre-TCP-connect.**
3. **Pre-HTTP-request-write.**

After the request body has been written (and especially after a 2xx
has been received), cancellation has no effect — the side effect
(target system received the webhook) cannot be undone. This is per
the panel "cooperative" rule: cancel does not retroactively suppress
already-completed side effects.

### 8.5 `cleanup` queue safe points

- Backup jobs: between snapshot phase and upload phase, between upload
  chunks (every 10MB). After the backup manifest is written, cancel is
  rejected — partial manifests would corrupt restic state.
- Pin decrement / GC sweeps: per-batch. A 100-row batch runs to
  completion before checking cancel.
- ACME challenge polls: **never cancellable** — panel pin: "ACME
  challenges always run to completion or `revoke`."

OPEN QUESTION: Should `webhook` deliveries that have been written but
not yet acknowledged be cancellable on operator override? The panel
rule says no; we ship as no. If the target system accepts the request
after we have given up, it stays accepted.

## 9. Cross-references

- `SCHEMA_PHASE_0_ALPHA.sql` — the `jobs` table DDL, indexes, and
  partition stubs.
- `SCHEMA_NOTES.md` — partitioning / VACUUM / role separation context.
- `agile/features/FEATURE_POSTGRES_JOB_QUEUE.md` — sourced the SQL
  fragments and the per-tenant fairness sketch.
- `agile/features/FEATURE_DEPLOYMENT_STATE_MACHINE.md` — the
  cancel-not-safe window between rollout step 6 and step 9.
- `agile/features/FEATURE_AUDIT_HASH_CHAIN.md` — every dead-letter
  replay emits an audit row.
- `agile/features/FEATURE_OFFHOST_BACKUP.md` — backup runs are
  `cleanup`-class jobs.
