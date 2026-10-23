# Phase 0 Worker S08 - QA / Release Gate Handoff

- **Worker:** S08 QA / Release Gate
- **Workspace:** `D:/projects/midnight`
- **Primary source:** `agile/sprints/SPRINT_08_FAILURE_DRILLS.md`
- **Reviewed release/testing sources:** `agile/handoff/QUALITY_AND_TEST_STRATEGY.md`, `agile/RELEASE_PLAN.md`, `agile/handoff/SECURITY_REVIEW_PLAN.md`, `agile/sprints/SPRINT_07_OBSERVABILITY_SUPPORT.md`, `agile/handoff/OPERATIONS_RUNBOOK_INDEX.md`
- **Status:** Phase 0 exit-gate handoff complete. This workspace is planning-only for S08: local Git metadata exists, but no product code, Go module, `.github/`, or release-gate workflow exists yet.

## Scope

S08 owns the Phase 0 exit gate as an enforceable release system, not just a checklist:

- Failure-drill harness and all Phase 0 drill test definitions for D-01 through D-19.
- Install timing harness for local and public profiles on Ubuntu 24.04 and Debian 12.
- Upgrade, migration idempotence, agent/server skew, and workload-survival gates required by Sprint 8.
- Single Phase 0 release-gate workflow for `v0.*-alpha*` tags.
- Aggregate `phase0-exit-report.md` artifact and final `agile/handoff/phase0-exit.md` signoff packet.
- Security/release signoff handoff that ties redaction, audit, telemetry, KEK, support bundle, backup/restore, runbooks, artifact signing, and tag protection together.

Out of scope:

- Implementing runtime, ingress, BuildKit, backup, webhook, volume, or upgrade product features. S08 consumes those surfaces and fails the gate when they are missing.
- Reclassifying Sprint 10 custom-domain/webhook scope or MVP Beta upgrade/volume product scope. S08 can define a narrow Alpha fixture only where Sprint 8 explicitly requires one.
- Relaxing panel-locked release criteria. Any deferral requires PM3/release/security signoff and an ADR or sprint change.

## Owned Future Implementation Paths

When the product repository exists, S08 owns or co-owns these paths:

| Path | Ownership |
|---|---|
| `test/drills/` or `tests/drills/` | Drill test packages. Resolve singular/plural path conflict before first PR; `QUALITY_AND_TEST_STRATEGY.md` uses `test/drills`, Sprint 8 says `tests/drills`. |
| `test/drills/harness/` | Shared Go harness: VM provisioner, fault injectors, event/audit taps, doctor probes, runbook verifier, JUnit/markdown emitters. |
| `test/drills/fixtures/` | Fake registry, Pebble ACME, CoreDNS/TXT resolver, MinIO/SFTP backup targets, BuildKit cache corruptor, clock/network/log/disk injectors. |
| `test/install-timing/` | Fresh-VM install timing runner, wall-clock accounting, DNS-deny netns wrapper, p95 aggregation. |
| `test/upgrade/` | N-1 upgrade, skip-version refusal, schema idempotence, compat/skew, workload-survival probes. |
| `tools/phase0-gate/` | Report aggregator, gate manifest, last-green lookup, local `act` subset wrapper. |
| `.github/workflows/phase0-exit-gate.yml` | Single release gate workflow for Phase 0 alpha tags and main/nightly runs. |
| `docs/release/phase0-gate.md` | Operator/release-manager instructions for running and interpreting the gate. |
| `docs/compat/components.md` | Consumed by skew tests; generated/owned by release/SRE with S03/S04/S05 inputs. |
| `agile/handoff/phase0-exit.md` | Final PM3/release-manager signoff packet produced by EXIT-813. |

## Dependencies From S01-S07

| Worker | Required output for S08 |
|---|---|
| S01 Foundations | Git repo, Go module, CI skeleton, `.github/`, required check names, branch/tag protection export, CODEOWNERS, release-signing scaffolding, supply-chain checks, `Makefile`/task targets. |
| S02 Installer/Ops | `.deb` package, systemd units, preflight/doctor framework, daemon.json live-restore baseline, cgroup/Docker/time/port checks, local install profile, runbook packaging, uninstall/reinstall behavior. |
| S03 API/Data | Error-code registry, OpenAPI artifact, operation/SSE surface, idempotency middleware, migration runner, schema versioning, queue leases/reclaim, audit/event append APIs, `/api/v1alpha/compat`. |
| S04 Runtime/Agent | Agent registration/heartbeat/reconnect, Docker Engine API client, runtime-unavailable backoff, route-target observations, container survival under daemon restart, clock-skew rejection, LOST semantics, no crash-loop guarantees. |
| S05 Build/Registry | Fake/failing image path, BuildKit cache corruption injector, build metrics/events, registry/pin/rollback fixtures, provenance/signing outputs, `BuildKitCacheCorruption` alert signal. |
| S06 Ingress/Logs/DX | Traefik route writer, generated-domain TLS, ACME/Pebble fixtures, timeline/log APIs, domain diagnostics, failure-code surfacing, route freshness/hysteresis, ACME restore throttling events. |
| S07 Observability/Support | Alert pack, metrics, support bundle zero-leak fixture, backup/restore flows, audit verify CLI, runbooks, telemetry default-off fixture, feature kill switches. S08 consumes `S07_SUPPORT_BACKUP_OBS.md` plus Sprint 7 story outputs for final gate evidence. |

## Scope Conflicts To Resolve

These are release risks because Sprint 8 asks for gates that sit on later-phase product surfaces.

| Conflict | Source tension | S08 release-gate resolution |
|---|---|---|
| Upgrade tests in Phase 0 | `FEATURE_UPGRADE_TRANSACTION.md` marks the full upgrade transaction as MVP Beta, while Sprint 8 requires UPG-807, UPG-808, and UPG-809 for Phase 0 exit. | S08 owns the tests and gate. Release/SRE must either land a narrow Alpha-compatible upgrade path before Sprint 8 close or PM3 must record a formal scope change. Do not silently skip UPG jobs in `phase0-exit-gate.yml`. |
| Webhook drill D-08 | Full webhook/GitHub App work is Sprint 10/MVP Beta, but Sprint 8 requires D-08 against a Sprint 6 inbound signed webhook prototype. | Gate only the prototype contract: HMAC verify, delivery-id + event-hash dedupe, duplicate/reorder semantics, audit/event output. Do not require GitHub App, outbound delivery, dead-letter UI, or full custom webhook product UX in Phase 0. |
| Volume read-only remount D-17 | Durable tenant volumes are MVP Beta/Sprint 11 territory, but D-17 is a Phase 0 required drill. | Use a deterministic loopback/platform-managed mount fixture that exercises the agent/storage failure classifier and no-restart-storm behavior. Do not expose or claim the full tenant-volume product before its owning sprint. |

Additional boundary note: D-09 DNS verification and custom-domain language must stay on generated-domain/ACME Phase 0 primitives unless PM3 explicitly promotes Sprint 10 custom-domain UX into Phase 0.

## Dependency Matrix For The 19 Drills

D-14 has three mandatory subcases: 10s, 60s, and 10min. Count it as one parent drill for the Sprint 8 "19/19" exit checklist, but as three CI scenarios when runtime cost allows.

| Drill | Harness file | Required upstreams | Gate assertions |
|---|---|---|---|
| D-01 Kill `midnight-agent` | `test/drills/d01_kill_agent_test.go` | S02 systemd, S03 host/audit events, S04 heartbeat/LOST, S06 timeline, S07 `AgentLost` alert/runbook | `AGENT_LOST` within 3x heartbeat, workloads survive, `agent.lost`, offline diagnostic, `restart-service.md`. |
| D-02 Kill `midnight-server` | `test/drills/d02_kill_server_test.go` | S02 systemd, S03 operations/health, S04 reconnect backoff, S06 route stability, S07 alert/runbook | API down <=30s, agents reconnect, workloads keep serving, `server.crash`, health diagnostic, `failed-deploy-triage.md`. |
| D-03 Kill `midnight-worker` | `test/drills/d03_kill_worker_test.go` | S03 queue leases/reaper, S05/S06 job events, S07 `DeployJobStuck` alert | Lease reclaimed, no double execution, `job.lease.reclaimed`, timeline retry visible, no false `DeployJobStuck`. |
| D-04 Restart Docker daemon | `test/drills/d04_docker_restart_test.go` | S02 daemon.json live-restore, S04 Docker reconnect, S06 route/log continuity, S07 runbook | `live-restore:true`, container uptime crosses restart, `runtime.daemon.restarted`, doctor `daemon.live-restore`, `regenerate-traefik-config.md`. |
| D-05 Image pull failure | `test/drills/d05_image_pull_failure_test.go` | S04 pull/error mapping, S05 fake registry, S06 failure timeline, S03 registry codes | Previous active unchanged, `IMAGE_PULL_FAILED`, `deployment.failed`, retry hint, `failed-deploy-triage.md`. |
| D-06 Readiness fails | `test/drills/d06_readiness_failure_test.go` | S04/S06 healthcheck split, S03 deployment FSM, S06 timeline | Candidate not promoted, previous active serving, `HEALTHCHECK_FAILED` or `PORT_NOT_LISTENING`, readiness attempts visible. |
| D-07 Disk pressure | `test/drills/d07_disk_pressure_test.go` | S02 storage roots, S05 build blocking, S07 disk metrics/alert, S03 reason codes | Builds blocked at 85%, deploys/volumes refused at 92%, `BUILDS_BLOCKED_DISK_PRESSURE` and `DISK_PRESSURE`, doctor warn->fail. |
| D-08 Webhook duplicate/reorder | `test/drills/d08_webhook_duplicate_reorder_test.go` | S03 idempotency/events, Sprint 6 prototype/S06 contract, S07 runbook index | Identical `(delivery_id,event_hash)` deduped, differing hash accepted, no double execute, `webhook.delivery.duplicate`. |
| D-09 DNS verification break | `test/drills/d09_dns_verification_break_test.go` | S06 generated-domain DNS diagnostics, S03 domain state/audit, S07 runbook | TXT mismatch, `DOMAIN_OWNERSHIP_LAPSED` / `DNS_VERIFICATION_FAILED`, route grace behavior, `domain.verification.failed`. |
| D-10 Clock skew >5min | `test/drills/d10_clock_skew_test.go` | S02 time sync check, S04 registration skew, S06 ACME skew gate, S07 doctor | Registration refused, ACME ops blocked, `AGENT_CLOCK_SKEW`, `agent.clock.skew`, doctor `time.sync` fail. |
| D-11 journald wedged | `test/drills/d11_journald_wedged_test.go` | S02 journald/logrotate drop-ins, S04/S06 json-file logs, S07 alert/support bundle | Platform degraded not failed, tenant logs unaffected, `PLATFORM_DEGRADED`, `journald.unavailable`, doctor warn. |
| D-12 `/var/log` full | `test/drills/d12_var_log_full_test.go` | S02 logrotate, S06 log caps, S07 disk metrics/alert | No crash-loop, logrotate/rate-limit behavior, `DISK_PRESSURE` logs root, `host.logs.full`, doctor fail. |
| D-13 Postgres connection exhaustion | `test/drills/d13_pg_connection_exhaustion_test.go` | S02/S03 role limits, S03 DB pool/timeouts, S07 audit verify | App role bounded, idles reaped, read-mostly degradation, no corruption, `PLATFORM_DEGRADED`, `db.connections.exhausted`. |
| D-14 Network partition | `test/drills/d14_network_partition_test.go` | S04 reconnect/warmup/LOST, S06 route freshness, S03 events, S07 runbooks | 10s no route flap, 60s `AGENT_LOST` then recover, 10min route reconciliation/`HOST_UNAVAILABLE` if last host, full event chain. |
| D-15 BuildKit cache corruption | `test/drills/d15_buildkit_cache_corruption_test.go` | S05 corruptor/retry metric, S06 timeline, S07 alert | First build fails fast, cache invalidated, retry succeeds, `BUILD_FAILED` transient, `buildkit.cache.corrupt`, alert fires. |
| D-16 Docker socket EOF | `test/drills/d16_docker_socket_eof_test.go` | S04 daemon proxy/backoff, S06 diagnostics, S07 doctor | Agent stays alive, backoff/recovery on STOP/CONT, `AGENT_RUNTIME_UNAVAILABLE`, `runtime.socket.eof`, doctor fail until recovery. |
| D-17 Volume read-only remount | `test/drills/d17_volume_readonly_remount_test.go` | S04 mount/runtime handling, future storage shim, S03 codes/events, S07 disk runbook | Explicit read-only state, no restart storm, `VOLUME_MOUNT_FAILED`, `volume.readonly.detected`, degraded health banner. |
| D-18 KEK loss | `test/drills/d18_kek_loss_test.go` | S02 KEK path/mode, S03 audit/events, S07 backup/restore, security signoff | Platform refuses start, irrecoverable message unless passphrase backup exists, `secrets.kek.missing`, doctor fail, restore path demonstrated. |
| D-19 ACME-state restore at scale | `test/drills/d19_acme_restore_scale_test.go` | S06 ACME/Pebble, S07 restore order, S03 operations/SSE | 50-domain restore, <=20/hour throttle, staging first batch, `RESTORE_ACME_THROTTLED` status not failure, `acme.restore.throttled`. |

## Acceptance Checklist

- [ ] `Drill` harness exists with VM provisioner, deterministic fault injectors, event tap, audit tap, doctor probe, runbook verifier, JUnit XML emitter, and markdown matrix emitter.
- [ ] Harness meta-tests intentionally break each assertion class and prove failures are caught: state, reason code, audit/event, user diagnostic, runbook.
- [ ] All 19 parent drills exist; D-14 runs the 10s/60s/10min subcases and reports them separately.
- [ ] Every drill asserts pre-state, fault injection, post-state, reason code, audit/event row, user diagnostic, and runbook annotation/result.
- [ ] Local-mode install timing runs 10 times per distro and reports p95 < 15 minutes, with no external DNS observed.
- [ ] Public-mode install timing runs 10 times per distro and reports p95 < 15 minutes excluding DNS propagation and ACME issuance pseudo-time.
- [ ] N-1 upgrade, broken-migration auto-rollback, skip-version refusal, schema idempotence, agent/server skew, and server-restart workload-survival tests are wired.
- [ ] `.github/workflows/phase0-exit-gate.yml` runs the full matrix on main/nightly and protects `v0.*-alpha*` tags.
- [ ] Workflow emits `phase0-exit-report.md` with pass/fail, reason code, linked logs, and runbook for every gate.
- [ ] `act` local subset runs unit/meta/harness smoke without requiring cloud VMs.
- [ ] Security checks in the gate include zero-leak support bundle, telemetry default-off, audit verify perf, supply-chain scans, and no external telemetry/log export without explicit config.
- [ ] Tag protection for `v0.*-alpha*` is enforced and recorded in the final signoff packet.
- [ ] `agile/handoff/phase0-exit.md` includes drill snapshot, install timings, last-green commit, shipped error codes, kill switches, runbooks, known deferrals, and design-partner signoffs.

## Security And Release Signoff Handoff

S08 should hand PM3, the release manager, and the security lead one packet before a Phase 0 alpha tag is cut:

- `phase0-exit-report.md` artifact from the last green workflow run, tied to exact commit SHA and package version.
- Security signoff section: redaction zero-leak fixture, telemetry default-off capture, KEK-loss drill result, support-bundle exclusions, SSRF-safe outbound-client checks where available, audit verify result, signed artifact/SBOM/provenance status.
- Release signoff section: install p95s, N-1 upgrade result, migration idempotence result, compat/skew result, workload-survival result, tag-protection evidence, package artifact list, runbook list.
- Deferral section: any drill not truly implemented must name owner, blocker, accepted risk, target sprint, and signers. Missing drills are release blockers unless PM3 records an explicit change.
- Design-partner section: two signoffs minimum for Sprint 8 `EXIT-813`; release plan asks for five partner signoffs for `v0.3.0-alpha`, so PM3 must reconcile the count before tagging.

## Risks

| Risk | Impact | Mitigation |
|---|---|---|
| No Git/product repo exists in this workspace | Cannot implement workflows, tests, tag protection, or code paths yet | Start by instantiating S01 scaffold; keep S08 handoff as release-gate contract until code exists. |
| Sprint 8 loaded over capacity | Drills or gate emitters can slip | Land DRILL-801 harness first and parallelize drill authorship; defer only through PM3 signoff. |
| VM drills are flaky and slow | False release blockers or hidden failures | Deterministic local fault injectors, cached images, retry only for infra setup, require 5 consecutive green nights before tag. |
| Upgrade/webhook/volume scope conflicts | Gate can demand features outside Phase 0 implementation | Use the narrow fixtures above or formally change scope; never silently skip jobs. |
| D-14 long partition consumes CI minutes | Release workflow becomes impractical | Split PR/main/nightly cadence: short subcases on main, 10min on nightly and pre-tag. |
| Security-sensitive drill artifacts leak secrets | Release evidence becomes unsafe to share | Run redaction over drill logs, support bundles, and workflow artifacts; fail on raw KEK, tokens, ACME private keys, or auth headers. |
| Runbook links missing or stale | Alerts/drills pass technically but fail operator recovery requirement | Harness must assert runbook existence and, where possible, walkthrough result or CLI `runbook describe`. |
| S07 handoff absent | S08 may consume ambiguous supportability outputs | Require an S07 completion handoff before final Phase 0 signoff, or cite Sprint 7 story evidence directly in `phase0-exit.md`. |

## Exact First Implementation Tasks

1. Instantiate or verify the S01 repo scaffold, then resolve `test/drills` vs `tests/drills` path naming in a short ADR or release-gate PR.
2. Create `test/drills/harness` with the `Drill` interface, assertion result model, JUnit emitter, markdown matrix emitter, and meta-test fixtures that deliberately fail each assertion category.
3. Add a `phase0-gate.yaml` manifest listing all parent gates: D-01..D-19, UPG-807..809, INST-810..811, support-bundle, telemetry, audit, and supply-chain checks.
4. Implement harness adapters for S03/S07 observability first: operation polling, SSE event capture, audit query, doctor JSON capture, alert/runbook lookup.
5. Build the install timing runner for local mode with a fake VM provider first, then add real Ubuntu 24.04 and Debian 12 providers.
6. Build the public-mode timing fixture using CoreDNS/Pebble and explicit timer exclusion buckets for DNS propagation and ACME issuance.
7. Land low-dependency drills first: D-05 fake registry, D-06 readiness failure, D-10 clock skew, D-16 Docker socket EOF, D-01 kill agent.
8. Add resource/restore drills next: D-07, D-12, D-13, D-18, D-19, consuming S07 backup/restore and alert outputs.
9. Add D-14 network partition with the 10s subcase first, then 60s, then 10min nightly-only until pre-tag.
10. Add D-08 prototype webhook drill and D-17 loopback volume read-only drill only after their scope is signed by PM3/release/S08 and owning lanes.
11. Implement `test/upgrade` gates: schema idempotence, N-1 install/upgrade/smoke, broken migration rollback, skip-version refusal, `/api/v1alpha/compat` skew matrix, server-restart workload-survival prober.
12. Create `.github/workflows/phase0-exit-gate.yml` with matrix jobs, artifact upload, `act` subset, and tag trigger for `v0.*-alpha*`.
13. Add `tools/phase0-gate` aggregator that produces `phase0-exit-report.md` and fails if any required row lacks result, reason code, event/audit proof, diagnostic, or runbook.
14. Wire branch/tag protection evidence collection once the GitHub repository exists.
15. Draft `agile/handoff/phase0-exit.md` template early so every gate writes the exact evidence PM3 and the release manager need for EXIT-813.

## Completion Bar

S08 is complete only when `phase0-exit-gate.yml` is green against main for the last required window, all 19 parent drills have five consecutive green nightly runs, install timing p95s are below 15 minutes on both supported distros, security/release evidence is attached to the last-green commit, protected alpha tags require the gate, and PM3 plus the release manager sign `agile/handoff/phase0-exit.md`.
