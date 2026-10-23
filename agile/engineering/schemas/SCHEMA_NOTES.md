# SCHEMA_NOTES.md — Midnight Phase 0 Alpha schema-of-record companion

Status: normative. Pairs with `SCHEMA_PHASE_0_ALPHA.sql`.
Audience: control-plane engineers, migration authors, DBA/ops.

This document explains the conventions, indexing, partitioning, role
separation, migration tooling, vacuum tuning, and backup boundaries that
the SQL file relies on. Where a panel decision pins a column, index, or
operational rule, the relevant decision is quoted inline.

---

## 1. Naming conventions

- **Tables**: plural `snake_case` nouns (`users`, `deployments`,
  `deployment_image_pins`). Partition children carry a date suffix:
  `audit_logs_2026_04`. Never use mixed-case identifiers; PostgreSQL
  lowercases unquoted identifiers and quoting them invites accidents.
- **Columns**: `snake_case`. Foreign keys are named `<singular>_id`:
  `service_id`, `environment_id`, `parent_deployment_id`. Self-references
  use a meaningful prefix (`parent_`, `consumer_`, `target_`) and never a
  bare `id`.
- **Primary keys**: app-generated ULID-prefixed `text` for every public
  resource. Internal append-only logs (`events`, `audit_logs`) use
  `bigserial` because the row id is internal and the public id is the
  CloudEvents `ce_id` text column. The panel pins:

  > "Resource ID format is normative: `<prefix>_<26-char Crockford base32
  > ULID>` (or UUIDv7) for time-sortability. Reserved prefixes: `prj`,
  > `env`, `svc`, `dep`, `op`, `job`, `dom`, `vol`, `var`, `tok`, `usr`,
  > `sa`, `tpl`, `evt`, `idem`, `plan`, `inst`. Total length ≤ 32. Clients
  > must not parse the prefix for type checks; use `kind`."

- **Constraints**: `<table>_<column>_chk` for `CHECK`, `<table>_<column>_uq`
  for `UNIQUE`, `<table>_<column>_fk` for explicitly-named foreign keys.
- **Indexes**: `<table>_<purpose>_idx`. Partial indexes spell out the
  partial predicate intent in the name where possible
  (`api_tokens_active_idx`, `deployments_state_active_idx`).
- **Triggers**: `<table>_rv` for the `resource_version` bump trigger so
  the role of the trigger is obvious from `\d`.

## 2. ULID prefix taxonomy (final list for Phase 0 Alpha)

The panel's reserved-prefix list is normative; the Alpha tables map to
concrete prefixes as follows. Prefixes are 2–4 characters, lowercase, and
followed by an underscore and the 26-char Crockford base32 ULID body.

| Resource                          | Prefix    | Example                                |
|-----------------------------------|-----------|----------------------------------------|
| User                              | `usr`     | `usr_01HZX9PQ8K7Q9F4R2N5S3T7M2V`       |
| Workspace                         | `ws`      | `ws_01HZX9PQ8K…`                       |
| Workspace member (composite)      | n/a       | (composite PK; no surrogate id)        |
| Service account                   | `sa`      | `sa_01HZX9PQ8K…`                       |
| API token                         | `tok`     | `tok_01HZX9PQ8K…`                      |
| Bootstrap token                   | `btok`    | `btok_01HZX9PQ8K…`                     |
| Project                           | `prj`     | `prj_01HZX9PQ8K…`                      |
| Environment                       | `env`     | `env_01HZX9PQ8K…`                      |
| Service                           | `svc`     | `svc_01HZX9PQ8K…`                      |
| Service source                    | `src`     | `src_01HZX9PQ8K…`                      |
| Variable                          | `var`     | `var_01HZX9PQ8K…`                      |
| Variable reference                | `vrf`     | `vrf_01HZX9PQ8K…`                      |
| Domain                            | `dom`     | `dom_01HZX9PQ8K…`                      |
| Deployment                        | `dep`     | `dep_01HZX9PQ8K…`                      |
| Deployment snapshot               | `snp`     | `snp_01HZX9PQ8K…`                      |
| Deployment instance               | `inst`    | `inst_01HZX9PQ8K…`                     |
| Build                             | `bld`     | `bld_01HZX9PQ8K…`                      |
| Host                              | `hst`     | `hst_01HZX9PQ8K…`                      |
| Route                             | `rte`     | `rte_01HZX9PQ8K…`                      |
| CloudEvents id (event row)        | `evt`     | `evt_01HZX9PQ8K…` (in `events.ce_id`)  |
| Job                               | `job`     | `job_01HZX9PQ8K…`                      |
| Idempotency key                   | `idem`    | `idem_<client-supplied>` normalized    |
| Operation                         | `op`      | `op_01HZX9PQ8K…`                       |
| Failure classification (code)     | n/a       | (`code` is the SCREAMING_SNAKE PK)     |

Reserved-but-not-Alpha prefixes (deferred-domain tables): `vol`, `tpl`,
`plan`. The schema file does not declare those tables; the prefix is
documented here so collisions are avoided in v1+.

OPEN QUESTION: The panel's reserved-prefix list does not include `ws`,
`btok`, `src`, `vrf`, `snp`, `bld`, `hst`, `rte`, `inst` explicitly. We
have allocated these for Phase 0 Alpha because every Alpha resource needs
a stable id. Confirm with the API/IaC contract owner before v1.

## 3. Partitioning strategy

### Tables that are partitioned

Per panel decision, the following high-write tables are monthly range-
partitioned:

> "High-write tables (`events`, `audit_logs`, `jobs`, `host_heartbeats`,
> `deployment_instances`) are monthly range-partitioned with daily
> pruning workers. Autovacuum tuned per-table
> (`autovacuum_vacuum_scale_factor=0.02`, fillfactor 80) for the queue."

- `events` — partitioned on `occurred_at`.
- `audit_logs` — partitioned on `occurred_at`.
- `jobs` — partitioned on `created_at`.
- `host_heartbeats` — partitioned on `occurred_at`.
- `deployment_instances` — partitioned on `created_at`.

### Why monthly (not daily, not weekly)

- **Daily** is too granular for these tables: at expected Phase 0 volumes
  (single-node, tens of services, low thousands of events/day) daily
  partitions create ≥ 365 children per year per table, which makes
  planner overhead measurable on every query. Monthly partitions cap
  child count at ~12/year/table, which Postgres handles trivially.
- **Weekly** introduces awkward boundaries that do not align with the
  retention policy units (which are stated in days but reasoned in
  months), and complicates the daily-pruning worker.
- **Monthly** matches the audit-checkpoint cadence (daily checkpoints
  rolled up into a per-month `audit_checkpoints.partition_label`),
  matches the off-host backup sizing assumptions, and gives a clean
  boundary for the "is this partition closed?" check that gates pruning.

### Daily-partition-pruning worker

A `cleanup`-class job (run by the queue described in
`SCHEMA_QUEUE_CONTRACT.md`) executes once daily per partitioned table:

1. Identify partitions older than the retention policy
   (`retention_days`) for the table:
   - `events`: 90 days (default; operator-tunable).
   - `audit_logs`: 365 days (default; tunable upward, never below 90 to
     respect the panel's "audit retention" pin reason).
   - `jobs`: 30 days post-terminal-state.
   - `host_heartbeats`: 14 days.
   - `deployment_instances`: 30 days post-`REMOVED`.
2. For `audit_logs`, refuse to prune unless `audit_checkpoints.is_final`
   for that `partition_label` is `true`. This implements the panel's
   "pruning attempted without final checkpoint" failure-mode rejection.
3. Compute the next month's partition and `CREATE TABLE ... PARTITION OF
   ... FOR VALUES FROM (...) TO (...)`. The bootstrap DDL ships two
   months pre-created; this worker keeps two months of headroom rolling.
4. `DETACH PARTITION` then `DROP TABLE` for partitions outside retention.
   Detach-then-drop avoids holding an `ACCESS EXCLUSIVE` lock on the
   parent for the lifetime of the drop.
5. Audit-log the prune (`partition_dropped`, `rows_pruned`,
   `oldest_row_at`, `final_checkpoint_id`) so the chain records the
   action.

`pg_partman` is the recommended automation: it handles step 3 and step 4
mechanically. The Alpha installer ships `pg_partman` as a recommended
package; the manual fallback is the worker described above.

### Hot-path indexes (pinned by panel where applicable)

The panel cites the dequeue predicate and several other hot paths. Index
definitions in the SQL file:

- **Job dequeue**:
  ```sql
  CREATE INDEX jobs_dequeue_idx
    ON jobs (queue, priority DESC, available_at ASC)
    WHERE state = 'pending';
  ```
  The panel's exact predicate is "(queue, available_at, priority) WHERE
  lease_expires_at IS NULL OR lease_expires_at < now()". Postgres rejects
  `now()` inside an index predicate (non-IMMUTABLE), so we use
  `state = 'pending'` as the indexable equivalent — a row can only be
  `pending` if its lease has expired or never existed. The dequeue SQL
  in `SCHEMA_QUEUE_CONTRACT.md` still applies the runtime
  `lease_expires_at IS NULL OR lease_expires_at < now()` check inside
  the `WHERE` clause. The reaper job converts expired-leased rows back to
  `pending` so the index stays accurate.

- **Reaper sweep**:
  ```sql
  CREATE INDEX jobs_reaper_idx
    ON jobs (state, lease_expires_at)
    WHERE state = 'leased';
  ```

- **Idempotency lookup**: immutable attempts use the composite primary key
  `(actor, tenant, method, path_template, key_id, generation)`. Exactly one
  non-expired attempt may own the logical scope:
  ```sql
  CREATE UNIQUE INDEX idempotency_keys_active_scope_uq
    ON idempotency_keys (actor, tenant, method, path_template, key_id)
    WHERE state IN ('RESERVED','RUNNING','SUCCEEDED','FAILED');
  ```
  The API serializes this scope with a transaction advisory lock, compares the
  stored body hash, and translates a different-body replay to
  `409 IDEMPOTENCY_KEY_REUSED`. Expiry changes the prior generation to
  `EXPIRED`; history remains available while a new generation becomes active.

- **Route freshness probe**:
  ```sql
  CREATE INDEX routes_deployment_target_ready_idx
    ON routes (deployment_id) WHERE target_ready_at IS NOT NULL;
  CREATE INDEX routes_freshness_idx
    ON routes (last_observed_at) WHERE status = 'ready';
  ```
  The reconciler joins `(deployment_instances WHERE state='READY' COUNT)`
  with `(routes WHERE deployment_id=$id AND target_ready_at IS NOT NULL)`
  inside the `DEPLOYING -> ACTIVE` guard, per the panel:

  > "a 'Deployment reconciler' worker class joins
  > `deployment_instances.ready_count >= minReady` and
  > `routes.target_ready_at IS NOT NULL` for the candidate"

- **Image-pin probe**:
  ```sql
  CREATE INDEX deployment_image_pins_digest_active_idx
    ON deployment_image_pins (digest)
    WHERE pinned_until IS NULL OR pinned_until > now();
  ```
  Registry GC consults this on every sweep. Note: the partial-index
  predicate uses `now()`, which Postgres permits but does not
  re-evaluate after row insert. This is acceptable because (a) at insert
  time the row is correctly classified, (b) a daily sweep removes stale
  rows before they distort the index, and (c) the GC worker re-checks
  `pinned_until > now()` at query time.

- **Audit chain verify**: the partition's `(id, occurred_at)` primary
  key already provides id-order iteration. `audit_logs_checkpoint_idx`
  on `checkpoint_id` accelerates checkpoint-bounded verification:

  > "Audit log uses per-row hash chaining at MVP: `prev_hash` and
  > `row_hash = H(prev_hash || canonical_row)`, rooted in a daily
  > `audit_checkpoint` row signed with the Midnight release key or
  > operator-configured Ed25519 key. `midnight audit verify` CLI ships
  > in MVP."

## 4. PostgreSQL extensions

The `CREATE EXTENSION` block at the top of the SQL file requires:

- **`pgcrypto`** — required for `gen_random_bytes()`, used by the
  bootstrap-token issuer and any future random-id paths. Also provides
  `digest(...)` and `crypt(...)` if migrations need them.
- **`citext`** — `users.email` is `citext` so case-insensitive uniqueness
  is enforced at the database without manual `lower()` indexing.
- **`pg_partman` (recommended)** — operator-installed; not required by
  the DDL. The Alpha installer recommends it; absent it, the daily
  pruning worker handles partition lifecycle manually.

`pg_stat_statements` is recommended for ops but not required for schema
correctness; install it via `shared_preload_libraries` in
`postgresql.conf`, not in this DDL.

## 5. Postgres role separation (panel: security/trust boundary)

Three roles, each with explicit grants. The grant table is at the bottom
of `SCHEMA_PHASE_0_ALPHA.sql`. Quoting the panel:

> "Postgres role separation: `midnight_app` (CRUD only), `midnight_migrate`
> (DDL, migrations only), `midnight_audit_ro` (SELECT on audit/event
> tables). All roles have `statement_timeout = 30s` (audit_ro 5min) and
> `idle_in_transaction_session_timeout = 60s`. `pg_hba.conf` restricts to
> `127.0.0.1` and the agent network."

| Role                   | Tables (simplified)                                         | Allowed verbs                |
|------------------------|-------------------------------------------------------------|------------------------------|
| `midnight_app`         | All operational tables                                      | SELECT, INSERT, UPDATE, DELETE |
| `midnight_app`         | `events`, `audit_logs`                                      | SELECT, INSERT (no UPDATE/DELETE) |
| `midnight_app`         | `host_heartbeats`, `deployment_instances`                   | SELECT, INSERT, UPDATE       |
| `midnight_app`         | `audit_checkpoints`                                         | SELECT, INSERT               |
| `midnight_audit_ro`    | `audit_logs`, `audit_checkpoints`, `events`                 | SELECT only                  |
| `midnight_migrate`     | All tables                                                  | DDL + DML                    |

`statement_timeout` and `idle_in_transaction_session_timeout` are set per
role via `ALTER ROLE` in a separate migration (these settings are not in
the schema file because they belong to install/operate, not to schema
shape):

```sql
ALTER ROLE midnight_app       SET statement_timeout = '30s';
ALTER ROLE midnight_app       SET idle_in_transaction_session_timeout = '60s';
ALTER ROLE midnight_audit_ro  SET statement_timeout = '5min';
ALTER ROLE midnight_audit_ro  SET idle_in_transaction_session_timeout = '60s';
ALTER ROLE midnight_migrate   SET statement_timeout = '15min';
ALTER ROLE midnight_migrate   SET idle_in_transaction_session_timeout = '5min';
```

The `pg_hba.conf` rules ship with the `.deb`:

```
local   all   midnight_app       scram-sha-256
local   all   midnight_audit_ro  scram-sha-256
local   all   midnight_migrate   scram-sha-256
host    all   midnight_app       127.0.0.1/32        scram-sha-256
host    all   midnight_app       <agent_network>     scram-sha-256
host    all   midnight_audit_ro  127.0.0.1/32        scram-sha-256
host    all   midnight_migrate   127.0.0.1/32        scram-sha-256
```

OPEN QUESTION: A future enterprise install profile will add `hostssl`
clauses with `clientcert=verify-full`. The Alpha profile uses local +
loopback only, so plain `host` is sufficient. Confirm before opening
the agent network to a non-trusted segment.

## 6. Migration strategy

**Recommendation: `goose` for Phase 0 Alpha. Reassess against `atlas` at
the v1 graduation gate.**

Rationale:

- **`sqlc` is not a migration tool.** It is a Go code generator for SQL
  queries. It does not own schema lifecycle. We will use `sqlc` for the
  query layer, separately, but it is not in scope for this decision.
- **`goose`** is a single static binary, supports versioned `.sql`
  migrations with explicit `-- +goose Up` / `-- +goose Down` markers,
  has a clean tagging story for `reversible | forward-only`, and ships
  in Midnight's distro of choice (Ubuntu/Debian APT repositories carry
  recent versions). It is the smallest possible tool that satisfies the
  panel's pre-upgrade snapshot + advisory-locked txn requirement. The
  upgrade transaction (panel: "midnight upgrade takes a pre-upgrade
  snapshot tag, runs migrations in advisory-locked txns, executes a
  smoke probe with timeout") wraps `goose up` cleanly.
- **`atlas`** is more capable (declarative state, drift detection, HCL
  schema). It is the right answer at v1 when we have an IaC provider
  consuming the same schema. For Alpha its declarative model is more
  surface area than Phase 0 needs.

### Migration tagging

Every migration file in `db/migrations/` is named
`NNNN_<slug>.<reversible|forward_only>.sql`:

```
0001_initial_schema.reversible.sql
0002_seed_failure_classifications.reversible.sql
0003_partition_2026_06.reversible.sql
0007_drop_legacy_session_id.forward_only.sql
```

`forward_only` migrations are a release-gate concern: per panel,
`midnight upgrade` refuses to run them without `--accept-irreversible`
plus a verified pre-upgrade backup. The tag is parsed from the filename
by the upgrade tool; goose itself has no opinion.

### Advisory-locked transactions

Every migration runs inside a Postgres transaction that first acquires
`pg_advisory_xact_lock(hashtext('midnight_upgrade'))`. The `apt`
post-install hook acquires the same lock at the OS level by checking a
sentinel file; concurrent upgrades are refused. The panel pin:

> "`apt` post-install hooks refuse to run if another upgrade lock is
> held."

## 7. VACUUM / autovacuum tuning

The panel pins one explicit setting:

> "Autovacuum tuned per-table (`autovacuum_vacuum_scale_factor=0.02`,
> fillfactor 80) for the queue."

We extend this to the other high-write tables. Settings are applied via
`ALTER TABLE ... SET (...)`; they live in a separate migration (not in
the DDL file because they are operational tunables). Recommended values:

| Table                  | autovacuum_vacuum_scale_factor | autovacuum_analyze_scale_factor | fillfactor | autovacuum_vacuum_cost_limit |
|------------------------|-------------------------------|---------------------------------|------------|------------------------------|
| `jobs`                 | `0.02`                        | `0.05`                          | `80`       | `2000`                       |
| `host_heartbeats`      | `0.02`                        | `0.05`                          | `90`       | `2000`                       |
| `deployment_instances` | `0.05`                        | `0.10`                          | `80`       | `1500`                       |
| `events`               | `0.10`                        | `0.10`                          | `100`      | `1000` (insert-only)         |
| `audit_logs`           | `0.10`                        | `0.10`                          | `100`      | `1000` (insert-only)         |
| `routes`               | `0.10`                        | `0.20`                          | `90`       | default                      |
| `idempotency_keys`     | `0.05`                        | `0.10`                          | `80`       | `1500`                       |
| `operations`           | `0.05`                        | `0.10`                          | `80`       | `1500`                       |

Notes:

- `fillfactor=80` on the queue and on `idempotency_keys` leaves room for
  in-place HOT updates (state transitions on `jobs`; status updates on
  `idempotency_keys`).
- `events` and `audit_logs` are append-only; `fillfactor=100` is
  correct.
- Partitioned-table options propagate to children at create time. The
  daily-pruning worker re-applies these on each new partition it
  creates.

OPEN QUESTION: We have not pinned `autovacuum_freeze_max_age` per table;
default (200M) is acceptable until a real data set is observed.

## 8. Backup boundaries (panel: storage/registry/backup)

The panel separates **platform backup** (control-plane Postgres + KEK +
ACME state + signing keys) from **tenant backup** (project-scoped restic
repos for volumes, database-native backups for template databases). The
schema-of-record is a platform backup concern.

### Platform backup includes

These tables MUST be in the control-plane Postgres `pg_basebackup` + WAL
archive set (panel: "Control-plane Postgres has its own `pg_basebackup`
+ WAL archive sidecar to a separate physical destination"):

- All identity, RBAC, governance: `users`, `workspaces`,
  `workspace_members`, `service_accounts`, `api_tokens`,
  `bootstrap_tokens`.
- All resource graph: `projects`, `environments`, `services`,
  `service_sources`, `service_environment_state`, `variables`,
  `variable_references`, `domains`.
- All deployment record: `deployments`, `deployment_snapshots`,
  `deployment_image_pins`, `builds`.
- All routing: `routes`.
- All host registration: `hosts`. (Heartbeat partitions older than 14
  days are pruned and not part of backup; the most recent two months
  are.)
- All operations contract: `jobs` (recent partitions), `idempotency_keys`,
  `operations`, `failure_classifications`.
- **Audit and events**: `audit_logs`, `audit_checkpoints`, `events` —
  these are critical. Restore must `midnight audit verify` before
  resuming writes (panel cross-reference in
  `FEATURE_AUDIT_HASH_CHAIN.md`).

### Tenant backup includes (separately, not in this schema)

- restic repository per project under
  `/var/lib/midnight/backups/<project_id>/`, password sealed by KMS.
- Database-native backups (`pg_dumpall --globals-only` + per-DB
  `pg_dump`) for template databases.
- Volume contents (host bind mounts under `/var/lib/midnight/volumes/`).

### What the agent never reads

The host agent connects to Postgres only via the control-plane API. It
never executes SQL against any of the tables here directly. Specifically,
the agent has no read on:

- `variables` or `variable_references` (secret resolution happens via
  the control plane's secret-store boundary; the agent receives resolved
  values, redacted for display, at deploy-spec push time).
- `audit_logs`, `audit_checkpoints`, `events` (the agent emits events
  via the API; it never reads the chain).
- `api_tokens`, `bootstrap_tokens` (bearer token auth happens at the
  control-plane API boundary; the agent has its own token issued at
  registration).
- `idempotency_keys` (the agent does not own idempotency; the API does).
- `operations` (the agent reports observed instance state; the
  reconciler aggregates).

This separation is non-negotiable: an agent that can read the audit
chain, the secret store, or the variable references is a privilege
escalation path the panel explicitly forbids.

## 9. Cross-references

- `SCHEMA_PHASE_0_ALPHA.sql` — the DDL.
- `SCHEMA_QUEUE_CONTRACT.md` — the dequeue/heartbeat/reclaim contract
  for the `jobs` table.
- `agile/features/FEATURE_DEPLOYMENT_STATE_MACHINE.md` — pins the
  deployment state matrix and the reconciler's promotion guard.
- `agile/features/FEATURE_AUDIT_HASH_CHAIN.md` — pins the
  `audit_logs.row_hash` formula and the verify CLI exit codes.
- `agile/features/FEATURE_POSTGRES_JOB_QUEUE.md` — the source for the
  exact dequeue / heartbeat / reaper SQL fragments used in
  `SCHEMA_QUEUE_CONTRACT.md`.
- `agile/features/FEATURE_ROLLBACK_BY_DIGEST.md` — pins the
  `deployment_snapshots` field set and the `deployment_image_pins`
  reference-count semantics.
- `agile/features/FEATURE_OFFHOST_BACKUP.md` — pins the platform vs
  tenant backup boundary used in section 8.
