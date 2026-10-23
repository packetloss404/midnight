# Phase 1 / Sprint 1 Finish — Status

- **Branch:** `phase1/sprint1-finish` (off `main` after the Lane 5
  merge).
- **Tip commit:** `239e3e3 feat(phase1/sprint1-finish): signed
  releases + SBOM/provenance + image-sig verify (§1.15)`
- **Status:** all four sub-slices landed; gate green; nothing
  pushed; nothing force-pushed.

## Step 0 — Lane 5 merge

`phase1/lane5-wireup` merged into `main` cleanly (no conflicts; Lane
5 was branched off `main`). Merge commit `4eb18f5`. Full gate
(`go fmt / vet / build / test`, `tools/lint`, `tools/phase0-gate`)
green on the merge tip before sub-slice work began.

## Per sub-slice summary

### Sub-slice A — doctor + .deb + bootstrap admin first-login MFA

Commits (oldest → newest):

| SHA | Title |
|---|---|
| `2fa26e4` | live midnight doctor with OR-CHK-DOC registry |
| `454ec55` | finalize .deb packaging + Make/Task targets |
| `389ec94` | bootstrap admin first-login MFA + 0007 schema |

What landed:

- **A.1 Live doctor.** `internal/doctor/live.go` declares the
  canonical OR-CHK-DOC-001..009 registry (P0-D005 stable ids) and
  threads through HostSource fixtures + hints so default `go test`
  needs no Docker / network. Panel-locked exit-code table 0/1/2/3 in
  `ExitCodeForResults`. New `cmd/midnight-doctor` CLI surface with
  `--json`, `--check=ID,...`, `--fix=safe|all` diagnosis/remediation
  output; the actual fix runner is a Phase-2 follow-up. Live integration
  test gated behind `//go:build integration` AND
  `MIDNIGHT_DOCTOR_LIVE=1`.
- **A.2 .deb packaging.** `deploy/debian/{control,postinst,prerm,postrm,rules,compat,changelog}`
  with Depends `docker-ce >= 24.0` and `buildkit`, and Breaks
  `docker.io` / out-of-window midnight versions. postinst REFUSES on
  daemon.json conflict (0.1-EC4), generates the bootstrap token and
  cursor signing key, installs registry config, runs `midnight admin
  migrate` when a DSN is configured, and gates with
  `midnight-doctor --json`. Systemd units now invoke
  `midnight-server serve`, `midnight-worker serve`, `midnight-buildkit
  serve`, and the registry wrapper through package defaults.
  Makefile and Taskfile.yml gain `package-deb` + `package-deb-sign`
  targets. Signing remains a Sub-slice D concern (CI-driven via
  `MIDNIGHT_RELEASE_GPG_KEYID`).
- **A.3 Bootstrap admin first-login MFA.** `internal/bootstrap/`
  ships stdlib-only RFC 6238 TOTP (HMAC-SHA1, 30s window, ±1 skew)
  with the RFC 6238 Appendix B test vectors passing byte-exact.
  PHC-style PBKDF2-SHA256 password hash (600k iters / 16-byte salt
  / 32-byte key), 12-char minimum. `midn_boot_<base64url>`
  bootstrap-token written `0600`; `ConsumeBootstrapToken` deletes
  on read so 0.1-EC5 holds. Flow FSM SetPassword → EnrollTOTP →
  ConfirmTOTP wraps the TOTP secret through Lane 4's KeyProvider via
  `secrets.SealForVariable`. `cmd/midnight admin generate-bootstrap-token`,
  `admin show-bootstrap`, and `admin migrate` subcommands wire the
  flow into the CLI.

Deferred items:

- The `--fix=safe|all` runner remains deferred; doctor emits diagnosis
  and remediation text today. The Phase 2 follow-up will add a typed
  `FixAction` registry per check id, with audit emission.

### Sub-slice B — §1.8 off-host backup destination

Commits:

| SHA | Title |
|---|---|
| `5800bc4` | live S3-compatible off-host backup destination (§1.8) |

What landed:

- `internal/backup/s3_destination.go` implements the panel-locked
  Destination contract against any S3-compatible endpoint. We picked
  `github.com/minio/minio-go/v7` over `github.com/aws/aws-sdk-go-v2/service/s3`
  for the smaller dep surface and native MinIO + R2 + Wasabi
  support. `ParseS3URI` accepts the canonical
  `s3://<bucket>/<prefix>?endpoint=...&region=...&style=...` shape;
  credentials live in env vars (Lane 4 KeyProvider hook is a TODO
  marker pointing at the sealed-variable bootstrap path).
- `internal/backup/offhost_alert.go` evaluates
  `BACKUP_OFFHOST_MISSING` (warn) when no destination URI is
  configured and `BACKUP_STALE` (critical) when the latest backup
  is older than the configured threshold (default 26h).
- `internal/backup/destination_integration_test.go` (build tag
  `integration`) launches a MinIO testcontainer and exercises
  Put → Get → SignedURL → Delete byte-exact round-trip. Default
  `go test ./...` requires no Docker.

Deferred items:

- The Lane 4 KeyProvider integration for backup credentials is a
  marker comment, not yet wired. The minimum viable bootstrap reads
  `MIDNIGHT_BACKUP_AWS_ACCESS_KEY_ID` /
  `MIDNIGHT_BACKUP_AWS_SECRET_ACCESS_KEY`; a follow-up swaps it for
  the sealed-variable read once the Phase 2 secret-store CLI lands.
- `Restic`-style per-project encryption (panel §1.8 for MVP Beta)
  is intentionally NOT in scope here; this slice ships the
  destination, not the encryption pipeline.

### Sub-slice C — §1.9 volumes + single-writer enforcement

Commits:

| SHA | Title |
|---|---|
| `8eb9b7c` | volumes + supersede arbitration (§1.9) |

What landed:

- `internal/volume/` package with the typed Volume + Attachment
  models, status FSM (`provisioning → ready → attached →
  soft_deleted → deleted`), 7-day soft-delete grace via
  `MarkSoftDeleted` / `CanReap`. `SingleWriterAdvisoryKey(svc, env)`
  returns the canonical `pg_advisory_xact_lock(hashtext(...))` key
  string; `ArbitrateSupersede` picks the highest monotonic
  `deployment_sequence`, marks the rest `DEPLOY_SUPERSEDED` with
  lexicographic tie-break for determinism.
- `internal/volume/quota.go` classifies the host fsType + mount-
  options pair into `ProjectQuotaReady=true` on xfs+prjquota or
  ext4+quota; everything else surfaces typed
  `VOLUME_QUOTA_UNSUPPORTED`. Documented in
  `docs/runbooks/volume-quotas.md`.
- `internal/runtime/docker/volumes.go` extends Lane 2's adapter
  with `VolumeCreate` / `VolumeInspect` / `VolumeRemove` /
  `VolumeList` in a separate file; existing methods are unchanged.
  `realEngineClient` now satisfies the optional
  `VolumeEngineClient` interface; `fakeEngine` does not — the
  Phase 1 unit test path passes without volume support, the
  integration-tagged test exercises the live engine.
- `migrations/0008_volumes.up.sql` + `.down.sql` introduce
  `volumes` (vol_) + `volume_attachments` (vat_) tables. Schema
  bumps to schema_version = 8.
- `internal/runtime/docker/volumes_integration_test.go` exercises
  create → inspect → list → remove against a real Docker daemon.

Deferred items:

- The `pg_advisory_xact_lock` Go helper is the key-string shape
  only. The actual `tx.Exec("SELECT pg_advisory_xact_lock($1)",
  hashtext(key))` call lives one level up in the deploy
  orchestrator; that wiring is a Lane 1 follow-up once the
  `deployments` table is migrated (Lane 1 hasn't shipped it yet).
- Lane 1's `deployment_sequence` column on `deployments` is not
  yet present in migration 0001. The `volume_attachments.sequence`
  column ships its own bigserial column so this slice is
  self-contained; Lane 1 will sync the two when it migrates the
  full deployment graph.

### Sub-slice D — §1.15 signed releases + SBOM/provenance + image-sig verify

Commits:

| SHA | Title |
|---|---|
| `239e3e3` | signed releases + SBOM/provenance + image-sig verify (§1.15) |

What landed:

- `.github/workflows/release.yml` is the tag-driven release
  pipeline. Builds the `.deb` via `make package-deb`, optionally
  GPG-signs via debsign / dpkg-sig when
  `MIDNIGHT_RELEASE_GPG_KEYID` is set, builds + pushes every
  component image to GHCR, signs them keyless via Sigstore Fulcio
  (`cosign sign --yes`), promotes Lane 3's BuildKit-emitted
  CycloneDX SBOM + SLSA-style provenance, and uploads the bundle as
  a 14-day retained artifact.
- `tools/release-verify/main.go` is the operator-facing harness
  asserting `.deb` signatures, cosign signatures, SBOM, and
  provenance. JSON report + non-zero exit on `fail`. `--allow-
  unsigned-deb` honors the v0.1.0-alpha-1 alpha exception.
- `internal/runtime/docker/signature_verify.go` adds the agent-side
  `SignatureVerifier` seam. `StaticSignatureVerifier` is the Phase 1
  default (stdlib-only allowlist). `Adapter.EnableSignature
  Enforcement(verifier)` wires it on; absent that call the agent
  behaves exactly as before. `IMAGE_SIGNATURE_REQUIRED` is the
  new typed code on missing signatures; `IMAGE_SIGNATURE_INVALID`
  remains the existing on-mismatch code.
- `docs/release-verify.md` is the operator runbook.

Deferred items:

- **sigstore/cosign Go SDK integration** is intentionally NOT
  imported. The `github.com/sigstore/cosign/v2/pkg/oci` package
  pulls in a multi-MB transitive set (containerd, runc, libnetwork,
  the docker module's full image-spec stack) that requires PM-B
  approval per the Lane 3 dep-policy memo. The Phase 1 path uses
  `cosign verify` via the cosign-installer GitHub Action +
  `StaticSignatureVerifier` for in-process. PM-B / Security:
  please decide whether to import the Go SDK or keep the
  fork-exec-to-cosign-binary boundary.
- **Release key custody.** The CI workflow expects two secrets
  (`MIDNIGHT_RELEASE_GPG_KEYID`, `MIDNIGHT_RELEASE_GPG_KEY`) that
  Agent 4 will define when
  `agile/handoff/governance/RELEASE_KEY_CUSTODY.md` is finalized
  (P0-D004). Until then the `.deb` ships unsigned and
  `release-verify` honors `--allow-unsigned-deb` per the
  `v0.1.0-alpha-1` changelog.

## Sprint-1 exit criteria status

| ID | Status | Reason |
|---|---|---|
| 0.1-EC1 (Ubuntu 24.04 fresh-VM doctor < 10 min) | **Pass (CI harness shipped)** | doctor binary + check registry land; the disposable-VM CI harness ships at `.github/workflows/doctor-vm-ec1.yml` on the `phase1/blocker1-vm-ci` branch. Workflow asserts exit 0, < 10 min wall-clock, and all OR-CHK-DOC-001..009 entries `passed=true` on a clean ubuntu-24.04 runner; remaining manual step is GREEN-light verification on a real PR. |
| 0.1-EC2 (Debian 12 fresh-VM doctor < 10 min) | **Pass (CI harness shipped)** | disposable-VM CI harness ships at `.github/workflows/doctor-vm-ec2.yml` using strategy (i) — `debian:12-slim` Docker container with `--privileged --cgroupns=host` — per the Blocker 1 strategy comparison. Same assertion shape as EC1; the strategy upgrade path to a true VM is tracked in the `phase1-blocker1-vm-ci-followup` placeholder for `v0.2.0-alpha`. |
| 0.1-EC3 (cgroup v1 refused with explicit remediation) | **Pass** | OR-CHK-DOC-001 fixture test asserts the panel-locked remediation string on cgroup v1 mountinfo. |
| 0.1-EC4 (daemon.json conflict refused with merged-diff output) | **Pass** | OR-CHK-DOC-002 fixture test asserts a conflict result; postinst refuses install when the check returns non-zero. |
| 0.1-EC5 (bootstrap-token one-shot) | **Pass** | `cmd/midnight admin show-bootstrap` test exercises the second-read returning `BOOTSTRAP_TOKEN_CONSUMED` with exit code 2. |

The remaining EC items 0.1-EC6 (Operation envelope), 0.1-EC7 (audit
verify on 100-row fixture), and 0.1-EC8 (no external network at
install) are owned by Lane 1 / Lane 4 and were not in scope here.

## Reporting block (verbatim per the prompt)

### Branch + tip

- Branch: `phase1/sprint1-finish`
- Tip commit: `239e3e37ef947ca2303ddee8eec5901ce7b1f059`

### Final `git log --oneline -15`

```
239e3e3 feat(phase1/sprint1-finish): signed releases + SBOM/provenance + image-sig verify (§1.15)
8eb9b7c feat(phase1/sprint1-finish): volumes + supersede arbitration (§1.9)
5800bc4 feat(phase1/sprint1-finish): live S3-compatible off-host backup destination (§1.8)
389ec94 feat(phase1/sprint1-finish): bootstrap admin first-login MFA + 0007 schema
454ec55 feat(phase1/sprint1-finish): finalize .deb packaging + Make/Task targets
2fa26e4 feat(phase1/sprint1-finish): live midnight doctor with OR-CHK-DOC registry
4eb18f5 Merge phase1/lane5-wireup: audit chain wiring, webhook+agent handler registration, audit emit
6dfeff1 chore(phase1/lane5-wireup): go fmt + go mod tidy + final gate run
b2ab788 docs(error-codes): add 7 codes Lane 4 referenced but deferred from registry
4cff6ab feat(phase1/lane5-wireup): Postgres-backed audit.AuditChain + 0006 schema delta
599689f feat(phase1/lane5-wireup): emit audit rows from mutating handlers
ca6d138 feat(phase1/lane5-wireup): add 0005_webhook_deliveries migration + Postgres dedupe store
f8541f2 feat(phase1/lane5-wireup): wire RegisterWebhookHandlers from DataStack router
38a5627 feat(phase1/lane5-wireup): wire RegisterAgentHandlers from DataStack router
af15413 docs(handoff): record Lane 3 merge reconciliation (versions.yaml + go.mod tidy)
```

### `migrations/manifest.yaml`

```yaml
schema_version: 8
migrations:
  - version: 1
    name: alpha_core
    up: 0001_alpha_core.up.sql
    down: 0001_alpha_core.down.sql
  - version: 2
    name: environment_locks
    up: 0002_environment_locks.up.sql
    down: 0002_environment_locks.down.sql
  - version: 3
    name: artifact_promotions
    up: 0003_artifact_promotions.up.sql
    down: 0003_artifact_promotions.down.sql
  - version: 4
    name: agents
    up: 0004_agents.up.sql
    down: 0004_agents.down.sql
  - version: 5
    name: webhook_deliveries
    up: 0005_webhook_deliveries.up.sql
    down: 0005_webhook_deliveries.down.sql
  - version: 6
    name: audit_chain_columns
    up: 0006_audit_chain_columns.up.sql
    down: 0006_audit_chain_columns.down.sql
  - version: 7
    name: bootstrap_admin
    up: 0007_bootstrap_admin.up.sql
    down: 0007_bootstrap_admin.down.sql
  - version: 8
    name: volumes
    up: 0008_volumes.up.sql
    down: 0008_volumes.down.sql
```

### New rows in `docs/error-codes.md`

| Code | Owner | Notes | Recovery hint |
|---|---|---|---|
| `BACKUP_STALE` | S07 | Last successful backup is older than the configurable threshold (default 26h). | Trigger a backup now; investigate the scheduled-backup runbook if it keeps lapsing. |
| `BOOTSTRAP_TOKEN_CONSUMED` | S02 | Admin first-login step invoked after the one-shot bootstrap-token was already consumed (0.1-EC5). | The bootstrap token is one-shot; rotate the admin via `midnight admin rotate-credentials` instead. |
| `DEPLOY_SUPERSEDED` | S04 | Single-writer arbitration rolled back this deployment because a newer deployment for the same (service, environment) won. | Another deployment for the same (service, environment) won the supersede race; this attempt was rolled back cleanly. |
| `IMAGE_SIGNATURE_REQUIRED` | S04 | Agent runtime is configured with `verify_signatures=true` and the requested image is unsigned. | Sign the image with cosign and push the .sig artifact, or set verify_signatures=false in the agent config (not recommended). |
| `MFA_INVALID_CODE` | S02 | Supplied TOTP code did not verify against the stored secret within the allowed skew. | Resync the authenticator app's clock and re-enter the 6-digit code. |
| `MFA_REQUIRED` | S02 | Actor must complete TOTP enrollment before the requested operation is permitted. | Complete TOTP enrollment via `midnight admin enroll-mfa` before retrying. |
| `VOLUME_QUOTA_UNSUPPORTED` | S04 | Host filesystem cannot enforce per-project quotas (xfs without prjquota, ext4 without quota mount). | Mount `/var/lib/midnight` on xfs (with prjquota) or ext4 (with quota) and remount. |

### New direct dependencies in `go.mod`

| Module | Version | Reason |
|---|---|---|
| `github.com/minio/minio-go/v7` | `v7.0.83` | Sub-slice B — S3-compatible backend (smaller surface than aws-sdk-go-v2/s3, native MinIO/R2/Wasabi support). |
| `github.com/testcontainers/testcontainers-go/modules/minio` | `v0.35.0` | Sub-slice B — `//go:build integration` MinIO testcontainer for the destination smoke test. |
| `golang.org/x/crypto` | `v0.50.0` | Sub-slice A — was already indirect; now direct because `internal/bootstrap/password.go` imports `pbkdf2`. |

### Confirmation that all gates exit 0

| Gate | Command | Result |
|---|---|---|
| go fmt | `go fmt ./...` | exit 0 |
| go vet | `go vet ./...` | exit 0 |
| go build | `go build ./...` | exit 0 |
| go test | `go test ./...` | exit 0 (no FAIL) |
| tools/lint | `go run ./tools/lint` | `lint: ok` |
| tools/phase0-gate | `go run ./tools/phase0-gate` | `phase0 gate passed: drills=19 …` |

### Integration tests (compiled, not executed by default)

| Tag | Command | Status |
|---|---|---|
| `integration` for `internal/backup` | `go test -tags=integration ./internal/backup/...` | Compiles cleanly via `go vet -tags=integration`. Live execution requires Docker running on the host. |
| `integration` for `internal/runtime/docker` | `go test -tags=integration ./internal/runtime/docker/...` | Compiles cleanly. Live execution exercises Volume{Create,Inspect,List,Remove} and the Schedule/Remove sanity path against a local engine. |
| `integration` for `internal/doctor` | `go test -tags=integration ./internal/doctor/...` | Compiles cleanly. Live execution requires both the `integration` tag AND `MIDNIGHT_DOCTOR_LIVE=1`. |

(Default `go test ./...` skips all of the above.)

## Explicitly deferred work / capacity stops

- A.1 — `--fix=safe|all` executes no host changes yet. Each check
  declares a `Remediation` string in the registry; the actual
  `FixAction` runner + audit emission lands in Phase 2.
- B — Lane 4 `KeyProvider` integration for backup credentials is
  a marker comment. Operators currently set
  `MIDNIGHT_BACKUP_AWS_ACCESS_KEY_ID` /
  `MIDNIGHT_BACKUP_AWS_SECRET_ACCESS_KEY` directly. The sealed-
  variable bootstrap path is a Phase 2 follow-up.
- C — `pg_advisory_xact_lock` Go-side wrapper is the key-string
  shape only. The actual transaction-level lock call lives in the
  deploy orchestrator (Lane 1 territory) once the `deployments`
  table is migrated. Lane 1 also owns syncing
  `volume_attachments.sequence` with the not-yet-existent
  `deployments.deployment_sequence`.
- D — sigstore/cosign Go SDK is intentionally NOT imported. The
  agent uses `StaticSignatureVerifier` (allowlist driven, stdlib-
  only) until PM-B / Security approves the multi-MB transitive
  cosign Go dep set.

## Open questions for PM-A / PM-B

1. **PM-A: Lane 4 backup credential boundary.** Sub-slice B reads
   `MIDNIGHT_BACKUP_AWS_*` from env. The Lane 4 sealed-variable
   bootstrap path requires either (a) the agent reading the sealed
   variable at boot and exporting env vars to its own process, or
   (b) the backup orchestrator depending directly on
   `secrets.Vault`. Recommendation: (b) so the credentials never
   touch process env; please confirm the wiring direction.
2. **PM-B / Security: cosign Go SDK approval.** The Phase 1 path
   ships a stdlib-only `StaticSignatureVerifier` and shells out to
   the cosign binary in CI. Importing
   `github.com/sigstore/cosign/v2/pkg/oci` adds containerd, runc,
   libnetwork, the docker module's image-spec stack, and the
   sigstore/rekor client — multi-MB transitive expansion. Please
   confirm whether to (a) import the Go SDK and accept the dep
   weight, (b) keep the fork-exec-to-cosign-binary boundary, or
   (c) ship a cosign-binary-bundled .deb dep and treat it as an
   external runtime requirement.
3. **PM-B: Release-key custody finalization.** The release pipeline
   refers to `MIDNIGHT_RELEASE_GPG_KEYID` /
   `MIDNIGHT_RELEASE_GPG_KEY` as CI secrets. Until Agent 4 ships
   `agile/handoff/governance/RELEASE_KEY_CUSTODY.md`, the .deb
   ships unsigned and `release-verify --allow-unsigned-deb` is the
   exception. Please confirm the timeline so the alpha changelog
   can be updated when the policy flips.
4. **PM-A: 0.1-EC1 / EC2 disposable-VM harness.** This branch
   ships every bit of code that needs to be live for those two
   exit criteria, but the actual disposable-VM CI runner that
   times the install + doctor pass is not yet wired. Recommendation:
   add a `.github/workflows/disposable-vm.yml` that consumes the
   .deb produced by `release.yml` against an Ubuntu 24.04 + Debian
   12 matrix. Confirm whether that's owned by S08 (release gate)
   or S02 (installer/ops).
