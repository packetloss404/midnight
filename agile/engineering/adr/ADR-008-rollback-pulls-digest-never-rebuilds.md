# ADR-008: Rollback pulls a recorded image digest; rollback never rebuilds

- Status: Accepted (from panel review)
- Date: 2026-04-25
- Deciders: Eight-Specialist Panel
- Tags: [phase-0, build-supply-chain, control-plane, immutability, rollback]

## Context

A "rollback" that re-runs the previous commit's build is wrong: the build environment, base images, package mirrors, BuildKit/Dockerfile-frontend version, and Railpack/Nixpacks plan version drift between the original build and "now." The rebuilt artifact is not the artifact that was previously known good; it is a new, unverified artifact that happens to share a commit SHA. This is a category of incident, not an edge case: a base-image patch release breaks the runtime; a transitive npm dependency is yanked; the language runtime channel ships a regression. Operators rolling back during an incident need the **exact** image they had before, byte-for-byte.

The eight-specialist panel resolved this normatively:

> "Rollback always pulls the previously built image digest; rollback never rebuilds. If the digest is unretrievable, rollback fails with reason code `ROLLBACK_IMAGE_UNAVAILABLE`." (line 77, reaffirmed at line 837 and line 2402)

> "Deployment snapshots store: source SHA, builder type/version/digest, **resolved Railpack/Nixpacks build plan JSON**, **resolved base image digests** for every `FROM`, and the produced image digest. Rollback replays the recorded plan; it does not re-invoke Railpack." (line 78)

> "External-registry digests are probed daily; missing digests for protected deployments emit `EXTERNAL_DIGEST_MISSING`. Production environments default to mirroring active and last-N rollback digests into the embedded registry as a safety net." (line 79)

> "The runtime agent verifies image signatures (cosign or equivalent) before pull/start in the default profile. Mismatch produces `IMAGE_SIGNATURE_INVALID`." (line 80)

> "Rollback creates a new deployment from an old snapshot; it never mutates the old deployment." (line 780)

The panel also pinned that snapshots are **fully self-contained**: resolved variable values are deep-copied; sensitive values are stored as references to a content-addressed secret store with reference counting; `deployment_image_pins` is a reference-counting table consulted by registry GC (line 112, ADR-012).

## Decision

Midnight's rollback contract is:

1. **Rollback never invokes a builder.** It pulls the previously recorded `image_digest` from the configured registries (embedded mirror first, external fallback). The rollback code path has zero compile-time references to BuildKit, Railpack, or Nixpacks libraries.
2. **If the recorded digest is unretrievable from any configured registry**, rollback fails clean with `ROLLBACK_IMAGE_UNAVAILABLE`. The previous `ACTIVE` deployment is unchanged.
3. **Deployment snapshots are immutable** after creation. `UPDATE` returns `409 SNAPSHOT_IMMUTABLE`.
4. **Snapshots are self-contained** with at minimum: source SHA, builder type/version/digest, resolved Railpack/Nixpacks build-plan JSON, resolved base-image digests for every `FROM`, BuildKit version and Dockerfile-frontend digest, the produced `image_digest`, the `image_signature`, and references to SBOM and SLSA-style provenance (line 78, line 844).
5. **`deployment_image_pins` reference counting** prevents registry GC from reclaiming a digest while it is referenced by an active deployment, an in-window rollback target, an artifact promotion, a restore manifest, or audit/evidence retention (line 112, line 1379, ADR-012).
6. **Production environments mirror** active and last-N rollback digests (default N=5) into the embedded registry as a safety net against external-registry deletion (line 79).
7. **Image signatures are verified** by the runtime agent before pull/start (cosign or equivalent); mismatch yields `IMAGE_SIGNATURE_INVALID` and rollback fails (line 80).
8. **Rollback creates a NEW row** in `deployments`; the old row is unchanged. `parent_deployment_id` and `rollback_source` populate (acceptance criterion 10 in `FEATURE_ROLLBACK_BY_DIGEST.md`).
9. **External-registry probing** runs daily for protected deployments; missing digests emit `EXTERNAL_DIGEST_MISSING` to alerts (line 79).

The CLI surface is panel-locked at line 184: `midnight rollback [--to DEPLOYMENT_ID | --to-revision N | --previous]`. Default is `--previous` with a confirmation prompt unless `--yes`.

## Alternatives Considered

- **Rebuild on rollback when the digest is missing.** Rejected by panel: "rollback never rebuilds" (line 77). Even if the rebuild "would have succeeded," the produced artifact is not the artifact the snapshot promises. Documented as forbidden in `FEATURE_ROLLBACK_BY_DIGEST.md` "Out of Scope."
- **Mutate the old deployment's state to roll forward.** Rejected: panel-locked immutability (line 780). Snapshots are evidence; mutating them destroys the audit trail (ADR-011 hash chain would also catch this).
- **Skip signature verification on rollback (assume previously verified).** Rejected: signature verification is a runtime invariant (line 80), not a one-time gate at first deploy. A registry compromise can replace a digest's content; cosign verification is the only catch.
- **Store the image as a tarball blob inside Postgres.** Rejected: catastrophic for backup size and Postgres performance. The embedded registry mirror serves the same purpose with proper blob handling.
- **Pin everything by mutable tag with a "last good" pointer.** Rejected: the panel's whole stance is "Deploy by image digest, not mutable tag" (line 836). Mutable tags violate the guarantee.
- **Auto-rollback on metric regression.** Documented as out of MVP scope (Phase 2 progressive delivery). This ADR covers manual and upgrade-auto rollback only.

## Consequences

Positive:

- A rollback during an incident is a digest pull, not a build. The blast radius is "agent pulls and starts a known-good image." The previous active deployment serves traffic until the new one is ready.
- The snapshot completeness contract makes rollback offline-defensible: even if Railpack changes its plan format, the recorded JSON survives.
- `deployment_image_pins` keeps registry GC from being a tripwire (panel: line 112; line 1379 — pinned digests unreclaimable while pin count > 0).
- External-registry-deletion incidents are detected by the daily probe before they become rollback failures.

Negative:

- Embedded-registry storage grows with N rollback digests per service per environment; defaults are tunable via panel-locked retention (line 144: "last 10 image tags per service plus all digests in active rollback windows"). Disk pressure thresholds catch overgrowth (line 145).
- Untrusted-build digests cannot be rolled back into non-preview environments without explicit approval (panel: line 83, line 839). Operators must accept this boundary.
- Cross-service or cross-environment rollback is out of scope (`FEATURE_ROLLBACK_BY_DIGEST.md` "Out of Scope").
- The snapshot row is heavy (multiple JSONB blobs); we mitigate by storing SBOM/provenance/build-log payloads as content-addressed references rather than inline.

Follow-up work:

- Snapshot schema migration, immutability trigger, `SNAPSHOT_IMMUTABLE` error code (`SPRINT_05_BUILDKIT.md`).
- Rollback worker that pulls digest with embedded-mirror preference (`SPRINT_04_HOST_AGENT.md`, `SPRINT_05_BUILDKIT.md`).
- External-digest daily probe job (`SPRINT_12_REGISTRY_PROVENANCE.md`).
- Production-environment last-N mirror (`SPRINT_12_REGISTRY_PROVENANCE.md`).
- Pin-table reference counting consulted by registry GC sweep (panel: line 144, ADR-012).

## Compliance / Verification

Concrete gates a reviewer or auditor checks:

- **Test name `TestRollbackHasNoBuilderDependency`** in `pkg/rollback`: an import-graph or compile-time test asserts the rollback code path has zero references to `pkg/build/buildkit`, `pkg/build/railpack`, `pkg/build/nixpacks`. Regression fails the build.
- **Test name `TestRollbackImageUnavailable`** in `pkg/rollback`: a fixture where the recorded digest is absent from every configured registry produces `ROLLBACK_IMAGE_UNAVAILABLE` and does not partially mutate state. The previous `ACTIVE` is unchanged.
- **Test name `TestSnapshotImmutable`** in `pkg/control/snapshots`: any attempt to `UPDATE deployment_snapshots` returns `409 SNAPSHOT_IMMUTABLE`; database-level trigger backs the API check.
- **Test name `TestSnapshotCompleteFields`** in `pkg/control/snapshots`: every required field from panel line 78 (source SHA, builder type/version/digest, resolved plan JSON, base image digests, produced digest, image signature) is non-NULL or the build fails with a typed error.
- **Test name `TestRollbackEmbeddedMirrorPreference`** in `pkg/rollback`: external registry returns 404; embedded mirror has the blob; pull succeeds.
- **Test name `TestExternalDigestMissingProbe`** in `pkg/registry/probe`: introduce a missing digest in the external registry; daily probe emits `EXTERNAL_DIGEST_MISSING`; mirror is created if production environment.
- **Test name `TestImageSignatureInvalidOnRollback`** in `pkg/rollback`: cosign verification fails; new deployment enters `FAILED IMAGE_SIGNATURE_INVALID`; previous `ACTIVE` serves throughout.
- **Test name `TestRotatedSecretRollback`** in `pkg/rollback`: snapshot taken at v1 of a secret; rotate to v2; rollback resolves at v2 (current); audit records the access.
- **CI gate `gate.registry-gc-respects-pins`:** registry GC dry-run + apply asserts no pinned digest is reclaimed; "would-have-pinned" line per protected digest.
- **Runbook check `OR-CHK-EXTERNAL-DIGEST`** in `midnight doctor`: reports any deployment with a missing external digest in the last 24h.
- **Code review checklist item `CR-ROLLBACK-01`:** any new code that touches deployment lifecycle must document whether it is a rollback path; rollback paths must not import builder packages.
- **Failure drill `DRILL-REGISTRY-OUTAGE`** in `SPRINT_08_FAILURE_DRILLS.md`: registry outage during rollback; deploy enters `IMAGE_PULL_FAILED` and retries; after exhaustion, `FAILED`; previous `ACTIVE` serves throughout.

## References

- spec: lines 77–84, 112, 144, 184, 780, 836–839, 844–849, 1373–1380, 1475, 2402
- features: `FEATURE_ROLLBACK_BY_DIGEST.md`, `FEATURE_DEPLOYMENT_STATE_MACHINE.md`, `FEATURE_AUDIT_HASH_CHAIN.md`, `FEATURE_UPGRADE_TRANSACTION.md`
- sprints: `SPRINT_05_BUILDKIT.md`, `SPRINT_08_FAILURE_DRILLS.md`, `SPRINT_12_REGISTRY_PROVENANCE.md`, `SPRINT_13_UPGRADE_TX.md`
- handoff: `QUALITY_AND_TEST_STRATEGY.md`, `OPERATIONS_RUNBOOK_INDEX.md`
