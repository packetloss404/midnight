# ADR-011: Per-row audit hash chain at MVP; WORM/SIEM later

- Status: Accepted (from panel review, reclassified from "later")
- Date: 2026-04-25
- Deciders: Eight-Specialist Panel
- Tags: [mvp-beta, security, audit, tamper-evidence, panel-reclassification]

## Context

Midnight's audit log is the source of truth for "who changed what, when" — variable mutations (without secret plaintext), deployment lifecycle, domain attach/verify, certificate issuance, volume backup/restore, host registration, role changes, operator/admin actions (line 1174). Without tamper evidence at the row level, a database admin (or a compromised application connection that escalates) can edit or delete a row and no other row indicates the tampering. Full enterprise tamper evidence requires external WORM/SIEM sinks or transparency-log-style anchoring; the panel ruled that those are Phase 2 enhancements, but that the **floor must be in place at MVP**:

> "Audit log uses per-row hash chaining at MVP: `prev_hash` and `row_hash = H(prev_hash || canonical_row)`, rooted in a daily `audit_checkpoint` row signed with the Midnight release key or operator-configured Ed25519 key. `midnight audit verify` CLI ships in MVP. JSONL export includes `prev_hash`, `row_hash`, `checkpoint_id`." (line 121)

> "Hash chain detects edit/delete in the audit table even before WORM/SIEM is configured. Full tamper evidence against platform admins still requires external WORM/SIEM sink or transparency-log-style anchoring later, but the hash chain is the floor." (line 1261)

> "Audit log per-row hash chain ships at MVP, not 'later.'" (line 2399, panel resolution)

> "Postgres role separation: `midnight_app` (CRUD only), `midnight_migrate` (DDL, migrations only), `midnight_audit_ro` (SELECT on audit/event tables). All roles have `statement_timeout = 30s` (audit_ro 5min) and `idle_in_transaction_session_timeout = 60s`. `pg_hba.conf` restricts to `127.0.0.1` and the agent network." (line 132)

The chain is asymmetric tamper evidence: edit or delete of any row breaks the chain at that row's successor (its `prev_hash` no longer matches the recomputed `row_hash`); reorder breaks the chain at the reordered row; insert of a forged row in the middle requires recomputing every subsequent `row_hash`, which is detectable by re-verifying any subsequent **signed** checkpoint. The signing key is operator-configured in production-grade installs (separate from the Midnight release key), giving operators the ability to detect rewrites by anyone who lacks that key.

## Decision

Midnight's audit log uses **per-row hash chaining at MVP**, with the following normative properties (from `FEATURE_AUDIT_HASH_CHAIN.md`):

1. **Row schema** includes `prev_hash`, `row_hash`, `checkpoint_id` plus the canonical event fields. `audit_logs` is monthly range-partitioned on `occurred_at` with a daily pruning worker that produces a `final` checkpoint per partition before drop.
2. **Hash function:** `row_hash = SHA-256(prev_hash || canonical_row)`, where `canonical_row` is RFC 8785 (JCS) JSON canonicalization of the row's domain fields. `canonical_form_version = 'v1'` is recorded.
3. **Genesis:** the first row's `prev_hash = SHA-256("midnight-audit-v1")`. The chain is per-instance, not per-tenant.
4. **Insert path:** `audit_logs` inserts go through a transactional helper that acquires a per-chain advisory lock, reads the previous row's `row_hash`, computes the new `row_hash`, and inserts atomically.
5. **Daily checkpoint:** an `audit_checkpoint` worker creates a row at UTC midnight that signs `(checkpoint_id, last_row_hash, last_row_id, started_at, ended_at)` with **Ed25519**. The signing key is either the Midnight release key or an operator-configured Ed25519 key (configured at install time).
6. **`midnight audit verify` CLI ships in MVP** and verifies live database or JSONL export. Exit codes: 0 healthy, 2 chain break, 3 signature failure, 1 warnings only.
7. **JSONL export** includes `prev_hash`, `row_hash`, `checkpoint_id` and is the only export format at MVP. CSV is "later if useful" (panel: line 1262).
8. **Postgres role separation enforces insert-only at the database:** `midnight_app` has `INSERT` only on `audit_logs` (no `UPDATE`/`DELETE`); `midnight_audit_ro` has `SELECT`; `midnight_migrate` has DDL but is restricted to migrations (panel: line 132).
9. **Sealed variable mutations** record only metadata (length-bucket, version, last-rotated-at). No secret plaintext appears in audit payloads (panel: line 1178; `FEATURE_AUDIT_HASH_CHAIN.md` AC 11).
10. **Health endpoint:** `GET /api/v1alpha/audit/chain/health` returns `last_row_id`, `last_checkpoint_id`, `last_checkpoint_signed_at`, `signing_key_fingerprint`, `chain_genesis`.

## Alternatives Considered

- **No hash chain at MVP; rely on SIEM streaming.** Rejected by panel reclassification (line 2399). SIEM is a separate operator deployment; an OSS install must have a tamper-evidence floor without it. The chain is the floor.
- **Merkle tree per partition with periodic root publication.** Rejected at MVP: more complex than per-row chaining for marginal benefit at the MVP scale; partial-export proofs are nice but not panel-required ("Cryptographic accumulators / Merkle proofs for partial export — not required at MVP," `FEATURE_AUDIT_HASH_CHAIN.md` Out of Scope). Could be added later without breaking the chain.
- **External WORM bucket (S3 Object Lock) at MVP.** Rejected at MVP: requires every operator to provision and pay for an S3-compatible object store with Object Lock. Phase 2 enhancement (`FEATURE_AUDIT_HASH_CHAIN.md` Out of Scope).
- **Sigstore Rekor anchoring.** Rejected at MVP: third-party transparency log dependency, no offline operation. Documented as later (panel: line 1261).
- **Symmetric MAC instead of Ed25519 signature.** Rejected: a MAC verifier needs the signing key, defeating the operator-vs-platform tamper-evidence goal. Ed25519 lets a verifier hold only the public key.
- **Database trigger that prevents `UPDATE`/`DELETE`.** Considered as **complementary**, not as a replacement: triggers can be dropped by `midnight_migrate`. The role-separation grant + the chain together are stronger than either alone. We adopt both: role-separation grant *and* the chain.

## Consequences

Positive:

- Edit/delete/reorder of audit rows is detectable by anyone holding the public verification key, including external auditors and design partners.
- Daily signed checkpoints anchor segments of the chain so verification can be incremental and partition pruning is safe (a `final` checkpoint signs the last hash before drop).
- `midnight audit verify` produces a clear exit code; suitable for CI in operator environments.
- Role separation at Postgres makes the application path itself unable to mutate audit rows.

Negative:

- Insert path acquires an advisory lock; under burst, audit inserts serialize. Mitigated: lock contention is short, and we surface `_insert_latency_seconds` as a metric. Bursty workloads (e.g., bulk import) can still proceed; the chain just commits each row in order.
- Operator-configured signing key adds a key-management burden; we document the tradeoff (operator-configured = stronger evidence vs platform admin; release-key = weaker but zero-config).
- Verification time on a long chain grows linearly. Mitigated by checkpoint-incremental verification (`midnight audit verify --since CHECKPOINT_ID`).
- Pruning must produce a `final` checkpoint before drop or the chain becomes unverifiable across partition boundaries.

Follow-up work:

- Sealed-variable audit fields and length-bucket encoding (`SPRINT_09_VARS_SECRETS_AUDIT.md`).
- Daily checkpoint worker, Ed25519 keypair setup at install, key fingerprint surfaced in `midnight doctor` (`SPRINT_09_VARS_SECRETS_AUDIT.md`).
- `audit_checkpoint` table migration with `prev_checkpoint_id` chaining.
- New error codes `AUDIT_CHAIN_BROKEN`, `AUDIT_CHECKPOINT_SIGNATURE_INVALID`, `AUDIT_KEY_NOT_FOUND` added to the locked failure-code registry (open question in feature dossier; resolve in MVP Beta).
- Webhook event `dev.midnight.audit.checkpoint` fires daily.

## Compliance / Verification

Concrete gates a reviewer or auditor checks:

- **Test name `TestCanonicalRowDeterministic`** in `pkg/audit`: byte-identical canonicalization across two implementations of JCS for the same input (golden fixtures).
- **Test name `TestRowHashRecompute`** in `pkg/audit`: `row_hash` recomputation across 10k rows is deterministic and order-stable.
- **Test name `TestEd25519CheckpointRoundtrip`** in `pkg/audit`: signature round-trip; `audit_checkpoint` signs and verifies with the operator-configured key.
- **Test name `TestVerifyDetectsEdit`** in `pkg/audit`: integration test runs `UPDATE audit_logs SET ...` as `midnight_migrate`; `midnight audit verify` exits with code 2 and reports the row id at the break (panel: line 1261).
- **Test name `TestVerifyDetectsDelete`** in `pkg/audit`: `DELETE FROM audit_logs WHERE id = ...`; verify detects break at the deleted row's successor.
- **Test name `TestVerifyDetectsReorder`** in `pkg/audit`: a sequence edit is caught.
- **Test name `TestRoleSeparationGrants`** in `pkg/storage/roles`: `midnight_app` has no `UPDATE`/`DELETE` privilege on `audit_logs`; `\dp audit_logs` is captured in the test fixture (panel: line 132).
- **Test name `TestPartitionFinalCheckpoint`** in `pkg/audit`: pruning produces a `final` checkpoint per partition before drop; verify after pruning still passes for retained data.
- **CI gate `gate.audit-export-jsonl`:** the audit export endpoint streams NDJSON with cursor pagination and a final manifest line (start/end checkpoint ids, total row count, hash of stream).
- **Runbook check `OR-CHK-AUDIT-CHAIN`** in `midnight doctor`: reports `signing_key_fingerprint`, `last_row_id`, `last_checkpoint_id`, `last_checkpoint_signed_at`. Drift triggers an alert.
- **Code review checklist item `CR-AUDIT-01`:** any new audit event must use the transactional insert helper; bypassing it (e.g., raw `INSERT`) is rejected.
- **Failure drill `DRILL-AUDIT-EDIT`** in `SPRINT_08_FAILURE_DRILLS.md`: simulate an in-database edit; `midnight audit verify` detects and reports.
- **Failure drill `DRILL-KEY-ROTATION`**: rotate Ed25519 key; new checkpoints sign with v_n+1; verify accepts both keys during overlap; old key revoked after grace.

## References

- spec: lines 121, 130, 132, 1174–1184, 1258–1265, 2399
- features: `FEATURE_AUDIT_HASH_CHAIN.md`, `FEATURE_OFFHOST_BACKUP.md`, `FEATURE_DEPLOYMENT_STATE_MACHINE.md`
- sprints: `SPRINT_08_FAILURE_DRILLS.md`, `SPRINT_09_VARS_SECRETS_AUDIT.md`, `SPRINT_13_UPGRADE_TX.md`
- handoff: `SECURITY_REVIEW_PLAN.md`, `OPERATIONS_RUNBOOK_INDEX.md`
