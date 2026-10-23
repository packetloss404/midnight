# ADR-012: Deployment snapshot is self-contained; image pins are reference-counted

- Status: Accepted (from panel review)
- Date: 2026-04-25
- Deciders: Eight-Specialist Panel
- Tags: [phase-0, mvp-beta, control-plane, snapshots, registry-gc, immutability]

## Context

A deployment snapshot is the evidence that a deployment happened. If a snapshot is incomplete — missing the resolved Railpack/Nixpacks plan, missing base-image digests, missing the secret references — it cannot be replayed offline-defensibly. If snapshot rows reference live secret values, a rotated secret silently changes what "rollback" means. If registry GC reclaims a digest while a deployment still references it (active, in rollback window, in a restore manifest, or pinned by audit retention), the rollback contract (ADR-008) is broken. The eight-specialist panel resolved all three concerns in one normative paragraph:

> "Deployment snapshot is fully self-contained: resolved variable values are deep-copied (sensitive values stored as references to a content-addressed secret store with reference counting). `deployment_image_pins` reference-counting table is consulted by registry GC; pinned digests are unreclaimable while pin count > 0 or within the rollback window." (line 112)

> "Deployment snapshots store: source SHA, builder type/version/digest, **resolved Railpack/Nixpacks build plan JSON**, **resolved base image digests** for every `FROM`, and the produced image digest. Rollback replays the recorded plan; it does not re-invoke Railpack." (line 78)

> "Default retention: last 10 image tags per service plus all digests in active rollback windows. `midnight registry gc --dry-run` and `midnight registry gc --apply` ship in Phase 0." (line 144)

> "Full destructive blob GC runs only with registry read-only or stopped, a global GC lock, dry-run, and post-GC verification." (line 1379)

This ADR is the home for the snapshot completeness invariants and the pin-table semantics. It complements ADR-008 (rollback never rebuilds) and ADR-011 (audit hash chain) — those define the rollback and tamper contracts; this ADR defines the data-completeness contract that makes them implementable.

## Decision

Midnight's deployment snapshot is **fully self-contained** and **immutable** after creation. The `deployment_snapshots` row carries every field required to replay the deployment without re-invoking Railpack, BuildKit, or any builder. Sensitive values are stored as content-addressed references with reference counting. A separate `deployment_image_pins` table reference-counts every digest required by an active deployment, an in-window rollback target, an artifact promotion, a restore manifest, or audit/evidence retention. Registry GC consults the pin table and refuses to reclaim digests with `pin_count > 0`.

### Required snapshot fields (panel-locked)

The full schema is in `FEATURE_ROLLBACK_BY_DIGEST.md`. Required (build-time error if missing):

- `id`, `deployment_id`, `service_id`, `environment_id`
- `source_sha` (when source is git/Dockerfile-from-source)
- `builder_type`, `builder_version`, `builder_image_digest`
- `buildkit_version`, `dockerfile_frontend_digest` (when builder is Dockerfile)
- `resolved_build_plan` (jsonb — full Railpack/Nixpacks plan or Dockerfile path)
- `resolved_base_image_digests` (jsonb — `{ "FROM ref": "sha256:..." }` for every `FROM`)
- `image_repository`, `image_digest`, `image_signature`
- `runtime_command`, `runtime_resource_limits`, `healthcheck_spec`
- `resolved_runtime_vars` (jsonb — non-secret resolved values, deep-copied)
- `resolved_runtime_secret_refs` (jsonb — array of `{ name, secret_ref, version }`)
- `created_at`, `created_by`

Snapshot rows are immutable: any `UPDATE` returns `409 SNAPSHOT_IMMUTABLE`. A database-level trigger backs the API check.

### Variable resolution

- Non-secret variables are **deep-copied** into the snapshot at build/runtime resolution time. A subsequent rename or value change of the source variable does not retroactively alter the snapshot.
- Sensitive variables are stored as **content-addressed secret references**. The reference points to a row in the secret store keyed by content hash; the secret store has reference counting independent of the deployment-image pin table. Rollback resolves the reference at deploy time against the **current ciphertext** (line 132 audit, panel: `FEATURE_ROLLBACK_BY_DIGEST.md` AC 9).
- Sealed variables follow ADR-aligned semantics (panel: line 120, line 1223): rotation is "new value, not view-then-edit." Rollback uses the snapshot's secret reference, which dereferences to the **current** secret value.

### `deployment_image_pins` reference counting

```text
deployment_image_pins
  digest          (R) sha256:...
  service_id      (R)
  deployment_id   (R)
  reason          (R) 'active' | 'rollback_window' | 'promotion' | 'restore_manifest' | 'audit_retention'
  pinned_until    (R) timestamp; null = while reason holds
  created_at      (R)
PRIMARY KEY (digest, service_id, deployment_id, reason)
```

GC's effective pin count for a digest is:

```sql
SELECT count(*) FROM deployment_image_pins
WHERE digest = ?
  AND (pinned_until IS NULL OR pinned_until > now())
```

Registry GC refuses to reclaim any digest with `pin_count > 0`. The non-destructive daily sweep (line 1377) deletes only untagged manifests older than 14 days that are not referenced by any deployment, rollback window, restore manifest, or audit/evidence retention. Full destructive blob GC runs only with registry read-only or stopped, a global GC lock, dry-run, and post-GC verification (line 1379).

### Default retention

- **Last 10 image tags per service** plus all digests in active rollback windows (panel: line 144).
- **Default `rollback_window` = 14 days** (open question in feature dossier; default proposed and adopted unless tightened in MVP Beta).
- **Production-environment last-N digest mirror** (default `N = 5`) into the embedded registry (panel: line 79).

## Alternatives Considered

- **Lazy snapshot: store source SHA only and re-resolve everything at rollback time.** Rejected: re-resolution drifts (base images, Railpack channel, npm registry, etc.). Defeats the panel's "rollback never rebuilds" contract (ADR-008).
- **Snapshot embeds secret plaintext.** Rejected: panel-locked. "Secret plaintext must not appear in deployment snapshots, logs, AI context, webhooks, support bundles, or audit payloads" (line 1221).
- **Single global pin counter per digest, no `reason` column.** Rejected: makes GC dry-run output less useful (operator can't see "this digest is pinned because of restore manifest A and rollback window B"). The compound primary key allows multi-cause pinning; GC reasoning is auditable.
- **No reference counting; rely on tag retention only.** Rejected: tag retention ("last 10 image tags") protects active code paths but not in-progress restore manifests, audit retention, or production last-N mirror digests. Panel locked the reference-count table (line 112).
- **Embed image bytes (the full image tarball) in Postgres.** Rejected: catastrophic for backup size and Postgres performance. Embedded registry mirror (panel: line 79) handles blob storage correctly.
- **Snapshot at API request time, not at build completion.** Rejected: API-time snapshot lacks `image_digest`, `image_signature`, `dockerfile_frontend_digest`, and `resolved_base_image_digests` — all of which are only known after BuildKit produces the image.

## Consequences

Positive:

- Rollback is offline-defensible: even if external registries forget the digest, the snapshot row + embedded mirror gives a complete record.
- Registry GC cannot accidentally reclaim a digest that is in an active rollback window, a restore manifest, or audit retention.
- Sensitive values rotate without retroactively altering snapshots; the snapshot stores the **reference**, not the secret.
- The `reason` column on the pin table makes GC dry-run output diagnostic ("would-have-pinned because of `restore_manifest`").
- Schema completeness is a build-time error, not a runtime surprise.

Negative:

- Snapshot rows are JSONB-heavy; large `resolved_build_plan` and `resolved_base_image_digests` for multi-stage Dockerfiles can be a few KB each. Acceptable.
- Reference-count table has high write rate at deploy/rollback boundaries; we mitigate by partitioning if churn becomes a problem (out of MVP).
- Pin-table consultation adds a JOIN to GC sweeps; embedded registry GC is already opt-in (panel: line 1379), so the cost is bounded.
- Sealed variable rollback semantics are subtle: "rollback uses current value" matches the rotate-only contract, but operators need to be told this clearly. Documented in the rollback runbook.

Follow-up work:

- Snapshot completeness validator runs at insert time (`SPRINT_05_BUILDKIT.md`).
- `SNAPSHOT_IMMUTABLE` error code added to the locked failure-code registry.
- `deployment_image_pins` migration with the panel-locked primary key (`SPRINT_12_REGISTRY_PROVENANCE.md`).
- Registry GC sweep + dry-run/apply CLI ships in Phase 0 (`SPRINT_12_REGISTRY_PROVENANCE.md`).
- Production last-N mirror worker (`SPRINT_12_REGISTRY_PROVENANCE.md`).

## Compliance / Verification

Concrete gates a reviewer or auditor checks:

- **Test name `TestSnapshotCompleteFields`** in `pkg/control/snapshots`: every required field is non-NULL or build fails with a typed error.
- **Test name `TestSnapshotImmutable`** in `pkg/control/snapshots`: `UPDATE deployment_snapshots ...` returns `409 SNAPSHOT_IMMUTABLE` from API and from a database trigger.
- **Test name `TestVariableDeepCopy`** in `pkg/control/snapshots`: change a non-secret variable's value after snapshot creation; the snapshot's `resolved_runtime_vars` is unchanged.
- **Test name `TestSecretReferenceCurrent`** in `pkg/control/snapshots`: a rotated-secret fixture asserts rollback uses the current ciphertext (snapshot stores the reference, not the value); audit row records the access.
- **Test name `TestImagePinReferenceCount`** in `pkg/control/pins`: concurrent insert/decrement is consistent; pin count never goes negative; pin reasons are stable.
- **Test name `TestRegistryGCRespectsPins`** in `pkg/registry/gc`: dry-run shows pinned digests as untouched; apply respects pins; "would-have-pinned" output names every protected digest with reason.
- **Test name `TestPinReasonAfterRollback`** in `pkg/control/pins`: rollback to deployment B from deployment A; B gets `active` pin, A's `active` becomes `rollback_window`; pin counts are consistent.
- **CI gate `gate.snapshot-schema-required`:** schema migration test asserts every required column is `NOT NULL` and a database trigger blocks `UPDATE`/`DELETE` on `deployment_snapshots`.
- **CI gate `gate.gc-dry-run-required`:** integration test runs `midnight registry gc --dry-run` against a fixture with pinned digests; asserts no pinned digest appears in the "would-reclaim" set.
- **Runbook check `OR-CHK-PINS`** in `midnight doctor`: reports the per-service pin count and any digest with `pin_count = 0` that is still inside a rollback window (drift).
- **Code review checklist item `CR-SNAPSHOT-01`:** any change to snapshot fields requires a migration and a backward-compatible read path; field removal requires deprecation per the panel-locked deprecation header machinery (line 171).
- **Failure drill `DRILL-GC-RESPECTS-PINS`** in `SPRINT_08_FAILURE_DRILLS.md`: trigger a GC sweep mid-rollback; assert no pinned digest is reclaimed; rollback proceeds successfully.
- **Failure drill `DRILL-EXTERNAL-DIGEST-MISSING`**: external registry deletes a digest that is pinned in `restore_manifest`; daily probe emits `EXTERNAL_DIGEST_MISSING`; mirror is created.

## References

- spec: lines 78, 79, 112, 144, 780, 1221, 1373–1380, 1379
- features: `FEATURE_ROLLBACK_BY_DIGEST.md`, `FEATURE_DEPLOYMENT_STATE_MACHINE.md`, `FEATURE_AUDIT_HASH_CHAIN.md`, `FEATURE_OFFHOST_BACKUP.md`
- sprints: `SPRINT_05_BUILDKIT.md`, `SPRINT_08_FAILURE_DRILLS.md`, `SPRINT_12_REGISTRY_PROVENANCE.md`, `SPRINT_13_UPGRADE_TX.md`
- handoff: `QUALITY_AND_TEST_STRATEGY.md`, `SECURITY_REVIEW_PLAN.md`, `OPERATIONS_RUNBOOK_INDEX.md`
