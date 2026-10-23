# Midnight — Security Review Plan

This document is the single source for security review process: threat-model boundaries, high-risk surfaces with mandatory pre-merge review, the pre-merge security checklist, pen-test cadence, vulnerability disclosure policy reference, and per-feature mandatory reviewers.

The threat model and trust boundary follow the Eight-Specialist Panel locked decisions in `D:/projects/midnight/midnight-platform-plan.md` lines 71–204 and the Security Hard Lines section at lines 2307–2331. Where this document and the spec disagree, the spec wins; open an ADR.

---

## 1. Threat model boundaries

### 1.1 What Midnight Alpha protects against

Midnight Alpha is **trusted self-hosted with hardened defaults**. The threat model assumes:

- The operator owns the host, the Docker daemon, and all workloads on it.
- Users with `Developer` or `Deployer` roles are trusted because deploying code on rootful Docker is host-root-equivalent. The `BUILD_ROOTFUL` warning surfaced by `midnight doctor` is a reminder, not a guard (`spec` line 128).
- Operators want protection from: accidental cross-project secret exposure, accidental tenant-to-tenant network reachability, supply-chain regressions, lost integrity of audit/state, unsafe defaults, and configuration drift.

Midnight Alpha **does not** claim to protect against:

- A malicious tenant with deploy rights. They have host-root-equivalent access. (`spec` line 64.)
- A public multi-tenant cloud threat model. That requires separate builder/runtime trust domains, microVM/gVisor/Kata sandboxing, abuse controls, tamper-evident SIEM/WORM. Out of MVP. (`spec` line 901.)
- A nation-state-level attacker with persistent host access.

### 1.2 The "deploy = host root in Alpha" rule

This is a panel-locked invariant. It must be communicated:

- In the dashboard at the moment the operator grants `Developer`/`Deployer` to a user, with explicit acknowledgement.
- In `midnight doctor` whenever co-located rootful BuildKit is detected (`BUILD_ROOTFUL` warning).
- In the install docs.
- In the README's "Threat model" section.

Removing or weakening this rule requires the security lead **and** an accepted ADR.

### 1.3 Trust boundary diagram

```
+-----------------------------------------------------------+
|  Untrusted: Internet, Git provider webhooks, AI providers |
|  Crossings: signed webhook, SSRF-safe HTTP client,        |
|             ACME, image-pull from external registry       |
+-----------------------------------------------------------+
                 |  validated, redacted, signed
                 v
+-----------------------------------------------------------+
|  Semi-trusted: workspace user via dashboard/CLI/API/MCP   |
|  Crossings: authn (cookie | bearer), authz, idempotency,  |
|             rate limit, audit                             |
+-----------------------------------------------------------+
                 |  authorized
                 v
+-----------------------------------------------------------+
|  Trusted control plane                                     |
|  - midnight-server (API, dashboard)                        |
|  - midnight-worker (build, deploy, reconcile, webhook,     |
|    cleanup queues)                                         |
|  - midnight-agent (host reconciler, narrow Docker API      |
|    client)                                                 |
|  - midnight-buildkit, midnight-registry, traefik           |
|  - postgres (separate roles: app, migrate, audit_ro)       |
+-----------------------------------------------------------+
                 |  trusted but bounded
                 v
+-----------------------------------------------------------+
|  Tenant runtime: container workloads, volumes, networks   |
|  Forbidden: host PID/IPC/network, privileged, arbitrary    |
|             devices, arbitrary bind mounts, Docker socket, |
|             containerd socket, BuildKit socket, registry  |
|             credentials, control-plane admin ports        |
+-----------------------------------------------------------+
```

---

## 2. High-risk surfaces with mandatory pre-merge review

Any PR that adds, removes, or modifies code in any of the following surfaces requires pre-merge review by the named reviewer below. The reviewer must be approved on the PR before merge, separately from the standard two-reviewer rule.

| Surface | Why high-risk | Mandatory reviewer |
| --- | --- | --- |
| Bootstrap admin (token generation, one-shot read, file lifecycle, MFA enrolment) | First-foot-in-door; `0600 root:root` file lifecycle is the only protection | Security lead |
| KEK at `/etc/midnight/secrets.key` (creation, rotation, rewrap, retention) | KEK loss is irrecoverable; rotation correctness is a release gate | Security lead + storage lead |
| Secret store / DEKs / sealed variables | Sealed-variable no-reveal invariant is panel-locked; spec leakage = critical bug | Security lead |
| Reference-variable cross-scope authz | Cross-scope authz must be enforced at create time **and** at every resolution | Security lead + control-plane lead |
| Build host (BuildKit lifecycle, untrusted-build classification, cache scoping, secret mounts) | Builds are hostile code execution; cache poisoning, secret leak via `ARG`, fork-PR cache writes | Security lead + build/supply-chain lead |
| Image signature verification (cosign), SBOM/provenance generation | Defines the supply-chain integrity guarantee; `IMAGE_SIGNATURE_INVALID` must fail-closed | Security lead + build/supply-chain lead |
| Ingress (Traefik dynamic config writes, atomic rename, `/.well-known/acme-challenge/*` reservation, Docker socket non-mounting) | Misuse can let tenants steal certs, hijack routes, or escalate via socket | Networking lead + security lead |
| ACME (state persistence, byte-exact restore, throttling, CAA preflight, port-80 reachability check, DNS-01 provider plug-ins) | Rate-limit burns; cert-chain integrity; provider-credential handling | Networking lead + security lead |
| Webhook ingestion — inbound (signature verify, replay window, dead-letter, idempotency) | SSRF, replay, signature-confusion, fork-PR secret isolation | Security lead + control-plane lead |
| Webhook delivery — outbound (signing, two-key rotation, SSRF-safe HTTP client) | Multi-`v1` rotation, replay window 300s, single shared client invariant | Security lead + networking lead |
| AI gateway (context assembly, redaction, tool broker, eval gates, kill switch) | Prompt injection, secret leakage to providers, excessive agency | Security lead + AI lead (if assigned) |
| MCP server (service-account scoping, mandatory `Idempotency-Key`, Class 3+ approval, per-client rate + cost budget) | Class confusion can mutate production via chat | Security lead + AI lead (if assigned) |
| Dashboard auth (`__Host-` cookies, CSRF, Origin check, CSP, HSTS, COOP, Permissions-Policy, logout-everywhere) | Browser is the largest attack surface; cookie/CSP regressions are subtle | Security lead + frontend lead |
| Audit hash chain (per-row chaining, daily checkpoint signing with release/operator Ed25519 key, `midnight audit verify`) | Tamper-evidence; chain regressions are silent failures | Security lead + control-plane lead |
| Registry signing & retention (cosign, reference-counting, daily sweep, full GC opt-in with quiesce/lock/dry-run/post-GC verify) | Wrong GC deletes a digest still inside a rollback window | Security lead + storage lead |
| Docker socket exposure (agent is the only consumer; rejection of unsafe specs) | One leaked socket mount = host compromise | Linux runtime lead + security lead |
| Postgres role separation (`midnight_app`, `midnight_migrate`, `midnight_audit_ro`, `pg_hba.conf` restrictions) | Privilege creep; SQL-via-app should not DDL | Security lead + control-plane lead |
| Idempotency-key handling (TTL, body hash, in-flight lock, key-vs-operation expiry ordering) | Replay can double-charge state; concurrent in-flight must return 409 | Control-plane lead + security lead |
| Outbound SSRF-safe HTTP client (single shared instance) | Bypass = SSRF to cloud metadata, link-local, RFC1918 | Security lead |
| Bootstrap-token + API-token format (`midn_<class>_<26-base32>`, prefix surfaced, sha256-stored, GitHub Secret Scanning Partner submission for v1) | Token-shape stability is a long-tail commitment | Security lead |
| Volume ownership (UID/GID mapping, `userns-remap`, `OWNERSHIP_CONFLICT`) | Wrong UID = data corruption or permission escalation | Storage lead + Linux runtime lead |
| Backup repositories (restic password sealed by KMS, separate operator passphrase for KEK backup, restore-test) | Backup-of-backup cycle bug = total data loss | Storage lead + security lead |
| `midnight shell`/`ssh` recording (asciicast, redaction pipeline, two-person approval for production) | Recorded session = sensitive evidence | Security lead + operator lead |
| Public-client-vars build-output secret scan (`BUILD_OUTPUT_SECRET_LEAK`) | One leaked token via webpack = total compromise | Security lead + build/supply-chain lead |

---

## 3. Pre-merge security checklist

Reviewer must verify these on any PR that touches code or config. PRs with a high-risk-surface label require the mandatory reviewer's signoff in addition to the standard two reviewers.

### 3.1 Authentication / authorization

- [ ] Every new endpoint declares an auth scope in the OpenAPI definition.
- [ ] Authorization is checked centrally (not ad hoc); the central middleware is invoked.
- [ ] Object-level authz is correct: workspace, project, environment, service, deployment, variable, volume, domain, backup, template.
- [ ] Production-tier actions have stricter checks than staging/dev.
- [ ] Cross-scope reference-variable authz is enforced at create AND at every resolve.
- [ ] Service-account scope is the **minimum** necessary; no `*:*`.

### 3.2 Secrets

- [ ] No secret plaintext in: deployment snapshots, logs, AI context, webhook payloads, support bundles, audit payloads, JSON responses.
- [ ] `BuildKit secret mount` is used instead of Docker `ARG` for build-time secrets.
- [ ] Sealed variables: no API path returns plaintext after create.
- [ ] DEK/KEK lifecycle: rotate is reversible during the grace window; loss is documented.
- [ ] Secret-derived values cannot silently become plain variables.
- [ ] No new secret class introduced without a corresponding redaction fixture.

### 3.3 Inputs

- [ ] All user input is validated against an explicit schema (JSON schema, OpenAPI, Go struct + validator).
- [ ] Service slugs are RFC-1035 valid.
- [ ] No raw SQL with string concatenation; use parameterized queries.
- [ ] No path traversal in volume/registry/log paths; symlink rejection on volume roots.
- [ ] No unbounded request bodies; `maxRequestBodyBytes` (32 MiB default) enforced.
- [ ] No untrusted YAML/JSON parsed without size/depth limits.

### 3.4 Outputs / responses

- [ ] Error envelope includes `code`, `message`, `request_id`, `docs_url`, optional `details`. No stack traces, no internal hostnames, no Postgres errors leaked.
- [ ] Redaction runs on logs, support bundles, AI context, webhook payloads, build output, audit exports.
- [ ] `secret_leakage_rate == 0` on the redaction fixture set.

### 3.5 Network

- [ ] Outbound HTTP uses the single shared SSRF-safe client.
- [ ] No `net.Dial` or `http.Client{}` constructed in production code (a CI rule rejects it).
- [ ] No new bind to `0.0.0.0` without a documented reason; admin/control endpoints bind to localhost or the agent network.
- [ ] Tenant networks cannot reach metadata, agent APIs, Docker sockets, BuildKit sockets, registry credentials, or control-plane admin ports.
- [ ] `DOCKER-USER` chain integration is preserved; no `iptables -F` semantics on platform start.

### 3.6 Files / paths

- [ ] No mode > `0600` on secret-bearing files.
- [ ] `/etc/midnight/secrets.key` is `0400 midnight:midnight`.
- [ ] No `O_TRUNC` writes to dynamic config; use temp-file + atomic rename on the same filesystem.
- [ ] No `os.MkdirTemp` in `/tmp` for sensitive data; use `/var/lib/midnight/` paths with explicit ownership.

### 3.7 Containers / Docker

- [ ] No tenant container uses host PID, host network, host IPC, privileged mode, arbitrary devices, or arbitrary host bind mounts.
- [ ] `no-new-privileges=true` set.
- [ ] Cap drop-all + explicit allowlist (`CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `FSETID`, `KILL`, `SETGID`, `SETUID`, `NET_BIND_SERVICE`, `SETPCAP`, `SYS_CHROOT`).
- [ ] PIDs limit, memory hard limit, CPU quota set.
- [ ] `HostConfig.Init = true` (tini) for platform-managed containers.
- [ ] No mount of `/var/run/docker.sock` into anything except `midnight-agent`.

### 3.8 Audit / events

- [ ] Every state-changing action emits an audit event participating in the hash chain.
- [ ] Audit fields: `request_id`, `correlation_id`, `actor`, `actor_type`, `auth_method`, `source_ip`, `user_agent`, `resource`, `action`, `before`, `after` (where safe), `policy_decision`, `timestamp`.
- [ ] CloudEvents lifecycle event emitted for resource changes.

### 3.9 Idempotency

- [ ] `POST/PATCH/DELETE` requires `Idempotency-Key`.
- [ ] Replay returns identical body + `Idempotent-Replayed: true`.
- [ ] Concurrent in-flight returns `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` with `Retry-After`.
- [ ] Same key + different body returns `409 IDEMPOTENCY_KEY_REUSED`.
- [ ] `idempotency_key.expiry >= operation.expiry`; operation GC forbidden before key GC.

### 3.10 Supply chain (for changes to release/CI)

- [ ] `govulncheck`, `osv-scanner`, `npm audit --omit=dev` are required checks on PRs.
- [ ] Lockfiles required; npm packages pinned by integrity hash.
- [ ] PRs from forks do not get repo secrets and do not push images.
- [ ] Release artifacts built only from protected branches.
- [ ] cosign signatures, SBOM, in-toto SLSA provenance attached to releases.
- [ ] APT signing key rotation, fingerprint, expiry, revocation procedure published.

### 3.11 AI features (when shipped)

- [ ] AI features are off by default.
- [ ] Workspace policy decides allowed providers.
- [ ] Per-user/model rate limits, context size caps, monthly cost budget enforced.
- [ ] No secret plaintext in AI context.
- [ ] Mutating production actions require human approval out-of-band.
- [ ] Eval gates (§ Quality and Test Strategy § 5.7) are green.
- [ ] AI tools cannot bypass RBAC, policy, redaction, audit, or human approval.

---

## 4. Pen-test cadence (recommended)

| Phase | Cadence | Scope |
| --- | --- | --- |
| Pre-Phase 0 release | One internal review pass by the security lead and one rotating non-team reviewer | Bootstrap admin, KEK, secret store, ingress/ACME, agent → Docker API, audit chain |
| Phase 0 release | Targeted external pen-test of the published Alpha (3–5 day engagement). Scope: install path, bootstrap, deploy/rollback, ingress/ACME, support-bundle redaction, audit verify | Findings categorized P0–P3; P0 blocks Alpha tag. |
| MVP Beta release | External pen-test (5–10 day) covering everything in Alpha plus: GitHub App, custom domains, sealed variables, off-host backup, image signing, KEK rotation, upgrade transaction | P0 blocks; P1 must have shipped fix or accepted-risk ADR. |
| v1 Foundations release | External pen-test (10–15 day) covering everything in MVP Beta plus: OIDC, quotas/retention, plan/dry-run/import/drift, scheduled backups + sample restore, OTLP/log-drain, MCP server, AI gateway if shipped | P0 blocks; published findings + remediation as part of release notes. |
| Post-v1 | Annual external pen-test plus a continuous bug-bounty program (scope, eligibility, payout schedule published in `SECURITY.md`). |

Findings flow into:

- Story tracker (`D:/projects/midnight/agile/`) with severity label.
- New negative tests (unit/integration).
- New redaction fixtures if secret-leak related.
- New drills if a failure mode was discovered.
- ADR if a structural fix is needed.

---

## 5. Vulnerability disclosure policy reference

The repository ships a `SECURITY.md` at the root with:

- Contact: `security@midnight.dev` (placeholder until project name is finalized).
- PGP public key fingerprint and link to the key.
- Scope: what is in-scope for disclosure (the Midnight repo and signed releases) and what is not (forks, third-party templates, operator-modified deployments).
- Process: report → ack within 72h → triage within 7 days → fix targeted within 90 days → coordinated disclosure → CVE if applicable → release advisory in `docs/security-advisories/`.
- Safe-harbor language for good-faith research.
- Bug-bounty information (when active).
- Hall-of-thanks list.

Aligned with the panel decision (`spec` line 1268): "MVP should publish security contact, vulnerability disclosure policy, and secure release process."

---

## 6. Per-feature mandatory reviewers

The list below maps the feature dossier IDs (which PM-2 is producing under `D:/projects/midnight/agile/features/`) to mandatory reviewers. When a feature dossier is created, this list is the source of truth for which security reviewer must approve PRs against that feature.

If a feature ID listed below does not yet have a dossier, it still must be reviewed by the named reviewer when stories appear in sprints.

| Feature ID (placeholder) | Surface | Mandatory reviewer |
| --- | --- | --- |
| FEAT-AUDIT-HASH-CHAIN | per-row hash chain, daily checkpoint signing, `midnight audit verify` | Security lead |
| FEAT-BOOTSTRAP-ADMIN | first-start token, one-shot file, MFA enrolment | Security lead |
| FEAT-KEK-LIFECYCLE | KEK creation, rotation, rewrap, retention, KEK-loss drill | Security lead + storage lead |
| FEAT-SEALED-VARS | no-reveal invariant, rotation, audit/snapshot elision | Security lead |
| FEAT-REFERENCE-VARS | cross-scope authz at create + resolve, cross-tier denial | Security lead + control-plane lead |
| FEAT-IMAGE-SIGNING | cosign verify on pull/start, SBOM, in-toto SLSA, `IMAGE_SIGNATURE_INVALID` | Security lead + build/supply-chain lead |
| FEAT-BUILDKIT-CONTAINMENT | BuildKit unprivileged user, cgroup cap, dedicated tmpfs, no read access to platform paths | Security lead + build/supply-chain lead |
| FEAT-UNTRUSTED-BUILD-CLASS | fork PR / non-member / non-trusted-branch classification, ephemeral cache, quarantine path | Security lead + build/supply-chain lead |
| FEAT-INGRESS-ACME | dynamic config atomic rename, `/.well-known/acme-challenge/*` reservation, port-80 reachability prober, CAA preflight, throttled restore | Networking lead + security lead |
| FEAT-DNS01-PROVIDERS | typed `DnsProvider` interface, Cloudflare/Route53/manual, credential isolation | Networking lead + security lead |
| FEAT-WEBHOOK-INGEST | signature verify, replay, dead-letter, idempotency, signed-webhook secret rotation | Security lead + control-plane lead |
| FEAT-WEBHOOK-DELIVERY | outbound HMAC signing, two-key 30-day rotation, replay window 300s, kid-rotated `v1` | Security lead + networking lead |
| FEAT-SSRF-CLIENT | single shared SSRF-safe HTTP client, hostname-resolved-once, IP-pinned connection | Security lead |
| FEAT-DASHBOARD-AUTH | `__Host-` cookies, CSRF, Origin check, CSP, HSTS, COOP, logout-everywhere | Security lead + frontend lead |
| FEAT-CLI-AUTH | OAuth 2.0 device authz / PKCE, OS keychain storage | Security lead + frontend lead |
| FEAT-AI-GATEWAY | context assembly, redaction passes, tool broker, eval gates, kill switch | Security lead + AI lead |
| FEAT-MCP-SERVER | service-account scoping, mandatory idempotency, Class 3+ approval, rate + cost budget | Security lead + AI lead |
| FEAT-REGISTRY-GC | reference-counting, dry-run, full GC quiesce/lock/post-verify | Security lead + storage lead |
| FEAT-BACKUP-RESTIC | restic password sealed by KMS, operator passphrase for KEK backup, off-host destination | Storage lead + security lead |
| FEAT-VOLUME-OWNERSHIP | UID/GID `chown` on first mount, `userns-remap`, `OWNERSHIP_CONFLICT` | Storage lead + Linux runtime lead |
| FEAT-DOCKER-DAEMON-BASELINE | installer-enforced `daemon.json`, conflict refusal, AppArmor profile, `cgroup v2` preflight | Linux runtime lead + security lead |
| FEAT-AGENT-SPEC-VALIDATION | narrowed Docker spec allowlist, rejection of unknown options, capability allowlist | Linux runtime lead + security lead |
| FEAT-POSTGRES-ROLES | `midnight_app`, `midnight_migrate`, `midnight_audit_ro` separation, `pg_hba.conf` | Security lead + control-plane lead |
| FEAT-IDEMPOTENCY | TTL, body hash, in-flight lock, key-vs-operation expiry ordering | Control-plane lead + security lead |
| FEAT-OPERATIONS | `Operation` resource, polling, SSE events, `Last-Event-ID` reconnect | Control-plane lead |
| FEAT-DEPLOYMENT-FSM | state matrix, `pg_advisory_xact_lock`, monotonic supersede sequence, cancel safety | Control-plane lead |
| FEAT-RESTORE-PLATFORM | platform restore order, KEK + Postgres + ACME + registry + tenant volumes, throttled ACME reissue | Storage lead + security lead + release/SRE lead |
| FEAT-UPGRADE-TX | snapshot → migrate → smoke → auto-rollback, skip-version refusal | Release/SRE lead + security lead |
| FEAT-SUPPORT-BUNDLE | redaction preview, manifest, opt-in upload | Security lead + release/SRE lead |
| FEAT-OIDC-LOGIN (v1) | PKCE + nonce + state, MFA delegation, break-glass | Security lead |
| FEAT-PR-PREVIEWS (v1) | trust class, no production secrets, no production-data clone, scrub policy | Security lead + control-plane lead |
| FEAT-OTLP-EXPORT (v1) | attribute allowlist, redaction, retention, scope by workspace/project | Security lead + observability lead |

The mandatory reviewer column is binding. A PR for a feature on this list cannot merge without the listed reviewer's approval, even if other reviewers have approved.

---

## 7. Continuous review activities

Beyond per-PR review, the security lead owns:

- Quarterly review of the panel-locked decisions list. Spot any drift between code and decisions; raise stories.
- Quarterly review of the redaction fixture set. Ensure new secret classes have fixtures.
- Quarterly review of the error-code registry. Ensure new failure modes from production have codes.
- Quarterly review of the runbook set. Ensure runbooks remain accurate and walkthrough-tested.
- Sprint-cadence triage of `govulncheck`/`osv-scanner` findings.
- Weekly review of release-key access logs and signing-job audit.
- After each pen-test or external report, a written closure document linking findings to tests, fixtures, drills, or ADRs.

---

## 8. What we do NOT have in scope (be explicit)

The panel made these out-of-scope; do not let scope creep silently:

- SAML SSO, SCIM provisioning, LDAP/AD direct auth, BYOK / customer-managed KMS, native SIEM/ITSM connectors, enterprise support SLAs, formal SOC 2 / ISO 27001 evidence automation. (`spec` line 46.)
- Public hosted multi-tenancy with malicious tenants. (`spec` lines 901–908.)
- SLSA Build Level claims without audited controls. (`spec` line 80.)
- Mutating AI agents. (`spec` lines 38, 1646.)
- Full disconnected install bundle. (`spec` line 2188.)
- Public TCP exposure of databases by default. (`spec` lines 1063, 1253.)
- Production database cloning into PR previews. (`spec` line 1850.)

If a customer ask points at any of the above, the answer is: "v1 Foundations or later, see panel decision."
