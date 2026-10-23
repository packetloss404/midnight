# Midnight — Live Execution Checklist (Phase 1+ Engineering Lanes)

Source of truth: `midnight-platform-plan.md`. Companion: `agile/RELEASE_PLAN.md`, `agile/EPICS.md`.

This checklist began as the inventory of planner/contract code that needed live execution paths. As of the `v0.1.0-alpha` greenlight package, several Phase 1 engineering lanes have landed live implementations while the product phase remains Phase 0 Alpha with approvals/signoffs received per product owner confirmation on 2026-04-30. Treat older row text as the original gap statement and verify current status against `agile/handoff/phase1-subagents/PHASE1_INTEGRATION_REPORT.md` and `agile/handoff/governance/V010_ALPHA_GREENLIGHT_BRIEF.md`.

Legend:
- **Planner** — what is in `internal/...` today
- **Live path** — concrete external dependency, side effect, or integration that is not yet in the repo
- **Acceptance** — minimum integration test bar (real backend, not in-memory fixture)
- **Blocks** — downstream phases or epics gated on this slice

`go.mod` is no longer dependency-free; Phase 1 lanes introduced the approved Postgres, Docker, lego, MinIO, testcontainers, and crypto/network dependencies. This file is now a dependency-introduction and remaining-live-verification roadmap, not a refactor list.

---

## 0. Cross-Cutting Foundations (must land before any phase ships live)

These are not phase-specific; every other slice depends on them.

### 0.1 Postgres-backed persistence
- **Planner today:** `internal/api/resource_store.go`, `internal/api/operation_store.go`, `internal/audit/store.go`, `internal/idempotency/memory_store.go`, `internal/queue/lease.go`, `internal/timeline/store.go` are all in-memory.
- **Live path:** introduce `jackc/pgx/v5` (or `database/sql` + `lib/pq`); add `internal/db/pg/` with connection pool, migrations runner reading `migrations/`, transaction helpers, and per-store Postgres implementations behind the existing interfaces.
- **Acceptance:** integration test against a real Postgres 15+ instance; restart the process and observe state persisted.
- **Blocks:** every other Phase 1+ slice. Nothing real ships until this lands.

### 0.2 Real cursor signing & token issuance
- `internal/api/router.go:25` — `defaultCursorSecret = []byte("midnight-phase0-cursor-placeholder")`.
- **Live path:** secret loaded from envelope-encrypted config (see 1.4); rotation supported; tests cover decode failure and signature mismatch.

### 0.3 Migration manifest
- `internal/api/router.go:763` — `UpChecksum: "sha256:placeholder"`.
- **Live path:** real SHA-256 over migration files; `internal/db/migrations.go` already validates manifest shape — wire actual file reads.

### 0.4 Replace in-memory stores in API endpoints
- `internal/api/router.go` — 30+ handlers return `failurecodes.NotImplemented`. Inventory:
  - operations cancel/get/stream
  - project create/get/list
  - service create/get/list/update
  - migrations list/run/down
  - billing endpoints
  - dashboard endpoints
  - enterprise identity/audit/controls
  - isolation/build plan
  - release/disconnected-bundle
  - routing-HA readiness
  - config-code import/export
- **Live path:** each handler maps to a Postgres-backed store call + audit emit + idempotency check + operation record.

---

## 1. Phase 1 — Self-Hosted v1

Phase goal: usable production-adjacent self-hosted PaaS for one VM.

### 1.1 Live Docker Engine runtime
- **Planner today:** `internal/runtime/local/local.go` — "in-memory RuntimeAdapter that records intents without touching Docker."
- **Live path:** new `internal/runtime/docker/` using `github.com/docker/docker/client`; Docker Engine API ≥ v1.43; daemon.json baseline enforcement (overlay2, live-restore, no-new-privileges, log size caps); deterministic container names/labels.
- **Acceptance:** schedule → start → reconcile → stop → remove against a real Docker daemon on Ubuntu 24.04.
- **Blocks:** everything user-visible. Without this, no app ever runs.

### 1.2 BuildKit Dockerfile builds (pinned, local cache, no remote)
- **Planner today:** `internal/build/plan.go`, `internal/build/dockerfile.go`, `internal/build/provenance.go` — config validation and provenance shape only.
- **Live path:** `cmd/midnight-buildkit/` invokes a pinned BuildKit binary or container; per-service local cache; multi-arch opt-in via explicit `platforms:`; SBOM + provenance emitted to disk and indexed.
- **Acceptance:** build a sample Dockerfile, push to embedded registry, reproduce digest deterministically.

### 1.3 Embedded OCI registry + GC
- **Planner today:** `internal/registry/gc_machine.go`, `internal/registry/pin.go`, `internal/registry/retention.go` — state machine and pin counting in memory.
- **Live path:** `cmd/midnight-registry/` running `distribution/distribution`; protected-digest probe before GC; GC dry-run/apply with writes paused.
- **Acceptance:** push, pull, retention sweep, GC apply on a real registry; rollback pulls a stored digest (no rebuild).

### 1.4 Variables/secrets envelope encryption
- **Planner today:** `internal/secrets/` contains only `doc.go`.
- **Live path:** AES-GCM data keys wrapped by KEK; KEK rotation path; sealed variables shape; reference cross-scope authorization.
- **Acceptance:** rotate KEK, decrypt all variables, prove no plaintext on disk.

### 1.5 Traefik file-provider ingress + ACME
- **Planner today:** `internal/router/planner.go`, `internal/router/traefik.go`, `internal/acme/orchestrator.go` — generates JSON config and ACME plans in memory; planner only.
- **Live path:** atomic write of Traefik dynamic-config files to disk; Traefik watches the directory; ACME HTTP-01 + DNS-01 wildcard for `apps-domain` against Let's Encrypt staging then production; ACME-state restore throttled per LE rate limits.
- **Acceptance:** generated subdomain serves 200 OK with valid certificate end-to-end on a real VM.

### 1.6 Custom domains (TXT ownership, routing verify, ACME issuance)
- **Live path:** TXT verification probe; refuse routing before verification; ACME HTTP-01 issuance.
- **Acceptance:** add a real domain on a real DNS zone, verify, issue cert, route.

### 1.7 Postgres template + database-native logical backup
- **Planner today:** `internal/templates/database.go` — template marketplace metadata only.
- **Live path:** template that provisions a real Postgres container, uses `pg_dump` for logical backup, off-host destination upload (S3-compatible).
- **Acceptance:** create from template, backup, restore, smoke test.

### 1.8 Off-host backup destination
- **Planner today:** `internal/backup/destination.go:14` — `ErrNotImplemented` from `Put`/`Get`/`Delete`.
- **Live path:** S3-compatible client; signed URLs; `BACKUP_OFFHOST_MISSING` warning when destination unset; `BACKUP_STALE` raised when scheduled backup older than threshold.
- **Acceptance:** scheduled backup writes to MinIO/S3; restore from off-host succeeds.

### 1.9 Volumes + single-writer enforcement
- **Live path:** Docker volume create/attach/detach; `pg_advisory_xact_lock` per `(service, environment)` for single-flight; supersede arbitration via monotonic deployment sequence.
- **Acceptance:** two concurrent deploys produce one winner; loser cancels with typed reason code.

### 1.10 GitHub App + signed webhooks + environment locks + artifact promotion
- **Planner today:** `internal/webhook/sign.go` — HMAC validation only.
- **Live path:** GitHub App registration; signed webhook receiver with replay protection; required-CI status check; environment locks; artifact promotion records.
- **Acceptance:** open PR → preview → merge → promote to production blocked by lock until check passes.

### 1.11 Real audit hash chain export + verify
- **Planner today:** `internal/audit/chain.go`, `internal/audit/drain_plan.go` — chain math correct, store in-memory.
- **Live path:** Postgres-backed audit table with hash chain; JSONL export to off-host destination; `midnight audit verify` CLI walks the chain.
- **Acceptance:** tamper detection passes on chain break.

### 1.12 Health checks + structured failure classification
- **Live path:** real HTTP/TCP probes from agent; promotion guarded by readiness; failure → typed reason code → audit + timeline event.

### 1.13 Upgrade transaction
- **Planner today:** none — the placeholder `internal/upgrade/` package (doc.go only) was deleted as unreachable code.
- **Live path:** snapshot → migrate → smoke → rollback flow; refuse skip-version; `.deb` Depends/Breaks encode supported window.
- **Acceptance:** N-1 → N upgrade smoke test; intentional smoke failure rolls back cleanly.

### 1.14 Railpack builder integration
- **Live path:** invoke pinned Railpack binary; store resolved-plan-by-digest; audit on channel change.
- **Acceptance:** build a non-Dockerfile project end-to-end.

### 1.15 Signed releases + SBOM/provenance + image-signature verify
- **Live path:** cosign signing in CI; SBOM + SLSA-style provenance; signed `.deb` with GPG; release-verify doc.
- **Acceptance:** clean Ubuntu 24.04 + Debian 12 install verifies signature using published fingerprint.

### 1.16 Host agent ↔ control-plane real network
- **Planner today:** `cmd/midnight-agent/` exists; heartbeat is in-memory.
- **Live path:** mTLS over HTTP/2 or gRPC; registration, heartbeat, reconciliation; `VERSION_SKEW_REJECTED` on out-of-window agent.
- **Acceptance:** agent on a separate VM registers, heartbeats, reconciles a deploy.

### 1.17 Support bundle off-host upload
- **Planner today:** `internal/supportbundle/archive.go` — bundles to memory.
- **Live path:** zip to disk + redaction + optional off-host upload + signed URL for share.

### 1.18 Prometheus metrics endpoint + Grafana dashboard
- **Live path:** wire `prometheus/client_golang`; expose `/metrics`; ship Grafana dashboard JSON.

### 1.19 Failure drills harness
- **Planner today:** test fixtures present.
- **Live path:** drill harness runs against a real installation; emits evidence artifacts; gate release.

---

## 2. Phase 2 — Team & Reliability

### 2.1 OIDC/OAuth login
- **Planner today:** `internal/auth/policy.go` validates issuer/callback URL shapes.
- **Live path:** OIDC discovery fetch; PKCE flow; ID token verification using JWKS; session cookie issuance; logout.
- **Acceptance:** login against a real IdP (e.g., Keycloak in CI); session persists across restart.

### 2.2 Workspace/team model in Postgres
- **Planner today:** none — the unreachable in-memory `internal/workspace` store was deleted.
- **Live path:** Postgres-backed workspace, team, membership tables; transactional updates; cascade rules.
- **Acceptance:** invite user → join team → role evaluated via `internal/auth/policy.go` against DB-backed bindings.

### 2.3 Policy-as-code authoring/testing
- **Planner today:** none — the unreachable `internal/policy` package (a second, unused RBAC model) was deleted; live authorization lives in `internal/identity/access.go`.
- **Live path:** API + UI for editing policies; staged enforce mode; OPA/Rego embedding behind the existing decision envelope.
- **Acceptance:** edit a deny rule → run tests → flip to enforce → request denied with audit row.

### 2.4 Audit log UI/API
- **Live path:** paginated read API (cursor-based) over Postgres audit table; UI for filtering by subject/scope/action.

### 2.5 GitLab native integration
- **Planner today:** none — the unreachable `internal/gitlab` webhook validators and preview planners were deleted.
- **Live path:** GitLab OAuth app; MR webhook handler triggers preview env lifecycle; status reporting back to GitLab pipeline.
- **Acceptance:** open MR on real GitLab → preview env created → merge → preview destroyed.

### 2.6 PR preview environments (provider-neutral)
- **Planner today:** `internal/previews/planning.go` — pure planner with TTL math.
- **Live path:** trust/data/TTL rules enforced at preview create; secrets policy (no production inheritance for forks); auto-destroy.
- **Acceptance:** untrusted fork preview cannot read production secrets; TTL hits, env destroyed.

### 2.7 Config-as-code diff/apply
- **Planner today:** `pkg/configcode/` exists.
- **Live path:** `midnight config plan/apply`; diff against current state; idempotent apply; drift detection.

### 2.8 Agent drain/maintenance mode
- **Live path:** drain command relocates instances; maintenance flag refuses scheduling; reconciler honors both.

### 2.9 Terraform/OpenTofu provider
- **Planner today:** `providers/terraform/midnightprovider/` scaffold.
- **Live path:** real provider built with `terraform-plugin-framework`; CRUD against `/api/v1` for project/service/env/var/domain; published to registry.
- **Acceptance:** `terraform apply` provisions a project end-to-end.

### 2.10 SIEM / log-drain integrations
- **Planner today:** `internal/audit/drain_plan.go` validates drain shape.
- **Live path:** HTTP NDJSON drain with mTLS; backoff/retry; dead-letter; Splunk HEC and generic Elastic adapters.
- **Acceptance:** audit events flow to a real Splunk/Elastic in CI; replay after drain outage works.

### 2.11 Compliance evidence export
- **Live path:** scheduled NDJSON export of audit + deploys + policy decisions to off-host destination with retention metadata.

### 2.12 Read-only AI diagnosis
- **Live path:** redaction layer; per-workspace opt-in; budget controls; logs/repo content never sent without explicit opt-in; eval baselines.

### 2.13 Additional database templates
- **Live path:** template per officially supported DB (MySQL, Redis, etc.) **only after** support gates pass for each.

---

## 3. Phase 3 — Multi-Host & Adapters

### 3.1 Multi-host scheduling
- **Planner today:** `internal/scheduler/` exists; planning-only.
- **Live path:** scheduler picks host based on capacity, residency, anti-affinity; cross-host reconcile; host fail → reschedule.
- **Acceptance:** kill a host VM mid-deploy; instances reschedule to surviving hosts.

### 3.2 WireGuard private networking
- **Planner today:** `internal/networking/wireguard.go` — peer config struct only.
- **Live path:** install/configure `wg`/`wg-quick`; peer key rotation; `internal/networking/dns.go` programs internal DNS for service discovery.
- **Acceptance:** services on different hosts resolve and reach each other only over the mesh; traffic refused outside the mesh.

### 3.3 Internal DNS
- **Live path:** authoritative resolver for `service.environment.project.midnight.internal`; agents register on heartbeat.

### 3.4 TCP proxy
- **Planner today:** `internal/tcpproxy/plan.go` only.
- **Live path:** Traefik TCP routers programmed via file-provider; SNI routing; rate-limit hooks.
- **Acceptance:** route a non-HTTP TCP service through the public ingress.

### 3.5 Registry cache improvements
- **Live path:** pull-through cache per host; cache eviction policy; signed-image verification preserved.

### 3.6 Kubernetes runtime adapter
- **Planner today:** none — the in-memory `internal/runtime/kubernetes` planning adapter was deleted; the `internal/runtime/conformance` suite remains.
- **Live path:** `client-go`; CRDs for service/instance; reconciler maps `BuildArtifact -> ScheduleRequest -> InstanceStatus -> RoutingTarget`; passes `internal/runtime/conformance/` suite against a real cluster (kind in CI).
- **Acceptance:** subset of deployment-state acceptance suite green on real Kubernetes.

### 3.7 Volume attach/detach where backend allows
- **Live path:** Docker named volumes for Profile A; CSI for Kubernetes; refused gracefully where backend disallows.

### 3.8 Pulumi provider
- **Planner today:** `providers/pulumi/midnightprovider/` scaffold.
- **Live path:** real provider with `pulumi-go-provider`; published.

### 3.9 OIDC workload identity / federated CI tokens
- **Planner today:** `internal/auth/federated_ci.go` validates token shape.
- **Live path:** JWKS fetch + verify; trust GitHub Actions / GitLab CI / generic OIDC issuers; short-lived deploy tokens.
- **Acceptance:** GitHub Actions workflow deploys without long-lived secret.

### 3.10 Native Jenkins/Azure DevOps/Bitbucket adapters
- **Live path:** if demand exists. Skip without it.

---

## 4. Phase 4 — Hosted/Commercial Optionality

### 4.1 Multi-tenant hardening — live label enforcement
- **Planner today:** `internal/tenancy.Scope` validates labels.
- **Live path:** middleware on every mutating API call validates `midnight.workspace`/`project`/`environment` ownership against authenticated principal; runtime/build admit hooks call `tenancy.ValidateLabels`; cross-tenant resource read fails closed.
- **Acceptance:** authz matrix test proves no cross-tenant read or mutate possible.

### 4.2 Build isolation launcher (gVisor → Kata → microVM)
- **Planner today:** `internal/isolation/build.go` returns plans only.
- **Live path:**
  - gVisor: run BuildKit worker under `runsc`; private tenant cache; egress denied via netns.
  - Kata: same plan, Kata runtime instead of `runsc`.
  - microVM: Firecracker/Cloud Hypervisor; image build + launch + RPC; teardown.
- **Acceptance:** untrusted Dockerfile cannot read host filesystem or other tenants' caches; egress drops by default.

### 4.3 Durable metering store + replay cursor
- **Planner today:** `internal/metering/metering.go` aggregates events in memory.
- **Live path:** Postgres `usage_events` table; replay cursor per consumer; idempotent emit from API/scheduler/build/runtime sources; aggregator reads windowed.
- **Acceptance:** restart process mid-window, no double-count.

### 4.4 Plan-limit enforcement middleware
- **Planner today:** `internal/limits/limits.go` evaluates snapshots.
- **Live path:** middleware on schedule/build admission calls `limits.Evaluate`; over-limit returns typed reason code; audit emitted.
- **Acceptance:** push past plan → admission refused → audit row.

### 4.5 Live abuse controls
- **Planner today:** `internal/abuse/abuse.go` returns soft decisions.
- **Live path:** rate-limit token buckets; signal aggregation across recent activity; `throttle` and `require_review` enforced at API edge.
- **Acceptance:** burst attack throttled; review queue surfaces decisions.

### 4.6 Invoice generation + payment provider
- **Planner today:** none — `internal/api/billing.go` returns NotImplemented.
- **Live path:** Stripe (or equivalent) integration; invoice issuance from metered usage; webhook handler for payment events; customer portal route.
- **Acceptance:** monthly close generates correct invoice; failed payment dunning works.

### 4.7 Multi-region routing execution
- **Planner today:** `internal/networking/region_routing.go` returns plans.
- **Live path:** DNS provider client (Route53/Cloudflare) updates weights; Traefik dynamic config per region; live health probes feed back into plan; failover steps executed under operator approval or automatic with circuit breaker.
- **Acceptance:** simulated regional outage reroutes traffic within configured RTO.

### 4.8 HA control plane (Profile C) live
- **Planner today:** `internal/controlplane/ha/` rollup readiness.
- **Live path:** leader election (etcd or Postgres advisory locks); migration leader gate enforced; failover command + automated failover with quorum check; backup leadership; standby promotion.
- **Acceptance:** kill leader; new leader takes over within target; no split-brain writes.

### 4.9 Enterprise SSO live (OIDC)
- **Planner today:** `internal/enterprise/identity/` validates provider shape.
- **Live path:** OIDC discovery; assertion validation; group-to-role mapping persisted; session store; SLO endpoint.
- **Acceptance:** real IdP (Okta/Azure AD/Google) login provisions session with mapped role.

### 4.10 SAML
- **Live path:** SAML SP using `crewjam/saml`; ACS at validated path; signing-cert pinning; assertion replay protection.
- **Acceptance:** real IdP SAML login round trip.

### 4.11 SCIM provisioning
- **Live path:** SCIM 2.0 endpoints (Users, Groups, Bulk); token auth from `internal/enterprise/identity` config; create/update/disable propagated to workspace store.
- **Acceptance:** disable user in IdP → access revoked within SCIM sync window.

### 4.12 BYOK (customer-managed keys)
- **Planner today:** `internal/enterprise/controls.go` validates BYOK references.
- **Live path:** AWS KMS / GCP KMS / HashiCorp Vault clients behind a `KeyProvider` interface; data keys generated and wrapped by customer KMS; rotation; revocation makes data unreadable.
- **Acceptance:** revoke customer KEK → reads fail with typed reason; restore key → reads resume.

### 4.13 Audit export delivery
- **Planner today:** `internal/enterprise/audit/` validates export request.
- **Live path:** NDJSON export job to S3-compatible storage; signed download for `download` mode; HTTP/Splunk drain for `log_drain` mode; retention metadata enforced.
- **Acceptance:** export 30 days of audit, verify hash chain end to end at destination.

### 4.14 Disconnected install bundle tooling
- **Planner today:** `internal/release/disconnected_bundle.go` validates manifest.
- **Live path:** APT mirror tool (reprepro/aptly); OCI mirror (skopeo); cosign signing of bundle; offline docs build; `midnight verify-bundle` CLI.
- **Acceptance:** install on an air-gapped Ubuntu 24.04 VM using only the bundle.

### 4.15 Enterprise LTS
- **Live path:** `enterprise-lts` channel; `supported_until` honored in upgrade refusal; backports policy.
- **Acceptance:** LTS install refuses upgrade past supported window with `LTS_OUT_OF_SUPPORT`.

### 4.16 Support SLA workflow
- **Live path:** ticket intake (web/email); sev1–sev4 routing; on-call paging integration; response-time tracking against `internal/enterprise.ControlsPlan` SLA targets.

### 4.17 DPA / subprocessor / data residency
- **Live path:** DPA acceptance recorded per workspace; subprocessor list URL served; 30-day notice mailer; residency enforced at scheduling (4.7) and storage layer.

---

## 5. Suggested Sequencing

If the goal is shipping rather than completing in plan order, sequence by what unblocks the most:

1. **0.1 Postgres-backed persistence** — unblocks every other live slice
2. **1.1 Live Docker** — unblocks the first product loop demo
3. **1.5 Traefik + ACME** — unblocks public HTTPS
4. **1.2 BuildKit** — unblocks deploy-from-source
5. **1.16 Agent network** — unblocks multi-host (3.1, 3.6)
6. **1.4 Secrets envelope encryption** — unblocks 2.x audit and 4.12 BYOK
7. **1.10 GitHub App + signed webhooks** — unblocks 2.5 GitLab and 2.6 previews
8. **1.11 Audit hash chain in Postgres** — unblocks 2.10 SIEM, 4.13 export
9. **2.1 OIDC login** — unblocks every multi-user scenario
10. **2.9 Terraform provider** — unblocks provider-grade automation claim
11. **3.6 Kubernetes adapter** — unblocks platform/SRE persona
12. **4.3 Durable metering** — unblocks every commercial claim
13. **4.8 HA control plane** — unblocks hosted offering

Anything below 4.3 is unsellable as a hosted product without metering.

---

## 6. Definition of "Phase Complete"

A phase is complete when, for every row in its section:

- The live path is implemented behind the existing planner interface.
- An integration test runs against the real backend (not in-memory) in CI.
- The `internal/runtime/conformance/` suite (or equivalent for the slice) passes.
- A failure drill exists for the slice and is in the drills harness.
- A runbook entry exists for operator-visible failures.
- An audit event is emitted for any state-changing call.
- OpenAPI is updated and `oasdiff` is clean.
- A design-partner (or for Phase 4, a hosted-pilot customer) has run the slice on real infrastructure and signed off.

Today, zero rows in this checklist meet that bar.
