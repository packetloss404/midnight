# ADR-002: Postgres is the single source of truth for control-plane state

- Status: Accepted (from panel review)
- Date: 2026-04-25
- Deciders: Eight-Specialist Panel
- Tags: [phase-0, control-plane, durability, correctness]

## Context

Midnight's whole product thesis is that **the control plane is the product, not the runtime** (lines 422–470). Every promise — immutable deployment snapshots, replayable rollbacks, idempotent operations, lease-and-heartbeat job execution, audit hash chains, single-flight per `(service, environment)`, transactional outbox between API mutations and side effects — collapses to "we need ACID, partitioned tables, advisory locks, `SKIP LOCKED`, monotonic sequences, and `pg_basebackup`/WAL archiving." The senior panel made this explicit:

> "Postgres is source of truth, not Docker state." (line 586)

The eight-specialist panel further pinned Postgres as the substrate for the durable job queue, idempotency keys, partitioned high-write tables, and the audit log:

- Job queue uses `SELECT … FOR UPDATE SKIP LOCKED` with leases, heartbeats, and monthly partitions (lines 102–103).
- Single-flight per service/environment uses `pg_advisory_xact_lock(hashtext('svc:'||service_id||':'||env_id))` (line 104).
- `runLatest` arbitration uses a monotonic `bigserial` deployment sequence, never wall-clock time (line 104).
- High-write tables (`events`, `audit_logs`, `jobs`, `host_heartbeats`, `deployment_instances`) are monthly range-partitioned (line 103).
- Idempotency keys, operations, and the transactional outbox all live in the same database to commit atomically with the domain mutation (lines 102–113, 1547).
- Control-plane Postgres has its own `pg_basebackup` + WAL archive sidecar to a separate physical destination configured at install (line 147).

This ADR locks Postgres as **the** state store: not "Postgres plus etcd," not "Postgres plus Redis for the queue," not "Postgres plus Kafka for events." A separate metrics path (Prometheus) is allowed. Logs route through Docker `json-file` (line 95). Everything else lives in Postgres.

## Decision

Midnight uses **Postgres** as the single source of truth for all control-plane state, including:

- Resource model (workspaces, projects, environments, services, deployments, snapshots, variables, domains, routes, hosts).
- Durable job queue with leases, heartbeats, retries, dead-letter (ADR-003).
- Idempotency keys and operations (line 102, 107).
- Audit log with per-row hash chain (line 121, ADR-011).
- Deployment image pin reference counting (line 112, ADR-012).
- Transactional outbox for side effects (ACME, deploys, DNS, webhooks) committed in the same transaction as their cause (line 108).

Control-plane Postgres is **separate** from any tenant Postgres template instance. Loss of the control-plane database triggers `PLATFORM_DEGRADED`; user data planes continue serving traffic in read-only mode while the control plane is down (line 147).

## Alternatives Considered

- **Postgres + etcd or Consul for distributed locks/leader election.** Rejected: at single-node Phase 0 scale, advisory locks and `SKIP LOCKED` cover every ordering need; a second consensus system doubles the install/upgrade/backup surface and is exactly what the panel wanted to avoid by disallowing Kubernetes as the default install (line 23).
- **Postgres + Redis for the job queue.** Rejected: Redis is not durable by default, requires AOF/RDB tuning to even approach Postgres durability, splits the transactional-outbox guarantee (we cannot commit a Redis enqueue and a Postgres state change atomically), and adds a second backup/restore path. The panel locked the queue contract on Postgres semantics — `FOR UPDATE SKIP LOCKED`, leases, heartbeats — that map cleanly to Redis only by reinventing them (line 102).
- **Postgres + Kafka for events.** Rejected at Phase 0: CloudEvents emission is satisfied by the transactional outbox + a delivery worker (line 108, line 177). Kafka adds operational complexity, separate backup, separate retention, and a separate failure mode (`MIRROR_LAG`) for zero Phase 0 benefit.
- **Postgres + dedicated audit store (e.g., S3 immutable bucket).** Rejected for MVP: the panel explicitly chose **per-row hash chain in the audit table itself** as the floor (line 121); WORM/SIEM is a Phase 2 enhancement, not the Phase 0 substrate.
- **CockroachDB / Yugabyte / Spanner-style distributed SQL.** Rejected: single-node operator simplicity is part of the first product loop. Distributed SQL belongs to a hypothetical Phase 4 hosted variant, not the OSS install.
- **SQLite.** Rejected: no `FOR UPDATE SKIP LOCKED`, no advisory locks, no per-table autovacuum tuning, no role-based privilege separation as required by the security panel (line 132).

## Consequences

Positive:

- One backup contract (`pg_basebackup` + WAL archive, line 147).
- One ACID boundary for the transactional outbox; "commit or nothing" between domain mutation and queued side-effect.
- One privilege boundary: `midnight_app` (CRUD), `midnight_migrate` (DDL), `midnight_audit_ro` (SELECT on audit/event tables) (line 132).
- Postgres role separation enforces the audit-chain insert-only contract at the database, not just the application (line 132, ADR-011).
- Standard Postgres operational knowledge transfers; design partners can run their own backups.

Negative:

- Postgres is now a single point of failure for the control plane. Mitigation: `pg_basebackup` + WAL archive sidecar to a separate physical destination, restore runbook (line 147, `OPERATIONS_RUNBOOK_INDEX.md`).
- Vertical scaling ceiling at very large fleets. Out of MVP scope. Reassessment when active deployment count, jobs/sec, or audit volume exceeds documented thresholds (`autovacuum_vacuum_scale_factor=0.02`, fillfactor 80 — line 103).
- Long-running migrations require advisory locking and care; covered by ADR-aligned upgrade transaction in `FEATURE_UPGRADE_TRANSACTION.md`.
- All consumers must respect role separation; a careless `GRANT` on `audit_logs` breaks the chain integrity story.

Follow-up work:

- Migration tool selection (`golang-migrate`/`goose`) and migration-tagging (`reversible|forward-only`, line 192) committed before Sprint 3 ends.
- Per-table autovacuum tuning baked into the schema migrations (`fillfactor=80`, `autovacuum_vacuum_scale_factor=0.02`).
- WAL-archive sidecar destination tested in `SPRINT_13_UPGRADE_TX.md` failure drills (KEK loss + control-plane Postgres restore).
- `pg_hba.conf` template restricts roles to `127.0.0.1` and the agent network (line 132).

## Compliance / Verification

Concrete gates a reviewer or auditor checks:

- **Test name `TestPostgresIsTheOnlyStateStore`** in `pkg/storage`: a static analysis or import-graph test asserts no production package imports `redis`, `etcd`, `consul`, `nats`, or `kafka` clients. New dependencies require an ADR amendment.
- **Runbook check `OR-CHK-PG-001`** in `midnight doctor`: verifies a successful `pg_isready` against the control-plane DSN and reports `last_wal_archive_at`.
- **CI gate `gate.role-separation`:** integration test asserts `midnight_app` has no `UPDATE`/`DELETE` privilege on `audit_logs`; `\du` and `\dp audit_logs` are captured in the test fixture (panel: line 132, ADR-011).
- **CI gate `gate.partition-policy`:** schema test asserts `events`, `audit_logs`, `jobs`, `host_heartbeats`, `deployment_instances` are partitioned with monthly range partitions and that the daily pruning worker is registered.
- **Code review checklist item `CR-PG-01`:** any new high-write table is reviewed for partitioning policy and autovacuum tuning before merge.
- **Failure drill `DRILL-PG-RESTORE`** in `SPRINT_08_FAILURE_DRILLS.md`: control-plane Postgres restore from WAL archive completes within RTO; user data plane stays in read-only mode during the drill.

## References

- spec: lines 102–113, 121, 132, 147, 192, 422–470, 481–509, 586–593, 1547–1570
- features: `FEATURE_POSTGRES_JOB_QUEUE.md`, `FEATURE_AUDIT_HASH_CHAIN.md`, `FEATURE_DEPLOYMENT_STATE_MACHINE.md`, `FEATURE_UPGRADE_TRANSACTION.md`
- sprints: `SPRINT_01_FOUNDATIONS.md`, `SPRINT_03_CONTROL_PLANE_API.md`, `SPRINT_08_FAILURE_DRILLS.md`, `SPRINT_13_UPGRADE_TX.md`
- handoff: `OPERATIONS_RUNBOOK_INDEX.md`, `QUALITY_AND_TEST_STRATEGY.md`
