# Phase 0 Worker S07: Support / Backup / Observability Handoff

- **Worker:** S07 Support/Backup/Observability
- **Primary source:** `agile/sprints/SPRINT_07_OBSERVABILITY_SUPPORT.md`
- **Reviewed sources:**
  - `agile/features/FEATURE_OFFHOST_BACKUP.md`
  - `agile/features/FEATURE_AUDIT_HASH_CHAIN.md`
  - `agile/handoff/OPERATIONS_RUNBOOK_INDEX.md`
  - `agile/handoff/phase0-subagents/S02_INSTALLER_OPS.md`
  - `agile/handoff/phase0-subagents/S03_API_DATA.md`
  - `agile/handoff/phase0-subagents/S05_BUILD_REGISTRY.md`
  - `agile/handoff/phase0-subagents/S06_INGRESS_LOGS_DX.md`
  - `agile/sprints/SPRINT_08_FAILURE_DRILLS.md`
- **Status:** Phase 0 planning handoff complete. This workspace is documentation-only at `D:/projects/midnight`; local Git metadata exists, but no implementation code or product scaffold exists yet.

## Scope

S07 owns the operator supportability gate for Phase 0 Alpha:

- Default Prometheus alert rules pack with exactly the eight locked Alpha alerts:
  `DiskPressure`, `AgentLost`, `BackupFailed`, `CertExpiringSoon`,
  `DeployJobStuck`, `BuildKitCacheCorruption`, `ConntrackPressure`, and
  `BackupOffhostMissing`.
- Default Grafana dashboard packaging for the S07 alert/metric set.
- `BackupOffhostMissing` doctor check, stable check ID
  `OR-CHK-080 backup.offhost.configured`, and matching metric
  `midnight_backup_offhost_destinations_total`.
- `midnight support-bundle` CLI, deterministic tar.zst archive, preview
  manifest, selector grammar, size cap, per-category truncation, and locked
  redaction exclusions.
- Platform backup and restore smoke surface: `midnight backup create --platform`,
  `midnight restore --platform`, restore step recording, ACME-state restore
  checks, and step-10 smoke probes.
- `midnight audit verify` CLI for audit hash-chain verification.
- Phase 0 runbook packaging and `midnight runbook list/describe` metadata for
  the eight Phase 0 runbooks.
- Opt-in telemetry scaffolding: default-off install behavior, payload schema,
  forbidden-key fixture, and emergency feature disable.
- `midnight feature` kill-switch surface for `acme.issuance`, `registry.gc`,
  `ai.gateway`, `auto.remediation`, and `telemetry`, with state reflected in
  `doctor`.

S07 does not own:

- Host metric collection inside the runtime agent. S07 owns the metric names,
  alert rules, fixtures, and dashboard expectations; S04/S06 provide agent-side
  data collection and ingress/ACME metrics.
- Generic API envelope, idempotency, Operation/SSE substrate, job queue, and
  schema migration framework. These are S03 contracts.
- BuildKit, registry internals, provenance, registry GC, or image pins. These
  are S05 contracts that S07 consumes for backup, alerts, and support bundles.
- Full custom-domain product flow. S07 consumes S06's ACME state and domain
  diagnostics for backup/restore and support artifacts.
- Sprint 8 drill execution. S07 exports fixtures, runbooks, metrics, and
  artifacts that S08 gates.

## Phase 0 Boundary Flag: Off-Host Backup

There is a direct scope conflict that must be resolved before implementation:

- `SPRINT_07_OBSERVABILITY_SUPPORT.md` makes off-host platform backup part of
  the Sprint 7 supportability gate. BAK-706 asks for
  `midnight backup create --platform` with `s3`, `sftp`, and `local` providers
  shipping, and GCS scaffolded behind a feature flag if capacity slips.
- `agile/features/FEATURE_OFFHOST_BACKUP.md` classifies off-host backup as
  **MVP Beta**, not Phase 0 Alpha. It explicitly says Alpha may be local-only
  with a loud `BACKUP_OFFHOST_MISSING` warning, while MVP Beta requires at
  least one external destination.

Recommended Phase 0 interpretation unless PM3/product explicitly re-scopes:

1. Phase 0 Alpha must ship the `BackupDestination` interface, destination config
   validation, `local://` or `file://` local platform backup, encrypted backup
   metadata, `BACKUP_OFFHOST_MISSING` doctor warning, matching metric, alert,
   and support/runbook coverage.
2. External off-host providers (`s3://`, `sftp://`, `gcs://`, `rsync://`) should
   land behind feature flags or CI-only integration fixtures unless the Phase 0
   boundary is formally widened.
3. Sprint 8 should gate "no off-host destination configured" as a warning/alert
   in Alpha, not as a Phase 0 install failure, unless the above re-scope is
   approved.
4. MVP Beta should promote at least one external destination to required, and
   production-grade installs should refuse "complete" status without it.

This handoff treats `BackupOffhostMissing` as mandatory Phase 0. It treats
production-grade off-host provider completeness as a scope decision that must be
confirmed before code lands.

## Owned Future Implementation Paths

If the repository layout from S01 lands with different names, keep these as
ownership boundaries and map them to the canonical Go/module paths.

| Area | Future paths |
|---|---|
| Alert pack and dashboard | `packaging/deb/etc/midnight/prometheus/midnight-alerts.rules.yml`, `packaging/deb/usr/share/midnight/dashboards/midnight-overview.json`, `observability/prometheus/`, `observability/grafana/`, `tests/observability/alerts/` |
| Support bundle CLI | `cmd/midnight/support_bundle.go`, `internal/supportbundle/`, `internal/supportbundle/manifest/`, `internal/supportbundle/selectors/`, `internal/supportbundle/archive/`, `tests/fixtures/supportbundle/` |
| Redaction integration | `internal/redaction/`, `internal/supportbundle/redaction/`, `tests/fixtures/redaction/secret-leak-gate/` |
| Backup destinations | `cmd/midnight/backup.go`, `internal/backup/`, `internal/backup/destinations/`, `internal/backup/destinations/local/`, `internal/backup/destinations/s3/`, `internal/backup/destinations/sftp/`, `internal/backup/metadata/` |
| Platform restore | `cmd/midnight/restore.go`, `internal/restore/platform/`, `internal/restore/smoke/`, `internal/restore/acme/`, `internal/operations/restore_steps/` |
| Audit verify | `cmd/midnight/audit.go`, `internal/audit/verify/`, `internal/audit/canonical/`, `internal/audit/checkpoint/`, `tests/fixtures/audit-chain/` |
| Doctor integration | `internal/doctor/checks/backup_offhost.go`, `internal/doctor/checks/features.go`, `docs/doctor/checks.md` |
| Runbooks | `docs/runbooks/`, `packaging/deb/usr/share/midnight/runbooks/`, `cmd/midnight/runbook.go`, `internal/runbooks/` |
| Telemetry | `cmd/midnight/feature.go`, `internal/telemetry/`, `internal/telemetry/schema/`, `packaging/deb/usr/share/midnight/telemetry-schema.json`, `tests/fixtures/telemetry/` |
| Feature flags | `internal/features/`, `internal/features/store/`, `internal/features/doctor/`, `docs/features.md` |
| Packaging tests | `tests/package/deb_contents_test.go`, `tests/package/runbook_links_test.go`, `tests/package/promtool_test.go` |
| Sprint 8 exports | `tests/drills/fixtures/s07/`, `scripts/demo/sprint7-supportability.sh`, `artifacts/examples/support-bundle-manifest.json`, `artifacts/examples/phase0-alert-fixtures/` |

## Dependencies From S02

S07 depends on S02 Installer/Ops for:

- `.deb` packaging paths and maintainer-script hooks that install:
  `/etc/midnight/prometheus/midnight-alerts.rules.yml`,
  `/usr/share/midnight/dashboards/midnight-overview.json`,
  `/usr/share/midnight/runbooks/*.md`, and
  `/usr/share/midnight/telemetry-schema.json`.
- `midnight doctor` framework: stable check IDs, JSON schema, exit codes
  `0 healthy`, `1 warn`, `2 fail`, `3 config error`, check registration, and
  compatibility tests preventing check ID removal/rename.
- Runbook packaging and `midnight runbook list/describe` binary surface.
- Install-time telemetry prompt behavior: interactive installs default disabled;
  non-interactive installs remain disabled unless explicit `--telemetry=on`.
- Platform path creation and modes for `/etc/midnight`, `/etc/midnight/secrets.key`,
  `/var/lib/midnight/backups`, `/var/lib/midnight/logs`, `/var/lib/midnight/acme`,
  registry data, and install manifest.
- KEK bootstrap and file mode checks. S07 backup/restore and KEK-loss drill
  messaging rely on S02's `/etc/midnight/secrets.key` contract.
- Package content tests and VM install matrix for Ubuntu 24.04 and Debian 12.

Blocking S02 confirmations:

- Canonical packaged alert path. Sprint 7 uses
  `/etc/midnight/prometheus/midnight-alerts.rules.yml`; S06 handoff mentions
  `/etc/prometheus/rules/midnight-alerts.yml`. Prefer the Sprint 7 path unless
  packaging owners choose a single canonical path and update all references.
- Whether runbook source lives under `docs/runbooks/` or a packaging-specific
  source tree before build-time package-version substitution.
- Whether the installer feature-flag store is file-backed, DB-backed, or both at
  first boot.

## Dependencies From S03

S07 depends on S03 API/Data for:

- Error envelope and additive-only error-code registry. S07 needs at minimum:
  `BACKUP_FAILED`, `BACKUP_STALE`, `BACKUP_UNVERIFIED`,
  `BACKUP_OFFHOST_MISSING`, `BACKUP_SIZE_DEGRADED`,
  `BACKUP_COLOCATED_RISK`, `RESTORE_FAILED`,
  `RESTORE_ACME_THROTTLED`, `AUDIT_CHAIN_BROKEN`,
  `AUDIT_CHECKPOINT_SIGNATURE_INVALID`, `AUDIT_KEY_NOT_FOUND`,
  `SUPPORT_BUNDLE_INVALID_SELECTOR`, and telemetry/feature config errors.
- Operation resource contract: `202 Accepted`, `Location:
  /api/v1alpha/operations/{op_id}`, polling headers, retry hints, and SSE
  stream at `/operations/{id}/events`.
- Restore step persistence. S07 needs a `restore_steps` table or equivalent
  operation-step store with `started_at`, `finished_at`, `status`, and
  `reason_code` per locked restore step.
- Queue substrate for backup, restore, verification, telemetry send, and audit
  checkpoint verification jobs. S07 should use the S03 `cleanup` queue where the
  backup feature calls for backup/restore/verification work.
- Schema foundations: `audit_logs`, `audit_checkpoints`, `jobs`, `operations`,
  `events`, `domains`, `routes`, registry metadata, and any backup tables or
  feature-flag tables S07 extends.
- Audit insertion helpers and canonical row contract for `midnight audit verify`.
- JSONL audit export shape if verify supports offline `--from-jsonl` in the
  implementation phase.
- Feature flag persistence and propagation semantics if FEATURE-501 is surfaced
  through S03 APIs.

Blocking S03 confirmations:

- Audit verify exit-code drift: Sprint 7 says `0 ok`, `2 chain-broken`,
  `3 config error`, `4 checkpoint-signature-invalid`; `FEATURE_AUDIT_HASH_CHAIN.md`
  says `0 success`, `2 chain break`, `3 signature failure`, `1 warnings`.
  For Phase 0, use the Sprint 7 CLI contract unless registry/API owners revise
  it before implementation.
- Whether `audit_logs.id` is included in canonical row hashing. The feature doc
  proposes including it but leaves it as an open question.
- Where `backup_destinations`, `backups`, `backup_runs`, and `restore_runs`
  land in migrations, and whether they are Phase 0 tables or MVP Beta tables
  gated by the off-host scope decision.
- Whether `docs/error-codes.md` is generated from
  `agile/engineering/contracts/API_ERROR_CODE_REGISTRY.md` or maintained
  manually.

## Dependencies From S05

S07 depends on S05 Build/Registry for:

- `midnight_buildkit_cache_corrupt_total`, BuildKit cache verifier output, and
  deterministic cache-corruption fixture for the `BuildKitCacheCorruption` alert
  and Sprint 8 D-15.
- Build queue and deploy queue metrics needed by `DeployJobStuck`, including
  heartbeat gap and lease TTL context for build/deploy jobs.
- Registry backup contract: embedded registry metadata and blobs are included
  in platform backup, restored before smoke checks, and verified so pinned
  digests remain pullable.
- Registry GC feature flag `registry.gc`, including disable behavior reflected
  in `midnight feature list --json` and `doctor`.
- Support bundle safe inputs: build logs, BuildKit config, registry GC dry-run
  reports, provenance refs, registry probe output, and redaction constraints.
- Restore smoke probes against registry accessibility and pinned digest retrieval.
- Runbook links and error-code mappings for registry/rollback failures that
  appear in support bundles and alert annotations.

Blocking S05 confirmations:

- Exact metric names for build queue stuck state and BuildKit cache corruption.
- Whether registry metadata/blobs are backed up through restic directly or via a
  S05 export manifest consumed by S07.
- Exact feature flag identifier for destructive registry GC. Use `registry.gc`
  unless S05 changes it before implementation.

## Dependencies From S06

S07 depends on S06 Ingress/Logs/DX for:

- ACME state path and ownership contract. `acme.json` must be part of platform
  backup and restored byte-exact with mode `0600` before Traefik starts.
- ACME restore/reissue telemetry: `RESTORE_ACME_THROTTLED` operation progress,
  priority order, <=20 certs/hour restore throttle, and LE staging first-batch
  behavior for the reissue path.
- Cert expiry metrics for `CertExpiringSoon` and domain diagnostics for support
  bundles.
- App log NDJSON shape, log cursors, redaction-safe timeline/events, and
  per-selector cap/truncation markers consumed by `support-bundle`.
- Traefik static and dynamic config snapshots safe for support bundle inclusion.
- Route reconciliation events, deployment timeline events, failure codes, and
  generated-domain diagnostic fields.
- Runbook inputs for `domain-tls-failure.md`,
  `regenerate-traefik-config.md`, `failed-deploy-triage.md`, and
  `collect-support-bundle.md`.
- Dashboard panels and metrics for route/TLS/log/timeline state that appear in
  `midnight-overview.json`.

Blocking S06 confirmations:

- Canonical location and owner for `acme.json` on disk and in packaged restore
  tests.
- Whether timeline/support-bundle events come from the S03 `events` table or a
  dedicated `deployment_timeline_events` table.
- Final S06 metric names for certificate expiry, Traefik reloads, route
  reconciliation, log tail lag, and ACME restore progress.

## Exported Runbooks And Artifacts To S08

S08 failure drills and the Phase 0 exit gate consume these S07 exports:

| Export | S08 consumer |
|---|---|
| `midnight-alerts.rules.yml` | DRILL-801 runbook annotation lookup; D-01, D-07, D-15 alert firing assertions; GATE-812 promtool check. |
| Alert synthetic metric fixtures | DRILL-801 meta-tests and GATE-812 alert fire/recover assertions. |
| `midnight-overview.json` | Phase 0 demo evidence and package-content gate. |
| `collect-support-bundle.md` | Support-bundle gate and operator recovery link in alert annotations. |
| `backup-restore.md` | D-18 KEK-loss recovery, platform backup/restore smoke, and backup failure recovery path. |
| `disk-pressure.md` | D-07 and D-12 recovery runbook lookup. |
| `domain-tls-failure.md` | D-09 and D-19 recovery/diagnostic runbook lookup. |
| `regenerate-traefik-config.md` | D-04 and route/config recovery lookup. |
| `failed-deploy-triage.md` | D-02, D-05, D-06, D-10, D-16 default diagnostic entry point. |
| `restart-service.md` | D-01 agent/service recovery variant. |
| `rollback.md` | Rollback preservation evidence used by Phase 0 exit signoff. |
| Support-bundle zero-leak fixture | GATE-812 release gate; verifies secrets, tokens, auth headers, cookies, private keys, and raw env dumps are absent. |
| Support-bundle manifest schema and sample | S08 report artifact and supportability demo. |
| Backup/restore smoke fixture | D-18, D-19, UPG-807, and platform restore smoke assertions. |
| Audit-chain verify fixtures | GATE-812 audit-verify perf/check gate and tamper/signature tests. |
| Telemetry default-off fixture | GATE-812 no-external-telemetry check. |
| Telemetry schema | GATE-812 schema-conformance check. |
| Feature flag fixture | GATE-812 feature kill-switch list and doctor reflection checks. |
| `OR-CHK-080` doctor JSON snapshot | OBS-703 and Sprint 8 doctor assertion for off-host warning. |

Phase 0 required runbooks from the runbook index are the eight in
`agile/handoff/OPERATIONS_RUNBOOK_INDEX.md`:

- `failed-deploy-triage.md`
- `rollback.md`
- `restart-service.md`
- `domain-tls-failure.md`
- `disk-pressure.md`
- `backup-restore.md`
- `regenerate-traefik-config.md`
- `collect-support-bundle.md`

S08 references MVP Beta runbooks such as `host-drain.md`, `retry-webhook.md`,
and `restore-platform.md`. If those remain MVP Beta, S08 must either accept the
Phase 0 substitute listed in the failure-drill matrix or explicitly re-scope the
additional runbooks into Phase 0.

## Acceptance Checklist

Observability:

- [ ] `/etc/midnight/prometheus/midnight-alerts.rules.yml` ships with exactly
  the eight locked alerts and no extras.
- [ ] Each alert has `alert`, `expr`, `for`, `labels.severity`,
  `annotations.summary`, and `annotations.runbook_url` pointing to
  `/usr/share/midnight/runbooks/*.md`.
- [ ] `DiskPressure` thresholding aligns to 75 percent warn and 85 percent
  critical for alerting, with 92 percent block behavior documented in the
  runbook.
- [ ] `BackupOffhostMissing` fires when
  `midnight_backup_offhost_destinations_total == 0`.
- [ ] `BuildKitCacheCorruption` fires on cache-verify failure metric >=1 within
  10 minutes.
- [ ] `ConntrackPressure` fires at >80 percent of `nf_conntrack_max`.
- [ ] `DeployJobStuck` fires when build/deploy queue heartbeat gap exceeds
  `3 x lease_ttl`.
- [ ] `promtool check rules` and synthetic fire/recover fixtures pass.
- [ ] `midnight-overview.json` ships in the `.deb` and references these alerts.

Support bundle:

- [ ] CLI supports `midnight support-bundle [--since 24h] [--max-size 200MB]
  [--include=logs,metrics,events,config] [--out PATH] [--preview]`.
- [ ] Selector enum is closed to `logs`, `metrics`, `events`, `config`,
  `runbooks`, and `doctor`.
- [ ] Unknown selector returns `SUPPORT_BUNDLE_INVALID_SELECTOR` and exit `3`.
- [ ] Deterministic tar.zst contains top-level `manifest.json`.
- [ ] Manifest lists every included file, byte size, redaction class,
  category truncation, `redaction.passes_run`, and
  `redaction.findings_count`.
- [ ] `--preview` prints the manifest without writing archive payload.
- [ ] Hard-blocked categories are never included: `secrets/*`, sealed values,
  tokens, auth/cookie headers, `/etc/midnight/secrets.key`, ACME private keys,
  KEK material, private keys, and raw container env dumps.
- [ ] Zero-leak fixture injects known secrets into logs/config and fails on any
  plaintext recovery.
- [ ] Cap fixture proves bundle <=200 MB and oldest-first per-category
  truncation is reflected in the manifest.

Backup and restore:

- [ ] Off-host scope decision recorded before external providers are made
  Phase 0 blocking.
- [ ] `midnight backup create --platform` supports local Alpha backup and the
  approved external provider set, behind feature flags where needed.
- [ ] Backup metadata records consistency level per artifact.
- [ ] Platform backup includes Postgres logical dump, `/etc/midnight`,
  `/etc/midnight/secrets.key` encrypted with operator passphrase, registry
  metadata/blobs if embedded, template metadata, and ACME state when present.
- [ ] Missing operator passphrase for `secrets.key` archive fails before any
  off-host write.
- [ ] `midnight restore --platform` executes the locked 11-step order.
- [ ] Restore step state is persisted and idempotent on restart.
- [ ] Byte-exact ACME path restores `acme.json` with mode `0600`, verifies
  SHA-256, and starts Traefik only after verification.
- [ ] Reissue-required ACME path emits `RESTORE_ACME_THROTTLED` as progress, not
  failure, and honors <=20/hour pacing.
- [ ] Restore smoke checks cover schema, `secrets.key` decrypt test, registry
  `200` probe, Traefik reload, and internal DNS synthetic service.

Audit verify:

- [ ] CLI supports `midnight audit verify [--since DATE] [--until DATE]
  [--checkpoint-id ID] [--key PATH] [--json]`.
- [ ] Verifier streams rows in order, recomputes `row_hash`, and validates
  checkpoint signatures with operator or release Ed25519 key.
- [ ] Exit-code contract is settled and covered by tests before implementation.
- [ ] JSON output reports first divergent row hash, expected/actual values,
  checkpoint id, and signature status.
- [ ] 1M-row synthetic verification meets the Sprint 7 performance budget.

Runbooks:

- [ ] The eight Phase 0 runbooks ship at `/usr/share/midnight/runbooks/*.md`.
- [ ] Each runbook includes front matter and sections required by the runbook
  index: title, version/package pin, summary, preconditions, steps,
  verification, rollback, linked error codes, audit/evidence, and related docs.
- [ ] `midnight runbook list` returns exactly the Phase 0 set for Alpha.
- [ ] `midnight runbook describe <id>` prints metadata and installed path.
- [ ] Alert runbook URLs resolve to installed files in package tests.

Telemetry and features:

- [ ] Telemetry defaults disabled for interactive and non-interactive installs.
- [ ] Non-interactive enablement requires explicit `--telemetry=on`.
- [ ] Payload schema only permits install id, version, OS, kernel, Docker
  version, count metrics, reason-code histograms, and alert-firing histogram.
- [ ] Payload builder is statically tested against forbidden project, service,
  workspace, user, domain, log, and tenant-identifying keys.
- [ ] Client validates payload against
  `/usr/share/midnight/telemetry-schema.json` before send.
- [ ] `midnight feature disable telemetry` stops outbound sends immediately.
- [ ] `midnight feature list --json` includes `acme.issuance`, `registry.gc`,
  `ai.gateway`, `auto.remediation`, and `telemetry`.
- [ ] `midnight doctor --json` includes the `features` map and reflects flag
  flips within 5 seconds.

## Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Off-host provider scope is unresolved between Sprint 7 and feature docs | Phase 0 exit may block on MVP Beta work | Make local backup plus loud off-host warning mandatory; require PM3 decision before external providers become a Phase 0 gate. |
| Audit verify exit-code drift | CLI contract tests and runbooks disagree | Settle against Sprint 7 or revise both feature and sprint docs before code. |
| Alert metric names drift from S04/S05/S06 implementations | Alert pack cannot fire in Sprint 8 | Create metric contract fixtures before agent/build/ingress implementation finishes. |
| Support bundle leaks secrets from S05/S06 artifacts | Critical release blocker | Hard-block path classes, run multi-pass redaction on every text payload, and gate release on synthetic secret fixture. |
| ACME restore starts Traefik before byte-exact state verification | Unnecessary reissue or rate-limit exhaustion | Restore state machine must verify hash, mode, and owner before Traefik start; S08 D-19 covers this. |
| Telemetry default-off regresses in installer or feature refactor | Violates Phase 0 exit criterion | Add fresh-install default-off test and forbidden outbound egress test to GATE-812. |
| Runbook list expands beyond Phase 0 without product decision | Sprint 7 documentation scope balloons | Ship the eight Phase 0 runbooks; track MVP Beta additions separately unless re-scoped. |
| Restore smoke depends on S05 registry and S06 Traefik readiness | BAK-708 flakes or blocks | Define stub smoke interfaces early and replace with real probes as dependencies land. |
| `promtool` availability differs in CI/package test images | Alert validation skipped accidentally | Pin promtool version in tooling or containerize alert tests. |

## Exact First Implementation Tasks

1. Resolve the off-host backup boundary in writing with PM3/product:
   Alpha local-only plus warning, or external providers as Phase 0 blocking.
   Record the chosen provider set and whether `local://` or `file://` is the
   canonical local URI.
2. Create S07 package/artifact skeletons for alert rules, dashboard, runbooks,
   telemetry schema, support bundle fixtures, audit fixtures, and backup/restore
   smoke fixtures.
3. Add the alert rules file with the eight locked alerts and a synthetic metric
   fixture suite. Run `promtool check rules` in CI.
4. Lock the metric contract document with S04/S05/S06: disk/inode ratios,
   conntrack, agent heartbeat, build queue heartbeat, BuildKit cache corruption,
   backup failure/off-host destination count, cert expiry, and deploy job stuck.
5. Implement `OR-CHK-080 backup.offhost.configured` in the doctor registry with
   JSON snapshot tests, exit `1` warning behavior, fix hint, docs URL, and
   matching metric update.
6. Implement support-bundle selector parsing and validation first, including
   `SUPPORT_BUNDLE_INVALID_SELECTOR` and table-driven tests.
7. Implement support-bundle manifest generation and `--preview` before archive
   writing. Freeze the manifest JSON schema.
8. Implement redaction pipeline integration for support-bundle text payloads:
   in-memory secret exact-match pass, regex token/key pass, secret-name table
   pass, URL credential pass, and Authorization header pass.
9. Add the zero-leak release fixture with injected secrets across logs, configs,
   BuildKit excerpts, Traefik snapshots, domain diagnostics, and registry
   reports.
10. Implement deterministic tar.zst archive writing with category budgets,
    oldest-first truncation, and reproducibility tests over quiesced inputs.
11. Add backup metadata structs, consistency-level enum, and backup set planner
    for Postgres, `/etc/midnight`, `secrets.key`, registry, templates, and
    ACME state.
12. Implement local platform backup creation and passphrase preflight for the
    `secrets.key` archive. Do not write any artifact if the operator passphrase
    is missing.
13. Add external destination provider interfaces and only implement external
    providers according to the Phase 0 boundary decision. Keep GCS scaffolded
    behind feature flag if capacity slips.
14. Implement restore state machine with the locked 11 steps, persisted
    step-state, idempotent resume, maintenance mode, and `--include-tenant`
    stop-after-step-8 behavior.
15. Implement ACME byte-exact restore verification: SHA-256 match, mode `0600`,
    Traefik owner, and no Traefik start before verification passes.
16. Implement ACME reissue queue simulation with priority order, <=20/hour
    throttle, staging-first marker, and `RESTORE_ACME_THROTTLED` operation
    progress event.
17. Implement restore smoke probe interfaces for schema, KEK decrypt, registry,
    Traefik reload, and synthetic internal DNS. Start with fakes, then wire real
    S03/S05/S06 probes.
18. Settle audit verify canonical-row and exit-code drift, then implement the
    streaming verifier and JSON output. Add clean, tamper, missing-key,
    bad-signature, JSONL, and 1M-row perf fixtures.
19. Add the eight Phase 0 runbook markdown files from the index template, with
    package-version substitution placeholders and linked reason codes.
20. Implement `midnight runbook list/describe` over installed metadata and add
    package-content plus broken-link tests.
21. Add telemetry schema and payload builder with static forbidden-key tests.
    Default disabled must be proven by fresh-install fixture before any sender
    code is enabled.
22. Implement `midnight feature list/enable/disable`, feature state store,
    `doctor` feature map, and flip propagation test for `telemetry`,
    `registry.gc`, `acme.issuance`, `ai.gateway`, and `auto.remediation`.
23. Export S07 fixtures to S08 under a stable directory: alert fire/recover,
    support-bundle zero-leak, audit tamper/signature/perf, telemetry default-off,
    backup/restore smoke, and `OR-CHK-080` doctor snapshots.
24. Add `.deb` content tests to verify alert rules, dashboard, runbooks,
    telemetry schema, and package paths are present on Ubuntu 24.04 and Debian
    12 images.
25. Create `scripts/demo/sprint7-supportability.sh` that runs support-bundle
    preview, backup create, restore smoke, audit verify, feature list, telemetry
    disabled check, and alert fixture validation for the Sprint 7 demo.

## Ready-To-Implement Notes

- Treat S07 artifacts as release gates, not only documentation. Every operator
  promise here needs a package test or Sprint 8 fixture.
- Support bundle must be conservative: exclude first, then opt into safe
  diagnostics by selector.
- Restore ordering is more important than restore speed. ACME state must be
  restored and verified before Traefik start to avoid accidental reissue.
- Feature flags must be observable in `doctor`; a disabled feature that still
  runs work is a release blocker.
- Telemetry must remain default-off even if the endpoint is configured. Explicit
  enablement is required before the sender can emit anything.
- Alert and dashboard work should land before all producers exist, using
  synthetic fixtures as the stable contract for S04/S05/S06.
