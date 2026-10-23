# Midnight — Release Plan

Document type: Phase-to-tag mapping, release-train cadence, and per-release exit criteria.
Companion to: `D:/projects/midnight/agile/PROGRAM_OVERVIEW.md`.
Source of record: `D:/projects/midnight/midnight-platform-plan.md`.
Status: current product phase is Phase 0 Alpha; `v0.1.0-alpha` governance approvals and design-partner signoffs were received per product owner / user confirmation on 2026-04-30. Phase 1 implementation lanes are integrated as engineering work, including durable Postgres boot/migrations and package-managed service start paths, but release tags still follow the Phase 0 Alpha train below. Remaining operational/technical caveats are tracked in `agile/handoff/governance/V010_ALPHA_GREENLIGHT_BRIEF.md`. All dates are planning estimates; commitments are made at release-kickoff.

---

## 1. Release Train Overview

### 1.1 Cadence

- **Sprint length: 2 weeks.** Fixed. Sprint 1 day = Tuesday (avoid Monday holidays / Friday cuts).
- **Phase 0 Alpha = 8 sprints (~16 weeks)** producing 3 alpha tags.
- **MVP Beta = 5 core sprints (~10 weeks)** producing 3 beta tags; release kickoff may add stabilization without renumbering the backlog.
- **v1 Foundations = 10 sprints (~20 weeks)** producing release candidates and `v1.0.0`.
- **Phase 2 / 3 / 4** ship on a rolling minor cadence after `v1.0.0`.

Mid-sprint demo on Wednesday of week 2; sprint review with design partners on Thursday of week 2; release decision on Friday.

### 1.2 Tag map

| Phase | Tags |
|---|---|
| **Phase 0 Alpha** | `v0.1.0-alpha`, `v0.2.0-alpha`, `v0.3.0-alpha` |
| **MVP Beta** | `v0.4.0-beta`, `v0.5.0-beta`, `v0.6.0-beta` |
| **v1 Foundations** | `v0.7.0-rc`, `v0.8.0-rc`, `v0.9.0-rc`, `v1.0.0` |
| **Phase 2 — Team & Reliability** | `v1.1.x` → `v1.4.x` |
| **Phase 3 — Multi-Host & Adapters** | `v1.5.x` → `v2.0.0` |
| **Phase 4 — Hosted/Commercial** | `v3.x+` |

Versioning rules (panel-locked):

- Semver `MAJOR.MINOR.PATCH` for the Go API surface, packaged binaries, and CLI surface.
- Data schemas have an independent integer `schema_version` upgraded only by migrations.
- API path versioning is URL-only (`/api/v1alpha`, `/api/v1`). No media-type or header versioning.
- **Skip-version upgrades refused** with `UPGRADE_SKIP_FORBIDDEN`. `.deb` `Depends:`/`Breaks:` encode the supported window.

### 1.3 Release-channel naming

Following the spec's release-channel guidance:

- `dev` — every merge to `main` produces an internal nightly. Not for design partners.
- `alpha` — Phase 0 builds. Design-partner gated. Hard-coded `--profile=local` warning if used outside design-partner agreement.
- `beta` — MVP Beta builds. Public OSS announcement. Production-adjacent, not "production-ready".
- `stable` — `v1.0.0` and onward.
- `enterprise-lts` — added in Phase 4. Not in scope before then.

---

## 2. Phase 0 — Design Partner Alpha (3 releases, 8 sprints / ~16 weeks)

**Phase goal.** Prove the first product loop end-to-end on one VM: install, deploy, route HTTPS, keep running, diagnose, recover.

**Profile.** Trusted self-hosted single-node (Profile A). Rootful Docker. Ubuntu 24.04 + Debian 12 only.

**Design-partner gate.** No alpha release ships without at least one design-partner walkthrough on a freshly provisioned VM, with a written report listing every friction point.

### 2.0 Phase 0 release governance

Phase 0 was initially executed by eight worker subagents (`S01` through `S08`) coordinated by two PM subagents. That local-scaffold gate is historical; the current `v0.1.0-alpha` package also includes integrated Phase 1 engineering lanes recorded under `agile/handoff/phase1-subagents/`.

| Role | Accountability |
|---|---|
| **PM-A Delivery Coordination** | Integrated sprint execution, dependency closure, DoR/DoD enforcement, and cross-lane blocker escalation. |
| **PM-B Release/Risk Governance** | Release gates, scope-boundary decisions, risk register, design-partner evidence, and final exit packet completeness. |
| **S08 QA/Release Gate** | Gate harness and evidence execution. S08 reports gate status to PM-B. |

Worker lane mapping:

Note: the table below is the product release-train map. The Phase 1 lane/Lane 5/Sprint 1 engineering integrations do not by themselves advance the product phase beyond Phase 0 Alpha. `v0.1.0-alpha` approvals/signoffs were received per product owner / user confirmation on 2026-04-30; remaining operational/technical caveats stay in the greenlight brief.

| Alpha tag | Primary lanes | Release-gate focus |
|---|---|---|
| `v0.1.0-alpha` | S01 Foundations, S02 Installer/Ops, S03 API/Data | Install/bootstrap/doctor, API skeleton, schema, queue, audit seed, CI/supply-chain baseline. |
| `v0.2.0-alpha` | S04 Runtime/Agent, S06 Ingress/Logs/DX | Existing-image deploy, generated HTTPS, logs/timeline, typed failure, rollback path. |
| `v0.3.0-alpha` | S05 Build/Registry, S07 Support/Backup/Obs, S08 QA/Release Gate | BuildKit, registry, support bundle, backup/restore, telemetry default-off, full drill gate. |

Phase 0 scope boundaries unless explicitly re-scoped through `handoff/PHASE0_DECISION_LOG.md`:

| Topic | Phase 0 boundary |
|---|---|
| Custom domains | Generated apps-domain is public scope. Custom-domain HTTP-01 may exist only as a hidden/admin primitive unless product re-scopes it. |
| Off-host backup | Local platform backup/restore and `BACKUP_OFFHOST_MISSING` warning/alert are required. External off-host providers are non-blocking unless re-scoped. |
| Upgrade transaction | Full transactional upgrade remains MVP Beta. Alpha may gate schema idempotence and reinstall/smoke unless a narrow upgrade story is approved. |
| Webhook duplicate drill | Gate a prototype HMAC/delivery-id/event-hash dedupe path only; no GitHub App product claim. |
| Volume read-only remount drill | Use loopback/platform-managed fixture for drill behavior; no tenant-volume product claim. |

Design-partner signoff counts for alpha tags:

| Tag | Required signoff |
|---|---|
| `v0.1.0-alpha` | Two design partners install successfully using only published install docs. |
| `v0.2.0-alpha` | Three design partners complete the first product loop unaided. |
| `v0.3.0-alpha` | Five design partners sign off on the first product loop; at least one runs backup/restore drill. |

### 2.1 `v0.1.0-alpha` — "Install + Bootstrap" (Sprints 1–2)

**Theme.** A fresh VM can install Midnight, complete bootstrap, and pass `midnight doctor`. No deploy is promised by this product tag; integrated runtime/build/deploy components are engineering readiness for later Alpha tags.

**Scope.**

- Repository structure, coding standards, local dev environment.
- Linux package artifacts (`.deb`), package defaults, post-install bootstrap/cursor/registry setup, and systemd unit files for `midnight-server`, `midnight-worker`, `midnight-agent`, `midnight-buildkit`, `midnight-registry`.
- Installer preflight: OS/version/arch, systemd, **cgroup v2 hard-fail**, Docker daemon health, distro `docker.io` rejection, Engine API ≥ v1.43, `daemon.json` baseline (write/merge with conflict refusal), ports 80/443, UFW/firewalld/nftables/iptables mode, AppArmor/SELinux, **time sync mandatory**, hostname/FQDN.
- `midnight doctor` (live diagnostics) with stable check IDs `OR-CHK-001+`, exit-code table (0/1/2/3), `--check=<id>`, `--fix=safe|all`.
- Bootstrap admin: token written `0600 root:root /var/lib/midnight/bootstrap-token`; `midnight admin show-bootstrap` is one-shot and deletes on read; mandatory password+TOTP on first login.
- `/api/v1alpha` skeleton with stable error envelope (`code, message, request_id, docs_url, details`), Idempotency-Key plumbing, operation resources, cursor pagination (HMAC-signed).
- Postgres minimal schema: `users, workspaces, workspace_members, service_accounts, api_tokens, projects, environments, services, hosts, host_heartbeats, jobs, idempotency_keys, operations, events, audit_logs`.
- Postgres job queue contract (lease/heartbeat/skip-locked) implemented but only `cleanup` queue active.
- Audit log per-row hash chain bootstrapped from row 1; daily checkpoint signing key generated.
- Default Grafana dashboard `midnight-overview.json` shipped as baseline observability content.

**Exit criteria.**

| ID | Criterion | Verification |
|---|---|---|
| 0.1-EC1 | Fresh Ubuntu 24.04 VM → `midnight doctor` passes in <10 min | CI harness on disposable VM |
| 0.1-EC2 | Fresh Debian 12 VM → `midnight doctor` passes in <10 min | CI harness on disposable VM |
| 0.1-EC3 | cgroup v1 host refused with explicit remediation | Negative-path test |
| 0.1-EC4 | `daemon.json` conflict refused with merged-diff output | Negative-path test |
| 0.1-EC5 | Bootstrap token is one-shot; second `show-bootstrap` returns "already consumed" | Integration test |
| 0.1-EC6 | `/api/v1alpha/operations/{id}` returns valid Operation envelope; `Idempotency-Key` reuse returns 409 with conflict payload | Schemathesis |
| 0.1-EC7 | `midnight audit verify` passes on a synthetic 100-row audit fixture | Test fixture |
| 0.1-EC8 | No external network calls during install except APT repo + Docker repo + (optional) telemetry-opt-in URL | Network-capture test |

**Design-partner gate.** Two design partners install successfully unaided using only `/usr/share/midnight/runbooks/install.md`.

### 2.2 `v0.2.0-alpha` — "First Deploy" (Sprints 3–4)

**Theme.** Deploy a stateless web service from an existing OCI image, route through Traefik on the apps-domain, see logs/timeline, and roll back.

**Scope (additive).**

- Postgres schema extends with `deployments, deployment_instances, deployment_snapshots, builds, routes, variables, variable_references, domains, failure_classifications, service_sources`. Monthly partitioning on `events, audit_logs, jobs, host_heartbeats, deployment_instances`.
- Job queue activates `deploy, reconcile, webhook` classes with bounded per-tenant concurrency.
- Host agent: full registration + heartbeat (clock-skew check `>30s` → `AGENT_CLOCK_SKEW`), Docker Engine API client (no shell-out), reconciler that respects deterministic container names + Midnight labels.
- Default container hardening: drop-all + spec-locked capability allowlist, AppArmor `midnight-default` profile loaded, seccomp default, `HostConfig.Init=true`, `nofile=65536:1048576`, PIDs limit, memory hard limit, CPU quota.
- Image signature verification: cosign verify before pull/start; `IMAGE_SIGNATURE_INVALID` on mismatch. Default profile = enforced; `--allow-unsigned` is explicit.
- Deploy from existing OCI image: snapshot recorded with image digest (no build path yet); guarded transition functions for `QUEUED → BUILDING → DEPLOYING → ACTIVE/SUPERSEDED/FAILED`.
- Atomic single-flight per `(service, environment)` via `pg_advisory_xact_lock`; `runLatest` arbitration via `bigserial`.
- Traefik via file-provider only (no Docker socket mount). Dynamic config files written via temp-file + atomic rename. `/.well-known/acme-challenge/*` reserved at max router priority.
- ACME HTTP-01 for generated subdomains; **DNS-01 + wildcard for the apps-domain** with `DnsProvider` interface (Cloudflare, Route53, `manual`).
- ACME state batched issuance ≤ N/min, hard cap 40 issuances/registered-domain/week, LE staging fallback.
- Installer external reachability prober for :80/:443 before declaring ACME ready.
- CAA preflight before any ACME submit.
- Logs: stdout/stderr capture via Docker `json-file` (source of truth), structured event indexing into Postgres, NDJSON shape `{ts, stream, level, msg, attrs}`.
- Deployment timeline: source SHA, builder, image digest, host, container start, healthcheck, route update, TLS issuance, fail reason — visible via API + CLI.
- Healthcheck taxonomy: three probes (`startup`, `readiness`, `liveness`); only readiness gates route health; default `startupTimeout >= 180s`.
- Rollback by digest: `midnight rollback [--to DEPLOYMENT_ID | --to-revision N | --previous]`. Default `--previous` with confirmation.
- CLI Phase-0 surface: `login, init, link, deploy, status, logs, rollback, domain, doctor, preflight`. NDJSON output for read commands; `--json` everywhere.
- Dashboard Phase-0 journeys: bootstrap admin, create project, create service, deploy from image, watch timeline/logs, configure generated domain, diagnose failed deploy, roll back.
- Failure reason codes: at minimum `IMAGE_PULL_FAILED, IMAGE_SIGNATURE_INVALID, IMAGE_ARCH_UNAVAILABLE, CONTAINER_EXITED, COMMAND_NOT_FOUND, PORT_NOT_LISTENING, APP_BOUND_LOCALHOST, HEALTHCHECK_FAILED, ROUTE_RECONCILIATION_FAILED, TLS_ISSUANCE_FAILED, DNS_VERIFICATION_FAILED, OOM_KILLED, HOST_UNAVAILABLE, ROLLBACK_IMAGE_UNAVAILABLE, INGRESS_PORT80_UNREACHABLE, AGENT_CLOCK_SKEW, DOCKER_ENGINE_UNSUPPORTED, IDEMPOTENCY_REQUEST_IN_PROGRESS, IDEMPOTENCY_KEY_REUSED, RESOURCE_VERSION_MISMATCH, CANCEL_NOT_SAFE`.

**Exit criteria.**

| ID | Criterion | Verification |
|---|---|---|
| 0.2-EC1 | Local mode (`--profile=local`, `*.midnight.local`, self-signed): fresh-VM-to-200-OK in <15 min | CI harness |
| 0.2-EC2 | Public mode: fresh-VM-to-200-OK on generated subdomain in <15 min excluding DNS propagation + ACME | CI harness with mocked DNS |
| 0.2-EC3 | Wildcard certificate issued via DNS-01 for the apps-domain on Cloudflare provider | LE staging end-to-end |
| 0.2-EC4 | Wildcard certificate issued via DNS-01 on Route53 provider | LE staging end-to-end |
| 0.2-EC5 | Rollback by digest succeeds; previous active deployment serves traffic until candidate is ready | Three-service test matrix |
| 0.2-EC6 | Failed deploy of every fixture (10+ shapes) produces a registry reason code + recovery hint | Failure-fixture corpus |
| 0.2-EC7 | `runLatest` race: 10 concurrent push deploys → exactly 1 ACTIVE, 9 SUPERSEDED, no orphan instance | Chaos test |
| 0.2-EC8 | Cancellation in route-add → drain window returns `CANCEL_NOT_SAFE` | Negative-path test |
| 0.2-EC9 | Idempotency-Key: same body → same operation; different body → 409 IDEMPOTENCY_KEY_REUSED; concurrent in-flight → 409 IDEMPOTENCY_REQUEST_IN_PROGRESS | Schemathesis + race test |
| 0.2-EC10 | Image signature verification refuses unsigned image with `IMAGE_SIGNATURE_INVALID` | Negative-path test |
| 0.2-EC11 | Internal DNS TTL = 5s with stale-on-not-ready removal verified | Reconcile test |
| 0.2-EC12 | Traefik never has Docker socket mounted | Container-spec audit in CI |

**Design-partner gate.** At least 3 design partners deploy a real web app on their own VM and complete the first product loop unaided.

### 2.3 `v0.3.0-alpha` — "Build, Backup, Drill" (Sprints 5–8)

**Theme.** Phase 0 hardening: optional Dockerfile build, ingress/logs closeout, support bundle, platform backup/restore smoke, observability/supportability, and all required Alpha failure drills green.

**Scope (additive).**

- BuildKit Dockerfile build (pinned BuildKit + Dockerfile frontend digests), local per-service cache only, no remote shared cache. Untrusted-build classification scaffolded (can refuse but not yet exercised since no GitHub App).
- Build defaults: 30-min timeout, per-host concurrency `min(4, cores/2)`, per-project 2; disk-pressure rules (warn 75%, degrade 85%, block 92%).
- Build snapshot stores: source SHA, builder type/version/digest, resolved plan JSON, resolved base image digests for every `FROM`, produced image digest. Rollback never rebuilds.
- Co-located BuildKit: dedicated unprivileged user, CPU/memory cgroup cap, dedicated tmpfs, no read access to `/var/lib/midnight/{db,secrets,registry/data,kms}`. `BUILD_ROOTFUL` warning surfaced by `midnight doctor`.
- Static scan of build output for `midn_, AKIA, xoxb-, JWT shape` → `BUILD_OUTPUT_SECRET_LEAK`.
- Embedded registry retention: last 10 image tags per service + all rollback-window digests. Daily ref-count + tag-retention sweep. `midnight registry gc --dry-run|--apply`.
- `midnight support-bundle` with redaction preview; defaults: 24h window, 200 MB cap, `--include=logs,metrics,events,config`. Manifest reports `truncated:true` per category.
- `midnight backup create --platform` (control-plane Postgres + `/etc/midnight` + `secrets.key` + registry metadata + ACME state) and `midnight restore --platform` smoke test on a fresh VM.
- ACME state restore at scale: byte-exact restore; if reissue is required, ≤ 20 certs/hour with priority queue (TLS-expired first), LE staging for first batch, surface progress as `RESTORE_ACME_THROTTLED`.
- `midnight feature {list,disable,enable}` with kill switches for `acme.issuance, registry.gc, ai.gateway, auto.remediation`. Status surfaced in `doctor`.
- Manual runbooks shipped at `/usr/share/midnight/runbooks/*.md`: failed deploy triage, rollback, restart service, domain/TLS failure, disk pressure, backup/restore, regenerate Traefik config, collect support bundle.
- Default Prometheus alert rules shipped: `DiskPressure, AgentLost, BackupFailed, CertExpiringSoon, DeployJobStuck, BuildKitCacheCorruption, ConntrackPressure, BackupOffhostMissing`.
- `oasdiff` breaking-change check wired into CI.
- Telemetry: opt-in only at install time, single endpoint, no project/service names, only counts and reason-code histograms.

**Required Alpha failure drills (all must be green to ship `v0.3.0-alpha`):**

- [ ] Kill `midnight-agent` mid-deploy → reconciliation resumes after restart; deployment reaches `ACTIVE` or fails with reason code.
- [ ] Kill `midnight-server` during running deploy → workloads survive; deploy resumes after restart.
- [ ] Kill `midnight-worker` mid-job → job lease expires; another worker picks up; no double-effect.
- [ ] Restart Docker daemon (`live-restore: true`) → existing containers survive; agent enters `AGENT_RUNTIME_UNAVAILABLE` with backoff, recovers without crash-loop.
- [ ] Fail image pull → `IMAGE_PULL_FAILED` with retry budget; deployment fails cleanly.
- [ ] Fail readiness → previous active deployment continues serving; candidate fails with `HEALTHCHECK_FAILED`.
- [ ] Disk pressure: fill `/var/lib/midnight` to 85% → builds blocked; to 92% → deploys + volumes blocked.
- [ ] Duplicate webhook delivery → idempotency key dedupes; no double-deploy.
- [ ] Broken DNS verification (TXT mismatch) → `DNS_VERIFICATION_FAILED`; cert never requested.
- [ ] Clock skew >5min → ACME ops refused; agent registration refused with `AGENT_CLOCK_SKEW`.
- [ ] journald wedged → platform logs continue via `json-file`; alert raised.
- [ ] `/var/log` full → no platform crash-loop; cleanup runbook restores service.
- [ ] Postgres connection exhaustion → graceful degradation (read-only mode); user data planes continue.
- [ ] Network partition for 30s / 5min / 30min → agent reconnect with jittered backoff; control plane "warmup" mode allows only Class-A remediation.
- [ ] BuildKit local-cache corruption → cache reclaimed; subsequent build succeeds.
- [ ] Docker socket EOF → `AGENT_RUNTIME_UNAVAILABLE`; no crash-loop.
- [ ] Volume read-only remount → service marked degraded; explicit reason code.
- [ ] **KEK loss** → recovery is impossible (verified); operator-supplied separate-passphrase backup pathway works on independent backup.
- [ ] **ACME state restore at scale** → 50 generated subdomains, restored byte-exact, throttled reissue if forced, no LE rate-limit burn in production.

**Exit criteria.**

| ID | Criterion | Verification |
|---|---|---|
| 0.3-EC1 | Dockerfile build of three reference apps (Node, Python, Go) succeeds with snapshot recording | CI |
| 0.3-EC2 | Rollback by digest works after build path is enabled (does not re-invoke build) | Integration |
| 0.3-EC3 | All required Alpha failure drills above are green | Drill harness |
| 0.3-EC4 | Support-bundle redaction: 0 secret leakage on the seeded secret-fixture corpus | Redaction test |
| 0.3-EC5 | Platform backup → fresh-VM restore → smoke check completes in <30 min | Drill |
| 0.3-EC6 | `midnight registry gc --dry-run` reports correctly without modifying state | Integration |
| 0.3-EC7 | Disk-pressure thresholds: 75/85/92% and 70/90% for inodes verified end-to-end | Drill |
| 0.3-EC8 | Telemetry opt-in: zero outbound calls without explicit `--enable-telemetry` | Network capture |
| 0.3-EC9 | At least 5 design partners sign off on the first product loop | Written sign-off |

**Design-partner gate.** Five-partner sign-off; at least one partner runs the full backup-restore drill on their own VM.

---

## 3. MVP Beta (3 releases, 5 core sprints / ~10 weeks)

**Phase goal.** Production-adjacent self-hosted PaaS for small teams. Foundations enterprise teams cannot retrofit later (audit chain, signed releases, off-host backup, RBAC, GitHub App, sealed variables).

**Profile.** A (single-node) hardened.

### 3.1 `v0.4.0-beta` — "Variables, Secrets, RBAC" (Sprints 9–10)

**Theme.** Multi-service projects with environments, variables/secrets, sealed variables, and deny-by-default authorization. Custom domains end-to-end.

**Scope (additive).**

- Variables: `plain, secret, sealed, shared, reference, system`. Required system variables shipped (`MIDNIGHT_PROJECT_ID, …, PORT`).
- Envelope encryption: KEK at `/etc/midnight/secrets.key` (`0400 midnight:midnight`); rotation via background `operations` job rewraps DEKs with progress; KEK_n retained 30 days. KEK loss is documented irrecoverable. KEK-loss drill is a release gate.
- **Sealed variables**: no API path returns plaintext after create; rotation = new value not view-then-edit; CLI has no `--reveal`; UI shows last-rotated and length-bucket only; audit/snapshot exports elide plaintext.
- Reference variable cross-scope authorization: actor must have read access on referenced variable's scope at creation; consuming service's deploy principal must have read access at every resolution. Cross-environment-tier references denied unless explicit policy allows.
- Build-time vs runtime separation: `buildVars, buildSecrets (BuildKit secret mounts), runtimeVars, runtimeSecrets, publicClientVars, systemVars`.
- RBAC v1: `Workspace owner, Project admin, Developer, Deployer, Viewer`. Deny-by-default authorization middleware on every API/CLI/dashboard/MCP/webhook/SA path.
- Authorization tests are release gates.
- Custom domains: TXT ownership at `_midnight-verify.<apex>`, routing record validation (CNAME for subdomains; ALIAS/ANAME/CNAME-flattening/A/AAAA for apex), HTTP-01 issuance after ownership verification. Wildcard custom domains require DNS-01 (already shipped in Alpha for apps-domain).
- Apex bindings store `advertisedIngressAddresses` set used at verification; periodic re-verification job for IP drift.
- Multiple environments (`production, staging, …`) with branch mapping; environment-tier semantics.
- Postgres schema extensions: `volumes, volume_attachments, backups, repository_connections, registry_connections, secret_store_connections, change_sets, change_set_operations, environment_protection_rules, deployment_approvals, artifact_promotions, retention_policies, evidence_artifacts, webhook_endpoints, webhook_subscriptions, webhook_deliveries, webhook_delivery_attempts, dead_letter_events`.
- Production environment protection: required reviewers, prevent self-approval, allowed deployers, allowed branches/tags, wait timer, deploy freeze windows, admin bypass setting, break-glass with full audit.
- Minimal built-in policies: deny privileged/host-network/host-PID/host-IPC/arbitrary devices/arbitrary bind mounts; require resource limits; require owner/team metadata; block mutable image tags when digest available; block public DB TCP exposure unless approved; require custom domain verification before routing or cert.

**Exit criteria.**

| ID | Criterion | Verification |
|---|---|---|
| 0.4-EC1 | Sealed variable spec compliance: zero API paths return plaintext post-create | Schemathesis fuzz |
| 0.4-EC2 | KEK rotation rewraps all DEKs without service downtime; rollback to KEK_n works during 30-day grace | Drill |
| 0.4-EC3 | Reference variable cross-scope authorization: cross-environment-tier reference denied unless policy allows | Authz test |
| 0.4-EC4 | Custom domain TXT verification refuses certificate issuance before TXT proves | Negative-path |
| 0.4-EC5 | RBAC: Deployer cannot read secrets; Viewer cannot deploy; Developer cannot bypass production protection | Authz matrix |
| 0.4-EC6 | Production environment protection: self-approval refused; branch allowlist enforced | Integration |
| 0.4-EC7 | KEK loss drill documented and run; recovery via operator-supplied separate passphrase verified | Drill |

**Design-partner gate.** Two design partners deploy a multi-service app with custom domain in production-tier environment.

### 3.2 `v0.5.0-beta` — "GitHub, Audit, Volumes, Postgres" (Sprints 11–12)

**Theme.** GitHub push deploys, audit chain export, single-writer volumes with quotas, Postgres template with database-native backup, off-host backup destination.

**Scope (additive).**

- GitHub App push deploys: operator-owned shared App and user-provided App credentials; webhook signature verification (`X-Hub-Signature-256`); delivery ID + event hash dedupe; install/repo-access/suspension lifecycle handled.
- Webhook ingestion: store provider delivery ID + event hash; enqueue async work before processing; respond within provider deadline; handlers idempotent; redelivery + manual replay; poison events → dead-letter with diagnostics.
- Outbound webhook signing: `Midnight-Signature: t=<unix>,v1=<hex>,kid=<key_id>`, replay window 300s, two active keys per endpoint, 30-day rotation.
- Required CI status check, environment locks, artifact promotion records (source SHA, image digest, SBOM, provenance, checks, approvals, actor, gate results, target environment).
- Provider-neutral event contract: `push, pull_request_*, branch_deleted, tag_created, check_completed, pipeline_completed, manual_deploy_requested, image_published, deployment_approved/rejected, alert_fired`.
- Untrusted-build classification activates: fork PR / non-member commit / non-trusted branch → no project secrets, no shared cache, ephemeral cache only, quarantine registry, no promotion without explicit approval.
- Audit log JSONL export with hash chain (`prev_hash, row_hash, checkpoint_id`); `midnight audit verify` CLI for the full chain; Ed25519 daily checkpoint signing with operator-configurable key.
- Audit export schema includes request ID, correlation ID, actor, actor type, auth method, source IP, user agent, resource, action, before/after metadata where safe, policy decision, timestamp.
- Volumes: single-writer per stateful service; XFS/ext4 project quotas (`prjquota` mount option required); BuildKit cache + registry on quota-isolated subtrees; soft-delete 7-day grace (configurable down to 24h, never below); online grow only (`RESIZE_SHRINK_UNSUPPORTED`).
- Volume directories owned `root:midnight`, mode 0700, until first mount; on first mount agent reads template's expected UID/GID and `chown`s; UID conflict → `OWNERSHIP_CONFLICT`.
- Postgres template (only one in MVP Beta): pinned image, mount path, internal port, healthcheck, generated credentials, connection URL format, expected UID/GID, backup/restore/smoke commands.
- Postgres logical backup: `pg_dumpall --globals-only` + `pg_dump --format=custom --blobs --no-owner` per DB. Restore = globals first, then `pg_restore -j <n>` against fresh same-major cluster. Databases >50 GB → `BACKUP_SIZE_DEGRADED`.
- Restic repository per project at `/var/lib/midnight/backups/<project_id>/`; per-project password sealed by KMS; off-host destination configurable (`s3://`, `gcs://`, `sftp://`, rsync). `BACKUP_OFFHOST_MISSING` from `doctor` if absent.
- Each new backup config runs one-time restore-into-throwaway-volume + smoke check before being marked `verified`. `BACKUP_UNVERIFIED` if it fails.
- `BACKUP_STALE` state when policy disabled or 2× period elapses without successful backup. Per-service RPO = `time_since_last_successful_backup`.
- Deployment image pins reference-counting table consulted by registry GC; pinned digests unreclaimable while pin count > 0 or within rollback window.
- Mid-Beta API stability check: `oasdiff` zero breaking changes between any `v0.4.x` → `v0.5.x` minor.

**Exit criteria.**

| ID | Criterion | Verification |
|---|---|---|
| 0.5-EC1 | GitHub push → deploy → status report cycle completes in <90s P95 on the reference app | Synthetic test |
| 0.5-EC2 | Webhook signature verification + idempotency: replay does not double-deploy | Integration |
| 0.5-EC3 | Fork PR build receives no project secrets; cache is ephemeral; registry path is quarantine | Integration |
| 0.5-EC4 | `midnight audit verify` detects 100% of seeded tamper attempts (insert/delete/edit) | Tamper fixture |
| 0.5-EC5 | Off-host backup restore succeeds on `s3://` and `sftp://`/rsync | Drill |
| 0.5-EC6 | Postgres template: backup → restore-to-replacement-volume → smoke succeeds | Drill |
| 0.5-EC7 | `prjquota` enforced: over-quota write produces explicit error, not crash | Integration |
| 0.5-EC8 | `BACKUP_OFFHOST_MISSING` warning fires when no off-host destination configured | Negative-path |
| 0.5-EC9 | Volume soft-delete 7-day grace works; hard delete is logged | Integration |

**Design-partner gate.** At least 2 design partners successfully use GitHub App push deploys for a week with audit export reviewed.

### 3.3 `v0.6.0-beta` — "Signed Release, Upgrade Transaction, Vuln Disclosure" (Sprint 13)

**Theme.** Trustworthy releases, transactional upgrades, public security posture.

**Scope (additive).**

- Signed releases: APT archive signing key with published fingerprint/expiry/rotation procedure; cosign signatures on OCI/binary artifacts; SBOM + SLSA-style provenance attestations attached to release; verification instructions in `/usr/share/midnight/release-verify.md`.
- Public claim language: "SLSA-style provenance generated"; no SLSA Level claim until audit.
- Midnight's own supply chain: lockfiles required; CI runs `govulncheck`, `osv-scanner`, `npm audit --omit=dev` as required checks; PRs from forks do not get repo secrets and do not push images; release artifacts built only from protected branches; npm packages pinned by integrity hash.
- `midnight upgrade` transaction: pre-upgrade snapshot tag, migrations in advisory-locked txns, smoke probe with timeout, on failure auto-`midnight rollback --to-snapshot=<tag>`. Migrations tagged `reversible|forward-only`; forward-only requires `--accept-irreversible` plus verified pre-upgrade backup. `apt` post-install hook refuses to run if another upgrade lock is held.
- `midnight upgrade-plan` resolves the path; skip-version refused with `UPGRADE_SKIP_FORBIDDEN`. `.deb` `Depends:`/`Breaks:` constraints encode min/max compatible versions.
- Component compatibility matrix published as artifact (`/docs/compat/*.md`): server, worker, agent, BuildKit, Traefik, Docker Engine, Postgres, Railpack with supported range relative to server N and behavior on skew.
- Server exposes `/api/v1alpha/compat`; worker/agent registrations outside window refused with `VERSION_SKEW_REJECTED`.
- Vulnerability disclosure policy published with `security@` mailbox + PGP key; pre-disclosure window documented.
- `midnight uninstall` enumeration: stops units; preserves data paths; removes systemd unit files; removes APT source/keyring only with `--purge-repo`; flushes `midnight-*` iptables chains; removes `midnight_*` Docker networks only if empty; leaves `daemon.json` edits with marker comment.
- logrotate drop-in shipped (`/etc/logrotate.d/midnight`).
- journald drop-in shipped with `RateLimitBurst, SystemMaxUse, SystemKeepFree`.
- N-1 → current upgrade test (CI) with auto-rollback on injected failure (3 modes: migration error, smoke failure, post-upgrade health failure).

**Exit criteria — MVP Beta exit (must hold across `v0.6.0-beta`):**

| ID | Criterion | Verification |
|---|---|---|
| 0.6-EC1 | Signed `.deb` verified with published key fingerprint; verification instructions tested by docs CI | Release CI |
| 0.6-EC2 | Cosign signatures + SBOM + SLSA-style provenance attached to every release artifact | Release CI |
| 0.6-EC3 | N-1 → current upgrade succeeds; injected failures auto-rollback to snapshot tag | Drill |
| 0.6-EC4 | Skip-version (N-2 → current) refused with `UPGRADE_SKIP_FORBIDDEN` | Negative-path |
| 0.6-EC5 | Vulnerability disclosure policy published; `security@` reachable; PGP key works | Manual |
| 0.6-EC6 | `midnight uninstall` preserves volumes/registry/Postgres/secrets unless `--purge` flags pass | Integration |
| 0.6-EC7 | `oasdiff` zero breaking changes across `v0.4.x` → `v0.6.x` | CI |
| 0.6-EC8 | All Alpha failure drills + Beta-additional drills (interrupted upgrade, registry outage, ACME renewal failure) green | Drill harness |

**Design-partner gate.** Three design partners successfully run an upgrade with auto-rollback drill and confirm signed-release verification.

---

## 4. v1 Foundations (4 releases, ~20 weeks)

**Phase goal.** Stable `/api/v1`, IaC-grade contracts, scheduled backups with verification, OIDC, read-only AI diagnosis, quotas/showback, plan/dry-run/drift/import-export.

**Profile.** A (single-node) plus opt-in early Profile B groundwork (no support claim until Phase 3).

### 4.1 `v0.7.0-rc` — "OIDC, Quotas, Retention" (Sprints 17–20)

**Theme.** Identity, governance, retention.

**Scope (additive).**

- OIDC/OAuth login: PKCE + nonce + state; MFA delegated to IdP; break-glass local admin remains explicit and audited.
- Service-account scoped tokens: two simultaneously valid tokens for ≥ 24h overlap during rotation; CLI auto-revokes tokens unused for N days.
- Quotas: CPU, memory, disk, logs, builds, services, domains, backups, preview environments. Quota policies in Postgres; usage measurements ledger.
- Required allocation labels: `owner, team, cost_center, environment_tier, data_classification, business_service, compliance_scope`.
- Retention policies for logs, audit events, deployment records, backups, support bundles, AI traces, webhook deliveries.
- Usage/showback ledger (no billing yet).

### 4.2 `v0.8.0-rc` — "Plan, Drift, Import/Export" (Sprints 21–24)

**Theme.** IaC-shaped declarative resources, plan/dry-run, drift detection, non-secret state export.

**Scope (additive).**

- Plan JSONSchema at `schemas/plan/v1.json` with `formatVersion: "1.0"`. Provider Resource Matrix per resource (`midnight_project, midnight_environment, midnight_service, midnight_domain, midnight_variable, midnight_volume, midnight_template_instance`).
- `applyChangeSet` atomic at DB level: all `If-Match` checks evaluated together; first conflict returns 409 with conflicting resource list and nothing applied. Side effects via transactional outbox.
- `midnight config validate/diff/explain`; `midnight config apply --dry-run`.
- `midnight export --project --format midnight-yaml|json` for non-secret desired state.
- `midnight import service|domain|volume`.
- Drift detection covers service config, domains, routes, variables metadata, deployment source, volumes, host/runtime observed state, and dashboard/API changes vs config-as-code.
- CloudEvents catalog (event type → schema → since-version) shipped; AsyncAPI generated from registry.

### 4.3 `v0.9.0-rc` — "Scheduled Backups, PR Previews, Read-Only AI" (Sprints 25–30)

**Theme.** Default-on scheduled backups, PR preview environments with trust rules, optional read-only AI diagnosis.

**Scope (additive).**

- Default scheduled backup policy enabled at template instantiation (daily, 7-day retention). `BACKUP_STALE` if disabled or 2× period without success.
- Weekly sample restore for at least one backup per service (v1 commitment).
- PR preview environments: PR open/synchronize/close/branch-delete lifecycle; idle TTL + retention TTL; fork PR secret isolation; preview URL posted back to provider; default empty data; production database cloning disabled.
- Read-only AI diagnosis (`explainDeploymentFailure, summarizeLogs, explainConfig, compareDeployments, generateRunbook`) gated by:
  - AI features disabled by default; workspace policy decides allowed providers.
  - Multi-pass redaction pipeline release gate `secret_leakage_rate == 0` on fixture set.
  - Eval gates: deployment diagnosis accuracy, secret leakage rate, prompt-injection resistance, tool-selection accuracy, required-approval recall, unsafe-action refusal rate, citation correctness, hallucinated-API rate, remediation success rate.
- MCP server contract: SA tokens with `mcp:tools` plus per-tool scopes; mutating tool calls require `Idempotency-Key`; Class 3+ tools require out-of-band human approval through dashboard.
- Conftest-compatible policy validation for configs/templates.

### 4.4 `v1.0.0` — "Stable" (Sprints 31–32)

**Theme.** Promote `/api/v1alpha` → `/api/v1`. Lock contract.

**Scope.**

- Promote `v1alpha` → `v1`; deprecation header machinery (RFC 8594 `Sunset`, `Deprecation`).
- Two consecutive SDK releases generated without manual patches.
- At least one external IaC consumer integration in production (design-partner case study).
- Pact contracts green for CLI, dashboard, Terraform provider (early-access), MCP.

### 4.5 v1 Graduation Rubric (panel-locked, copied verbatim)

The v1 graduation rubric is concrete and binding. **All six must hold simultaneously to ship `v1.0.0`:**

- [ ] **(a)** Zero `oasdiff` breaking-change diff between two consecutive minors for **≥ 90 days**.
- [ ] **(b)** **≥ 95%** endpoint coverage by Schemathesis property tests.
- [ ] **(c)** Green Pact contracts for **CLI, dashboard, Terraform provider, MCP**.
- [ ] **(d)** Deprecation header machinery (RFC 8594 `Sunset`, `Deprecation`).
- [ ] **(e)** **Two consecutive SDK releases generated without manual patches.**
- [ ] **(f)** **At least one external IaC consumer integration in production.**

Until all six are checked, the project ships `v0.x` only. Release management owns this checklist; no exceptions.

### 4.6 v1 additional failure drills (release gates)

- [ ] Postgres restart during running jobs → leases reclaim, no double-effect.
- [ ] Interrupted upgrade → auto-rollback to snapshot tag.
- [ ] Backup restore to replacement volume → service redeploys cleanly.
- [ ] Registry outage → deploys queue and resume; rollback by digest still works (mirror to embedded registry).
- [ ] ACME renewal failure → diagnostic surfaced; fallback to staging if budget exhausted.
- [ ] Host reboot → workloads survive (`live-restore: true`), agent reconciles.
- [ ] Agent/control-plane version skew → outside compatibility window refused with `VERSION_SKEW_REJECTED`.

---

## 5. Phase 2 — Team & Reliability Layer (`v1.1.x` → `v1.4.x`)

**Phase goal.** Workspaces/team model, better RBAC, policy-as-code authoring/testing, audit log UI, GitLab native integration, full PR previews, Terraform/OpenTofu provider, SIEM/log-drain integrations, additional database templates after gates.

Per-release themes (planning, not committed):

- `v1.1.x` — Workspaces, audit log UI/API, agent drain/maintenance mode.
- `v1.2.x` — GitLab native integration; OPA-embedded authorization decisions; expanded policy library.
- `v1.3.x` — **Terraform/OpenTofu provider** (acceptance tests against disposable instances); SIEM streaming for audit; OTLP collector pipeline production-ready.
- `v1.4.x` — Additional database templates (Redis, MySQL, MongoDB single-node) after backup/restore/upgrade/UID-GID/smoke gates pass per template; read-only AI expanded with Class-2 draft actions (still no mutation).

Each `v1.x` minor must hold the v1 graduation rubric. Any breaking change requires a major bump (`v2.0.0`).

---

## 6. Phase 3 — Multi-Host & Runtime Adapters (`v1.5.x` → `v2.0.0`)

**Phase goal.** Scale beyond one machine.

Per-release themes:

- `v1.5.x` — Multi-host scheduling for stateless services; agent canary rollout; component compatibility matrix updates.
- `v1.6.x` — **WireGuard private networking**: PMTU probe at peer-up, default MTU 1380, scheduled key rotation with overlap, peer-add/remove FSM, conntrack flush on peer remove. Internal DNS resolver shipped.
- `v1.7.x` — **TCP proxy** (separate from HTTP ingress); generated host + external port; disabled by default for database templates; source IP policy.
- `v1.8.x` — Registry cache improvements; volume attach/detach where backend allows.
- `v1.9.x` — **Kubernetes runtime adapter**: passes a subset of deployment-state acceptance suite; runtime adapter boundary `BuildArtifact -> ScheduleRequest -> InstanceStatus -> RoutingTarget`.
- `v2.0.0` — Allowed only if Phase 3 introduces a breaking API change. Otherwise stay on `v1.x`.

---

## 7. Phase 4 — Hosted/Commercial Optionality (`v3.x+`)

**Phase goal.** Hosted control plane or managed offering without forking OSS core. Public multi-tenant hardening.

Major themes (no committed sequencing yet):

- Public multi-tenant hardening + stronger build isolation (microVM/gVisor/Kata).
- Billing/metering, plan limits, abuse controls.
- Multi-region routing.
- HA control plane (Profile C).
- Enterprise SSO, SAML, SCIM, BYOK, disconnected install bundles, enterprise LTS, support SLAs, DPA/subprocessor/data residency controls.

The core OSS project remains self-hostable and Profile-A-first throughout Phase 4.

---

## 8. Cross-Release Engineering Practices

These are not phase-specific; they apply on every release.

### 8.1 Definition of Done (per story)

- Acceptance tests green: happy path, failure path, idempotent retry where applicable, observable CLI/API status, supportability artifact (runbook entry or doctor check or reason code).
- OpenAPI lint green; `oasdiff` no breaking change unless an explicit minor/major bump justifies it.
- Authorization tests green for any authz-touching change.
- Failure-fixture corpus updated if a new reason code is introduced.
- Runbook entry shipped if a new operator-visible failure mode is introduced.
- Audit event emitted for any state-changing action.

### 8.2 Definition of Done (per release)

- All exit criteria for the tag are green.
- All required failure drills for the phase are green.
- Design-partner gate cleared.
- Release notes published with reason-code additions, schema migrations (`reversible|forward-only`), API changes, and verification instructions.
- Signed `.deb` + cosign signatures + SBOM + SLSA-style provenance + release-verify doc.

### 8.3 Branch and merge model

- Trunk-based on `main` with short-lived feature branches.
- Protected branches: `main`, `release/*`. PRs from forks do not receive secrets and do not push images.
- `release/v0.X` branch cut at the start of stabilization sprint of each tag; only fixes merge after that.
- Hot-fix releases (`v0.X.Y`) cut from `release/v0.X`.

### 8.4 CI gates (every PR)

- `govulncheck`, `osv-scanner`, `npm audit --omit=dev`.
- `oasdiff` against the latest tagged spec.
- Schemathesis property tests on changed endpoints.
- Authorization matrix tests.
- Idempotency key replay test.
- Build snapshot canonicalization test (RFC 8785).
- Linux CI lane (Ubuntu 24.04 + Debian 12) for any package or systemd change.

### 8.5 Release verification (every tag)

- APT install on clean Ubuntu 24.04 + Debian 12.
- `midnight doctor` clean post-install.
- Signed-release verification using published key fingerprint.
- N-1 upgrade smoke test (from `v0.4.0-beta` onward).

---

## 9. Compatibility & Skew Window

Component compatibility matrix is a published artifact. The default supported window:

| Component | Window relative to server `N` | Behavior on skew |
|---|---|---|
| Server | `N` | n/a |
| Worker | `[N-1, N]` | Older = read-only mode; newer = refused |
| Agent | `[N-2, N]` | Older = degraded heartbeat-only; newer = refused with `VERSION_SKEW_REJECTED` |
| BuildKit | pinned digest per server release | Mismatch = refused |
| Traefik | pinned major within server release | Mismatch = `doctor` warns |
| Docker Engine | API ≥ v1.43, Engine ≥ 24.0 | Outside = refused with `DOCKER_ENGINE_UNSUPPORTED` |
| Postgres | pinned major per server release | Major mismatch = refused |
| Railpack | pinned digest per builder release | Used by digest, never `latest` |

Skip-version upgrades (e.g., `v0.4.0-beta` → `v0.6.0-beta`) are refused. Operators must walk the path: `v0.4 → v0.5 → v0.6`.

---

## 10. Communication & Demo Plan

| Forum | Frequency | Audience | Owner |
|---|---|---|---|
| Sprint review demo | Every 2 weeks | Internal + design partners | Eng lead |
| Design-partner Slack | Continuous | Design partners | PM |
| Public release notes | Per tag | Public OSS audience (post-Beta) | Docs lead |
| Roadmap update | Per phase | Public OSS audience | PM |
| Security advisories | As needed | Public + `security@` subscribers | Security lead |
| Architecture review | Weekly | Internal team | Architect |

Pre-`v0.4.0-beta` (Alpha period), demos are private to design partners. Repository goes public at MVP Beta announcement.

---

## 11. Out-of-Scope Confirmation (Do Not Add to Any Release Below `v3.x`)

The following are explicitly not scheduled, and any proposal to add them earlier requires a panel-style review:

- Billing, plan limits, marketplace monetization.
- Public hosted multi-tenancy.
- Multi-region active-active.
- HA Postgres/HA databases.
- Autoscaling, scale-to-zero.
- Marketplace.
- Customer-managed keys (BYOK).
- SAML SSO, SCIM 2.0, LDAP/AD.
- Pulumi provider (Phase 3 earliest).
- Native Jenkins/Azure DevOps adapters (Phase 3 earliest if demand exists).
- Mutating AI agents.
- Full disconnected install bundle.
- SLSA Build Level claims (`SLSA-style` only until audit).

---

## 12. Decision Log Hooks

Whenever a release modifies a panel-locked decision, the change must be logged here in the form:

```
[YYYY-MM-DD] Release v0.X.Y modified <decision>: <previous> → <new>. Justification: <reason>. Signed off by: <reviewers>.
```

If the change cannot meet that bar, it is rejected. The eight-specialist panel decisions are not subject to silent drift.

---

## 13. Pointers

- Program overview: `D:/projects/midnight/agile/PROGRAM_OVERVIEW.md`
- Source spec (authoritative): `D:/projects/midnight/midnight-platform-plan.md`
- Critical path: see "Implementation Order" section of source spec.
- Eight-Specialist Panel Locked Decisions: source spec, top section. **Do not relitigate.**

End of release plan.
