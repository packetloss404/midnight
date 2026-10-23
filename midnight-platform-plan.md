# Midnight — Open-Source Railway-Like PaaS Research And Build Plan

> **Historical plan with a retained Phase 0 decision baseline.** This is not
> a current capability inventory or work queue. ADR-0001 preserves the panel
> decisions as normative for Phase 0; later accepted ADRs document changes.
> Use the [live ADR index](docs/adr/README.md), [backlog](BACKLOG.md), and
> [deployment readiness](docs/deployment-readiness.md) for maintained status.

Date: 2026-04-25

Status: revised architecture after web research, software-engineering review, Linux review, AI review, enterprise review, automation review, and an eight-specialist panel review.

Project name and codename: **Midnight**. Earlier drafts used the working codename `OpenRail`; that codename has been retired in favor of `Midnight`, which is also the proposed final project name. All binaries (`midnight`, `midnight-server`, `midnight-worker`, `midnight-agent`), system paths (`/var/lib/midnight`, `/etc/midnight`, `/usr/share/midnight`, `/var/log/midnight`), API token prefix (`midn_<class>_…`), config file (`midnight.yaml`), schema namespace (`midnight.dev/v1`), webhook signature header (`Midnight-Signature`), CloudEvents source (`//midnight/<instance_id>/…`) and event type prefix (`dev.midnight.<resource>.<verb>`), AppArmor profile (`midnight-default`), private DNS zone (`<service>.<environment>.<project>.internal`), and APT package family use this name. The agile delivery bundle in `agile/` has been renamed in lockstep.

Note: this document describes an open-source Railway-like platform. It should not use Railway branding, imply affiliation, or copy proprietary implementation. Railway is used only as a public product reference.

## Executive Summary

The feasible open-source version of Railway is not a full clone of Railway's private cloud. The credible path is a narrow self-hosted PaaS first: install on one trusted VM, deploy an app, get HTTPS, and keep it running. Immutable deployment records, logs, typed failure states, and safe rollback are the trust layer under that promise. Breadth such as GitHub App automation, database catalog, PR previews, IaC providers, AI diagnosis, multi-host scheduling, and enterprise controls layers on after this loop is proven.

The research and architecture review converged on this direction:

- Build a self-hosted-first PaaS around a Postgres-backed control plane, durable jobs, and host agents.
- Use OCI images as the deployment artifact.
- Use BuildKit with Dockerfile-first support and Railpack as the default zero-config builder.
- Treat Nixpacks as compatibility/reference because it is in maintenance mode.
- Use Traefik for public ingress, ACME TLS, host routing, WebSockets, target ports, and eventual TCP proxying.
- Use conservative persistent storage: one single-writer volume per stateful service in MVP.
- Model databases as template-backed services with volumes, generated credentials, private networking, optional TCP proxy, and backups.
- Make deployments immutable records with source, image digest, resolved config, resolved variables, and status history.
- Make Kubernetes a later runtime adapter, not the default installation requirement.
- Do not include billing, full multi-region, advanced autoscaling, marketplace, or public multi-tenant hosting in MVP.

The working codename in this document is `Midnight`. The final project name should avoid Railway's trademark.

## Second Review Locked Decisions

The Linux and AI review pass tightened several previously open decisions:

- Runtime v1 uses Docker Engine through the Docker Engine API. The agent must not shell out to the Docker CLI for normal operation. containerd is a later adapter.
- Ingress v1 uses Traefik because the Railway-like product model includes dynamic HTTP routing, ACME, and eventual TCP proxying for databases.
- Tier 1 Linux support starts with Ubuntu 24.04 LTS x86_64 and Debian 12 x86_64. Other distros are experimental until CI and packaging prove them.
- MVP packaging uses signed `.deb` packages and systemd units. A shell installer may bootstrap the APT repository, but must not become the package manager.
- MVP runtime profile is trusted self-hosted rootful Docker with hardened defaults. It is not safe public multi-tenancy.
- Database backups must be database-native by default. Generic volume snapshots are allowed only as crash-consistent generic volume backups or as coordinated storage-provider optimizations.
- AI features are disabled by default. The platform should be assistant-ready early through structured diagnostics, redaction, semantic action schemas, and audit events, but mutating agentic actions must wait for policy, approval, and eval guardrails.

## Enterprise And Automation Review Locked Decisions

The enterprise and automation review pass added one major constraint: Midnight should not market MVP as enterprise-ready, but the MVP control plane must be shaped so enterprise and automation features are not painful retrofits.

- MVP remains self-hosted-first and single-node-first, but must include enterprise-aware foundations: central deny-by-default authorization, ownership metadata, quotas, audit export, retention policy, signed packages, SBOM/provenance, and vulnerability disclosure.
- Local bootstrap admin plus scoped API tokens are Alpha foundations. OIDC/OAuth login, custom CA/proxy support, external OCI registry auth, signed webhooks, and OpenTelemetry/log export primitives move to MVP Beta or v1 Foundations based on design-partner need.
- SAML, SCIM, LDAP/AD, BYOK, formal SOC 2/ISO evidence automation, native SIEM connectors, native ITSM integrations, and enterprise support SLAs are later enterprise-track features.
- REST/OpenAPI must be provider-grade from day one: stable resource IDs, cursor pagination, idempotency keys, operation resources, scoped tokens, rate limits, versioned JSON, generated SDK readiness, and contract tests.
- IaC support is a control-plane design constraint, not a later integration. MVP needs declarative resources, `spec`/`status` separation, plan/dry-run JSON, drift detection, import/export, and non-secret state export. Terraform/OpenTofu provider can ship after MVP once the API is stable.
- CI/CD automation needs a minimal spine in MVP: GitHub App, signed webhook ingestion, required status checks, environment locks, artifact promotion records, fork PR secret isolation, and generic deploy API for GitLab/Jenkins/Azure DevOps.
- Governance primitives must exist early: production environment protection, approval records, minimal built-in policies, quota policies, usage/showback ledger, audit export, retention policies, and template review metadata.
- Operations automation must be typed and auditable: runbooks, webhook delivery history, dead-letter events, explicit health semantics, bounded self-healing, remediation safety, post-install smoke tests, upgrade tests, and failure drills.

## Mid-Level Challenge And Senior Panel Locked Decisions

The mid-level challenge review found that the document was strong as an architecture reference but still too broad to become an executable project plan. The senior panel agreed on these corrections:

- Split scope into `Phase 0 Alpha`, `MVP Beta`, and `v1 Foundations`. Do not treat the full enterprise-aware architecture as the first build target.
- Phase 0 Alpha proves one vertical slice: install on one VM, deploy one stateless web app from an existing OCI image or Dockerfile, route a generated HTTPS domain, keep it running, and use logs/timeline/failure reason plus rollback when recovery is needed.
- Midnight's first product loop is getting an app live with HTTPS on self-hosted infrastructure and keeping it running. Deployment comprehension and control-plane correctness are the trust layer, not the headline; Coolify is broader, Dokku is simpler, and CapRover is easier to bootstrap.
- Phase 0 uses `/api/v1alpha`, not stable `/api/v1`. Stable v1 waits until compatibility gates, schema migration policy, and contract tests exist.
- Phase 0 schema must be small. Future enterprise, AI, SCIM, evidence, plugin, and IaC tables are domain-map placeholders, not Alpha tables.
- Operation, job, deployment, and idempotency semantics must be explicit before implementation starts.
- Public deployment state should be small and user-meaningful. Internal phases/events can be detailed, but only guarded transition functions can move deployment state.
- Rootful Docker is accepted only for trusted self-hosted code. It is not a malicious-tenant or public multi-tenant boundary.
- Traefik must use Midnight-generated dynamic file provider config by default, not the Docker provider or tenant labels, and must not mount the Docker socket.
- BuildKit/Railpack must be pinned and version-recorded. Alpha uses local per-service cache only; no remote shared cache by default.
- Alpha should not ship PR previews, mutating AI, Terraform/Pulumi providers, native GitLab/Jenkins/Azure adapters, TCP proxy, scheduled backups, or multiple database templates.
- Postgres is the only official Alpha database template candidate, and only if backup/restore gates pass. MySQL/Redis/Mongo require separate v1 gates.
- Every milestone needs acceptance tests: happy path, failure path, idempotent retry where applicable, observable CLI/API status, and supportability artifacts.

## Eight-Specialist Panel Locked Decisions

A four-pass panel of eight specialist reviewers (build/supply-chain, Linux runtime/kernel, distributed systems/control-plane correctness, security/trust, storage/backup, networking/ingress/TLS, API/IaC contract, release/SRE/operations) audited the full document, argued conflicts, and reached consensus on the following locked decisions. These supersede earlier text where they conflict.

Build plane and supply chain:

- Rollback always pulls the previously built image digest; rollback never rebuilds. If the digest is unretrievable, rollback fails with reason code `ROLLBACK_IMAGE_UNAVAILABLE`.
- Deployment snapshots store: source SHA, builder type/version/digest, **resolved Railpack/Nixpacks build plan JSON**, **resolved base image digests** for every `FROM`, and the produced image digest. Rollback replays the recorded plan; it does not re-invoke Railpack.
- External-registry digests are probed daily; missing digests for protected deployments emit `EXTERNAL_DIGEST_MISSING`. Production environments default to mirroring active and last-N rollback digests into the embedded registry as a safety net.
- The runtime agent verifies image signatures (cosign or equivalent) before pull/start in the default profile. Mismatch produces `IMAGE_SIGNATURE_INVALID`. SLSA-style provenance is emitted as in-toto SLSA v1.0 statements; no SLSA Build Level is claimed until audited.
- Multi-arch builds are opt-in via an explicit `platforms:` service field; produce an OCI image index. The scheduler matches host arch against the manifest list and emits `IMAGE_ARCH_UNAVAILABLE` on mismatch. Cross-arch via QEMU is documented as slow and not recommended for production.
- Build defaults: 30-minute timeout (configurable, max 4h); per-host build concurrency `min(4, cores/2)`; per-project concurrency 2; on disk-pressure critical, in-flight builds run to completion or 10 minutes and BuildKit cache is the first reclaim target before any registry GC.
- Untrusted-build classification is explicit: any commit whose author is not a workspace member, any push from a fork, or any branch outside the trusted-branch allowlist. Untrusted builds run with no project secrets, no shared cache read or write (ephemeral per-build cache only), and push to a quarantine path in the registry. Untrusted digests cannot promote to non-preview environments without explicit approval.
- Co-located BuildKit in Alpha runs as a dedicated unprivileged user with a CPU/memory cgroup cap, dedicated tmpfs, and no read access to `/var/lib/midnight/{db,secrets,registry/data,kms}`. Rootless BuildKit is the v1-GA blocker.

Linux runtime and host:

- `cgroup v2` is hard-required (not "preferred"). Preflight refuses install on v1 with explicit `systemd.unified_cgroup_hierarchy=1` remediation.
- Required `daemon.json` baseline, written or merged by the installer: `live-restore: true`, `userland-proxy: false`, `iptables: true`, `no-new-privileges: true`, `storage-driver: overlay2`, explicit `default-address-pools` with conflict checking, `log-driver: json-file` with size/file caps. Installer refuses to proceed on conflict.
- Docker Engine API floor: v1.43 minimum (Engine ≥ 24.0); the agent refuses to start with `DOCKER_ENGINE_UNSUPPORTED` outside the supported range. The compatibility matrix is a published artifact, not a promise — `/docs/compat/docker.md` and CI-gated.
- Default container capability allowlist (added back after drop-all) for compatibility with common database/runtime images: `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `FSETID`, `KILL`, `SETGID`, `SETUID`, `NET_BIND_SERVICE`, `SETPCAP`, `SYS_CHROOT`. Templates declare additional caps via an operator-controlled allowlist.
- AppArmor: Midnight ships an `midnight-default` profile in the `.deb` with parser hooks; falls back to `docker-default` if AppArmor is unavailable. SELinux is promoted from "experimental" to Tier 2 supported in v1 (with `:z` pass-through on platform-managed mounts).
- `HostConfig.Init = true` (tini) is the default for all platform-managed containers to handle PID-1 zombie reaping and signal forwarding. Default ulimits are explicit (`nofile=65536:1048576`, `nproc=<pids-limit>`).
- systemd unit contract: every platform unit has `Restart=on-failure`, `RestartSec=5`, `StartLimitBurst=5`, `StartLimitIntervalSec=300`, `OOMScoreAdjust=-900` for the agent and server. Agent does not crash-loop on Docker absence; it enters `AGENT_RUNTIME_UNAVAILABLE` with backoff. systemd unit dependency DAG is pinned (`After=docker.service postgresql.service network-online.target`).
- journald drop-in shipped: `RateLimitBurst`, `SystemMaxUse`, `SystemKeepFree`. Tenant logs route through Docker `json-file` (not journald) as the source of truth.
- Firewall integration is active, not advisory: preflight detects `iptables-nft` vs `iptables-legacy` mismatch with Docker's effective backend and refuses install on mismatch; UFW detection installs an `/etc/ufw/after.rules` `DOCKER-USER` integration so UFW rules actually apply to tenant traffic. Midnight rules are appended after a sentinel comment line, idempotently reconciled.
- Time sync is mandatory: installer ensures `chrony` or `systemd-timesyncd` is enabled and synchronizing; agent reports clock offset in heartbeat and refuses ACME ops if offset > 5s. Control plane refuses agent registration with `AGENT_CLOCK_SKEW` if `|agent_clock - server_clock| > 30s`.
- IPv6 dual-stack: if the operator advertises AAAA on the apps-domain, installer enables `ipv6: true`, `ip6tables: true`, and `fixed-cidr-v6` in `daemon.json`. Otherwise AAAA is preflight-blocked.

Distributed systems and control-plane correctness:

- Postgres job queue contract is normative: columns include `lease_owner`, `lease_expires_at`, `heartbeat_at`, `attempt`, `available_at`, `priority`, `queue`, `partition_key`. Dequeue uses `SELECT … FOR UPDATE SKIP LOCKED` ordered by `(priority, available_at)` filtered by `queue` and lease availability. Heartbeat cadence = `lease_ttl / 3`; expired leases are reclaimed automatically. Separate per-class queues with independent worker pools and bounded concurrency: `build`, `deploy`, `reconcile`, `webhook`, `cleanup`. Per-tenant fairness via weighted-fair dequeue.
- High-write tables (`events`, `audit_logs`, `jobs`, `host_heartbeats`, `deployment_instances`) are monthly range-partitioned with daily pruning workers. Autovacuum tuned per-table (`autovacuum_vacuum_scale_factor=0.02`, fillfactor 80) for the queue.
- Atomic single-flight per (service, environment): every deployment-mutation path acquires `pg_advisory_xact_lock(hashtext('svc:'||service_id||':'||env_id))` or updates a `service_environment_state` row with optimistic concurrency. `runLatest` arbitration uses a monotonic deployment sequence (`bigserial`), never wall-clock time.
- Deployment state transition matrix is normative and ships in the doc (rows = from-state, columns = to-state, cells = guard). Terminal states are absorbing except `ACTIVE → SUPERSEDED` and `ACTIVE → REMOVING`. `CANCELING` may transition to `CANCELED` (clean) or `FAILED` (cancel itself failed) with reason code.
- Promotion authority is named: a "Deployment reconciler" worker class joins `deployment_instances.ready_count >= minReady` and `routes.target_ready_at IS NOT NULL` for the candidate, then performs guarded `UPDATE deployments SET state='ACTIVE' WHERE id=$id AND state='DEPLOYING' AND resource_version=$rv`.
- Idempotency state matrix on failure paths is explicit: `{first request still pending, succeeded, failed-terminal, key reserved before body store, key TTL expired}` × `{same body, different body}`, each cell defines the response. `idempotency_key.expiry >= operation.expiry`; operation GC is forbidden before key GC. In-flight duplicate during reservation returns `425` or a typed `409 IDEMPOTENCY_REQUEST_IN_PROGRESS`.
- `applyChangeSet` is atomic at the database level: all `If-Match` checks evaluated together; first conflict returns 409 with the conflicting resource list and nothing applied. Side effects (deploys, DNS, ACME) enqueue via the transactional outbox pattern within the same transaction.
- Cancellation is cooperative at named safe points only. Cancellation between route-add (step 6) and drain-completion (step 9) is rejected with `CANCEL_NOT_SAFE`; only rollback is permitted in that window. ACME challenges always run to completion or `revoke`.
- LOST instance handling: route membership requires `instance.last_observed_at > now() - route_freshness_ttl` (default 30s); below that, the instance is removed from the route but the deployment stays `ACTIVE` if other instances remain. Zero ready instances for `degraded_grace > 0` produces a documented degraded sub-state or `FAILED` with `HOST_UNAVAILABLE`. Re-add requires N consecutive healthy heartbeats (hysteresis).
- Reconcile-storm guard after control-plane outage: agent reconnect uses jittered exponential backoff with randomized window; control plane runs in throttled "warmup" mode for N seconds after start, allowing only Class-A remediation; webhook redelivery uses token-bucket per endpoint.
- Deployment snapshot is fully self-contained: resolved variable values are deep-copied (sensitive values stored as references to a content-addressed secret store with reference counting). `deployment_image_pins` reference-counting table is consulted by registry GC; pinned digests are unreclaimable while pin count > 0 or within the rollback window.

Security, secrets, and trust boundary:

- Bootstrap admin token is generated on first start of `midnight-server` and written to `0600 root:root /var/lib/midnight/bootstrap-token`. `midnight admin show-bootstrap` is one-shot and deletes the file on read. Mandatory password+TOTP set on first login. The bootstrap path auto-disables once a normal admin exists.
- API tokens use the format `midn_<class>_<26-char base32-256bit>` with public 6-char prefix surfaced in UI/audit and stored as `sha256(token)`. CLI auto-revokes tokens unused for N days. Submit prefix to GitHub Secret Scanning Partner Program in v1.
- KEK at `/etc/midnight/secrets.key` has mode `0400 midnight:midnight`. Rotation generates KEK_n+1, rewraps all DEKs in a background `operations` job with progress, retains KEK_n for `secret_kek_grace_days` (default 30). KEK loss is irrecoverable and documented explicitly. Operators MUST back the KEK up separately from Postgres backups (different repository, different passphrase). KEK loss is a tested failure drill.
- Reference variable resolver enforces cross-scope authorization: the actor creating a reference must have read access on the referenced variable's scope at creation time, and the consuming service's deploy principal must have read access at every resolution. Cross-environment-tier references (dev→prod or prod→preview) are denied unless explicit policy allows.
- Sealed variables ship in MVP Beta with precise spec: no API path returns plaintext after create; rotation requires a new value, not view-then-edit; CLI has no `--reveal` for sealed; UI shows last-rotated and length-bucket only; audit/snapshot exports elide plaintext.
- Audit log uses per-row hash chaining at MVP: `prev_hash` and `row_hash = H(prev_hash || canonical_row)`, rooted in a daily `audit_checkpoint` row signed with the Midnight release key or operator-configured Ed25519 key. `midnight audit verify` CLI ships in MVP. JSONL export includes `prev_hash`, `row_hash`, `checkpoint_id`.
- Authentication transport is normative: dashboard uses `__Host-` prefixed Secure/HttpOnly/SameSite=Lax cookies holding a 15-minute opaque session id backed by Postgres; CSRF via double-submit + Origin header check; CLI/API uses `Authorization: Bearer <midn_*>`; OIDC uses PKCE+nonce+state. JWTs are explicitly avoided unless specified. Logout-everywhere rotates `session_generation`.
- Dashboard browser headers: `Strict-Transport-Security: max-age=63072000; includeSubDomains; preload`, CSP `default-src 'self'; script-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'`, `X-Content-Type-Options: nosniff`, COOP=same-origin, minimal Permissions-Policy.
- SSRF-safe outbound HTTP client: a single shared client used by webhook delivery, git/registry/AI providers, and ACME, denying RFC1918, CGNAT, link-local, loopback, multicast, and cloud metadata endpoints (169.254.169.254, fd00:ec2::254). Hostname resolved once; connection pinned to resolved IP to prevent DNS rebinding. Test fixtures are required.
- Outbound webhook signing: `Midnight-Signature: t=<unix>,v1=<hex>,kid=<key_id>`, replay window 300s, two active signing keys per endpoint with 30-day rotation. Canonical signing input = `f"{timestamp}.{raw_body}"`. Multi-`v1` accepted during rotation overlap.
- AI redaction is a multi-pass pipeline with named passes: (1) byte-substring match against the in-memory secret bloom filter with verifying compare; (2) regex pack for AWS/GCP/Azure tokens, JWT, `-----BEGIN`, Slack/GitHub token shapes, base64 entropy >32; (3) `KEY=val` capture against the secret-name table; (4) URL credentials `://user:pass@`; (5) `Authorization` headers. Release gate: `secret_leakage_rate == 0` on the fixture set.
- MCP server contract: clients use service-account tokens with `mcp:tools` plus per-tool scopes (`mcp:explainDeploymentFailure`, `mcp:proposeAction`, …). Mutating tool calls require mandatory `Idempotency-Key`. Class 3+ tools require out-of-band human approval through the dashboard, never inline in chat. Per-MCP-client rate limit and monthly model-cost budget enforced.
- `BUILD_ROOTFUL` warning is surfaced by `midnight doctor` whenever co-located rootful BuildKit is detected. Granting `Developer`/`Deployer` roles in Alpha requires explicit operator acknowledgement that those roles are host-root-equivalent.
- `publicClientVars` is the only variable class that populates `process.env` during build alongside `buildVars`. A built-in static scan of build output for known secret prefixes (`midn_`, `AKIA`, `xoxb-`, JWT shape) fails the build with `BUILD_OUTPUT_SECRET_LEAK`.
- Backup repositories MUST be encrypted (restic password required); password sealed by the platform KMS. Backup of the secret store + KEK uses a separate operator-supplied passphrase to break the dependency cycle. Restore tests include a rotated-password fixture.
- `midnight shell`/`ssh` sessions are recorded as asciicast-equivalent streams stored with audit retention, surfaced in the deployment timeline, redacted by the same pipeline; cannot be disabled. Production-tier sessions require two-person approval.
- Postgres role separation: `midnight_app` (CRUD only), `midnight_migrate` (DDL, migrations only), `midnight_audit_ro` (SELECT on audit/event tables). All roles have `statement_timeout = 30s` (audit_ro 5min) and `idle_in_transaction_session_timeout = 60s`. `pg_hba.conf` restricts to `127.0.0.1` and the agent network.
- Midnight's own supply chain: lockfiles required; CI runs `govulncheck`, `osv-scanner`, `npm audit --omit=dev` as required checks; PRs from forks do not get repo secrets and do not push images; release artifacts built only from protected branches; npm packages pinned by integrity hash.

Storage, registry, and backup:

- Default scheduled backup policy is enabled at template instantiation (Alpha default: daily, 7-day retention). A service whose policy is disabled or has no successful backup in the last 2× period transitions to `BACKUP_STALE`. RPO is reported per service as `time_since_last_successful_backup`.
- Postgres logical backup uses `pg_dumpall --globals-only` plus `pg_dump --format=custom --blobs --no-owner` per database. Restore runs globals first, then `pg_restore -j <n>` against a fresh same-major cluster. Databases > 50 GB are flagged `BACKUP_SIZE_DEGRADED` and require the v1 physical-backup path.
- Major-version Postgres upgrades use logical dump-and-restore into a new replacement volume on the target image. Physical backups (`pg_basebackup`) restore only to the same major version; metadata records `restore_compatible_engine_versions`. In-place `pg_upgrade` is out of scope for MVP and v1.
- Volume directories owned by `root:midnight`, mode 0700, until first mount. On first mount the agent reads the template's expected container UID/GID and `chown`s appropriately, computing mapped host UID via `/etc/subuid` if `userns-remap` is enabled. UID conflict produces `OWNERSHIP_CONFLICT` and blocks mount.
- Each project gets its own restic repository under `/var/lib/midnight/backups/<project_id>/`. Repository password is generated per project, sealed by the platform KMS, and exported as part of project export/DR bundles. Cross-project dedupe is explicitly out of scope. Repo lock is single-writer; concurrent project backups queue.
- Off-host backup: at least one external destination (`s3://`, `gcs://`, `sftp://`, or rsync target) is configurable in MVP Beta; `BACKUP_OFFHOST_MISSING` warning fires from `doctor` when none is configured. Alpha may be local-only with explicit warning.
- Each new backup configuration runs a one-time restore-into-throwaway-volume + template smoke check before being marked `verified`. Backups whose verification fails surface `BACKUP_UNVERIFIED`. v1 runs a weekly sample restore for at least one backup per service.
- Registry: embedded registry retains the last 10 image tags per service plus all digests in active rollback windows and protected snapshots. A reference-count + tag-retention sweep runs daily; full destructive blob GC remains opt-in with quiesce/lock/dry-run/post-GC verify. `midnight registry gc --dry-run|--apply` ships in Phase 0.
- Disk pressure defaults: warn at 75%, degrade (block new builds) at 85%, block (refuse new deployments and volumes) at 92%. Inode thresholds: warn at 70%, block at 90%.
- Volume capacity enforced via XFS or ext4 project quotas (`prjquota` mount option required); BuildKit cache and registry blobs live on quota-isolated subtrees so platform churn cannot starve user volumes. Volumes can grow online; shrink is unsupported (`RESIZE_SHRINK_UNSUPPORTED`).
- Control-plane Postgres has its own `pg_basebackup` + WAL archive sidecar to a separate physical destination configured at install. Loss triggers `PLATFORM_DEGRADED` with a documented offline recovery runbook; user data planes continue serving traffic in read-only mode while the control plane is down.
- Deleted volumes are soft-deleted with a 7-day grace period (Alpha default, configurable down to 24h, never below). Hard deletion is logged.
- MongoDB v1 template is single-node only; sharded and multi-member replicated MongoDB are rejected at template validation. Postgres v1 WAL archiving is a per-service sidecar with `WAL_ARCHIVE_LAGGING` degradation state.

Networking, ingress, ACME, and DNS:

- DNS-01 + wildcard certificates for the platform `apps-domain` are Alpha-blocking, not deferred. HTTP-01-only generated domains hit Let's Encrypt 50-cert/registered-domain/week within tens of services. A typed `DnsProvider` interface (`presentTxt`, `cleanupTxt`, `getZone`, `propagationCheck`) ships in Alpha with at least Cloudflare, Route53, and `manual` providers; credentials live in the platform secret store.
- ACME state (`acme.json`) is part of the platform backup set; restore restores it byte-exact with `0600` ownership before Traefik starts. Cold-start without prior ACME state batches issuance at ≤ N requests/min with jittered retry, hard-caps at 40 issuances/registered-domain/week to leave headroom under the 50/week LE limit, and auto-falls back to LE staging if the budget is exceeded.
- Installer runs an external reachability check on :80 and :443 via an Midnight-hosted prober before declaring ACME ready. Unreachability gates ACME issuance and surfaces `INGRESS_PORT80_UNREACHABLE` with DNS-01 fallback guidance and an example coexistence snippet for upstream nginx/Apache.
- CAA preflight: lifecycle resolves CAA on the registered domain and parents before submitting to ACME. Refusal surfaces remediation, never burns a failed-validation budget.
- `advertisedIngressAddresses` is a versioned platform setting. Apex bindings store the address set used at verification. A periodic re-verification job detects A/AAAA drift; an IP-change runbook notifies affected customers and forces re-verification. Cloudflare CNAME flattening is recommended for apex.
- Wildcard custom domains (`*.example.com`) require DNS-01, ownership TXT at `_midnight-verify.<apex>`, more-specific subdomain wins over wildcard at routing, and is documented explicitly.
- Dynamic Traefik config files are written via temp-file + atomic rename on the same filesystem; never truncated in place. Midnight reserves `/.well-known/acme-challenge/*` at maximum router priority on every entrypoint serving :80; user routes claiming this path are rejected at validation.
- Ingress defaults are pinned (table to ship in the doc, not "configurable"): `maxRequestBodyBytes` (default 32 MiB), `idleTimeout` (180s), `readTimeout`/`writeTimeout` (60s), `webSocketIdleTimeout` (15m), trust-proxy hop count (1), forwarded header set (`X-Forwarded-For`, `X-Forwarded-Proto`, `X-Forwarded-Host`, `X-Real-IP`, `X-Request-Id`), h2c upstream opt-in for gRPC.
- Healthcheck taxonomy is three probes — `startup`, `readiness`, `liveness` — each with its own timeout/retry budget. `ECONNREFUSED` during startup is "still starting," not a failure. Default `startupTimeout >= 180s` per service with override; only readiness failures gate route health.
- Egress MVP fallback for stable outbound IP: services flagged `egress: stable-ip` route through an Midnight egress-proxy systemd unit with a documented IP. SMTP outbound (25/465/587) is blocked by default via `nftables` rules in `DOCKER-USER` with per-workspace allowlist. Conntrack table size and `nf_conntrack_max` tuning are applied by the installer; conntrack pressure is a required metric, not "where available."
- Internal DNS: agent runs a periodic invariant check that every Docker bridge's connected containers share one `(project_id, environment_id)`; failure raises a P1 drift alert. Service slugs must be RFC-1035 valid (rejected at create). Internal DNS TTL = 5s with stale-on-not-ready removal.
- Phase 3 multi-host (WireGuard) ships with PMTU probe at peer-up, conservative default MTU 1380 with per-link override, scheduled key rotation with overlap window, peer-add/remove FSM that quarantines IPAM until heartbeat, and conntrack flush on peer remove.
- Traefik Prometheus endpoint enabled by default on a control-plane-only listener with a fixed metric set (`traefik_router_requests_total`, `_request_duration_seconds_bucket`, `traefik_service_open_connections`, `traefik_config_reloads_total`, `acme_certificates_total`).

API, IaC, and DX contract:

- Resource ID format is normative: `<prefix>_<26-char Crockford base32 ULID>` (or UUIDv7) for time-sortability. Reserved prefixes: `prj`, `env`, `svc`, `dep`, `op`, `job`, `dom`, `vol`, `var`, `tok`, `usr`, `sa`, `tpl`, `evt`, `idem`, `plan`, `inst`. Total length ≤ 32. Clients must not parse the prefix for type checks; use `kind`.
- Cursor pagination: opaque base64url, server-signed (HMAC); tampering returns `400 INVALID_CURSOR`. `next_cursor: null` denotes end. `limit` defaults 50, max 200. No `total_count`. Cursors stable for ≥ 1 hour. Forward-only in v1.
- v1 graduation rubric is concrete and binding: (a) zero `oasdiff` breaking-change diff between two consecutive minors for ≥ 90 days; (b) ≥ 95% endpoint coverage by Schemathesis property tests; (c) green Pact contracts for CLI, dashboard, Terraform provider, MCP; (d) deprecation header machinery (RFC 8594 `Sunset`, `Deprecation`); (e) two consecutive SDK releases generated without manual patches; (f) at least one external IaC consumer integration in production.
- Idempotency-Key: scope = `(token_id, method, path_template)`. TTL 24h. Required on `POST/PATCH/DELETE`. Body hash = SHA-256 over RFC 8785 canonical JSON. Replay returns identical body and `Idempotent-Replayed: true`. Concurrent in-flight returns `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` with `Retry-After`.
- Mutating async endpoints return `202 Accepted` with `Location: /api/v1/operations/{op_id}` and the full Operation in the body. Polling: `GET /operations/{id}` with `Retry-After` integer-seconds and adaptive backoff hint (1s → 30s). Streaming default is SSE at `GET /operations/{id}/events`; reconnect via `Last-Event-ID`. WebSocket is documented as alternate at `/v1/ws` for log/exec multiplexing only.
- Error code registry ships at `docs/error-codes.md` as a flat `SCREAMING_SNAKE_CASE` registry with `code`, HTTP status, retriable, terminal, description, fix hint. Codes are additive-only; renames require a v-bump. New codes added in this review are listed under the locked-decisions appendix and integrated into the existing failure-code list.
- Auth header is `Authorization: Bearer <token>` only (never `X-Midnight-Token`). Service-account tokens support two simultaneously valid tokens for ≥ 24h overlap during rotation. CLI login uses OAuth 2.0 device authorization grant (RFC 8628) for headless and PKCE for workstations. Tokens go to OS keychain, not `.midnight/`.
- Plan JSONSchema is a normative artifact at `schemas/plan/v1.json` with `formatVersion: "1.0"` and a stability promise. A "Provider Resource Matrix" table — required/optional/computed/sensitive/write-only fields, ForceNew triggers, import ID format, refresh endpoint, drift fields — ships per resource (`midnight_project`, `midnight_environment`, `midnight_service`, `midnight_domain`, `midnight_variable`, `midnight_volume`, `midnight_template_instance`).
- CloudEvents binding is pinned: `specversion: 1.0`; `source: //midnight/<instance_id>/projects/<prj_id>`; `subject: <kind>/<id>`; `type: dev.midnight.<resource>.<verb>`; `datacontenttype: application/json`; `dataschema` URLs at `/schemas/events/<type>/<version>.json`; required CE extensions `traceparent`, `midnightrequestid`, `midnighttenantid`. The event catalog (event type → schema → since-version) ships in MVP; AsyncAPI is generated from the same registry.
- Default rate-limit budgets are pinned in the doc, not "configurable": user 600 req/min, service-account 1200, CLI 600, MCP 300, webhook receiver 6000, Git provider 1200, agent 12000. Two-tier `min(per-tenant, per-source-IP)`. `Retry-After` always integer seconds. Headers: `RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset`.
- API versioning is URL-path only (`/api/v1alpha`, `/api/v1`). Media-type and header versioning are explicitly out of scope. Field naming: API JSON = snake_case (already implied by `request_id`, `next_cursor`); YAML config = camelCase. SDKs translate at the boundary.
- `X-Request-Id` header is always emitted equal to body `request_id`. Inbound `Traceparent`/`Tracestate` (W3C) accepted; client-supplied `X-Request-Id` echoed if well-formed. `request_id` carried into audit, operation, and webhook events for correlation.
- CLI command reference is normative and ships in Phase 0:
  - `midnight logs <service> [--deployment ID] [--since 1h] [--tail 200] [--follow] [--json] [--grep PATTERN]`. NDJSON line shape `{ts, stream, level, msg, attrs}`. SSE pagination via `Last-Event-ID`.
  - `midnight deploy` produces a deterministic tar.zst, honors `.dockerignore` then `.midnightignore`, signs payload, performs S3-style multipart upload with resume; default cap 2 GiB; returns operation_id and prints the operations URL.
  - `midnight rollback [--to DEPLOYMENT_ID | --to-revision N | --previous]`. Default = `--previous` with confirmation prompt unless `--yes`.
  - `midnight doctor` (live diagnostics) is split from `midnight preflight` (install/upgrade preflight). Stable check IDs `OR-CHK-001`+, machine-readable JSON, exit-code table (`0` healthy, `1` warn, `2` fail, `3` config error), `--check=<id>`, `--fix=safe|all`. Adding a check is minor-version; renaming an ID is breaking.
  - `midnight link` in multi-service repos prompts for service or accepts `--service`. `.midnight/` stores project/env/service IDs only; tokens go to the OS keychain. Default `init` adds `.midnight/` to `.gitignore` (although it is safe to commit).
- Config-as-code: `schemaVersion` follows its own track (`midnight.dev/v1alpha1` → `…/v1`) with a compatibility table to API versions. `midnight config validate` runs gitleaks-style secret scanning and rejects on findings unless `--allow-inline-secret`. Take-back UX is via `midnight config release <field-path>` or removing the field with documented `unsetPolicy`.

Release, install, and day-2 operations:

- Phase 0 install criterion is split into two numbers: (a) Local mode (`--profile=local` using `*.midnight.local` + self-signed TLS) — fresh-VM-to-deployed-app under 15 minutes, no external DNS; (b) Public mode — under 15 minutes excluding DNS propagation and ACME issuance. A typical end-to-end including DNS is documented as 30–60 min.
- Upgrade is a transaction: `midnight upgrade` takes a pre-upgrade snapshot tag, runs migrations in advisory-locked txns, executes a smoke probe with timeout, and on failure auto-invokes `midnight rollback --to-snapshot=<tag>`. Migrations are tagged `reversible|forward-only`; forward-only requires `--accept-irreversible` plus verified pre-upgrade backup. `apt` post-install hooks refuse to run if another upgrade lock is held.
- Skip-version upgrades are unsupported and refused with `UPGRADE_SKIP_FORBIDDEN`. `.deb` `Depends:` and `Breaks:` constraints encode min/max compatible versions across server, worker, agent, BuildKit. `midnight upgrade-plan` resolves the path. Server exposes `/api/v1alpha/compat`; worker/agent registrations outside the window are refused with `VERSION_SKEW_REJECTED`.
- Component compatibility matrix is a published artifact in the doc: row per component (server, worker, agent, BuildKit, Traefik, Docker Engine, Postgres, Railpack), columns for supported range relative to server N and behavior on skew (older = read-only mode; newer = refused).
- Required Alpha failure drills include the original list plus: clock skew >5min, journald wedged, `/var/log` full, Postgres connection exhaustion, network partition (3 durations), BuildKit local-cache corruption, Docker socket EOF, volume read-only remount, KEK loss, ACME state restore at scale.
- Default Alpha alert rules ship as a Prometheus rules file: `DiskPressure`, `AgentLost`, `BackupFailed`, `CertExpiringSoon`, `DeployJobStuck`, `BuildKitCacheCorruption`, `ConntrackPressure`, `BackupOffhostMissing`. Default Grafana dashboard `midnight-overview.json` ships with the `.deb`. Default scrape interval 15s.
- logrotate drop-in `/etc/logrotate.d/midnight` ships in the `.deb` with documented defaults (`100M, 14 days, 7 rotations, copytruncate`).
- `midnight support-bundle` defaults: 24h time window, 200 MB size cap, `--include=logs,metrics,events,config` selectors. Manifest reports `truncated: true` per category.
- Manual runbooks ship as markdown at `/usr/share/midnight/runbooks/*.md` version-pinned to the package. `midnight runbook list/describe` ships in Phase 0; `midnight runbook run` (typed/automated execution model) arrives in Phase 1.
- Telemetry is opt-in only at install time, single endpoint, no project/service names; only counts and reason-code histograms. Payload schema published at `/usr/share/midnight/telemetry-schema.json`.
- `midnight feature {list,disable,enable}` ships in Phase 0 with kill switches for at least `acme.issuance`, `registry.gc`, `ai.gateway`, `auto.remediation`. Status surfaced in `doctor`.
- Versioning policy: semver `MAJOR.MINOR.PATCH` for the Go API surface, packaged binaries, and CLI surface. Data schemas have an independent integer `schema_version` upgraded only by migrations.
- ACME-state restore at scale (step 5 of Platform restore order): restore intact `acme.json` byte-exact; if reissue is required, stage at ≤ 20 certs/hour with priority queue (TLS-expired first, expiring-soon next, healthy last); use LE staging for the first batch; surface progress as `RESTORE_ACME_THROTTLED`, never as failure.
- `midnight uninstall` behavior is enumerated per artifact class: stops units; preserves data paths; removes systemd unit files; removes APT source/keyring only with `--purge-repo`; flushes `midnight-*` iptables chains; removes `midnight_*` Docker networks only if empty; leaves `daemon.json` edits with a marker comment for manual cleanup.

## Parity Review Locked Decisions

A four-reviewer parity pass (product surface, operational/lifecycle, platform/infrastructure, ecosystem/DX) cross-checked the document and the agile bundle against Railway's full public surface area. The findings are consolidated in `agile/parity-review/PARITY_CONSOLIDATED_FIXES.md`. The decisions below are binding additions to scope.

New first-class service types (v1 Foundations):

- **Cron service**. A service whose deployment is invoked on a schedule rather than continuously running. Config fields: `schedule` (cron expression), `timezone`, `timeout`, `retryPolicy {maxAttempts, backoff}`, `maxParallel`, `concurrencyPolicy {allow|forbid|replace}`. Cron service deployments are recorded as immutable deployment snapshots like web services; each invocation is an `Operation` and a `Job`. Failures emit `CRON_INVOCATION_FAILED`, missed-window emits `CRON_WINDOW_MISSED` if `concurrencyPolicy=forbid`. UI surfaces the next-run time and the last N invocations.
- **Static site service**. A service whose build emits a directory of static assets that are served by the platform's static asset server (Traefik file-server or a small Caddy/nginx sidecar). Config fields: `dist` (output directory), `index`, `notFound` (404 path), `redirects[]`, `rewrites[]`, `cacheControl` overrides per pattern. Static deployments do not run a container at runtime; they materialize assets on the host and route from the ingress. Compatible with generated and custom domains.
- **One-off / pre-deploy / run-once job**. A bounded execution of a service's image with a custom command, recorded as a `Deployment` in a new internal phase `PRE_DEPLOY` (and public state `RUNNING_JOB`). Triggered by the `predeploy:` block in `midnight.yaml` (runs to success before the rollout proceeds), by `midnight run --image <digest> -- <cmd>` (one-off), or by API. Pre-deploy job failure blocks the dependent deployment and emits `PREDEPLOY_FAILED`. Pre-deploy jobs do not mount runtime volumes by default but may opt-in to a database service's runtime variables for migrations.

CLI parity verbs (Phase 0–MVP Beta):

- The CLI command surface is expanded to cover every Railway CLI verb that maps to a Midnight capability. New or clarified verbs:
  - `midnight up` — bootstrap-and-deploy. Creates project/service if absent, links the working directory, uploads source, and triggers a deploy. Phase 0 ships this verb as the first-impression flow.
  - `midnight run -- <command>` — runs a one-off/pre-deploy job using the current service image. Replaces the earlier "local-only" framing; the CLI prints the chosen image digest and remote/local execution mode before acting.
  - `midnight connect <service>` — port-forward from local to a private-network service target.
  - `midnight forward` — alias of `connect` for symmetry; spec narrowed to "port-forward only", not shell.
  - `midnight add <template>` — instantiate a template into the current project.
  - `midnight redeploy [--deployment ID]` — redeploys an existing deployment by ID, no source change.
  - `midnight restart <service>` — restarts running instances without creating a new deployment.
  - `midnight open [resource]` — opens the dashboard URL for the resource in a browser.
  - `midnight whoami` — prints the authenticated principal and effective scopes.
  - `midnight logout` — revokes the local CLI token and clears the keychain entry.
  - `midnight version` — prints client + server compatibility tuple.
  - `midnight down [--service]` — stops a service or removes a deployment with confirmation.
  - `midnight unlink` — disassociates the working directory from a project.
  - `midnight docs [topic]` — opens the docs site to the relevant page.
  - `midnight completion <shell>` — emits shell completion script.
- All new verbs follow the existing CLI rules (read commands `--json`, mutating support `--dry-run`, non-interactive in CI, agent-facing JSON returns stable IDs and reason codes).

Variable reference templating syntax:

- The reference syntax is `${{ <scope>.<NAME> }}`. Scopes: `<service-name>` (within the same project/environment), `shared`, `system`, `secret`. Examples: `${{ postgres.DATABASE_URL }}`, `${{ shared.API_BASE }}`, `${{ system.MIDNIGHT_PUBLIC_DOMAIN }}`.
- References create explicit edges in `variable_references` and are evaluated at deployment-snapshot resolution. Unresolved references fail preflight with `VARIABLE_REFERENCE_FAILED`. Cycles fail with `VARIABLE_REFERENCE_CYCLE`. Cross-environment-tier references (dev→prod, prod→preview) are denied unless explicit policy allows.
- Templating is closed: literal `${{` is escaped as `$${{`; no command execution; no string interpolation outside the reference grammar. The grammar ships as a normative artifact in `engineering/contracts/VARIABLE_REFERENCE_SYNTAX.md`.

Generated-domain and custom-domain UX additions:

- Per-domain options surfaced on the route: `forceHttps` (default true), `redirectWww` (default off), `byoCertificate` (custom domains only — operator-uploaded TLS cert + key + intermediate chain stored encrypted under the platform KMS, with expiry tracking and pre-expiry alerts).
- BYO certificate path is required for installs that cannot use Let's Encrypt (air-gapped, regulated environments). Renewal becomes the operator's responsibility; the platform tracks `expires_at` and emits `BYO_CERT_EXPIRING` warnings.
- Domain options are exposed via `midnight domain set <hostname> --force-https=true --redirect-www=true --cert <path>`.

User-facing audit log:

- Workspace owners and project admins gain a user-facing audit-log view in the dashboard (separate from the operator audit). Filters: actor, action, resource, time range, environment-tier. JSONL export available to admins. The hash chain ships at MVP per panel; the user-facing surface lands in v1 Foundations.

Replicas semantics (MVP Beta):

- Service `replicas:` is accepted in `midnight.yaml` but constrained: `replicas > 1` is rejected with `REPLICAS_VOLUME_CONFLICT` if any volume is attached, with `REPLICAS_NOT_SCHEDULABLE` on single-host installs that lack capacity, and with `REPLICAS_REQUIRES_MULTIHOST` until Phase 3. Stateless web services may set `replicas: N` (N ≤ host capacity) in MVP Beta on a single host (multiple containers behind the same Traefik service).
- Per-replica system variable `MIDNIGHT_REPLICA_ID` is set; routing across replicas is round-robin by default. Sticky sessions, preferred zones, and weighted routing are deferred to Phase 3.

TCP proxy phase placement (corrected):

- TCP proxy is **late MVP Beta** (single-host), not Phase 3 and not Alpha. Required for public Postgres/MySQL/Redis/Mongo access on database templates. Disabled by default per template; explicit operator approval required to expose. Allocates a generated host port, maps to the service target port. Multi-host TCP proxy is Phase 3.

Templates ecosystem:

- A separate `midnightapp-templates` GitHub organization hosts official templates. Each template ships with `template.yaml` (declares services, variables, volumes, domains), an SBOM-reviewed pinned image set, a security-review status field, and a license declaration.
- "Deploy on Midnight" button URL scheme: `https://<dashboard>/new/template?source=<repo-url>&ref=<sha>`. Renders an instantiation flow with parameter binding.
- Template marketplace UI lands in Phase 2; v1 ships at least Postgres + MySQL + Redis + static-site + Node.js + Go + Python templates after each passes the panel-locked support gates.
- A template-best-practices repository (`midnightapp/template-best-practices`) ships in v1 documenting the review checklist.

Migration assistant additions:

- The migration assistant list (currently Compose / Heroku / Render / Fly / Vercel/Netlify / Kubernetes) gains **`railway.json` / `railway.toml`** as the highest-priority source — Railway is the most likely inbound migration path.

Notifications shipping plan:

- v1 Foundations ships native adapters for **Slack incoming webhook** and **email (SMTP)**. PagerDuty, Discord, Teams, Microsoft Teams Incoming Webhook are Phase 2 native adapters. Generic signed webhooks remain the Day-0 escape hatch.

External-registry image-pull credentials:

- Services may declare `registryAuth: <secret-name>` referencing a sealed credential set used by the agent for `docker pull` from private external registries (GHCR, ECR, GCR, ACR, Docker Hub private, Harbor, Nexus, Artifactory). Credentials never appear in build cache or logs. Rotation invalidates pull cache.

GraphQL re-evaluation trigger (deferral made explicit):

- ADR-009 defers GraphQL. The trigger to re-evaluate is documented in **ADR-014**: when at least three of these are true — (a) dashboard graph traversal exceeds 10 REST round-trips on the project page, (b) two or more design partners specifically request GraphQL for tooling, (c) external Railway-ecosystem tools require GraphQL adapters, (d) the SDK generator produces > 4 hand-patches per release for graph-shaped queries — the team drafts a GraphQL adapter ADR. Until the trigger fires, REST + cursor pagination + operation resources is the only public contract.

SDK pipeline (v1 graduation prerequisite):

- TypeScript, Python, and Go SDKs are owned, generated from the OpenAPI spec, published to npm / PyPI / pkg.go.dev, semver-coupled to API version, and required for v1 graduation rubric clause (e). A new sprint `S20_SDK_PIPELINE` in v1 owns the work: generator selection (e.g., Speakeasy, Stainless, oapi-codegen), CI publish flow, and contract-test coverage.

Runtime adapter boundary:

- The `BuildArtifact → ScheduleRequest → InstanceStatus → RoutingTarget` boundary is contract-tested by a runtime-adapter conformance suite that ships in **ADR-015** + Phase 1. Docker Engine API is the v1 implementation; containerd, Kubernetes, and Nomad are later adapters that must pass the same suite. The conformance suite verifies state transitions, idempotent reconciliation, observed-state reporting, image pull and signature verification, label hygiene, and clean detach.

Multi-host dataplane (Phase 1 design draft, Phase 3 GA):

- **ADR-016** + `FEATURE_MULTIHOST_DATAPLANE.md` are drafted in Phase 1 even though delivery is Phase 3. Scope: WireGuard mesh peer registry, IPAM allocator with conflict detection, peer-discovery protocol, MTU floor 1380 with PMTU probe, key rotation with overlap window, peer add/remove FSM that quarantines IPAM until heartbeat, conntrack flush on peer removal, NetworkPolicy primitive (egress allowlists per service), and a multi-host integration test rig.

PR preview environments shipping plan:

- PR previews are reclassified from "after the core deploy loop is stable" to a named **Sprint 15** in Phase 2 (immediately after MVP Beta exits). Trust/data/TTL rules already exist in spec; sprint owns implementation, GitHub App integration, fork-PR isolation, ephemeral-deletion lifecycle, and preview-URL comment posting.

GitHub Secret Scanning Partner Program:

- The `midn_<class>_…` token prefix is enrolled with GitHub Secret Scanning Partner Program in v1. Enrollment is owned by the security lead in Sprint 20.

Updated initial non-goals (parity declarations):

- Public hosted multi-tenant cloud (already listed).
- Full Railway parity (already listed) — note: this document now defines parity to mean *self-hosted-feature-parity for non-hosted features*; multi-region active-active, anycast routing, hosted CDN edge, and billing UI are intentional non-goals because they are properties of Railway's hosted infrastructure, not its product semantics.
- Multi-region active-active.
- Usage billing and plan enforcement.
- Fully managed database product.
- Kubernetes-only install.
- Generic container orchestration platform.
- **GraphQL public API at v1** (REST is the contract surface; ADR-014 sets the trigger).
- **Built-in CDN / edge cache equivalent to Cloudflare** (operator-paired).
- **Marketplace monetization** (templates are free; hosted track may add later).
- **Anycast IPv4/IPv6 with global routing** (hosted-track only).
- **GPU workloads** (Phase 4 reassessment; not a v1 promise).
- **Mobile native apps** (Railway has none either; symmetric non-goal).

New failure reason codes added to the registry:

- `CRON_INVOCATION_FAILED`
- `CRON_WINDOW_MISSED`
- `PREDEPLOY_FAILED`
- `STATIC_SITE_BUILD_INVALID`
- `STATIC_SITE_DIST_MISSING`
- `BYO_CERT_EXPIRING`
- `BYO_CERT_INVALID`
- `BYO_CERT_CHAIN_INCOMPLETE`
- `REPLICAS_VOLUME_CONFLICT`
- `REPLICAS_NOT_SCHEDULABLE`
- `REPLICAS_REQUIRES_MULTIHOST`
- `VARIABLE_REFERENCE_CYCLE`
- `REGISTRY_AUTH_FAILED`
- `MIGRATION_SOURCE_UNSUPPORTED`
- `TEMPLATE_REVIEW_REQUIRED`
- `TEMPLATE_PARAMETER_INVALID`
- `NOTIFICATION_DELIVERY_FAILED`

## Research Method

The research process used separate specialist passes:

- 8 research agents: Railway product/docs, Railway GitHub/open source, build systems, runtime orchestration, networking, data services, developer UX, and comparable OSS PaaS systems.
- 8 software architecture agents: scheduler, control plane, build/supply-chain, networking, security, storage/data, developer UX, and operations/roadmap.
- A second review pass with 4 Linux engineers and 4 AI engineers produced the locked decisions and implementation amendments in this document.
- A third review pass with 4 senior enterprise engineers and 4 senior automation engineers added the enterprise-readiness and automation-contract requirements.
- A fourth review pass with eight specialists (build/supply-chain, Linux runtime, distributed systems, security, storage, networking, API/IaC, and release/SRE) audited the consolidated document, argued conflicts in panel, and produced the locked decisions in the section above.
- A fifth review pass with four parity reviewers (product surface, operational/lifecycle, platform/infrastructure, ecosystem/DX) cross-checked Midnight's planned end-state against Railway's full public surface area and produced the parity-review locked decisions captured immediately above. Findings: `agile/parity-review/`.

## Railway Public Model

Railway's public docs expose a compact but deep model.

Core resources:

- Workspace: account/team/billing boundary.
- Project: application stack boundary.
- Environment: isolated copy of a project, such as production, staging, developer, or PR preview.
- Service: deployment target backed by one or more containers.
- Deployment: build/run attempt with lifecycle state and historical logs.
- Variable: runtime/build configuration, including references and sealed secrets.
- Volume: persistent storage attached to one service at a mount path.
- Domain: public HTTP endpoint, generated domain, custom domain, or TCP proxy endpoint.
- Template: reusable multi-service blueprint.

Important Railway behavior:

- Services are containers deployed from images.
- GitHub pushes, CLI uploads, Docker images, and templates can create deployments.
- Dockerfile takes precedence when present or configured.
- Railpack is Railway's current default source builder for new services.
- Nixpacks is open source but now maintenance-mode.
- Deployments have explicit states and can be redeployed, rolled back, canceled, restarted, or removed.
- Healthchecks gate startup promotion but are not continuous liveness monitoring in Railway docs.
- New deployment promotion overlaps slightly with old deployment shutdown for zero-downtime stateless rollouts.
- Old deployments receive SIGTERM then SIGKILL after configurable drain settings.
- Services with volumes cannot have multiple active deployments attached to the same volume.
- Private networking is runtime-only and project/environment-scoped.
- Private service discovery uses `<service>.railway.internal`.
- Public networking is domain-based, HTTPS by default, and target-port aware.
- TCP proxying exists separately for non-HTTP services.
- Databases are mostly service templates over Docker images plus volumes and variables.
- Variables can be service-scoped, shared, reference-based, sealed, or platform-provided.
- Staged changes make risky configuration mutations reviewable before deployment.
- The public API is GraphQL and the dashboard/CLI appear to use the same resource model.

Useful Railway public sources:

- Railway docs: https://docs.railway.com
- Railway GitHub organization: https://github.com/railwayapp
- Railpack: https://github.com/railwayapp/railpack
- Railpack docs: https://railpack.com
- Nixpacks: https://github.com/railwayapp/nixpacks
- Nixpacks docs: https://nixpacks.com/docs
- Railway CLI: https://github.com/railwayapp/cli
- Railway docs source: https://github.com/railwayapp/docs
- Railway templates org: https://github.com/railwayapp-templates
- Railway MCP server: https://github.com/railwayapp/railway-mcp-server
- Railway skills: https://github.com/railwayapp/railway-skills
- Domain Connect Go library: https://github.com/railwayapp/domainconnect-go

## Railway Open Source Assets To Reuse Or Study

High-value repositories:

- `railwayapp/railpack`: MIT, Go. Current source-to-OCI builder. Strong candidate for default zero-config builds.
- `railwayapp/nixpacks`: MIT, Rust. Mature builder architecture and provider model, but maintenance-mode.
- `railwayapp/cli`: MIT, Rust. Excellent CLI taxonomy and API/UX reference.
- `railwayapp/docs`: MIT, TypeScript/MDX. Behavioral spec for product semantics.
- `railwayapp-templates/*`: mixed licenses. Starter/template corpus and test fixture source.
- `railwayapp/railway-mcp-server`: MIT, TypeScript. Reference for agent-facing platform operations.
- `railwayapp/template-best-practices`: useful checklist for template quality.
- `railwayapp/domainconnect-go`: reusable DNS/domain automation reference.

Lower-level references:

- Railway has public forks or libraries around BuildKit, cgroups, and buildpacks. These are evidence of underlying concerns, not a complete platform implementation.

Conclusion: Railway has not open-sourced its full control plane, scheduler, runtime network, billing, or hosted platform. Enough behavior is public to design a compatible OSS system using existing open-source components.

## Comparable Open Source Systems

Relevant systems and lessons:

- Coolify: closest active self-hosted Railway-like product. Strong service catalog and BYO-server model. Avoid unchecked template sprawl before core reliability.
- Dokku: simple single-server PaaS with strong plugin model. Great simplicity reference. Multi-node is not the happy path.
- CapRover: easy Docker Swarm PaaS. Good onboarding/domain story. Swarm limits long-term direction.
- OpenShift/OKD: mature enterprise Kubernetes platform. Too heavy for the default OSS Railway-like install.
- Flynn and Deis/Hephy: valuable historical PaaS architecture references, but inactive/unmaintained.
- Cloud Native Buildpacks: mature standard for source-to-OCI builds, SBOMs, metadata, and rebasing. Good optional builder path.
- Kamal: pragmatic SSH/Docker deployment flow. Useful rollout inspiration, not a full PaaS.
- Nomad/Consul/Traefik: technically strong but current HashiCorp licensing complicates a clean OSS story.

Primary lesson: start closer to Coolify plus Dokku plus Kamal than OpenShift plus Knative.

## Enterprise And Automation Reference Sources

The enterprise and automation review used these sources as design references:

- OIDC: https://openid.net/developers/how-connect-works/
- SAML enterprise SSO reference: https://vercel.com/docs/saml
- SCIM protocol: https://www.rfc-editor.org/rfc/rfc7644
- OWASP authorization guidance: https://cheatsheetseries.owasp.org/cheatsheets/Authorization_Cheat_Sheet.html
- OWASP secrets management guidance: https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html
- NIST SSDF: https://csrc.nist.gov/pubs/sp/800/218/final
- SLSA requirements: https://slsa.dev/spec/v1.2/
- Azure Well-Architected Reliability: https://learn.microsoft.com/en-us/azure/well-architected/reliability/
- AWS Reliability Pillar: https://docs.aws.amazon.com/wellarchitected/latest/reliability-pillar/welcome.html
- Google Cloud reliability and DR: https://cloud.google.com/architecture/framework/reliability and https://cloud.google.com/architecture/dr-scenarios-planning-guide
- Cloud Foundry HA reference: https://docs.cloudfoundry.org/concepts/high-availability.html
- OpenShift disconnected install and release channels: https://docs.redhat.com/en/documentation/openshift_container_platform/4.16/html/disconnected_installation_mirroring/installing-mirroring-disconnected-about and https://docs.redhat.com/en/documentation/openshift_container_platform/4.16/html/updating_clusters/understanding-openshift-updates-1
- GitHub environments and deployment automation: https://docs.github.com/en/actions/deployment/targeting-different-environments/using-environments-for-deployment and https://docs.github.com/en/actions/use-cases-and-examples/deploying/deploying-with-github-actions
- GitLab protected environments and deployment safety: https://docs.gitlab.com/ci/environments/protected_environments/ and https://docs.gitlab.com/ci/environments/deployment_safety/
- Terraform Plugin Framework and plan semantics: https://developer.hashicorp.com/terraform/plugin/framework and https://developer.hashicorp.com/terraform/cli/commands/plan
- OpenTofu import: https://opentofu.org/docs/language/state/import/
- Pulumi preview/import: https://www.pulumi.com/docs/iac/cli/commands/pulumi_preview/ and https://www.pulumi.com/docs/iac/concepts/resources/options/import/
- Kubernetes API conventions: https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md
- Stripe idempotency: https://docs.stripe.com/api/idempotent_requests
- OpenAPI and AsyncAPI: https://spec.openapis.org/oas/latest.html and https://www.asyncapi.com/docs/reference/specification/latest
- CloudEvents: https://cloudevents.io/
- OPA, Gatekeeper, Kyverno, and Conftest: https://www.openpolicyagent.org/docs/latest/ https://open-policy-agent.github.io/gatekeeper/website/docs/ https://kyverno.io/docs/introduction/ https://www.conftest.dev/
- Rundeck, StackStorm, Temporal, and Argo Workflows/Events: https://docs.rundeck.com/docs/manual/jobs/ https://docs.stackstorm.com/overview.html https://docs.temporal.io/workflows https://argo-workflows.readthedocs.io/en/latest/ https://argoproj.github.io/argo-events/
- OpenTelemetry Collector and OTLP: https://opentelemetry.io/docs/collector/ and https://opentelemetry.io/docs/specs/otlp/
- Model Context Protocol: https://modelcontextprotocol.io/specification/2025-06-18

## Consensus Architecture

### Product Positioning

Midnight should be a self-hostable PaaS for small teams and developers who want Railway-like workflows on their own infrastructure.

Midnight's first product promise is install on a VM, deploy an app, get HTTPS, and keep it running on self-hosted infrastructure. The first target is one trusted VM, with the control-plane model shaped for multi-host scheduling later. Coolify already has broad self-hosted platform coverage; Dokku is simpler; CapRover is faster to bootstrap. Midnight should earn trust by making every deployment explainable, replayable, auditable, and recoverable through a small dashboard, CLI, and API surface.

Primary first promise:

```text
Install on a VM, deploy an app, get HTTPS, and keep it running.
```

Initial target users:

- Developers with one VPS today and a path to a small set of Linux hosts later.
- Small teams that want deploys, logs, variables, domains, and database templates without Kubernetes.
- OSS projects that want one-click deployment templates.
- Platform teams that may later want a Kubernetes adapter.

Initial non-goals:

- Hosted public multi-tenant cloud.
- Full Railway parity.
- Multi-region active-active.
- Usage billing and plan enforcement.
- Fully managed database product.
- Kubernetes-only install.
- Generic container orchestration platform.

### High-Level System

```text
User / Git provider / CI / CLI / Dashboard / API / MCP / Webhooks
          |
          v
Control Plane API + Web UI
          |
          +--> Postgres source of truth
          +--> Job workers / durable queue
          +--> Build workers with BuildKit
          +--> Internal registry
          +--> Webhook and event dispatcher
          +--> Policy and approval engine
          +--> OpenTelemetry/log export
          +--> Host agent command stream
          |
          v
Linux host agents
          |
          +--> container runtime
          +--> Traefik route reconciler
          +--> volume manager
          +--> log/metric collector
          +--> private network attachment
```

### Recommended Technology Stack

MVP default stack:

- Backend/control plane: Go.
- Dashboard: TypeScript + React.
- CLI: Go, sharing API client types where possible.
- Host agent: Go static binary.
- Database: Postgres.
- Queue: Postgres-backed queue first; NATS or Temporal later if needed.
- Builder: BuildKit, Dockerfile, Railpack; Nixpacks compatibility optional.
- Registry: embedded distribution registry or external OCI registry.
- Ingress: Traefik for HTTP/HTTPS, ACME, dynamic routing, and future TCP proxy support.
- Runtime: Docker Engine through the Docker Engine API. containerd is a later adapter.
- Identity: local bootstrap admin first; OIDC/OAuth in v1 Foundations; SAML/SCIM later.
- Policy: built-in validators and approval gates first; OPA/Rego-compatible policy later.
- Events: CloudEvents-style envelope for lifecycle events and signed webhooks.
- Metrics: cgroups/container stats stored in Postgres or Prometheus-compatible path.
- Logs: agent-managed stdout/stderr capture with rotation, retention, structured event indexing, and OTLP/log-drain export path; Loki/ClickHouse later.

API recommendation:

- MVP: REST with OpenAPI and explicit domain actions, plus Server-Sent Events or WebSocket event streams.
- Later: GraphQL query layer can be added if dashboard graph traversal becomes painful.
- Avoid generic CRUD endpoints for critical operations. Use semantic mutations such as `applyChangeSet`, `deployService`, `rollbackDeployment`, `verifyDomain`, and `restoreBackup`.
- The REST API is the contract for dashboard, CLI, SDKs, MCP, IaC providers, Git integrations, and webhooks.
- Every public operation needs a stable `operationId`, request/response schema, error schema, auth scope, rate-limit class, idempotency behavior, and examples.

This differs from Railway's public GraphQL API, but is simpler for broad OSS integration. The resource model should still be graph-based internally.

## Control Plane

The control plane is the product. Container execution is replaceable; resource modeling, diffing, snapshots, events, and reconciliation are not.

Core responsibilities:

- Identity and auth.
- Workspaces, projects, environments, services.
- Desired state and staged changes.
- Deployment snapshots.
- Variables and secrets.
- Domain and routing state.
- Volumes and backup state.
- Build and deployment job orchestration.
- Event stream and audit log.
- Agent registration and health.
- API, CLI, dashboard, and MCP parity over the same semantic action model.

Conceptual state layers:

```text
dashboard/service desired state
  + staged changes
  + config-as-code overlay
  + platform defaults
  + resolved variables
  = immutable deployment snapshot

deployment snapshot
  -> build image
  -> schedule instances
  -> observed runtime state
```

Minimal resource model:

```text
Workspace
  Project
    Environment
      Service
        Deployment
        Variable
        VolumeAttachment
        DomainBinding
      SharedVariable
      ChangeSet
      Event
      AuditLog
```

Important graph edges:

- Service consumes variable.
- Variable references another variable or service output.
- Domain routes to service target port.
- Volume attaches to service.
- Template instantiates resources.
- Deployment snapshots source/config/variables/image.
- Service depends on service through reference variables.

Phase 0 Alpha Postgres tables:

```text
users
workspaces
workspace_members
service_accounts
api_tokens
projects
environments
services
service_sources
variables
variable_references
domains
deployments
deployment_instances
deployment_snapshots
builds
hosts
host_heartbeats
routes
events
audit_logs
jobs
idempotency_keys
operations
failure_classifications
```

Future domain tables, added only when the feature ships:

```text
identity_providers
scim_connections
token_scopes
source_integrations
repository_connections
volumes
volume_attachments
backups
repo_analyses
repo_analysis_findings
repo_analysis_suggestions
plans
plan_resource_changes
resource_imports
drift_reports
tcp_proxies
change_sets
change_set_operations
audit_exports
operation_locks
webhook_endpoints
webhook_subscriptions
webhook_deliveries
webhook_delivery_attempts
dead_letter_events
external_pipeline_runs
external_checks
deployment_gates
environment_protection_rules
deployment_approvals
artifact_promotions
release_channels
quota_policies
quota_usages
usage_measurements
allocation_labels
policy_definitions
policy_decisions
approval_requests
approval_steps
retention_policies
evidence_artifacts
templates
template_versions
template_reviews
template_instantiations
registry_connections
secret_store_connections
notification_channels
external_system_links
runbooks
runbook_versions
runbook_executions
runbook_steps
runbook_approvals
remediation_policies
remediation_attempts
mcp_clients
agent_sessions
agent_tool_calls
agent_action_plans
agent_approvals
agent_policy_decisions
incident_memories
runbook_documents
prompt_versions
eval_datasets
eval_runs
```

Non-negotiable rules:

- Postgres is source of truth, not Docker state.
- Deployment snapshots are immutable after creation.
- Rollback creates a new deployment from an old snapshot.
- Config-as-code affects deployment resolution but does not silently mutate dashboard state.
- Host agents report observed state; they do not own desired state.
- All long-running operations must be idempotent and retryable.
- Every infrastructure side effect emits events.

## API, Automation, And IaC Contract

Automation is a control-plane design constraint. Midnight must work for humans in the dashboard and for machines through API, CLI, SDKs, MCP, Terraform/OpenTofu, Pulumi, Git providers, and webhooks.

Public API contract:

- Phase 0 Alpha uses REST `/api/v1alpha` with OpenAPI as a release artifact.
- Self-hosted v1 graduates to `/api/v1` only after compatibility gates, acceptance tests, migration policy, and core resources are stable.
- Stable error envelope: `code`, `message`, `request_id`, `docs_url`, and optional structured `details`.
- Cursor pagination for mutable lists: `limit`, `cursor`, and `next_cursor`.
- Rate limits with `429`, `Retry-After`, remaining/reset headers, and separate budgets for users, service accounts, CLI, MCP clients, webhooks, Git providers, and host agents.
- Long-running actions return an `operation_id` and support polling plus event-stream follow mode.
- Mutating public action endpoints require `Idempotency-Key`.
- Idempotency stores actor, scope, request hash, response or operation ID, status, and expiry.
- Reusing the same key with different parameters returns a typed conflict.
- High-risk updates require optimistic concurrency through `resourceVersion` or `If-Match`.

Operation, job, deployment, and idempotency semantics:

- `Operation` is the public API-facing async promise returned by mutating actions. It records state, target resource, metadata, result, error, actor, idempotency key, and expiry.
- `Job` is an internal durable worker execution record. It owns leases, retries, worker ownership, attempt count, heartbeat, timeout, cancellation, and dead-letter state.
- `Deployment` is the domain resource representing a deploy attempt with an immutable snapshot. It survives after operations/jobs expire.
- `IdempotencyKey` de-duplicates requests by actor, endpoint/action, target resource, and request body hash.
- Retrying the same async request returns the same `operation_id`.
- Reusing a key with different parameters returns `409 IDEMPOTENCY_KEY_REUSED`.
- API validation failures return normal API errors and should not create operations. Execution failures after acceptance are recorded on the operation and target resource.
- Jobs may be many-to-one with an operation. The operation aggregates child job state and exposes user-meaningful progress.

Declarative resource shape for automation:

```yaml
apiVersion: midnight.dev/v1
kind: Service
metadata:
  id: svc_123
  name: api
  generation: 3
  resourceVersion: rv_456
  labels: {}
  annotations: {}
spec: {}
status:
  observedGeneration: 3
  conditions: []
```

Condition format:

```yaml
type: Ready
status: "True"
observedGeneration: 3
reason: HealthcheckPassed
message: Service is ready and routed.
lastTransitionTime: "2026-04-25T00:00:00Z"
```

Plan/apply model:

- Phase 0 Alpha supports validate-only/preflight responses for core deploy, domain, and service changes.
- Persisted `Plan` resources, full drift, import/export, IaC preview semantics, and applyable plan resources are v1+ features.
- Future Plan JSON includes `formatVersion`, `applyable`, `complete`, `errored`, `priorState`, `plannedState`, `resourceChanges`, `resourceDrift`, `diagnostics`, `sensitivePaths`, `replacementReasons`, `requiresApproval`, and `expiresAt`.
- Dashboard staged changes, CLI dry-run, MCP proposed actions, and future Terraform/Pulumi previews should converge on this plan model when it ships.

Import/export and drift:

- `midnight export --project --format midnight-yaml|json` exports non-secret desired state.
- `midnight import service`, `midnight import domain`, and `midnight import volume` import existing resources into managed state.
- `midnight state export/import` is operator-only for repair workflows.
- Drift detection covers service config, domains, routes, variables metadata, deployment source, volumes, host/runtime observed state, and dashboard/API changes vs config-as-code.

Terraform/OpenTofu/Pulumi roadmap:

- MVP designs stable resource IDs, import IDs, lifecycle semantics, sensitive/write-only fields, and read/refresh APIs.
- Phase 1 ships a Terraform/OpenTofu provider for project, environment, service, domain, variable metadata, volume, and template instantiation.
- Phase 1 provider acceptance tests run against disposable Midnight instances.
- Phase 2 ships Pulumi support after API semantics stabilize.

Event and webhook contract:

- External lifecycle events use a CloudEvents-style envelope.
- Webhooks use HMAC-SHA256 signatures, timestamp header, event ID, delivery ID, retry schedule, delivery logs, replay endpoint, and endpoint disablement after repeated failure.
- Publish an event catalog and later an AsyncAPI spec once event coverage stabilizes.

Contract testing:

- OpenAPI linting and backward-compatibility checks in CI.
- Generated SDK smoke tests.
- Schemathesis-style API property tests.
- Pact or equivalent consumer contracts for CLI, dashboard, MCP, and provider integrations.
- Webhook schema fixture tests and replay tests.

Phase 0 API acceptance tests:

- Golden error-envelope tests for `400`, `401`, `403`, `404`, `409`, `422`, `429`, and `500`.
- Idempotency tests: same key/body returns same operation, same key/different body returns conflict, concurrent duplicate does not create two deployments.
- Operation lifecycle tests: async action returns operation, polling works, execution failure sets operation error and target reason code.
- OpenAPI lint requires operation IDs, auth scopes, schemas, examples, rate-limit class, idempotency behavior, and error schema.

## Deployment State Machine

Deployment state and container state must be separate.

Public deployment states should stay small and user-meaningful:

```text
QUEUED
BUILDING
DEPLOYING
ACTIVE
SUPERSEDED
FAILED
CANCELING
CANCELED
REMOVING
REMOVED
```

Internal deployment phases can be more granular and are stored as events/operation metadata:

```text
CREATED
QUEUED
BUILDING
BUILD_FAILED
IMAGE_READY
SCHEDULING
STARTING
READINESS_CHECKING
READY
PROMOTING
ACTIVE
DRAINING_PREVIOUS
COMPLETED
FAILED
CANCELING
CANCELED
REMOVING
REMOVED
```

Instance states:

```text
ALLOCATED
PULLING
CREATING
STARTING
RUNNING
READY
UNHEALTHY
CRASHED
RESTARTING
DRAINING
STOPPED
REMOVED
LOST
```

Rollout algorithm:

1. Resolve source, config, variables, and runtime settings into a deployment snapshot.
2. Build or pull an immutable image digest.
3. Schedule candidate instance(s).
4. Start candidate instance(s).
5. Wait for readiness healthcheck if configured.
6. Add candidate instances to route table.
7. Mark candidate deployment active.
8. Drain old deployment instances.
9. Send SIGTERM, wait configured grace period, then SIGKILL if needed.
10. Mark old deployment completed or removed.
11. Emit deployment events throughout.

If the candidate deployment fails readiness, keep the previous active deployment serving traffic.

Transition ownership and guards:

- API owns creation, cancellation request, rollback request, and idempotency response.
- Worker owns build/deploy phase transitions through guarded control-plane transition functions.
- Scheduler owns placement decisions.
- Agent owns observed instance state only and cannot directly mark deployments `ACTIVE`, `FAILED`, or `REMOVED`.
- Route reconciler owns route target readiness status.
- Domain/TLS worker owns domain verification and certificate state.
- Only one deployment mutation per service/environment can be active at a time unless a newer deployment explicitly supersedes the older one.
- `ACTIVE` requires candidate readiness and committed route target.
- Candidate failure never removes the previous active deployment.
- Rollback creates a new deployment from an old snapshot; it never mutates the old deployment.

## Build Plane

The platform should orchestrate builds, not invent language build logic.

Builder priority:

1. Explicit Dockerfile path.
2. Dockerfile in service root.
3. Explicit Railpack.
4. Default Railpack.
5. Explicit Nixpacks compatibility.
6. Optional Cloud Native Buildpacks later.

Build pipeline:

```text
Git event / CLI upload / manual deploy
  -> resolve service root and watch paths
  -> resolve build config
  -> select builder
  -> generate build plan
  -> run BuildKit
  -> push OCI image to registry
  -> store image digest, build metadata, logs, SBOM/provenance
  -> create deployment candidate
```

Build metadata to store:

- Source provider, repository, branch, commit SHA.
- Service root and config file path.
- Builder type and version.
- Dockerfile path or Railpack/Nixpacks plan.
- Build command and generated start command.
- Image repository and digest.
- Build logs location.
- SBOM location.
- Provenance record.
- Cache key.
- Triggering actor.

Build security rules:

- Treat builds as hostile code execution.
- Phase 0 may co-locate BuildKit on the runtime node only for trusted-code installs.
- v1 should prefer rootless BuildKit with RootlessKit network isolation where practical, with documented rootful fallback.
- Run builds away from production runtime hosts for public multi-tenant hosting.
- Do not mount the host Docker socket into tenant-controlled builders.
- Runtime secrets are not build secrets.
- Build secrets use BuildKit secret mounts, not Docker `ARG`.
- Cache keys are scoped by tenant/project/service/builder/root.
- Alpha uses local per-service cache only. Remote/shared cache is disabled by default.
- Untrusted fork builds may not write shared cache or receive production/build secrets.
- Deploy by image digest, not mutable tag.
- Redact secrets from logs.
- Rollback always pulls the previously built image digest; rollback never rebuilds. If the digest is unretrievable, rollback fails with `ROLLBACK_IMAGE_UNAVAILABLE`.
- Build cache mounts are scoped per `(tenant, service, secret-binding-set)`; rotating any bound secret invalidates the cache. SBOM scanner runs on the final image only, never on cache layers. Build logs route through the same redaction pipeline as runtime logs before storage and before any AI-context export.
- Untrusted-build classification (fork PR, non-member commit author, or non-trusted-branch ref) yields no project secrets, no shared cache read or write, ephemeral per-build cache only, and quarantine-only registry path. Untrusted digests cannot promote to non-preview environments without explicit approval.

BuildKit and Railpack versioning:

- Pin BuildKit, Dockerfile frontend, Railpack, and SBOM scanner versions.
- Record BuildKit version, Dockerfile frontend digest, Railpack version/digest, SBOM scanner version, provenance mode, cache source/scope, and attestation digests in every build record.
- Invoke a pinned Railpack binary or container image by digest; do not embed Railpack logic into the control plane for Alpha.
- Reject implicit `latest` behavior in release builds.
- Generate SBOM and SLSA-style provenance by the platform/control plane, not tenant scripts.
- Do not claim SLSA L2/L3 until documented controls are implemented and verified.

## Runtime Plane

Runtime v1 uses Docker Engine through the Docker Engine API. The agent must not shell out to the Docker CLI for normal operation. containerd remains a later runtime adapter.

Host agent responsibilities:

- Register with the control plane.
- Heartbeat with version, host labels, capacity, and health.
- Pull image digests.
- Create and stop containers.
- Attach networks.
- Mount volumes.
- Enforce CPU/memory/storage limits.
- Enforce pids limits, restart backoff, log quotas, and host admission controls.
- Apply restart policy.
- Stream logs and metrics.
- Report observed instance state.
- Reconcile local containers against desired assignments.
- Update local proxy config or route targets.

Scheduler v1 responsibilities:

- Place services on available hosts.
- Respect volume locality.
- Block replicas for volume-backed services.
- Enforce resource limits.
- Prefer stable placement.
- Reschedule stateless services when a host is lost.
- Mark volume-backed services as requiring manual recovery unless safe attach/detach exists.

Runtime adapter boundary:

```text
BuildArtifact -> ScheduleRequest -> InstanceStatus -> RoutingTarget
```

This boundary lets Kubernetes become a later adapter without changing user-facing resources.

### Linux Runtime Contract

MVP runtime profile:

- Rootful Docker Engine on trusted self-hosted Linux hosts.
- Rootful Docker is not a malicious-tenant or public multi-tenant isolation boundary.
- Docker daemon access is host-root equivalent. Only `midnight-agent` may access the Docker Engine API.
- cgroup v2 is hard-required. Preflight refuses install on cgroup v1 with explicit `systemd.unified_cgroup_hierarchy=1` remediation. (Was "required or strongly preferred"; tightened by panel.)
- Tenant containers may not use host PID, host network, host IPC, privileged mode, arbitrary devices, or arbitrary host bind mounts.
- No tenant container may access Docker, containerd, BuildKit, agent, or registry sockets.
- The agent runs as a narrow privileged supervisor and validates every requested container spec against local policy.
- The agent validates a narrowed container spec allowlist and rejects unknown Docker options by default.
- The agent rejects unsafe specs even if the control plane sends them.
- Public multi-tenant hosting requires stronger isolation than rootful Docker, such as separate build hosts and microVM/gVisor/Kata-style sandboxing.

MVP trust boundary:

- Midnight protects trusted self-hosted operators from accidental cross-project exposure.
- It does not protect a public platform operator from malicious tenants.
- Users with deploy rights can execute arbitrary code in builds and containers.
- Hosted/public mode is blocked until separate builder/runtime trust domains, abuse controls, stronger sandboxing, and tamper-evident audit are implemented.

Default container hardening:

- `no-new-privileges=true`.
- Drop Linux capabilities by default; add back only explicit allowlisted capabilities.
- Default seccomp profile enabled.
- AppArmor enabled on Ubuntu/Debian where available.
- SELinux support is experimental until policies exist.
- PIDs limit required.
- Memory hard limit required; swap policy explicit.
- CPU quota/shares required.
- Root filesystem read-only option supported.
- Platform-managed volume mounts only.
- `nosuid,nodev` mount flags where compatible.

Resource enforcement:

- Scheduler must reserve CPU, memory, disk, inode, port, and log capacity before placement.
- Disk pressure is a host state and blocks new builds/deployments.
- Restart policies use exponential backoff and surface `CRASH_LOOP_BACKOFF` instead of restarting forever.
- Logs have per-service size/rate limits and explicit dropped-log events.

Trust profiles:

- Local/dev trusted single-node: easiest install, weakest isolation.
- Self-hosted team trusted-code: rootful Docker with hardened defaults.
- Self-hosted semi-trusted users: requires stricter RBAC, quotas, build isolation, and network policy.
- Public multi-tenant untrusted code: out of MVP; requires strong sandboxing, abuse controls, and separate builder/runtime trust domains.

Host reboot and recovery semantics:

- systemd restarts platform services in dependency order.
- Existing workload containers remain alive when possible during control-plane restart.
- Agent startup reconciles Docker labels and container state against Postgres desired state.
- Route config is regenerated from the route table, not trusted from stale proxy state.
- Volume-backed services are never blindly rescheduled after host loss.

Docker host contract:

- Supported Docker Engine versions are pinned in a compatibility matrix and tested in CI.
- Official Docker Engine packages from Docker's APT repo are the default supported path.
- Midnight does not publish workload ports directly through Docker; workload containers stay private and are reached through Traefik-managed routing.
- Docker resource names use deterministic opaque IDs derived from Midnight resource IDs.
- Agent reconciliation uses both deterministic names and Midnight labels.
- If Docker has a labeled container that Postgres does not know about, the agent reports drift and quarantines/removes only according to explicit policy.
- If Postgres expects a container that Docker lost, the agent reports observed loss and reconciliation decides restart/reschedule based on service state and volume rules.

## Networking Plane

Networking must be explicit, declarative, and scoped.

Network planes:

- Public ingress network: internet to Traefik to service targets.
- Tenant private service network: service-to-service traffic inside one project/environment.
- Control-plane/agent management network: authenticated platform control traffic.
- Build network: isolated from tenant runtime private networks.
- Storage/backup network: later, for backup repositories or storage backends.

Public HTTP ingress:

- Generated platform domains.
- A default DNS namespace for every project, with generated service/environment hostnames under the operator-controlled platform domain.
- Custom domains.
- Automatic ACME TLS.
- SNI/Host routing.
- HTTP/1.1 and HTTP/2 support.
- WebSocket support.
- Optional gRPC support if tested end to end.
- Target port mapping.
- Request IDs and forwarded headers.
- Route health tied to deployment readiness.

MVP ingress topology:

- Single-host v1 runs Traefik on the host and routes to containers on project/environment Docker networks.
- Multi-host is Phase 3 and requires a separate dataplane design before support is claimed.
- Midnight owns generated Traefik dynamic file-provider config; tenant workloads never modify proxy config.
- Traefik must not mount `/var/run/docker.sock` in the default profile.
- The Traefik Docker provider is not the default because Docker API access expands the proxy compromise blast radius and tenant labels are not source of truth.

Route table fields:

```text
hostname
hostname_type
workspace_id
project_id
environment_id
service_id
deployment_id
target_port
protocol
dns_target
verification_txt_name
verification_txt_value
ownership_verified_at
routing_verified_at
tls_status
certificate_id
verified_at
status
```

Default generated domain model:

- Each project gets a stable default DNS namespace under the configured platform domain, for example `<project-slug>.<apps-domain>`.
- Each public service receives a generated hostname under that namespace, for example `<service>.<environment>.<project-slug>.<apps-domain>` or a collision-resistant equivalent.
- Generated names are routable without user DNS work because the Midnight operator controls the platform domain.
- Self-hosted installs must configure either a wildcard DNS record, such as `*.apps.example.com`, pointing at the Midnight ingress, or an equivalent DNS automation integration.
- Generated hostnames receive TLS through Let's Encrypt using the platform-controlled domain flow.
- Alpha/v1 issues per-host certificates through HTTP-01. Wildcard generated-domain certificates require DNS-01 and are later.

Custom domain lifecycle:

1. User requests domain binding.
2. Platform provides two DNS records:
   - Ownership verification record: TXT record with an Midnight-generated token.
   - Routing record: CNAME to the generated Midnight DNS target for subdomains, or ALIAS/ANAME/CNAME-flattening/A/AAAA guidance for apex domains.
3. Platform verifies the TXT record to prove domain ownership.
4. Platform verifies the routing record points at the expected Midnight target.
5. Platform requests a Let's Encrypt certificate only after ownership verification passes.
6. Route becomes active only after ownership verification, routing verification, and TLS readiness.

Custom domain DNS details:

- Subdomain example: `app.example.com CNAME <service>.<environment>.<project-slug>.<apps-domain>` plus `_midnight-verify.app.example.com TXT <token>`.
- Apex/root domains require provider support for ALIAS, ANAME, or CNAME flattening, or explicit A/AAAA records to the ingress address plus the TXT verification record.
- TXT verification must be scoped to the workspace/project/environment domain binding and cannot be reused across tenants.
- Failed DNS states must be surfaced separately: missing TXT, wrong TXT, wrong CNAME/ALIAS/A/AAAA, DNS not propagated, CAA blocks issuance, port 80 blocked, IPv6 mismatch, or Let's Encrypt rate limit.
- Custom names are aliases to explicit service/domain bindings; they never automatically expose every service in a project.

Private networking:

- MVP single-host: one Docker bridge network per project/environment.
- Docker network names use opaque IDs, not slugs; human-readable service names are scoped aliases only.
- Midnight maintains an IPAM allocation table and validates against existing host routes and Docker networks before creating a bridge.
- No unrelated project/environment shares the same L2 bridge or flat subnet.
- No tenant workload may use host networking in MVP.
- Internal DNS resolves service names only inside the same project/environment.
- Custom internal FQDNs require an Midnight DNS resolver. Alpha uses Docker network aliases unless that resolver ships.
- Use an Midnight-owned internal zone, for example `<service>.<environment>.<project>.internal`; do not copy Railway's suffix when the resolver exists.
- Internal DNS should use low TTLs and remove targets that are not ready.
- Builds cannot access runtime private networks.
- Browser/client-side code cannot resolve private names.
- Multi-host private networking requires WireGuard plus IPAM, routes, DNS, firewall policy, key rotation, and MTU management. WireGuard alone is only encrypted transport.

TCP proxy:

- Late MVP Beta (single-host). Multi-host TCP proxy is Phase 3. (Resolved by parity review; earlier text said Phase 3 in some places.)
- Separate from HTTP ingress.
- Allocates generated host and external port.
- Maps to service target port.
- Required for public Postgres/MySQL/Redis/Mongo access.
- Disabled by default for database templates.
- Requires source IP policy or explicit warnings before recommending public database exposure.

Egress:

- Default NAT through host for MVP.
- Static outbound IP is a later egress gateway feature.
- SMTP restrictions and abuse controls are required before public hosting.

Firewall and dataplane ownership:

- Docker owns bridge/NAT rules in MVP; the installer must validate Docker iptables/nftables integration.
- Midnight does not edit Docker-owned chains. Midnight policy lives in `DOCKER-USER` and `midnight-*` chains only.
- Midnight-owned firewall chains use a clear `midnight-*` naming convention and must be idempotently reconciled.
- The installer must detect UFW/firewalld, occupied ports, nftables/legacy iptables mode, and conflicting reverse proxies.
- Tenant networks must not reach host metadata, agent APIs, Docker sockets, BuildKit sockets, registry credentials, or control-plane admin ports.

IPv6 policy:

- Internal tenant networking is IPv4-only in MVP unless explicitly implemented and tested.
- Public ingress should bind IPv6 when the host has IPv6.
- A and AAAA records must be validated independently for domain troubleshooting.

ACME policy:

- HTTP-01 is the MVP challenge path for custom domains pointing at ingress.
- DNS-01 + wildcard certificates for the platform `apps-domain` are Alpha-blocking, not deferred. HTTP-01-only generated domains hit Let's Encrypt's 50-cert/registered-domain/week limit within tens of services; the panel reclassified this as a launch-blocker, not a polish item.
- A typed `DnsProvider` interface ships in Alpha with at least Cloudflare, Route53, and `manual` providers; credentials live in the platform secret store.
- Installer runs an external reachability prober on :80 and :443 before declaring ACME ready; unreachable hosts gate ACME with `INGRESS_PORT80_UNREACHABLE` and DNS-01 fallback guidance.
- CAA records are pre-flighted on the registered domain and parents before submitting to ACME; refusal surfaces remediation, never burns failed-validation budget.
- Certificate state is persisted and included in platform backup or safely reissued.
- ACME retries must back off to avoid rate limits.
- Use Let's Encrypt staging in integration tests, installer validation, and repeated development smoke tests.
- Traefik OSS ACME is supported only for a single active ingress instance. HA ingress requires an external or elected certificate controller before support is claimed.
- ACME retry budgets, stale-domain cleanup, authorization-failure backoff, and rate-limit diagnostics are required before bulk custom-domain import.
- Let's Encrypt certificates are issued and renewed automatically for generated and verified custom domains.
- Certificate issuance must not start for custom domains until the TXT ownership check succeeds.
- Renewal state, expiry, challenge failures, and last successful issuance must be visible in domain diagnostics.

Networking observability:

- Route reconciliation status.
- Proxy backend health.
- DNS resolution status.
- ACME challenge status.
- Firewall ruleset hash/version.
- WireGuard peer status in Phase 3.
- NAT/conntrack pressure where available.

## Variables, Secrets, RBAC, And Audit

Variable types:

- Plain variable: readable by authorized users.
- Secret variable: encrypted and redacted.
- Sealed variable: write-only after creation.
- Shared variable: environment/project shared scope.
- Reference variable: points to another variable or service output.
- System variable: platform-provided metadata.

Required system variables:

```text
MIDNIGHT_PROJECT_ID
MIDNIGHT_PROJECT_NAME
MIDNIGHT_ENVIRONMENT_ID
MIDNIGHT_ENVIRONMENT_NAME
MIDNIGHT_SERVICE_ID
MIDNIGHT_SERVICE_NAME
MIDNIGHT_DEPLOYMENT_ID
MIDNIGHT_REPLICA_ID
MIDNIGHT_PUBLIC_DOMAIN
MIDNIGHT_PRIVATE_DOMAIN
PORT
```

Reference variables are graph edges. They must drive validation and redeploy impact analysis.

Build-time vs runtime configuration:

- `buildVars`: non-secret values available during image build.
- `buildSecrets`: secrets available only through BuildKit secret/SSH mounts.
- `runtimeVars`: non-secret values injected into running containers.
- `runtimeSecrets`: secrets injected only at runtime.
- `publicClientVars`: explicitly public values that may be exposed to browser/client bundles.
- `systemVars`: Midnight-generated metadata.
- Runtime secrets are not available during build by default.
- Build secrets must not become Docker `ARG`, image `ENV`, copied credential files, logs, support bundles, AI context, or webhook payloads.

Variable reference resolution:

- References are evaluated into the deployment snapshot at build/runtime resolution points according to variable visibility.
- Cycles fail validation with a typed reason code.
- Missing references fail preflight before deploy.
- Secret references propagate secrecy; a secret-derived value cannot silently become a plain variable.
- Rename/delete of a referenced service or variable creates affected-service warnings and requires explicit apply.

MVP RBAC:

- Workspace owner.
- Project admin.
- Developer.
- Deployer.
- Viewer.

Important permission separation:

- Deploying code does not imply reading secrets.
- Viewing logs can be separate from managing variables.
- Production access can be separate from staging/preview access.

Audit events to capture:

- Login and token events.
- Member and role changes.
- Variable create/update/delete without secret plaintext.
- Deployment create/cancel/rollback/restart.
- Domain attach/verify/delete.
- Certificate issue/renew/failure.
- Volume backup/restore/delete.
- Host agent registration/heartbeat/failure.
- Operator/admin actions.

## Enterprise Identity, Governance, And Compliance Foundations

Midnight MVP is not enterprise-ready as a hosted multi-tenant PaaS. It must still include the foundations that enterprise teams cannot retrofit later.

Identity requirements:

- Alpha supports local bootstrap admin.
- v1 Foundations adds OIDC/OAuth login compatible with common enterprise IdPs unless design partners require it earlier.
- MFA should be delegated to the IdP when OIDC is used.
- SAML SSO, enforced SSO, SCIM 2.0 user/group provisioning, LDAP/AD direct auth, and group sync are enterprise-track features.
- Break-glass local admin accounts must be explicit, strongly audited, and separable from normal SSO.
- Domain verification can claim workspace ownership and restrict automatic member onboarding later.

Authorization requirements:

- Deny by default.
- Central authorization middleware for every API, CLI, dashboard, MCP, webhook, and service-account operation.
- Object-level checks for workspace, project, environment, service, deployment, variable, volume, domain, backup, and template resources.
- RBAC roles are UX shortcuts; effective access is computed from subject, resource relationship, environment tier, action risk, and workspace policy.
- Production environment actions require stricter checks than staging/dev.
- Authorization tests are release gates.

Service accounts and tokens:

- Service accounts are first-class identities with scoped API tokens.
- Tokens have owner, scopes, expiry, last-used timestamp, rotation event, and revocation audit.
- Token scopes include `projects:read`, `services:write`, `deployments:create`, `deployments:rollback`, `variables:write`, `secrets:write`, `domains:write`, `backups:restore`, `audit:read`, `webhooks:write`, and `mcp:tools`.
- CLI login can use browser/device flow; CI should use service accounts or future OIDC federation instead of long-lived personal tokens.

Secrets and key management:

- MVP uses envelope encryption for secrets.
- Root key lives outside Postgres, for example `/etc/midnight/secrets.key` for single-node installs.
- Secret versions, key IDs, rotations, and accesses are auditable.
- Secret lifecycle includes create, inject, rotate, revoke, expire, delete/tombstone, backup/restore dependency, and break-glass policy.
- Secret plaintext must not appear in deployment snapshots, logs, AI context, webhooks, support bundles, or audit payloads.
- Decryption happens only at injection/use boundaries and is audited.
- Sealed variables ship in MVP Beta with precise spec: no API path returns plaintext after create; rotation requires a new value, not view-then-edit; CLI has no `--reveal` for sealed; UI shows last-rotated and length-bucket only; audit/snapshot exports elide plaintext. (Resolved by panel.)
- External secret-provider interface should exist before adding Vault, AWS Secrets Manager, Azure Key Vault, Google Secret Manager, or customer-managed KMS.
- BYOK/customer-managed KMS is an enterprise-track feature.

Governance primitives:

- Ownership metadata is required for workspaces/projects/environments/services.
- Required allocation labels: `owner`, `team`, `cost_center`, `environment_tier`, `data_classification`, `business_service`, and `compliance_scope`.
- Quotas exist for CPU, memory, disk, logs, builds, services, domains, backups, and preview environments.
- Usage metering/showback ledger exists before billing. Billing remains out of MVP.
- Retention policies exist for logs, audit events, deployment records, backups, support bundles, AI traces, and webhook deliveries.

Production environment protection:

- Required reviewers.
- Prevent self-approval.
- Allowed deployers.
- Allowed branches/tags.
- Wait timer.
- Deploy freeze windows.
- Admin bypass setting.
- Break-glass flow with full audit.
- Protected secrets released only after gates pass.

Minimal built-in policies:

- Deny privileged, host-network, host-PID, host-IPC, arbitrary device, and arbitrary bind-mount specs.
- Require resource limits.
- Require owner/team metadata.
- Block mutable image tags when a digest is available.
- Block public database TCP exposure unless explicitly approved.
- Require custom domain ownership verification before routing or certificate issuance.
- Block production secret inheritance into untrusted previews.
- Block backup restore/delete and volume delete without approval.

Audit and evidence:

- Audit events use a stable schema and application-level append-only storage with per-row hash chaining shipped in MVP: `prev_hash`, `row_hash = H(prev_hash || canonical_row)`, rooted in a daily `audit_checkpoint` row signed with the Midnight release key or an operator-configured Ed25519 key. `midnight audit verify` CLI ships in MVP.
- Hash chain detects edit/delete in the audit table even before WORM/SIEM is configured. Full tamper evidence against platform admins still requires external WORM/SIEM sink or transparency-log-style anchoring later, but the hash chain is the floor.
- Audit export supports JSONL at MVP, CSV later if useful.
- Audit export includes request ID, correlation ID, actor, actor type, auth method, source IP, user agent, resource, action, before/after metadata where safe, policy decision, and timestamp.
- SIEM streaming is Phase 2; JSONL export is MVP.
- Evidence artifacts connect controls to events for future SOC 2/ISO 27001 readiness without claiming certification.

Compliance and privacy posture:

- MVP should publish security contact, vulnerability disclosure policy, and secure release process.
- Midnight release artifacts should be signed and include SBOM/provenance.
- Public claims should say `SLSA-style provenance generated` until the project has audited controls for a specific SLSA Build Level.
- Midnight release artifacts must include signed checksums, cosign signatures where applicable, SBOMs, provenance attestations, release workflow identity, and verification instructions.
- Release root of trust includes APT archive signing key for packages and cosign/Sigstore or configured enterprise keys for OCI/binary artifacts.
- Hosted/commercial variants need DPA, subprocessors, data retention/deletion/export, telemetry disclosure, data residency controls, incident communication, and acceptable-use policy.
- Logs, support bundles, AI context, audit records, and deployment metadata may contain personal data and need retention/deletion policy.

Template and plugin governance:

- Official, community, and local templates have distinct trust classes.
- Official templates require owner, source repo, license, pinned images, SBOM/provenance, security review status, risk class, test status, update policy, and deprecation policy.
- No arbitrary backend plugin system in MVP.
- Future plugins require signed packages, permission manifest, admin approval, dependency scanning, runtime isolation, upgrade policy, and audit.

Enterprise integration foundations:

- Git providers use a provider interface. MVP implements GitHub App; later adds GitLab, Bitbucket, GitHub Enterprise Server, and GitLab self-managed.
- Registry providers use OCI-compatible auth and digest deployment. External registry support should cover GHCR, ECR, GCR/Artifact Registry, ACR, Docker Hub, Harbor, Nexus, and Artifactory patterns.
- Secret providers use a `SecretProvider` interface. MVP has local encrypted store; later adds Vault and cloud secret managers.
- Observability export uses OTLP and log drains first; SIEM-specific connectors come later.
- Notification integrations start with generic signed webhooks and optional Slack incoming webhook. Jira, ServiceNow, Teams, Slack app, and change-management integrations are later native adapters.
- Custom CA bundles and outbound proxy settings are required for enterprise Git, registry, webhook, package-manager, and telemetry paths.

## Storage And Databases

Storage MVP should be honest and conservative.

Volume rules:

- One volume per service in MVP.
- Single-writer only.
- No replicas for volume-backed services.
- Mounted only at runtime.
- Not mounted during build or pre-deploy command.
- Restore creates a replacement volume, never overwrites in place.
- Deleted volumes are soft-deleted with a 7-day grace period (Alpha default, configurable down to 24h, never below). During grace the volume is unmounted, owner notified, and recoverable via API/UI. Hard deletion is logged to audit.
- Volume capacity is enforced via XFS or ext4 project quotas (`prjquota` mount option required); BuildKit cache and registry blobs live on quota-isolated subtrees so platform churn cannot starve user volumes. Volumes can grow online; shrink is unsupported (`RESIZE_SHRINK_UNSUPPORTED`).

Database model:

```text
Database = service template + Docker image + persistent volume + generated variables + private DNS + optional TCP proxy + backup policy
```

Official database template scope:

- Alpha official template candidate: Postgres only, and only if backup/restore gates pass.
- v1 candidates: Redis, MySQL, and MongoDB, each gated by backup, restore, upgrade, UID/GID, smoke-test, and support-matrix requirements.

Database template fields:

- Image reference and pinned version.
- Mount path.
- Internal port.
- Healthcheck.
- Generated credentials.
- Connection URL format.
- Default runtime variables.
- Backup policy defaults.
- Upgrade notes.
- Supported engine versions.
- Restore compatibility.
- Expected container UID/GID and data directory ownership.
- Backup command, restore command, and smoke-test command.
- Upgrade/downgrade policy and known unsupported modes.

Backup lifecycle:

0. Default scheduled backup policy is enabled at template instantiation (Alpha default: daily, 7-day retention). A service whose policy is disabled or has no successful backup in the last 2× period transitions to `BACKUP_STALE` and is surfaced in UI/API. RPO is reported per service as `time_since_last_successful_backup`.
1. User may override the manual or scheduled backup policy.
2. Generic volume backups use restic or equivalent backup tooling and are labeled crash-consistent unless the service is stopped or a safe quiesce hook runs.
3. Official database templates use database-native backups by default.
4. Backup metadata records source service, volume, engine, engine version, image digest, template version, backup type, consistency level, size, checksum, status, expiry, and restore-test status.
5. Restore creates a new replacement volume, never overwrites in place.
6. Service is redeployed with restored replacement volume only after restore verification, ownership validation/remap, and template smoke check pass or are explicitly overridden.
7. Old volume is retained until explicit deletion.

Backups must record `consistency_level` as one of `logical-consistent`, `physical-pitr`, `quiesced-filesystem`, `stopped-service`, `crash-consistent`, or `unknown`. UI/API must display this value.

Product language must say `template-backed database services`, not `fully managed databases`.

Linux storage implementation:

- Default volume implementation is host bind mounts under `/var/lib/midnight/volumes/<volume_id>/data`.
- Metadata lives separately under `/var/lib/midnight/volumes/<volume_id>/meta`.
- Runtime volumes, BuildKit cache, registry blobs, logs, platform database, and backups must have separate tracked storage roots.
- ext4 or XFS on a dedicated data filesystem is the default recommendation.
- ZFS datasets, btrfs subvolumes, LVM snapshots, and cloud block snapshots are advanced optimizations, not MVP assumptions.
- No arbitrary host-path mounts for users.
- Restore preserves or explicitly remaps UID/GID/mode metadata.
- Volume metadata records UID, GID, mode, owning template/image expectation, and restore remap policy.
- Volume roots must reject symlink/path traversal.

Database-native backup defaults:

- Alpha Postgres: `pg_dumpall --globals-only` plus `pg_dump --format=custom --blobs --no-owner` per database. Restore runs globals first, then `pg_restore -j <n>` against a fresh same-major cluster. Databases > 50 GB are flagged `BACKUP_SIZE_DEGRADED` and require the v1 physical-backup path.
- Major-version Postgres upgrade is dump-and-restore into a new replacement volume on the target image. Physical backups (`pg_basebackup`) restore only to the same major version. In-place `pg_upgrade` is out of scope for MVP and v1.
- Postgres v1/later: `pg_basebackup` plus WAL archive for larger/advanced physical backups.
- MySQL/MariaDB: `mysqldump` for small/default backups; `mariabackup` or `xtrabackup` path later.
- MongoDB: `mongodump` by default.
- Redis: distinguish cache mode from durable mode; durable mode coordinates RDB/AOF persistence before backup.
- Redis templates must declare cache mode or durable mode. Cache mode has no durability promise.

Registry retention and garbage collection:

- Rollback depends on image digests being pullable.
- Protect image digests referenced by active deployments, rollback windows, artifact promotions, restore manifests, and evidence/audit retention.
- Destructive blob GC is opt-in in Alpha. A non-destructive reference-count + tag-retention sweep runs daily: untagged manifests older than 14 days that are not referenced by any deployment, rollback window, restore manifest, or audit/evidence retention are deleted.
- Default retention: last 10 image tags per service plus all digests in active rollback windows. `midnight registry gc --dry-run` and `midnight registry gc --apply` ship in Phase 0.
- Full destructive blob GC runs only with registry read-only or stopped, a global GC lock, dry-run, and post-GC verification.
- External registries: protected digests are probed daily; missing digests emit `EXTERNAL_DIGEST_MISSING`. Production environments mirror active and last-N rollback digests to the embedded registry by default as a safety net.

Disk pressure policy:

- Track disk use for volumes, logs, registry, BuildKit cache, backups, and control-plane Postgres separately.
- Default thresholds: warn at 75%, degrade (block new builds) at 85%, block (refuse new deployments and volumes) at 92%. Inode thresholds: warn at 70%, block at 90%. Operators may tune; defaults ship enabled.
- Automatic cleanup order: expired build temp dirs, old BuildKit cache, unreferenced registry blobs after grace period, logs past retention, expired backups by policy.
- Never delete user volumes automatically.
- Surface explicit states such as `DISK_PRESSURE`, `STORAGE_DEGRADED`, and `VOLUME_HOST_LOST`.

## Observability And Supportability

MVP observability:

- Build logs.
- Runtime stdout/stderr logs.
- Deployment timeline.
- Service health history.
- Host agent heartbeat.
- CPU, memory, disk, and network metrics.
- Ingress route status.
- TLS certificate status.
- Worker queue status.
- Failed deployment reason classification.

Deployment timeline should answer:

- What source was deployed?
- What builder ran?
- What image digest was produced?
- Which host was selected?
- Did the container start?
- Did the healthcheck pass?
- Was the route updated?
- Did TLS issue successfully?
- Why did it fail?

Support bundle should export redacted diagnostics:

- Platform version.
- Agent versions.
- Install profile.
- Recent platform logs.
- Recent deployment events.
- Agent health.
- Ingress status.
- Disk/memory/CPU snapshot.
- Queue status.
- Redacted config metadata.

Support bundle export must show a preview/manifest of included files and redaction classes. Secret values, sealed values, tokens, authorization headers, cookies, private keys, and raw environment dumps are never included. External upload is opt-in.

Observability export is operator-configured and disabled for external destinations by default. OTLP/log-drain pipelines must support attribute allowlisting, redaction, retention limits, and workspace/project scoping before being described as production-ready.

Health terms are normative — three distinct probes, each with its own timeout/retry budget:

- Startup gates initial boot. `ECONNREFUSED` during startup is "still starting," not a failure. Default `startupTimeout >= 180s` per service with override.
- Readiness controls route membership and deployment promotion. Only readiness failures gate route health.
- Liveness controls restarts after thresholds and backoff.
- Synthetic checks validate externally visible route, TLS, DNS, and private DNS behavior.
- These signals must not be collapsed into generic `healthcheck` language in implementation or docs.

Failure classification must be machine-readable, not just prose. Minimum reason codes:

- `BUILD_FAILED`
- `BUILD_TIMEOUT`
- `DEPENDENCY_INSTALL_FAILED`
- `IMAGE_PUSH_FAILED`
- `IMAGE_PULL_FAILED`
- `MISSING_REQUIRED_VARIABLE`
- `VARIABLE_REFERENCE_FAILED`
- `CONTAINER_EXITED`
- `COMMAND_NOT_FOUND`
- `PORT_NOT_LISTENING`
- `APP_BOUND_LOCALHOST`
- `HEALTHCHECK_FAILED`
- `ROUTE_RECONCILIATION_FAILED`
- `TLS_ISSUANCE_FAILED`
- `DNS_VERIFICATION_FAILED`
- `VOLUME_MOUNT_FAILED`
- `DISK_PRESSURE`
- `OOM_KILLED`
- `HOST_UNAVAILABLE`
- `PRIVATE_DNS_FAILED`
- `DATABASE_TEMPLATE_UNHEALTHY`
- `BACKUP_FAILED`
- `RESTORE_FAILED`
- `BACKUP_STALE`
- `BACKUP_UNVERIFIED`
- `BACKUP_OFFHOST_MISSING`
- `BACKUP_SIZE_DEGRADED`
- `BACKUP_COLOCATED_RISK`
- `WAL_ARCHIVE_LAGGING`
- `OWNERSHIP_CONFLICT`
- `RESIZE_SHRINK_UNSUPPORTED`
- `ROLLBACK_IMAGE_UNAVAILABLE`
- `EXTERNAL_DIGEST_MISSING`
- `IMAGE_SIGNATURE_INVALID`
- `IMAGE_ARCH_UNAVAILABLE`
- `BUILD_OUTPUT_SECRET_LEAK`
- `BUILD_SECRET_LEAK_DETECTED`
- `BUILDS_BLOCKED_DISK_PRESSURE`
- `IDEMPOTENCY_REQUEST_IN_PROGRESS`
- `INVALID_CURSOR`
- `RESOURCE_VERSION_MISMATCH`
- `OPERATION_SUPERSEDED`
- `CANCEL_NOT_SAFE`
- `CONFIG_FIELD_LOCKED`
- `DOCKER_ENGINE_UNSUPPORTED`
- `AGENT_RUNTIME_UNAVAILABLE`
- `AGENT_CLOCK_SKEW`
- `INGRESS_PORT80_UNREACHABLE`
- `DOMAIN_OWNERSHIP_LAPSED`
- `RESTORE_ACME_THROTTLED`
- `VERSION_SKEW_REJECTED`
- `UPGRADE_SKIP_FORBIDDEN`
- `PLATFORM_DEGRADED`
- `RATE_LIMITED`
- `IDEMPOTENCY_KEY_REUSED`
- `DEPLOY_PROVENANCE_UNVERIFIED`

## Operations Automation And Remediation

Operations automation should be typed, auditable, bounded, and safe before it becomes intelligent or autonomous.

Automation classes:

- Class A: deterministic self-healing with low blast radius and no approval, such as restarting a crashed stateless replica with backoff.
- Class B: safe operator runbook with optional approval by environment, such as restarting staging.
- Class C: production-impacting runbook requiring approval, such as rolling back production.
- Class D: data-risk runbook requiring elevated approval and backup precondition, such as restoring a database backup.
- Class E: forbidden automation, such as deleting user volumes automatically or exposing public databases without explicit approval.

Runbook model:

- Runbooks are versioned procedures with typed inputs, preconditions, steps, approvals, abort behavior, rollback/verification steps, and audit events.
- MVP manual runbooks: restart service, redeploy, rollback, renew certificate, re-run failed backup, restore backup to replacement volume, regenerate route config, drain host, retry webhook, and generate support bundle.
- Runbook executions record actor, inputs, resource IDs, step results, approvals, logs, and final verification.

Webhook ingestion rules:

- Verify signatures.
- Store provider delivery ID and event hash.
- Enqueue async work before processing.
- Respond within provider deadline.
- Handlers are idempotent.
- Redelivery and manual replay are supported.
- Poison events enter dead-letter state with diagnostics.

Health model:

- Startup checks gate deployment promotion.
- Readiness controls route membership.
- Liveness controls restart only after thresholds.
- Synthetic smoke checks verify generated/custom domains, TLS, expected HTTP status, and private DNS.
- Health transitions emit events and reason codes.

Remediation safety:

- Every automated remediation has preconditions, max attempts, cooldown, rate limit, audit event, and abort behavior.
- Automation fails closed when state is ambiguous.
- Stateful/data actions require backup freshness checks or explicit override.
- Automation must never delete user volumes or secrets as remediation.
- Circuit breakers stop repeated remediation storms.

Workflow engine decision:

- MVP uses Postgres durable jobs with leases, heartbeats, retries, cancellation, and dead-letter handling.
- Temporal is a later option for long-running, human-in-the-loop, retry-heavy workflows if Postgres job complexity grows.
- Argo Workflows/Events apply only when the Kubernetes runtime adapter exists.

Policy and dependency automation:

- MVP uses built-in Go validators plus JSON Schema for `midnight.yaml`.
- Phase 1/2 can add Conftest-compatible checks for configs/templates.
- Later can embed OPA for authorization and remediation policy decisions.
- Renovate is the default dependency automation for Go, Node, Dockerfiles, GitHub Actions, base images, and official template image pins; Dependabot security alerts/dependency review remain enabled where useful.

Failure and upgrade testing:

- Fresh install smoke test.
- N-1 to current upgrade test.
- Backup before upgrade and restore after upgrade.
- Schema migration idempotence test.
- Agent/control-plane version skew test.
- Workload survival during server restart.
- Failure drills are release gates. Each drill must assert expected state transition, reason code, audit/event trail, user-facing diagnostic, and recovery runbook result.
- Required Alpha drills: kill agent/server/worker, restart Docker daemon, fail image pull, fail readiness, trigger disk pressure, duplicate/reorder webhook if webhooks ship, and break DNS verification if domains ship.
- Required v1 drills: Postgres restart during jobs, interrupted upgrade, backup restore to replacement volume, registry outage, ACME renewal failure, host reboot, and agent/control-plane version skew.

Temporal adoption trigger: keep Postgres-backed durable jobs through Alpha and v1. Reassess Temporal only after operation histories, retries, cancellations, human approvals, compensation logic, or cross-day workflows exceed what can remain inspectable through operations, jobs, and events.

## AI-Assisted Operations

AI-assisted operations are a first-class design target, but not an excuse for unsafe automation. The MVP should make the platform assistant-ready through structured diagnostics, typed context, redaction, semantic action schemas, and audit events. Agentic mutating actions must wait until policy, approval, and eval gates exist.

AI use cases:

- Deployment failure diagnosis.
- Build and runtime log summarization.
- Config provenance explanation.
- Domain/TLS troubleshooting.
- Database/template troubleshooting.
- Incident timeline and runbook generation.
- Safe action proposal with dry-run and approval.

Assistant architecture:

```text
Dashboard / CLI / MCP client
        |
        v
AI Gateway / MCP Server
        |
        +--> Auth and effective permissions
        +--> Policy evaluator
        +--> Context assembler
        +--> Redaction service
        +--> Tool/action broker
        +--> Model provider adapter
        +--> Eval and trace recorder
        |
        v
Semantic Midnight APIs
```

Assistant context must be typed, bounded, and redacted:

- Actor and effective permissions.
- Deployment snapshot summary.
- Config provenance summary.
- Recent events and visible audit events.
- Redacted build/runtime log excerpts.
- Healthcheck attempts.
- Instance statuses.
- Route/TLS/domain status.
- Host resource pressure.
- Volume/backup status.
- Recent changes.
- Known failure classifications.
- Prior remediation attempts.

Assistant action classes:

- Class 0: read-only explanation and summarization.
- Class 1: read-only diagnosis using bounded diagnostic context.
- Class 2: draft-only changes, runbooks, config diffs, and commands.
- Class 3: low-risk mutations with confirmation, such as restarting staging.
- Class 4: high-risk mutations requiring approval, such as production rollback.
- Class 5: destructive or data-risk actions requiring elevated approval, such as backup restore, volume delete, production secret rotation, or public database exposure.
- Class 6: forbidden actions, such as revealing secrets, bypassing RBAC, disabling audit, routing unverified domains, or deploying mutable tags when a digest is available.

AI-callable operations must be semantic:

- `explainDeploymentFailure`
- `summarizeLogs`
- `explainConfig`
- `compareDeployments`
- `generateRunbook`
- `proposeAction`
- `dryRunAction`
- `executeApprovedAction`
- `createIncident`
- `updateIncidentTimeline`
- `exportDiagnosticContext`

AI safety rules:

- AI features are disabled by default.
- Workspace policy decides allowed providers: disabled, local model, OpenAI-compatible, Anthropic-compatible, Azure/OpenAI, or custom endpoint.
- Before any external provider is enabled, enforce workspace budget controls, per-user/model rate limits, context size caps, provider allowlist, and kill switch.
- Raw logs, repos, templates, support bundles, PR metadata, and model outputs are untrusted input.
- Redaction runs before model calls, embeddings, support bundle upload, or summaries.
- AI acts as the invoking user plus narrower assistant capability scopes.
- No secret plaintext is exposed to AI.
- Mutating production actions require human approval.
- AI tools cannot call generic CRUD or arbitrary shell/SSH/container exec in MVP.
- Every AI session, context package, tool call, policy decision, approval, model/provider/version, and generated action is audited.
- Prompt-injection, sensitive-disclosure, excessive-agency, and model-cost/DoS fixtures are release gates before any AI UI launches.

AI eval gates before mutating actions:

- Deployment diagnosis accuracy.
- Secret leakage rate.
- Prompt-injection resistance.
- Tool selection accuracy.
- Required-approval recall.
- Unsafe-action refusal rate.
- Citation/evidence correctness.
- Hallucinated API/action rate.
- Remediation success rate.

## Developer Experience

DX principle: every important action must be understandable, scriptable, reviewable, and reversible.

Core surfaces:

- API: stable contract.
- CLI: automation and debugging.
- Dashboard: comprehension and collaboration.
- Git integration: deploy triggers and PR environments.
- Config-as-code: long-term source-of-truth option.

CLI commands:

```bash
# Phase 0 Alpha
midnight login
midnight init
midnight link
midnight deploy
midnight status
midnight logs
midnight rollback
midnight domain
midnight doctor

# MVP Beta / v1 Foundations
midnight vars
midnight service
midnight env
midnight volumes
midnight backups
midnight github
midnight config
midnight events
midnight analyze
midnight migrate
midnight explain
```

CLI rules:

- Read commands support `--json`.
- Mutating commands support `--dry-run` when feasible.
- Commands show project, environment, and service before acting.
- CI usage is non-interactive.
- `midnight run`, when added, is local-only by default and visibly states when it injects remote variables.
- `midnight connect`, when added, means port-forward/tunnel only, not shell.
- `midnight shell` and `midnight ssh` are operator/debug features, disabled by default, and require RBAC, audit, TTL, protected-environment approval, and no Docker socket access.
- `midnight logs` supports service/environment/deployment filters.
- Agent-facing commands return stable IDs, structured reason codes, and suggested next actions in JSON.

Dashboard Phase 0 journeys:

- First install/bootstrap admin.
- Create project and service.
- Deploy and watch timeline/logs.
- Configure generated domain and basic variables.
- Diagnose failed deployment and roll back.

Pages are secondary to these journeys; the dashboard should not imply support for volumes, templates, previews, or multi-service graphs before those flows exist.

Source control MVP:

- GitHub App install.
- Connect repo/branch to service.
- Verify GitHub webhook signatures and delivery IDs.
- Push deploys.
- Required status-check gate support.
- Deployment status publishing back to GitHub.
- Environment-level deployment lock.
- PR preview environments after core deploy loop is stable.
- No automatic secrets for untrusted fork PRs.
- Generic incoming webhook and CLI deploy path for non-GitHub CI systems.

Monorepo assisted MVP:

- Alpha repo analysis is advisory only.
- Deterministic repo analysis proposes service roots, watch paths, package managers, Dockerfiles, frameworks, build/start commands, ports, and required env vars.
- AI may explain and synthesize suggestions from bounded evidence, but deterministic validators enforce the result.
- User confirmation is required before deploying newly detected services.
- Repo analysis never creates services, databases, variables, domains, or templates without explicit user confirmation.
- Explicit service root/watch paths remain the source of truth after confirmation.
- Show why a service did or did not deploy.

Repo analysis artifact:

```text
RepoAnalysis
  repository
  commit_sha
  detected_roots[]
  candidate_services[]
  package_managers[]
  frameworks[]
  dockerfiles[]
  compose_files[]
  env_examples[]
  dependency_graph
  inferred_commands
  inferred_ports
  inferred_env_vars
  confidence_scores
  evidence[]
  warnings[]
  generated_midnight_yaml_patch
```

Environment variable discovery should classify required, optional, build-time, runtime, secret, public frontend, generated, and reference variables. Missing required variables should fail preflight before a doomed deploy.

## CI/CD And Release Automation

CI/CD is part of the deploy experience, not a side integration.

Source control integrations:

- Source control is MVP Beta, not Phase 0 Alpha.
- Phase 0 deploys from local CLI, Dockerfile build, or existing OCI image.
- MVP Beta ships GitHub App integration behind a provider interface.
- MVP Beta verifies webhook signatures, stores delivery IDs, de-duplicates events, responds quickly, and processes asynchronously.
- GitLab, Bitbucket, GitHub Enterprise Server, GitLab self-managed, Jenkins, and Azure DevOps use generic webhook/API/CLI paths first and native integrations later.
- Platform-managed integrations should avoid personal access tokens. Prefer GitHub App installation tokens, provider service connections, and future OIDC federation.
- If GitHub App is unavailable, supported fallback is generic signed webhook plus service-account deploy token, CLI deploy from CI, or registry image-published deploy. PAT-based source access is discouraged and not the default.

GitHub App setup lifecycle:

- Support operator-owned shared GitHub App for a self-hosted instance and user-provided GitHub App credentials for private installs.
- Validate webhook URL reachability, TLS, app permissions, subscribed events, private key, webhook secret, installation ID, repository access, and setup/callback URL.
- Minimum initial permissions: repository contents read, metadata read, commit statuses or checks write, deployments write if using deployments, pull requests read for previews, and webhooks for push/pull request/installation events.
- Do not request administration, workflows, secrets, or broad organization permissions in MVP.
- Handle installation deletion, repository access removal, suspension, and rate limits.

Provider-neutral event contract:

- `push`
- `pull_request_opened`
- `pull_request_synchronized`
- `pull_request_closed`
- `branch_deleted`
- `tag_created`
- `check_completed`
- `pipeline_completed`
- `manual_deploy_requested`
- `image_published`
- `deployment_approved`
- `deployment_rejected`
- `alert_fired`

Environment protection:

- Branch/tag allowlist.
- Required reviewers.
- Prevent self-approval.
- Wait timer and deploy freeze window.
- One active deployment lock per environment/service.
- Required external checks.
- Required artifact policy.
- Protected secrets released only after gates pass.

Release gates:

- Pre-deploy gates: CI status, branch policy, artifact signature/SBOM/provenance policy, vulnerability threshold, change ticket, maintenance window.
- Post-deploy gates: readiness, synthetic checks, route/domain health, metrics quiet period, alert status, manual verification.
- Gate results are stored with the deployment or artifact promotion.

Artifact promotion:

- Build once, promote the same immutable image digest across environments by default.
- Promotion records include source SHA, image digest, SBOM/provenance, checks, approvals, actor, gate results, and target environment.
- Rebuild-on-promotion is not the default because it weakens reproducibility.

Preview environment lifecycle:

- PR open creates or updates a preview only if policy allows actor and ref.
- PR synchronize redeploys affected services.
- PR close, merge, or branch delete stops immediately and schedules deletion.
- Previews auto-stop after idle TTL and hard-delete after retention TTL.
- Fork previews receive no shared/production secrets, no cloned production data, no public TCP, and reduced quotas unless explicitly approved.
- Default preview data is empty/generated. Production database cloning is disabled by default and requires explicit approval and scrub policy.
- Preview URL is posted back to provider status/comment when configured.

Deployment race prevention:

- Push deploys default to `runLatest`: older pending deploys are canceled or marked superseded.
- Manual production deploys default to sequential with explicit cancellation controls.
- Environment locks are visible in API, dashboard, CLI, and audit events.

Progressive delivery roadmap:

- MVP keeps readiness-gated rollout for stateless services.
- Later adds canary weights, active/preview routes, pre/post promotion analysis, automated abort, and metric-driven rollback.

## Config-As-Code

Use an `midnight.yaml` file for OSS-native configuration.

Example:

```yaml
schemaVersion: midnight.dev/v1

services:
  api:
    type: web
    root: apps/api
    watch:
      - apps/api/**
      - packages/db/**
    build:
      builder: railpack
      command: pnpm build
    deploy:
      startCommand: pnpm start
      healthcheckPath: /health
      healthcheckTimeoutSeconds: 60
    variables:
      required:
        DATABASE_URL:
          kind: secret
          source: postgres.url

  web:
    type: web
    root: apps/web
    watch:
      - apps/web/**
      - packages/ui/**
    build:
      builder: railpack
    deploy:
      startCommand: pnpm start

environments:
  production:
    branch: main
  staging:
    branch: develop

previews:
  enabled: true
  ttl: 7d
```

Rules:

- Config file stores non-secret configuration.
- Secrets remain in the platform secrets store.
- Config values override dashboard settings only for fields present in the config overlay and only for the deployment snapshot being resolved.
- Config file does not silently mutate dashboard state.
- Resolved config snapshot is stored with every deployment.
- Dashboard, CLI, and AI explainers must show effective config and source provenance for each field.
- AI-generated config is proposed as a patch and must pass schema validation, secret scanning, and human review before application.

Config ownership and provenance:

- `midnight.yaml` is a deployment-resolution overlay. It does not own a resource unless a field is explicitly marked config-managed.
- Each effective field records `value`, `source`, `sourcePath`, `owner`, `lastChangedBy`, and `unsetPolicy`.
- Dashboard/API edits to config-managed fields are blocked or saved as staged dashboard values that remain shadowed until config ownership is removed.
- Precedence: preview environment override, named environment override, base config, template instantiation defaults, dashboard/API desired state, platform defaults.
- When a config-managed field is removed from code, Midnight falls back to dashboard/API desired state or platform default according to the field's documented `unsetPolicy`; removal never silently preserves stale config-owned values.

Config workflow:

```bash
midnight analyze repo --json
midnight config generate --from-analysis
midnight config validate
midnight config diff
midnight config explain
midnight config apply --dry-run
```

Migration assistant targets:

- **Railway** `railway.json` / `railway.toml` (highest priority — most likely inbound source per parity review).
- Docker Compose.
- Heroku Procfile/app.json/buildpacks.
- Render `render.yaml`.
- Fly `fly.toml`.
- Vercel/Netlify frontend projects.
- Kubernetes Deployment/Service/Ingress manifests.

Migration output must include generated config, variable mapping, volume mapping, unsupported feature warnings, manual checklist, and dry-run result.

## Linux Install, Packaging, And Operations

Supported Linux matrix:

- Tier 1: Ubuntu 24.04 LTS x86_64.
- Tier 1: Debian 12 x86_64.
- Tier 2/experimental: Ubuntu 22.04, ARM64, Fedora/RHEL-family after CI coverage exists.
- Unsupported initially: non-systemd distros, Docker Desktop, WSL production installs, NFS-backed runtime volumes, rootless-only hosts, and untested RPM distributions.

Minimum single-node profile:

- systemd.
- cgroup v2.
- Docker Engine in supported version range.
- ext4 or XFS data filesystem.
- 2 vCPU, 4 GB RAM, 40 GB disk minimum for demos; production-adjacent installs need more.
- Public ports 80 and 443 available unless running behind an external reverse proxy.

Packaging contract:

- Signed `.deb` packages through an Midnight APT repository.
- Midnight supports only official Docker Engine packages from Docker's APT repository or an operator-provided mirror matching the compatibility matrix; distro `docker.io` is unsupported unless explicitly tested.
- Shell installer may add repository, run preflight, install packages, write initial config, enable systemd, and exit.
- Installer must be idempotent and produce a machine-readable install manifest.
- `midnight uninstall` preserves tenant volumes, registry data, platform database, config, and secret keys unless a destructive flag is explicitly provided.
- APT repository uses signed Release metadata and a scoped Midnight archive keyring installed with `signed-by`, not `apt-key`.
- Publish signing key fingerprint, expiry, rotation, and revocation procedure.

Default paths:

```text
/usr/bin/midnight
/usr/bin/midnight-agent
/etc/midnight/midnight.yaml
/etc/midnight/agent.yaml
/etc/midnight/secrets.key
/var/lib/midnight/
/var/lib/midnight/postgres/
/var/lib/midnight/registry/
/var/lib/midnight/buildkit/
/var/lib/midnight/volumes/
/var/lib/midnight/backups/
/var/log/midnight/
```

systemd units:

- `midnight-server.service`: API and dashboard.
- `midnight-worker.service`: durable jobs, builds, domains, backups, cleanup.
- `midnight-agent.service`: local host reconciler and Docker Engine API client.
- `midnight-buildkit.service`: controlled BuildKit service.
- `midnight-registry.service`: optional embedded registry.
- `traefik.service`: ingress, installed as a managed dependency or documented external dependency.

Local development profile:

- Non-production only.
- Supports localhost or self-signed HTTP/TLS, disposable data roots, and relaxed public DNS/ACME checks.
- May use Docker Compose or dev-mode packages.
- Must not be used as production validation.

Installer preflight checks:

- OS/version/architecture.
- systemd and cgroup mode.
- Docker daemon health.
- Unsupported Docker packages such as distro `docker.io` when official Docker Engine is required.
- Docker Engine version/API compatibility, storage driver, default address pools, existing Docker networks/subnet overlaps, Docker socket exposure, and daemon TCP listeners.
- Traefik/ports 80/443 availability.
- DNS and outbound internet.
- Disk space, filesystem, inodes, memory, swap.
- UFW/firewalld/nftables/iptables mode.
- AppArmor/SELinux status.
- time sync and hostname/FQDN.
- existing reverse proxy conflicts.

Upgrade requirements:

- Pre-upgrade platform backup.
- Migration lock to prevent concurrent upgrades.
- Database schema migration with transaction or explicit recovery plan.
- Agent/control-plane compatibility matrix.
- systemd unit migration.
- config migration.
- rollback guidance and downgrade limits.
- post-upgrade smoke test.

Backup and disaster recovery:

- Platform backup includes control-plane Postgres, `/etc/midnight`, `/etc/midnight/secrets.key`, registry metadata/blobs if embedded, template metadata, and certificate state when needed.
- Tenant backup includes volumes, database-native backups, service metadata, and restore manifests.
- A database backup without the encryption key cannot recover encrypted secrets.
- `midnight backup create --platform`, `midnight restore --platform`, `midnight doctor`, and `midnight support-bundle` are required operator commands.
- Host loss with local volumes creates a `VOLUME_HOST_LOST` state and requires restore from backup to another host.

Platform restore order:

1. Install compatible OS, Docker Engine, and Midnight version.
2. Stop platform writers.
3. Restore `/etc/midnight` and `secrets.key`.
4. Restore control-plane Postgres.
5. Restore certificate/ACME state or mark domains for reissue.
6. Restore embedded registry or mark images rebuild-required.
7. Start control plane in maintenance mode.
8. Verify schema, key decryption, and registry accessibility.
9. Restore tenant backups to replacement volumes.
10. Run smoke checks.
11. Re-enable workers and ingress.

## Enterprise Deployment Profiles And HA/DR Roadmap

Midnight should publish supported deployment profiles instead of vague enterprise claims.

Profiles:

- Profile A: single-node self-hosted. MVP target. No HA claim.
- Profile B: single-region multi-node workers with one control plane. Phase 3 target. Workload capacity improves, but control-plane HA is limited.
- Profile C: single-region HA control plane. Later enterprise target with replicated API/worker components, external HA Postgres, external object storage, HA ingress, and tested rolling upgrades.
- Profile D: multi-region warm standby. Later enterprise target with documented failover and backup replication.
- Profile E: multi-region active-active. Explicitly out of near-term scope.

RPO/RTO guidance:

```text
Artifact                         MVP target                 Later enterprise target
Control-plane Postgres           manual/scheduled backup     PITR/WAL archive, RPO <= 15m
Platform config and secret key    operator-backed-up file     managed key backup and rotation
Tenant database templates         database-native backup      scheduled + restore-tested backups
Registry/build artifacts          backup or rebuild source    external object storage replication
Logs and metrics                  best effort unless drained  external sink with retention SLA
Audit events                      local durable table/export  tamper-evident SIEM/WORM export
```

Control-plane HA roadmap:

- API replicas behind a load balancer.
- Worker replicas using Postgres advisory locks or a dedicated durable workflow engine.
- External HA Postgres or managed Postgres.
- External object storage for registry blobs, backups, support bundles, and artifacts.
- Agent reconnection semantics during control-plane partitions.
- Degraded mode: already-running workloads continue when the control plane is unavailable.

Enterprise private/disconnected install requirements:

- Custom CA bundles for Git, registry, webhook, ACME alternatives, proxy, and outbound integrations.
- HTTP/HTTPS proxy and `NO_PROXY` support for Docker, BuildKit, Git, package managers, registry, webhooks, telemetry, and support upload.
- Private admin endpoint and private ingress mode later.
- Mirrored APT repository, mirrored OCI images, mirrored builder dependencies, local registry, offline docs/templates, signed update bundle, and offline vulnerability database for disconnected installs.

Release channels and upgrade governance:

- `dev`, `stable`, and later `enterprise-lts` channels.
- Component compatibility matrix for server, worker, agent, BuildKit, Traefik, Docker Engine, Postgres, and Railpack.
- Agent canary rollout for multi-host deployments.
- Health gates before continuing upgrades.
- Maintenance windows and deploy freezes.
- Explicit rollback/downgrade boundaries.

Enterprise observability integration:

- Prometheus-compatible metrics endpoint.
- OTLP export through an operator-managed or external OpenTelemetry Collector.
- Log drains for runtime/platform logs.
- Audit export JSONL in MVP, SIEM streaming later.
- Default alert rules for disk pressure, agent lost, failed backups, certificate expiry, queue backlog, registry errors, and repeated deploy failures.

## Scope Layers

Phase 0 Alpha must include:

- Fresh Ubuntu 24.04 VM install with signed `.deb`, systemd units, preflight, and `midnight doctor`.
- Local bootstrap admin and minimal service-account/API-token path.
- `/api/v1alpha` OpenAPI skeleton with stable error envelope, idempotency keys, operation resources, and core contract tests.
- Postgres-backed control plane with small Alpha schema.
- Host agent registration and heartbeat.
- Docker Engine API runtime for trusted self-hosted code.
- Deploy one stateless web service from an existing OCI image.
- Dockerfile build through controlled BuildKit if build path is staffed.
- Generated project/service domain through Traefik file provider and Let's Encrypt HTTP-01.
- Logs, deployment timeline, startup/readiness promotion, structured failure reasons, and rollback/redeploy.
- Basic runtime variables and redacted secret injection if needed for first app flows.
- Support bundle with redaction preview.
- Platform backup command and restore smoke test.
- Prometheus metrics endpoint.
- Manual runbooks for failed deploy triage, rollback, restart service, domain/TLS failure, disk pressure, backup/restore, regenerate Traefik config, and collect support bundle.
- Acceptance tests and failure drills for the supported single-node profile.

MVP Beta must add:

- Variables/secrets with envelope encryption and key rotation path.
- Sealed variables with the panel-locked spec.
- Custom domains with TXT ownership verification, routing record validation, and Let's Encrypt issuance (HTTP-01 for custom; DNS-01 wildcard for the platform `apps-domain` already shipped in Alpha per panel decision).
- GitHub App push deploys, signed webhooks, required status-check gate, environment locks, and artifact promotion records.
- Basic RBAC with deny-by-default authorization checks and the cross-scope authorization invariant on reference variables.
- JSONL audit export with per-row hash chain and `midnight audit verify`.
- Single-writer volumes with project quotas, soft-delete grace, and `BACKUP_STALE` state.
- Postgres template with manual database-native backup/restore, restore drill fixture, and at least one off-host backup destination configurable.
- Signed releases, SBOM/provenance for Midnight artifacts, runtime image-signature verification by the agent, and vulnerability disclosure policy.
- Upgrade transaction (snapshot → migrate → smoke → auto-rollback) and `apt`-level skip-version refusal.

v1 Foundations should add:

- OIDC/OAuth login.
- Quotas, ownership/allocation labels, retention policy defaults, and usage/showback ledger.
- Config-as-code diff/apply and non-secret desired-state export.
- Plan/dry-run resources, drift reports, and import/export basics.
- Scheduled backups and restore-test status.
- PR preview environments with trust/data/TTL rules.
- Additional database templates only after backup, restore, upgrade, UID/GID, and smoke-test gates pass.
- OTLP/log export through an operator-managed Collector with redaction policy.
- Minimal policy-as-code or Conftest-compatible validation.
- Read-only AI diagnosis only after redaction, policy, and eval baselines exist.

Explicitly out of MVP Beta:

- Billing.
- Public hosted multi-tenancy.
- Multi-region routing.
- Kubernetes adapter.
- Autoscaling.
- Scale-to-zero.
- HA databases.
- Marketplace monetization.
- Full template update system.
- Customer-managed keys.
- OIDC/OAuth login if not required by design partners remains v1 Foundations, not Phase 0.
- SAML SSO.
- SCIM provisioning.
- LDAP/AD direct auth.
- Terraform/OpenTofu provider.
- Pulumi provider.
- Native GitLab/Jenkins/Azure DevOps integrations.
- Native Jira/ServiceNow/SIEM connectors.
- Full disconnected install bundle.
- Advanced policy engine.
- Mutating AI agents.

## Roadmap

### Phase 0: Design Partner Alpha

Goal: prove install, deploy, observe, route, fail, and rollback one stateless service.

Deliverables:

- Single-node installer.
- Signed `.deb` package and systemd units.
- Installer preflight and `midnight doctor`.
- Control plane and Postgres.
- `/api/v1alpha` OpenAPI skeleton with idempotency, operation resources, and stable error envelope.
- Minimal service-account/API-token path and central authorization middleware.
- Host agent.
- BuildKit Dockerfile builds.
- Traefik HTTP ingress.
- Generated project/service domain support for local wildcard or configured root domain.
- Logs and deployment timeline.
- Structured failure classifications.
- Manual rollback.
- Platform backup command.
- Prometheus metrics endpoint.
- Support bundle.
- Manual runbooks for failed deploy triage, rollback, restart service, domain/TLS failure, disk pressure, backup/restore, regenerate Traefik config, and collect support bundle.

Exit criteria:

- Local mode (`--profile=local` using `*.midnight.local` + self-signed TLS): fresh-VM-to-deployed-app under 15 minutes, no external DNS.
- Public mode: under 15 minutes excluding DNS propagation and ACME issuance, measured from `midnight doctor` pass to first 200 OK on generated subdomain. Typical end-to-end including DNS is documented as 30-60 minutes.
- Failed deploy produces structured reason code and recovery hint.
- Rollback succeeds and preserves previous active deployment until candidate is ready.
- Support bundle redacts secrets.
- Platform backup/restore smoke passes.
- Required Alpha failure drills pass.
- No external telemetry/log export occurs without explicit operator configuration.

### Phase 1: Self-Hosted v1

Goal: usable production-adjacent self-hosted platform.

Deliverables:

- Stable installer and upgrade path.
- Upgrade preflight checks.
- Multiple services per project.
- Environments.
- Variables/secrets.
- Envelope encryption and secret key rotation path.
- Custom domains and TLS.
- Volumes.
- Postgres template.
- Manual and scheduled backups.
- Database-native backup hooks.
- Health checks.
- RBAC, quotas, audit export, retention policy, and production environment protection.
- GitHub App, required CI gate, environment locks, and artifact promotion records.
- Manual runbooks for rollback, redeploy, certificate renewal, backup, restore, and host drain.
- Railpack builds.
- Advisory repo analysis and config validation.
- Official docs and 5-10 templates.

### Phase 2: Team And Reliability Layer

Goal: usable by small teams.

Deliverables:

- Workspace/team model.
- Better RBAC.
- Policy-as-code authoring/testing.
- Audit log UI/API.
- OIDC/OAuth login.
- GitLab native integration.
- PR preview environments.
- Config-as-code diff/apply.
- Agent drain/maintenance mode.
- Terraform/OpenTofu provider.
- SIEM/log-drain integrations and compliance evidence export.
- Read-only AI diagnosis.
- Additional official database templates after support gates.

### Phase 3: Multi-Host And Runtime Adapters

Goal: scale beyond one machine.

Deliverables:

- Multi-host scheduling.
- WireGuard private networking.
- Internal DNS.
- TCP proxy.
- Registry cache improvements.
- Kubernetes adapter.
- Volume attach/detach support where backend allows.
- Pulumi provider.
- OIDC workload identity/federated CI tokens.
- Native Jenkins/Azure DevOps/Bitbucket adapters if demand exists.

### Phase 4: Hosted/Commercial Optionality

Goal: hosted control plane or managed offering without forking OSS core.

Deliverables:

- Public multi-tenant hardening.
- Stronger build isolation, ideally VM/microVM.
- Billing/metering.
- Plan limits.
- Abuse controls.
- Multi-region routing.
- HA control plane.
- Enterprise SSO/audit/export.
- SAML, SCIM, BYOK, disconnected install bundles, enterprise LTS, support SLAs, DPA/subprocessor/data residency controls.

## Security Hard Lines

Do not violate these:

- Do not expose Docker socket to tenant workloads.
- Do not expose containerd or BuildKit sockets to tenant workloads.
- Do not allow tenant workloads to use privileged mode, host networking, host PID/IPC, arbitrary devices, or arbitrary host bind mounts by default.
- Do not route custom domains before ownership verification.
- Do not auto-inherit production secrets into untrusted PR previews.
- Do not run public multi-tenant builds on the same trusted host as production runtime without strong isolation.
- Do not store secrets in plaintext.
- Do not deploy mutable image tags when an image digest is available.
- Do not allow mutating API operations without authorization, audit, idempotency, and typed operation records.
- Do not let webhooks trigger work before signature verification, replay protection, and idempotency checks.
- Do not allow production deploys to bypass configured environment protection rules.
- Do not claim SLSA L2/L3 unless documented controls are implemented and verified.
- Do not allow untrusted fork builds to write shared cache or receive production/build secrets.
- Do not run embedded registry garbage collection while writes are possible.
- Do not use `apt-key` or globally trusted APT repository keys.
- Do not put unrelated projects/environments on one flat private network.
- Do not market template-backed databases as fully managed DBaaS.
- Do not expose secret plaintext to AI tools or model providers.
- Do not allow AI tools to bypass RBAC, policy, redaction, audit, or human approval.
- Do not allow AI-generated mutating production actions without dry-run and explicit approval.
- Do not send raw logs, repos, or support bundles to external model providers without explicit workspace opt-in.

## Implementation Order

Every milestone requires acceptance tests: happy path, failure path, idempotent retry where applicable, observable CLI/API status, and supportability artifact validation.

Recommended Phase 0 critical path:

1. Repository structure, coding standards, local dev environment.
2. Linux package skeleton, systemd units, supported OS preflight, and `midnight doctor`.
3. `/api/v1alpha` skeleton, stable error envelope, idempotency keys, operation resources, local bootstrap admin, and minimal service token.
4. Alpha Postgres schema for project, environment, service, deployment, host, job, operation, idempotency, event, audit, variable, domain, route, and build.
5. Host agent registration, heartbeat, no-op reconciliation, and Docker Engine API compatibility checks.
6. Deploy existing OCI image to Docker via agent with deterministic container names and labels.
7. Traefik file-provider route reconciliation and generated domain routing.
8. Logs and deployment timeline.
9. Startup/readiness promotion, structured failure reason codes, and guarded deployment transitions.
10. Rollback/redeploy from immutable deployment snapshots.
11. BuildKit Dockerfile build with pinned versions, local per-service cache, and no remote cache.
12. Dashboard journey for create project, create service, deploy, inspect failure, and rollback.
13. Minimal CLI: `login`, `init`, `link`, `deploy`, `status`, `logs`, `rollback`, `domain`, and `doctor`.
14. Support bundle, platform backup command, restore smoke, Prometheus metrics endpoint, and Alpha failure drills.

After Phase 0:

- Add custom domain full flow if generated domains are reliable.
- Add Railpack builder integration.
- Add variables/secrets envelope encryption and secret key rotation path.
- Add Postgres template with manual logical backup/restore.
- Add GitHub App and signed webhook foundation.
- Add PR preview environments only after trust/data/TTL rules exist.
- Add multi-host/WireGuard.
- Add read-only AI diagnosis after redaction, policy, and eval baselines exist.

## Open Questions

- Which Railpack release cadence and compatibility policy should v1 support? (Panel pinned the lifecycle: stored resolved-plan-by-digest, audit on channel change, but cadence is product-track.)
- Should TCP proxy be late MVP or Phase 1 after database templates are stable?
- Which local model/provider abstractions should be supported first for optional AI diagnosis?

Resolved by second review:

- Runtime v1 is Docker Engine API, not Docker CLI or direct containerd.
- Ingress v1 is Traefik.
- Tier 1 OS support is Ubuntu 24.04 LTS and Debian 12.
- Database template backups are database-native by default.

Resolved by enterprise and automation review:

- MVP is not enterprise-ready, but must include enterprise-aware foundations.
- REST/OpenAPI is the MVP automation contract; GraphQL remains optional later.
- Local bootstrap auth, service accounts, scoped tokens, and operation resources are Alpha foundations; OIDC/OAuth, signed webhooks, audit export, quotas, and production protection move to MVP Beta/v1 Foundations.
- SAML, SCIM, BYOK, Terraform/OpenTofu provider, Pulumi provider, native SIEM/ITSM connectors, and enterprise LTS are later tracks.
- Plan/dry-run, import/export, drift, idempotency, cursor pagination, and operation resources are required for provider-grade automation.

Resolved by mid-level challenge and senior panel:

- Phase 0 Alpha is a narrow vertical slice, not the full architecture.
- Keep Postgres-backed durable jobs through Alpha and v1; reassess Temporal only after operation histories, retries, cancellations, human approvals, and compensation logic outgrow the Postgres job model.
- Invoke pinned Railpack binaries/images later; do not embed Railpack logic in the control plane for Alpha.
- Use Traefik file-provider config, not Traefik Docker provider, and do not mount Docker socket into Traefik.
- Alpha official database scope is Postgres only, and only after manual logical backup/restore gates pass.

Resolved by eight-specialist panel review:

- Sealed variables ship in MVP Beta with a precise no-reveal spec.
- DNS-01 + wildcard for the platform `apps-domain` is Alpha-blocking, not deferred.
- Off-host platform backup destination is required at MVP Beta, not Phase D.
- Audit log per-row hash chain ships at MVP, not "later."
- cgroup v2 is hard-required, not "preferred."
- `daemon.json` baseline (live-restore, overlay2, no-new-privileges, log size caps) is enforced by installer.
- Rollback always pulls a stored digest; rollback never rebuilds.
- Multi-arch builds are opt-in via explicit `platforms:` field with manifest-list scheduling.
- Atomic single-flight per (service, environment) uses `pg_advisory_xact_lock`; supersede arbitration uses a monotonic deployment sequence, not wall clock.
- Default scheduled backup is on at template instantiation; absent backups raise `BACKUP_STALE`.
- ACME-state restore at scale is throttled and never marketed as fast; LE rate-limit guards are first-class.
- Phase 0 install criterion is split into local-mode (15 min, no DNS) and public-mode (15 min excluding DNS/ACME).
- Upgrade is a transactional snapshot-migrate-smoke-rollback flow; skip-version is refused.
- API token format, cursor pagination scheme, error code registry, plan JSONSchema, and CloudEvents binding are normative artifacts of the v1alpha contract.

## Final Consensus

Build the boring, durable self-hosted PaaS first.

The first useful version is not a global cloud. It is a reliable single-node system for deploying an app, giving it HTTPS, and keeping it running with a strong control plane, reproducible builds, predictable rollouts, logs, clear failure states, and safe rollback.

The strengthened enterprise and automation consensus is: do not expand Alpha into an enterprise platform, but do not build an automation-hostile foundation. The control plane must start with operation resources, idempotency, audit events, clear resource ownership, and small declarative seams so Terraform, Pulumi, OIDC/SAML/SCIM, SIEM, ITSM, GitOps, and hosted enterprise operations can be added without rewriting the model.

The eight-specialist panel review added a final layer: every promise in this document must be enforceable, not aspirational. Compatibility matrices are artifacts, not phrases; defaults are pinned numbers, not "configurable"; rollback pulls digests rather than rebuilding; backups live off-host before MVP ships; audit chains its own integrity from day one; and the install/upgrade UX has explicit local-mode versus public-mode contracts so the 15-minute promise stops including 24-hour DNS propagation. The platform must be honest about what is enforced, what is documented but not enforced, and what is intentionally deferred.

The trust layer under the product promise is failure clarity: a failed deploy, bad build, dead host, broken domain, full disk, crashed container, bad healthcheck, lost host, expired certificate, exhausted disk, or interrupted upgrade must produce a clear state transition, a typed reason code, structured logs, an audit event, a runbook reference, and a safe recovery path.
