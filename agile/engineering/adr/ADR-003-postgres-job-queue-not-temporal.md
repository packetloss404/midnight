# ADR-003: Postgres-backed durable jobs through Alpha and v1; Temporal deferred

- Status: Accepted (from panel review)
- Date: 2026-04-25
- Deciders: Eight-Specialist Panel
- Tags: [phase-0, control-plane, durability, jobs, workflows, deferred-temporal]

## Context

Every long-running Midnight action — builds, deploys, route reconciles, ACME challenges, webhook deliveries, registry GC, scheduled backups, cleanup — is durable, leased, retryable, cancellable, and failure-classified. Three options dominate the workflow-engine market: a Postgres-backed leased queue, a dedicated workflow service like Temporal, or Argo Workflows on Kubernetes. The mid-level/senior panel explicitly resolved this:

> "Keep Postgres-backed durable jobs through Alpha and v1; reassess Temporal only after operation histories, retries, cancellations, human approvals, and compensation logic outgrow the Postgres job model." (line 2389; reaffirmed at line 1570)

The eight-specialist panel pinned the queue contract as normative:

> "Postgres job queue contract is normative: columns include `lease_owner`, `lease_expires_at`, `heartbeat_at`, `attempt`, `available_at`, `priority`, `queue`, `partition_key`. Dequeue uses `SELECT … FOR UPDATE SKIP LOCKED` ordered by `(priority, available_at)` filtered by `queue` and lease availability. Heartbeat cadence = `lease_ttl / 3`; expired leases are reclaimed automatically. Separate per-class queues with independent worker pools and bounded concurrency: `build`, `deploy`, `reconcile`, `webhook`, `cleanup`. Per-tenant fairness via weighted-fair dequeue." (line 102)

> "MVP uses Postgres durable jobs with leases, heartbeats, retries, cancellation, and dead-letter handling. Temporal is a later option for long-running, human-in-the-loop, retry-heavy workflows if Postgres job complexity grows." (lines 1547–1548)

The panel also locked the **transactional outbox** for committing domain mutations and side effects atomically (line 108: "Side effects … enqueue via the transactional outbox pattern within the same transaction"). Temporal cannot participate in a Postgres transaction; it is its own state store. Importing it as the durable queue would break the outbox guarantee that ADR-002 makes its centerpiece.

## Decision

Midnight uses a **single Postgres-backed durable job queue** through Phase 0 Alpha and v1. The queue contract from `FEATURE_POSTGRES_JOB_QUEUE.md` is normative: leases, heartbeats at `lease_ttl / 3`, retry with bounded exponential backoff and jitter, dead-letter, cooperative cancellation, per-class independent worker pools (`build`, `deploy`, `reconcile`, `webhook`, `cleanup`), per-tenant weighted-fair dequeue, monthly range partitioning on `created_at`, autovacuum tuned per-table.

**Temporal is deferred.** It will be reassessed only when the criteria below are met. Until then, the panel's words are normative: "Reassess Temporal only after operation histories, retries, cancellations, human approvals, compensation logic, or cross-day workflows exceed what can remain inspectable through operations, jobs, and events" (line 1570).

## Reassessment trigger

Adopting Temporal (or Cadence, or another dedicated workflow engine) requires **all** of:

1. A documented workflow whose state graph cannot be expressed as `(operation_id, child_jobs, audit_events, idempotency_keys)` without contortion — e.g., human-approval steps spanning weeks where in-flight job leases would be impractical.
2. ≥3 production workflows with cross-day durations, deep compensation logic (sagas), or human-in-the-loop pauses.
3. Operations dashboard pain: support engineers cannot answer "what is this deploy doing right now" using `GET /operations/{op_id}` plus the `jobs` table.
4. A migration ADR that addresses the loss of transactional outbox semantics — Temporal's events are not Postgres rows; the outbox guarantee will need a replacement.

Before any of those hold, Postgres jobs are sufficient. Attempting to introduce Temporal preemptively is rejected at code review.

## Alternatives Considered

- **Temporal at Phase 0.** Rejected: panel explicit (lines 1547–1570, 2389). It also breaks the transactional outbox (line 108) — a cause-and-effect commit guarantee that ADR-002 depends on. Operationally, Temporal adds a separate cluster, a separate persistence backend, a separate UI, a separate backup contract, separate IAM, and a separate compatibility matrix — all for a Phase 0 install that targets one VM in 15 minutes (line 191).
- **Argo Workflows / Argo Events.** Rejected: requires Kubernetes, which is a Phase 3 adapter, not the default runtime (line 23, line 1549).
- **Faktory / Sidekiq Pro / Resque.** Rejected: not durable in the sense Postgres is; not transactional; ecosystem is Ruby-first.
- **NATS JetStream as durable queue.** Rejected: cannot participate in the Postgres transaction (no XA), separate replication and backup, and the panel explicitly chose "Postgres-backed queue first; NATS or Temporal later if needed" (line 399).
- **Bare cron + database table without leases.** Rejected: produces dogpile, double-execution under retry, and silent stuck jobs. Panel locked leases + heartbeats + `SKIP LOCKED` (line 102).
- **River / GoCloak / asynq with Redis.** Rejected: Redis durability and the missing transactional outbox; same as Temporal but smaller. Postgres + `SKIP LOCKED` is good enough for the documented load (10k enqueue/min sustained, p95 dequeue < 50ms — feature acceptance criterion 11 in `FEATURE_POSTGRES_JOB_QUEUE.md`).

## Consequences

Positive:

- One operational substrate (Postgres) covers state, jobs, audit, and idempotency. One backup contract (ADR-002).
- Transactional outbox is correct by construction: enqueue runs `INSERT INTO jobs(…)` in the same transaction as the domain mutation; either both commit or neither does (line 108).
- Operators can inspect the queue with `psql`. No separate "workflow UI" to learn.
- `GET /operations/{op_id}` rolls up child job state without crossing a service boundary.

Negative:

- Postgres is doing more work; we mitigate with monthly partitions on `jobs` (line 103), per-table autovacuum tuning (`autovacuum_vacuum_scale_factor=0.02`, `fillfactor=80`), and bounded concurrency per queue class.
- Long-duration human-in-the-loop workflows (multi-day approvals) will fight the lease model. We document the workaround (operation parks waiting on an approval row, no active lease) and note that this is the canonical Temporal trigger.
- Per-tenant weighted-fair dequeue is non-trivial SQL; tested by an integration drill ("1 noisy tenant + 10 normal tenants, ≤ 2× fair share over 60s window").

Follow-up work:

- The full schema, dequeue/heartbeat/reclaim SQL, and metric set are normative in `FEATURE_POSTGRES_JOB_QUEUE.md`.
- Sprint 3 (`SPRINT_03_CONTROL_PLANE_API.md`) ships the queue migration, dequeue helper, and reaper goroutine.
- Sprint 8 failure drills (`SPRINT_08_FAILURE_DRILLS.md`) include "Postgres restart while jobs are leased" and "Postgres connection exhaustion."

## Compliance / Verification

Concrete gates a reviewer or auditor checks:

- **Test name `TestSkipLockedNoDoubleLease`** in `pkg/jobs`: 100 workers, 10 jobs, asserts no double-lease and no row left behind.
- **Test name `TestHeartbeatLossReclaim`** in `pkg/jobs`: simulates worker death; reaper requeues with `attempt += 1`; row state transitions match panel contract.
- **Test name `TestTransactionalOutbox`** in `pkg/jobs`: enqueue inside the same transaction as a domain mutation; abort the transaction; assert no orphan job.
- **Test name `TestPerTenantFairness`** in `pkg/jobs`: 1 noisy tenant + 10 normal tenants, asserts noisy tenant ≤ 2× fair share over 60s.
- **CI gate `gate.no-temporal-import`:** static analysis fails the build if any production package imports a Temporal, Cadence, or NATS-JetStream client. The reassessment trigger above is the only path to lift this.
- **Runbook check `OR-CHK-JOBS-DEPTH`** in `midnight doctor`: per-queue depth, oldest-pending age, dead-letter count; alerts via `DeployJobStuck` rule (panel: line 196).
- **Code review checklist item `CR-JOBS-01`:** any code path that enqueues a job must do so inside a transaction that also performs the domain mutation, or document why the transactional-outbox invariant does not apply.
- **Performance gate:** sustained 10k enqueue/min for 1h with p95 dequeue latency < 50ms; partition pruning closes month boundaries in < 5s (acceptance criterion 11, `FEATURE_POSTGRES_JOB_QUEUE.md`).

## References

- spec: lines 102–103, 108, 399, 614–620, 1545–1570, 2389
- features: `FEATURE_POSTGRES_JOB_QUEUE.md`, `FEATURE_DEPLOYMENT_STATE_MACHINE.md`, `FEATURE_AUDIT_HASH_CHAIN.md`
- sprints: `SPRINT_03_CONTROL_PLANE_API.md`, `SPRINT_08_FAILURE_DRILLS.md`, `SPRINT_05_BUILDKIT.md`, `SPRINT_06_INGRESS_LOGS.md`
- handoff: `QUALITY_AND_TEST_STRATEGY.md`, `OPERATIONS_RUNBOOK_INDEX.md`
