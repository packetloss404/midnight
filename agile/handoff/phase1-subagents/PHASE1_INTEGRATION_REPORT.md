---
id: PHASE1-INTEGRATION-REPORT
status: integrated
owner: PM-A / PM-B
deciders: [PM-A, PM-B]
related_decisions: [P0-D004, P0-D017, P0-D022]
date: 2026-04-27
---

# Phase 1 Integration Report

This is a single readable consolidation of the Phase 1 lane status
documents (`LANE1_STATUS.md`, `LANE2_STATUS.md`, `LANE3_STATUS.md`,
`LANE4_STATUS.md`, the Lane 5 wireup landed inside
`SPRINT1_FINISH_STATUS.md`, and the Sprint 1 finish itself). It is
meant to be read top-to-bottom as the post-integration walk-through;
the individual lane docs remain authoritative for per-lane specifics.

Tip of `main` after all merges: `3bd9798 Merge branch
'phase1/sprint1-finish'`. All gates green: `go fmt / vet / build /
test`, `go run ./tools/lint`, `go run ./tools/phase0-gate`.

## 1. Lane 1 — DATA (`phase1/lane1-data`)

**Merge commit on `main`:** `edaf996`.

**What landed.** `internal/db/pg/` Postgres seam (`dsn.go`, `pool.go`,
`tx.go`, `migrate.go`) with `MigrationRunner` that applies migrations
inside advisory-locked transactions and computes per-file SHA-256
digests; Postgres-backed stores for operations, resources (project /
environment / service), idempotency, audit chain, timeline, and queue
(`FOR UPDATE SKIP LOCKED` dequeue + lease/heartbeat lifecycle); cursor
signing seam (`internal/api/cursor_secret.go`) backed by a file or env
var; `migrations/0001_alpha_core` extended with the resource graph,
audit chain, operation events, panel-locked job/idempotency columns,
and hot-path indexes; `DataStack` + `NewRouterWithDataStack` wiring
the live handlers for operations, projects, services, environments,
audit, timeline, and migrations.

**Deferred.** Hosts/host_heartbeats Postgres stores (Lane 2's write
path); `release_evidence` / `events` partition handling (later sprint).
The audit verifier CLI is now implemented by Lane 4 and covered by the
greenlight evidence.

**Open questions resolved or accepted.** Go directive bumped to
`1.25.0` (driven by pgx v5.7.6 + testcontainers v0.35.0 transitive
`golang.org/x/sys` v0.42.0); `idempotency_keys.operation_id` ships as
plain `REFERENCES … ON DELETE SET NULL` (the panel-locked
`DEFERRABLE INITIALLY DEFERRED` semantic is a follow-up if needed).

## 2. Lane 4 — SECURITY + AUDIT (`phase1/lane4-security`)

**Merge commit on `main`:** `7197fa2`.

**What landed.** `internal/secrets/` envelope encryption with a
`KeyProvider` interface and a `MemoryKeyProvider` /
`NewFileKeyProvider`; AES-256-GCM seal/open with crypto/rand 96-bit
nonces and AAD-bound variable id; `Vault` + `SealedVariable` enforcing
no-reveal semantics; `CreateReference` / `ResolveReference` with
cross-tier authz (`AllowsTierEdge` + `CanRead` + cycle detection);
`RotateKEK` re-wrapping every persisted envelope.
`internal/audit/` packages the chain math (`canonical.go` RFC-8785,
`chain.go` SHA-256 over `prev_hash || canonical_row`, panel-locked
`GenesisHash() = SHA-256("midnight-audit-v1")`), Ed25519 PEM signer
(`keys.go`), `Checkpointer.Sign`, `Verify(ctx, source, keys)`,
streaming JSONL `Exporter` with trailing manifest, and `DrainPlan`
validator. CLI `cmd/midnight/audit_verify.go` ships the verifier with
the panel-locked exit codes (`0/1/2/3`) plus sysexits `64`/`66` for
usage / missing input. `internal/cloudevents/` for the panel-locked
extensions (`traceparent`, `midnightrequestid`, `midnighttenantid`).
`internal/webhook/` for Midnight outbound signing
(`Midnight-Signature: t=,v1=,kid=`, ±300s replay window, multi-key
rotation overlap), inbound dedupe, and the unified Receiver
(signature → replay → dedupe → CloudEvents decode → audit emit →
dispatch). `internal/integrations/github/` for the GitHub App
manifest validator + push-event parser + RequiredCheckPolicy with the
`DEPLOY_GATE_PENDING_CHECKS` reason. `internal/promotion/` for
environment locks (`ErrLockHeld`, audit emit on every transition) and
the append-only artifact-promotion `Ledger`. New migrations:
`0002_environment_locks`, `0003_artifact_promotions`.

**Cross-platform fix.** Workspace-wide `.gitattributes` pinning Go,
YAML, JSON, SQL, MD to LF. The pre-existing CRLF/LF mismatch in
`internal/router/testdata/` was repaired post-merge by re-checking out
the testdata under the new attributes rule.

**Deferred.** GitLab event parser (Phase 2); AWS KMS / GCP KMS / Vault
adapters (Phase 4); audit drain to Splunk HEC / Elastic (Phase 2 §2.10);
`midnight audit export` CLI subcommand (Lane 1 follow-up); live
`--from-db` audit verify (Lane 1 ships the Postgres `AuditChain` —
shipped in Lane 5 wireup).

**P0-D017 closure.** Lane 4 proposed the exit-code envelope inline.
That proposal is now ratified by `agile/handoff/governance/
AUDIT_VERIFY_EXIT_CODES.md`.

## 3. Lane 2 — RUNTIME (`phase1/lane2-runtime`)

**Merge commit on `main`:** `52858d6`.

**What landed.** `internal/runtime/docker/` Docker adapter against the
Engine API floor v1.43, with full `Schedule`, `Start`, `Stop`,
`Remove`, `Drain`, `Reconcile`, `StreamInstanceStatus`,
`ApplyNetworkPolicy`, `ResolveRoutingTarget`, and `PullVerify`
(idempotent on retry; reconciles missing containers as `LOST`).
Container hardening from ADR-004: `HostConfig.Init=true`,
`no-new-privileges`, `CapDrop=[ALL]` + panel-locked `CapAdd`, default
ulimits, `PidsLimit`, json-file logging with caps. `daemon.json`
baseline enforcement (`baseline.go`) refusing on conflict with
`DOCKER_DAEMON_BASELINE_CONFLICT`; `cgroup.go` hard-fails cgroup v1
with `CGROUP_V2_REQUIRED` and the panel-locked remediation string.
Conformance suite passes the Docker adapter (`adapter_test.go` calls
`conformance.Run` against a fake EngineClient); a build-tag
`integration` test exercises schedule → start → reconcile → stop →
remove against a real daemon. `pkg/agentapi/` introduces the shared
wire DTOs + `HeartbeatVerifier`. `cmd/midnight-agent/internal/control/`
runs register → heartbeat → reconcile → status over HTTPS + HTTP/2 +
HMAC, with mTLS-ready config plumbing. Server-side
`internal/api/agent_handlers.go` exposes `RegisterAgentHandlers(mux,
registry)` for the four panel-locked endpoints. New migrations:
`0004_agents.up.sql` (renumbered from Lane 2's original `0002` because
Lane 4 took 0002/0003).

**Deferred.** Server-side mTLS enforcement (HMAC heartbeat is the
floor); image-pull credential plumbing (Lane 4 secrets envelope hook);
health-probe runner (instance state mapping is in place); bridge
`NetworkCreate` in `ApplyNetworkPolicy`.

## 4. Lane 3 — BUILD + INGRESS (`phase1/lane3-build-ingress`)

**Merge commit on `main`:** `01d2ac7`.

**What landed.** `versions.yaml` extended with `buildkit: v0.13.2`,
`distribution: v3.0.0`, `traefik: v3.1.5`, `lego: v4.17.4`.
`cmd/midnight-buildkit/` shells out to a pinned `buildctl` and
emits deterministic SBOM (CycloneDX 1.5) + provenance (SLSA v1 / in-
toto v1) + digest + log artifacts. `cmd/midnight-registry/`
fork-execs the pinned `registry` binary and persists a JSON-backed
`FilePinStore` (`0600`, atomic rename, schema-versioned) with a GC
orchestrator that consults the pin store before any deletion and
calls `WritePauseController.PauseWrites` around apply.
`internal/router/publication_dir.go` introduces
`TraefikDynamicPublicationDirectory` — the typed wrapper for the
panel-locked Traefik file-provider directory contract; `PublishRouteGroup`
no-ops on hash-equal renders. `deploy/traefik/traefik.yaml` is the
panel-locked static config (60s read/write timeouts, 180s idle, 32MiB
max body, file provider, two LE resolvers — staging default + prod —
Prometheus, **no** Docker provider). `internal/acme/lego_user.go` +
`lego_issuer.go` implement the live ACME issuer over `go-acme/lego`
v4.17.4, with an explicit allow-list of `cloudflare` / `route53` /
`manual` providers and atomic state writes under `/var/lib/midnight/
acme/`. Pre-existing CRLF flake in `TestRenderTraefikDynamicV2GoldenFiles`
fixed by per-test newline normalization (Lane 4's `.gitattributes` is
the structural fix).

**Decisions of note.** Shell out to `buildctl` rather than import
`github.com/moby/buildkit/client` (multi-MB transitive expansion).
Fork-exec the `registry` binary rather than embed
`github.com/distribution/distribution/v3` (drags in OCI image-spec,
Azure / GCS / Swift / OSS storage drivers, vendored runc). Lego DNS
provider lookup restricted to `cloudflare` / `route53` / `manual`
(importing `github.com/go-acme/lego/v4/providers/dns` would activate
every DNS provider on lego's compatibility list).

**Deferred.** §1.6 custom domains TXT verification + HTTP-01;
§1.7 Postgres template + `pg_dump` backup hooks (depends on Lane 1);
§1.14 Railpack builder integration; §1.15 cosign signing + signed `.deb`
(picked up by Sprint 1 Sub-slice D); `pgsql` migration for
`deployment_image_pins` (Lane 1 follow-up).

## 5. Lane 5 wireup (folded into Sprint 1 finish)

**Merge commit on `main`:** `4eb18f5` (`Merge phase1/lane5-wireup`).

**What landed.** Postgres-backed `audit.AuditChain` (the live
implementation Lane 4's `AuditChain` interface was the seam for) plus
`0006_audit_chain_columns` migration. `RegisterAgentHandlers` and
`RegisterWebhookHandlers` are now wired from `internal/api/router.go`
via the DataStack — Lane 4 had shipped both registration functions
with TODO markers; Lane 5 wired them in. `0005_webhook_deliveries`
migration + Postgres-backed dedupe store replaces Lane 4's in-memory
`MemoryDedupeStore`. Audit rows now emit from every mutating
handler. Seven typed failure codes that Lane 4 had referenced but
deferred from `docs/error-codes.md` (the alphabetical-order lint
guard) are added by `b2ab788`.

## 6. Sprint 1 finish (`phase1/sprint1-finish`)

**Merge commit on `main`:** `3bd9798`.

Four sub-slices landed in series:

### 6.1 Sub-slice A — doctor + .deb + bootstrap admin first-login MFA

Commits: `2fa26e4` (live `midnight doctor` with OR-CHK-DOC registry),
`454ec55` (.deb packaging + Make/Task targets), `389ec94` (bootstrap
admin first-login MFA + `0007_bootstrap_admin`).

`internal/doctor/live.go` declares OR-CHK-DOC-001..009 (P0-D005 stable
ids) and threads `HostSource` fixtures so default `go test` needs no
Docker/network. `cmd/midnight-doctor` adds `--json`, `--check=ID,...`,
`--fix=safe|all` diagnosis/remediation output; the actual fix runner is
a Phase-2 follow-up. `deploy/debian/` adds `control`, `postinst`,
`prerm`, `postrm`, `rules`, `compat`, `changelog`, package defaults,
cursor-key and registry-config provisioning, with `Depends docker-ce >=
24.0` and `Breaks docker.io`; `postinst` refuses on `daemon.json`
conflict (0.1-EC4), applies embedded Postgres migrations when a DSN is
configured, and gates the install with `midnight-doctor --json`.
`internal/bootstrap/` ships stdlib-only RFC 6238 TOTP (HMAC-SHA1, 30s
window, ±1 skew, byte-exact against the RFC's Appendix B test
vectors), PHC-style PBKDF2-SHA256 password hash (600k iters / 16-byte
salt / 32-byte key, 12-char minimum), `midn_boot_<base64url>`
bootstrap-token at `0600`, `ConsumeBootstrapToken` deletes on read
(0.1-EC5), and the `SetPassword → EnrollTOTP → ConfirmTOTP` flow FSM
sealing the TOTP secret through Lane 4's `secrets.SealForVariable`.

**Deferred.** `--fix` runner. `midnight admin migrate` now opens the
configured DSN/DSN file and applies the embedded migration manifest; it
is no longer a report-only shell.

### 6.2 Sub-slice B — §1.8 off-host backup destination

Commit: `5800bc4`.

`internal/backup/s3_destination.go` implements the panel-locked
Destination contract against any S3-compatible endpoint via
`github.com/minio/minio-go/v7` (chosen over `aws-sdk-go-v2/service/s3`
for smaller surface and native MinIO + R2 + Wasabi support).
`ParseS3URI` accepts `s3://<bucket>/<prefix>?endpoint=...&region=...&style=...`.
`internal/backup/offhost_alert.go` evaluates `BACKUP_OFFHOST_MISSING`
(warn) when no destination URI is configured and `BACKUP_STALE`
(critical) when the latest backup is older than the threshold (default
26h). MinIO testcontainer integration test compiles and runs cleanly
under `go test -tags=integration`.

**Deferred.** Lane 4 KeyProvider integration for backup credentials —
follow-up swaps env-var bootstrap for sealed-variable read once the
Phase 2 secret-store CLI lands. Restic-style per-project encryption
is out of scope here.

### 6.3 Sub-slice C — §1.9 volumes + supersede arbitration

Commit: `8eb9b7c`.

`internal/volume/` ships the typed Volume + Attachment models, the
status FSM (provisioning → ready → attached → soft_deleted → deleted)
with 7-day soft-delete grace, `SingleWriterAdvisoryKey(svc, env)`
returning the canonical `pg_advisory_xact_lock(hashtext(...))` key
string, `ArbitrateSupersede` picking the highest monotonic
`deployment_sequence` and marking the rest `DEPLOY_SUPERSEDED` with
lexicographic tie-break, and `quota.go` classifying the host fsType +
mount-options pair into `ProjectQuotaReady=true` on xfs+prjquota /
ext4+quota (everything else surfaces `VOLUME_QUOTA_UNSUPPORTED`).
`internal/runtime/docker/volumes.go` extends Lane 2's adapter with the
Volume{Create,Inspect,Remove,List} surface; the Phase 1 unit test path
passes without volume support, the integration-tagged test exercises
the live engine. New migrations: `0008_volumes`.

**Deferred.** The `pg_advisory_xact_lock` Go-side wrapper is the
key-string shape only; the actual `tx.Exec(...)` lives in the deploy
orchestrator (Lane 1 follow-up once the `deployments` table is
migrated). Lane 1 also owns syncing
`volume_attachments.sequence` with `deployments.deployment_sequence`.

### 6.4 Sub-slice D — §1.15 signed releases + SBOM/provenance + image-sig verify

Commit: `239e3e3`.

`.github/workflows/release.yml` is the tag-driven release pipeline:
builds the .deb via `make package-deb`, optionally GPG-signs via
debsign / dpkg-sig when `MIDNIGHT_RELEASE_GPG_KEYID` is set, builds +
pushes every component image to GHCR, signs them keyless via Sigstore
Fulcio (`cosign sign --yes`), promotes Lane 3's BuildKit-emitted
CycloneDX SBOM + SLSA-style provenance, and uploads the bundle as a
14-day retained artifact. `tools/release-verify/main.go` is the
operator-facing harness asserting .deb signatures, cosign signatures,
SBOM, and provenance, with `--allow-unsigned-deb` honoring the
`v0.1.0-alpha-1` exception. `internal/runtime/docker/signature_verify.go`
adds the agent-side `SignatureVerifier` seam with
`StaticSignatureVerifier` (stdlib-only allowlist) as the Phase 1
default; `Adapter.EnableSignatureEnforcement(verifier)` wires it on,
and absent that call the agent behaves exactly as before. New typed
failure code: `IMAGE_SIGNATURE_REQUIRED`.

**Deferred.** sigstore/cosign Go SDK integration (multi-MB transitive
dep set; PM-B / Security to decide between binary fork-exec, Go SDK
import, or .deb-bundled cosign — the question is now logged in
`agile/handoff/governance/V010_ALPHA_GREENLIGHT_BRIEF.md` Section 4
blocker (3)). Release-key custody — the CI workflow expects two
secrets (`MIDNIGHT_RELEASE_GPG_KEYID`, `MIDNIGHT_RELEASE_GPG_KEY`)
that are populated only after the `RELEASE_KEY_CUSTODY.md`
operational checklist is co-signed.

## 7. Governance closure cross-links

The three Phase 0 deferred-decision closures landed on
`phase1/governance` (off `main` at `3bd9798`):

| Closure | Decision row | Document |
|---|---|---|
| Release-key custody | P0-D004 | `agile/handoff/governance/RELEASE_KEY_CUSTODY.md` |
| Audit verifier exit codes | P0-D017 | `agile/handoff/governance/AUDIT_VERIFY_EXIT_CODES.md` |
| Design-partner signoff counts | P0-D022 | `agile/handoff/governance/DESIGN_PARTNER_SIGNOFF_TEMPLATE.md` |

The single PM-B sign-off package for `v0.1.0-alpha` is
`agile/handoff/governance/V010_ALPHA_GREENLIGHT_BRIEF.md`. The
governance directory index is `agile/handoff/governance/README.md`.

## 8. Sprint-1 exit-criteria status

(Carried over from `SPRINT1_FINISH_STATUS.md`; canonical evidence
lives in `V010_ALPHA_GREENLIGHT_BRIEF.md` Section 2.2.)

| ID | Status |
|---|---|
| 0.1-EC1 | Pass (CI harness shipped; real-PR verification remains an operational evidence follow-up) |
| 0.1-EC2 | Pass (CI harness shipped; real-PR verification remains an operational evidence follow-up) |
| 0.1-EC3 | Pass |
| 0.1-EC4 | Pass |
| 0.1-EC5 | Pass |
| 0.1-EC6 | Pass (default lane) |
| 0.1-EC7 | Pass |
| 0.1-EC8 | Pass (by construction; CI harness produces the network-capture evidence) |

## 9. Greenlight conditions for `v0.1.0-alpha`

See `V010_ALPHA_GREENLIGHT_BRIEF.md` Section 4 for the canonical list.
Summary:

1. **Disposable-VM CI harness for 0.1-EC1/EC2** — workflows and
   `tools/vm-harness` are present; remaining operational evidence step
   is real-PR verification that both jobs go green.
2. **Backup credential boundary** — env-var posture is acceptable for
   `v0.1.0-alpha` / `v0.2.0-alpha`; sealed-variable posture is mandatory
   from `v0.3.0-alpha`; PM-B / Security approval was received per
   product owner / user confirmation on 2026-04-30.
3. **cosign Go SDK approval** — fork-exec of pinned `cosign` is the
   alpha/beta posture; PM-B / Security approval was received per
   product owner / user confirmation on 2026-04-30.
4. **First-protected-release custody execution** — `RELEASE_KEY_CUSTODY.md`
   operational checklist must be co-signed and an rc tag exercised before
   the final `v0.1.0-alpha` ships.
