# Phase 0 Worker S05 - Build / Registry Handoff

Status: completion planning handoff for Sprint 5 implementation.
Owner lane: BuildKit Dockerfile builds, provenance, embedded registry, image pins, and rollback by digest.

Primary sources:

- `agile/sprints/SPRINT_05_BUILDKIT.md`
- `agile/features/FEATURE_ROLLBACK_BY_DIGEST.md`
- `agile/engineering/adr/ADR-008-rollback-pulls-digest-never-rebuilds.md`
- `agile/engineering/adr/ADR-012-deployment-snapshot-self-contained.md`

## Scope

S05 owns the complete artifact path from uploaded source to deployable image digest:

- Pinned BuildKit, Dockerfile frontend, syft, and cosign versions, recorded in every build and checked at worker startup.
- BuildKit service packaging as a dedicated `midnight-buildkit` user with cgroup limits, tmpfs state, and explicit no-read boundaries around database, secrets, registry data, and KMS paths.
- `build` worker queue behavior: timeout, per-host and per-project concurrency, disk-pressure refusal, progress events, and Retry-After hints.
- Dockerfile build execution through BuildKit with local per-service cache only, no remote cache by default.
- Build-time variable handling: secrets only through BuildKit secret mounts, public/build variables through build args, and secret fingerprints stored without plaintext.
- Build-output secret scanning before push, with terminal `BUILD_OUTPUT_SECRET_LEAK`.
- In-toto SLSA v1.0 provenance emission and cosign signing per produced image. No SLSA Build Level is claimed.
- Embedded registry push, OCI referrers for attestations/signatures where supported, tag retention, pin-aware sweep, and destructive GC CLI with dry-run/apply.
- Untrusted-build classification, quarantine registry namespace, secret/cache isolation, and promotion denial without explicit approval.
- Deployment snapshot completeness for Dockerfile builds: source SHA, builder kind/version/digest, resolved Dockerfile plan, base image digests for every `FROM`, produced image digest, signature, SBOM/provenance refs.
- Rollback by digest end-to-end: rollback creates a new deployment row, pulls the recorded digest, never invokes BuildKit, and fails with `ROLLBACK_IMAGE_UNAVAILABLE` if unavailable.

Non-owned boundaries:

- S03 owns the generic API envelope, idempotency, operations, job queue substrate, auth, and base schema scaffolding.
- S04 owns runtime-agent container orchestration, Docker Engine API use, signature verification before pull, deployment instance reconciliation, and existing-image deploy.
- S06 owns ingress, logs, timeline rendering, final failure-code taxonomy wiring, and default Phase 0 observability polish.
- S07 owns support bundle, backup/restore, audit-verify CLI, alert packaging, and feature-kill-switch UX.
- S08 owns failure-drill harness and Phase 0 exit gate execution.

## Owned Future Implementation Paths

Create or update these paths when implementation begins. If the repository later chooses a different source layout, keep these ownership boundaries and update this handoff.

Versioning and compatibility:

- `versions.yaml` - BuildKit, Dockerfile frontend, syft, cosign pinned versions and digests.
- `docs/compat/buildkit.md` - compatibility matrix row and upgrade/skew behavior.
- `docs/security/build-provenance.md` - provenance, signing, no-SLSA-level-claim statement.
- `docs/security/untrusted-builds.md` - classification rules, quarantine, approval.
- `docs/control-plane/deployment-snapshot.md` - snapshot completeness and immutability.
- `docs/cli/registry-gc.md` - JSON output schema and apply semantics.

Packaging, systemd, and doctor:

- `packaging/deb/` or equivalent - BuildKit service, tmpfs mount, buildkitd config, `.deb` `Depends:`/`Breaks:`.
- `packaging/systemd/midnight-buildkit.service`
- `packaging/systemd/midnight-buildkit.slice`
- `packaging/tmpfiles.d/midnight-buildkit.conf`
- `internal/doctor/` - checks for pinned digest mismatch, rootful warning, permission boundaries, cache and registry health.

Build plane:

- `internal/buildkit/` - local BuildKit client wrapper, daemon digest check, solve invocation.
- `internal/build/worker/` - build queue worker, timeout, concurrency, disk-pressure behavior.
- `internal/build/cache/` - per-service local cache, quota checks, corruption handling.
- `internal/build/secrets/` - tmpfs secret files, auth checks, fingerprinting, cleanup.
- `internal/build/dockerfile/` - Dockerfile parser and `FROM` digest resolver.
- `internal/build/scan/` - layer/final-filesystem secret-prefix scanner.
- `internal/build/provenance/` - in-toto SLSA v1.0 statement generation.
- `internal/build/signing/` - cosign key/static-key/keyless integration.

Registry and pins:

- `internal/registry/embedded/` - push/pull probes, referrers, read-only switch.
- `internal/registry/retention/` - last-10 tag retention and rollback-window protection.
- `internal/registry/gc/` - dry-run/apply, advisory lock, post-GC verification.
- `internal/registry/probe/` - daily external digest probe for protected deployments.
- `internal/control/pins/` - `deployment_image_pins` reference counting and pin transitions.

Snapshots, deployments, rollback:

- `internal/control/snapshots/` - immutable snapshot insert validator.
- `internal/control/deployments/` - Dockerfile build to deployment handoff.
- `internal/control/rollback/` - digest-only rollback selector and enqueue path.
- `cmd/midnight/registry.go` - `midnight registry gc --dry-run|--apply`.
- `cmd/midnight/rollback.go` - `midnight rollback [--to|--to-revision|--previous]`.
- `migrations/` - deployment snapshot fields, pins table, build metadata fields.
- `runbooks/rollback-image-unavailable.md`
- `runbooks/registry-gc.md`
- `scripts/demo/sprint5-build.sh`

## Dependency Contracts From S03

S05 consumes these S03 contracts and must not bypass them:

- Job queue: enqueue and lease only through the S03 Postgres queue contract. Use queue names `build`, `deploy`, and `cleanup`; honor leases, heartbeat cadence, dead-letter behavior, `parent_operation_id`, and per-tenant `partition_key`.
- Operations: all user-visible build/deploy/rollback/registry-GC work reports through S03 Operation resources and SSE event streams. Build progress events must carry `operation_id` and `request_id`.
- Idempotency: `midnight deploy`, rollback, approval, and registry-GC apply endpoints require S03 Idempotency-Key semantics on mutating requests.
- Error envelope and registry: every new S05 code is additive and appears in `docs/error-codes.md`, including `BUILDKIT_VERSION_MISMATCH`, `BUILD_ROOTFUL`, `BUILD_TIMEOUT`, `BUILD_OUTPUT_SECRET_LEAK`, `BUILDKIT_CACHE_CORRUPTED`, `BUILD_FAILED`, `IMAGE_PUSH_FAILED`, `ROLLBACK_IMAGE_UNAVAILABLE`, `EXTERNAL_DIGEST_MISSING`, `UNTRUSTED_BUILD_PROMOTION_DENIED`, `REGISTRY_GC_VERIFY_FAILED`, and `SNAPSHOT_IMMUTABLE`.
- Auth and variable authorization: before BuildKit is invoked, S05 must call the S03 auth/scope path for every referenced variable. Unauthorized secret access fails with `VARIABLE_FORBIDDEN`.
- Schema base: extend the S03 `builds`, `deployments`, `deployment_snapshots`, `jobs`, `operations`, `events`, and `audit_logs` tables rather than creating parallel state stores.
- Audit/events: build starts, build failures, image signing, snapshot creation, registry-GC apply, rollback request/completion/failure, pin changes, and untrusted approvals must emit audit rows and event records through S03 infrastructure.
- Cursor/OpenAPI/contract tests: any S05 API surface must be documented in `schemas/openapi/v1alpha.yaml` and covered by the S03 Schemathesis/Pact scaffolding.

## Dependency Contracts From S04

S05 depends on these S04 deliverables:

- Existing-image deploy path: after a successful build and push, S05 enqueues a normal `deploy` job using the S4 deploy payload contract. Do not introduce a separate deploy mechanism for built images.
- Runtime signature verification: S05 produces signatures and provenance; S04 agent verifies signatures before pull/start. The digest and signature refs in snapshots must be sufficient for S04 verification.
- Deployment FSM: S05 build worker may drive `QUEUED -> BUILDING -> DEPLOYING` or `FAILED`; only the deployment reconciler promotes `DEPLOYING -> ACTIVE`. All transitions use guarded updates with resource version checks and per `(service, environment)` advisory lock where required.
- Image digest labels: S04 containers carry `midnight.image_digest`; S05 must provide immutable digests, not mutable tags, in deployment specs.
- Deployment image pins: S4 existing-image deploy inserts the initial pin row; S05 extends this to built images, rollback-window pins, GC consultation, and pin decrement.
- Host heartbeat/disk pressure: S05 build worker consumes host disk metrics and blocks new builds at the S5 thresholds; in-flight builds get the 10-minute grace. BuildKit cache prune runs before registry GC.
- Agent/runtime errors: image pull and signature failures remain S04 deployment failures (`IMAGE_PULL_FAILED`, `IMAGE_SIGNATURE_INVALID`). S05 rollback maps digest absence to `ROLLBACK_IMAGE_UNAVAILABLE` before or during the pull path as specified.
- Desired-state reconciliation: built-image deployments must look indistinguishable from existing-image deployments once they reach the agent.

## Exported Artifacts To S06

S06 ingress, logs, timeline, and failure taxonomy consume:

- Deployment timeline event payloads for phases `source`, `builder`, `image`, `host`, `start`, `healthcheck`, `route`, `tls`, and `failure`; S05 supplies the first three and rollback labels.
- Snapshot fields for timeline display: builder kind/version/digest, Dockerfile path/plan, resolved base image digests, produced image digest, signature ref, provenance URI, SBOM ref, trust classification, build duration, cache hit summary.
- Build and rollback reason codes with terminal/retriable classification for the S6 failure registry.
- Build logs already redacted and safe for S6 log/timeline rendering; secret scan matches expose pattern ids and offsets only, never matched bytes.
- Metrics for build queue depth, build duration, BuildKit cache size, BuildKit cache corruption, image push failures, registry-GC candidates, registry bytes reclaimable, and rollback-digest pull failures.
- Demo hooks for S6 Phase 0 narrative: deliberate secret leak -> `BUILD_OUTPUT_SECRET_LEAK`; rollback by digest timeline; BuildKit cache corruption recovery.

## Exported Artifacts To S07

S07 supportability, backup, audit verify, and alert packaging consume:

- Registry backup contract: embedded registry metadata and blobs are restorable as platform backup artifacts, with pinned digest retrievability verified after restore.
- Support bundle selectors and redaction rules for build logs, BuildKit config, registry GC reports, provenance refs, and registry probe output. Secrets, tokens, private keys, secret tmpfiles, and raw env dumps remain hard-blocked.
- Alert metrics for `BuildKitCacheCorruption`, `DeployJobStuck` on build queue, registry-GC verification failures, and disk-pressure build blocking.
- `registry.gc` feature flag kill switch behavior for `midnight feature list/disable/enable` and `doctor`.
- Runbooks for registry GC and rollback-image-unavailable, linked from error-code fix hints.
- Audit event schemas for build, signing, snapshot, pin, registry-GC, untrusted approval, external digest probe, and rollback actions.

## Exported Artifacts To S08

S08 drill and Phase 0 exit gate consume:

- Drill fixtures for image pull failure, readiness failure interaction, BuildKit cache corruption, secret leak, missing rollback digest, registry GC respecting pins, and rollback never rebuilding.
- A deterministic BuildKit cache corruption injector and metric/event pair: `BUILDKIT_CACHE_CORRUPTED` event plus `midnight_buildkit_cache_corrupt_total`.
- A rollback fixture: deploy A, deploy B, rollback to A with BuildKit build counter unchanged; then remove A's blob and assert `ROLLBACK_IMAGE_UNAVAILABLE`.
- Registry GC dry-run/apply fixture showing pinned digests never appear in reclaim candidates and post-GC sample verification runs.
- Phase 0 exit evidence: `scripts/demo/sprint5-build.sh`, provenance JSONL, cosign verification output, registry GC dry-run JSON, support-bundle sample with no secret plaintext.

## Acceptance Checklist

- [ ] `versions.yaml` pins BuildKit, Dockerfile frontend, syft, and cosign by version and digest; worker startup refuses mismatch with `BUILDKIT_VERSION_MISMATCH`.
- [ ] BuildKit runs as `midnight-buildkit`, under `midnight-buildkit.slice`, with tmpfs at `/run/midnight-buildkit`, cgroup caps, and no read access to protected `/var/lib/midnight/*` paths.
- [ ] `build` worker class uses S03 queue, default timeout 30 minutes, max 4 hours, per-host concurrency `min(4, cores/2)`, and per-project concurrency 2.
- [ ] Disk pressure blocks new builds at the sprint threshold; in-flight builds get the specified grace; BuildKit cache prune happens before registry GC.
- [ ] Build cache is local per service only. No registry or remote cache import/export is passed.
- [ ] Secrets are never passed as ARG/ENV. Secret mounts use tmpfs files mode `0400`, are deleted on build exit, and snapshot only name/fingerprint.
- [ ] Secret-prefix scan runs after build and before push; positive matches fail terminally with `BUILD_OUTPUT_SECRET_LEAK` and redacted evidence.
- [ ] Every produced image is signed and has in-toto SLSA v1.0 provenance pushed as an OCI referrer or documented fallback.
- [ ] Deployment snapshot captures all ADR-008/ADR-012 required fields and is immutable; update/delete attempts produce `SNAPSHOT_IMMUTABLE`.
- [ ] Dockerfile parser records every `FROM`, including multi-stage base images, with resolved digest before BuildKit solve.
- [ ] Registry retention keeps last 10 tags per service plus active pins, rollback-window pins, protected snapshots, promotions, restore manifests, and audit retention pins.
- [ ] `midnight registry gc --dry-run` is non-mutating JSON; `--apply --yes` takes the global lock, sets registry read-only, runs GC, and post-verifies sampled pinned digests.
- [ ] Untrusted builds use zero project secrets, ephemeral cache only, quarantine namespace, 7-day quarantine retention, and require explicit approval for non-preview promotion.
- [ ] Build E2E path goes source upload -> BuildKit -> scan -> sign/attest -> embedded registry -> snapshot -> deploy job -> S04 agent.
- [ ] Rollback creates a new deployment row from an old snapshot, pulls the recorded digest, verifies signature through S04, and never invokes BuildKit.
- [ ] Missing rollback digest fails as `ROLLBACK_IMAGE_UNAVAILABLE`; previous `ACTIVE` deployment remains serving.
- [ ] External registry protected digests are probed daily; missing production/protected digests emit `EXTERNAL_DIGEST_MISSING`.
- [ ] Docs, runbooks, OpenAPI, error-code registry, compatibility matrix, and demo script are updated before S05 is considered done.

## Risks

- BuildKit rootful worker mode is accepted for Alpha but security-sensitive. Mitigation: strict user separation, cgroup caps, no-read ACL tests, tmpfs-only secret files, and `BUILD_ROOTFUL` doctor warning.
- Cosign keyless/Fulcio dependencies can make CI flaky. Mitigation: static-key path for CI and offline verification fixtures; keyless remains supported for cloud installs.
- Secret scanner false positives can block legitimate builds. Mitigation: pattern-specific evidence without matched bytes, fixture negatives, and documented allow annotations only after security review.
- Build cache corruption is hard to reproduce. Mitigation: deterministic corruptor fixture owned by S05 and exported to S08.
- Registry GC is destructive after apply. Mitigation: dry-run by default, explicit `--yes`, read-only registry, advisory lock, post-GC verification, and feature kill switch `registry.gc`.
- Snapshot schema may drift from ADR-008/ADR-012. Mitigation: required-field insert validator, database immutability trigger, OpenAPI/schema tests, and code review checklist.
- Untrusted-build classification has weak Alpha source signals before full Git provider integration. Mitigation: document Alpha limits, classify conservatively on non-member author and branch allowlist, keep approval audit mandatory.
- S5-12 is a horizontal 8-point integration slice. Mitigation: land version/service/worker/snapshot/scanner/signing pieces behind a stub build operation before wiring full deploy.

## Exact First Implementation Tasks

1. Add `versions.yaml` entries for BuildKit, Dockerfile frontend, syft, and cosign with pinned digests; add the parser and startup digest check that returns `BUILDKIT_VERSION_MISMATCH`.
2. Add `docs/compat/buildkit.md` and the `.deb` compatibility constraints for BuildKit/frontend/syft/cosign.
3. Create the BuildKit packaging skeleton: `midnight-buildkit` user, service, slice, tmpfs mount, `/etc/midnight/buildkitd.toml`, and doctor checks for UID/cgroup/protected-path denial.
4. Extend migrations for `builds`, `deployment_snapshots`, and `deployment_image_pins` to carry required S5 fields; add snapshot immutability trigger and pin-count indexes.
5. Register the `build` worker pool against the S03 job queue with timeout, host concurrency, project advisory lock, heartbeat, cancellation, and operation progress events.
6. Implement Dockerfile context preparation: honor `.dockerignore`, resolve every `FROM` to a digest, and persist the snapshot draft before `BuildKit.Solve`.
7. Implement local per-service cache paths and quotas, plus an untrusted ephemeral cache path under `/run/midnight-buildkit/untrusted/<build_id>`.
8. Implement build variable resolution: reject unauthorized secrets, write BuildKit secret tmpfiles, pass only public/build variables as build args, and persist secret fingerprints.
9. Wire the first BuildKit solve path for a tiny Dockerfile, without signing yet, returning a produced local image digest and structured build events.
10. Add the build-output secret scanner and make it gate image push with terminal `BUILD_OUTPUT_SECRET_LEAK`.
11. Add cosign signing and in-toto provenance generation; push signatures/attestations beside the image.
12. Push to the embedded registry under service namespace or quarantine namespace, then enqueue the existing S04 deploy job with the immutable digest.
13. Implement registry retention preview, then `midnight registry gc --dry-run`, then guarded `--apply --yes` with post-verify.
14. Implement rollback API/CLI selector and worker path that imports no builder packages; add the no-build metric/import-graph test.
15. Add `scripts/demo/sprint5-build.sh` covering build, secret leak, untrusted quarantine, registry dry-run, and rollback by digest.

