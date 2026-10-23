---
id: GOV-V010-ALPHA-GREENLIGHT
release_tag: v0.1.0-alpha
status: approvals-received-per-product-owner-confirmation
owner: PM-B
deciders: [PM-B, Security, Release Manager, Product]
related_decisions: [P0-D004, P0-D017, P0-D022]
date: 2026-04-27
status_updated: 2026-04-30
---

# `v0.1.0-alpha` Greenlight Brief

This brief is the single PM-B sign-off package for the `v0.1.0-alpha`
release tag. It consolidates the three Phase 0 deferred-decision
closures (P0-D004 release-key custody, P0-D017 audit verifier exit
codes, P0-D022 design-partner signoff counts), the Phase 1 progress
through Sprint 1 plus the recent durable boot/package work, the
greenlight condition status, and the recommended go/no-go
conditions.

## Status update — 2026-04-30

Approvals and design-partner signoffs for the Phase 0 Alpha
`v0.1.0-alpha` greenlight have been received per product owner / user
confirmation on 2026-04-30. This update records governance status only:
the historical technical evidence and any explicit verification caveats
below remain as written unless a cited artifact proves otherwise.

## Section 1 — Phase 0 closure recap

Phase 0's historical local evidence gate is closed per `agile/handoff/
phase0-subagents/STATUS.md`. All eight worker lanes (S01 Foundations,
S02 Installer/Ops, S03 API/Data, S04 Runtime/Agent, S05 Build/Registry,
S06 Ingress/Logs/DX, S07 Support/Backup/Obs, S08 QA/Release Gate)
shipped release evidence, and the live phase0 gate is green (`go run
./tools/phase0-gate` reports 19 manifest drill rows and 8 validated
alerts; executed-drill mode reports 19 local assertions passing with no
skipped rows).
`PHASE0_DECISION_LOG.md` rows P0-D001 through P0-D022 are now all
`decided` after the closures landed in this branch (`phase1/governance`):
P0-D004 → `agile/handoff/governance/RELEASE_KEY_CUSTODY.md`,
P0-D017 → `agile/handoff/governance/AUDIT_VERIFY_EXIT_CODES.md`,
P0-D022 → `agile/handoff/governance/DESIGN_PARTNER_SIGNOFF_TEMPLATE.md`.

## Section 2 — Phase 1 Engineering Lanes Integrated for `v0.1.0-alpha`

### 2.1 § coverage table

The following table maps the panel-locked Phase 0 / Phase 1 sections to
their lane delivery on `main` after the Sprint 1 merge (commit
`3bd9798`). "Done" means the panel-locked behavior is implemented and
covered by tests in the default `go test ./...` lane (some live paths
are gated behind `//go:build integration` or env vars, which is the
panel-allowed boundary). "Partial" means structural seam landed +
default-test coverage but at least one panel-locked sub-behavior is
deferred. "Deferred" means the section is named here for completeness
but is post-`v0.1.0-alpha` work.

| § | Topic | Lane | Status | Reference |
|---|---|---|---|---|
| §0.1 | Repo + Go monorepo + tooling | S01 → Lane 1 | Done | `tools/lint`, `tools/phase0-gate`, `versions.yaml`. |
| §0.2 | Cursor signing | Lane 1 | Done | `internal/api/cursor_secret.go`. |
| §0.3 | Postgres schema + migrations | Lane 1 → Lane 5 | Done | `internal/db/pg/`, `migrations/0001..0008`. |
| §0.4 | API/v1alpha skeleton + DataStack router | Lane 1 → Lane 5 | Done | `internal/api/router.go` (`NewRouterWithDataStack`). |
| §1.1 | Live Docker runtime adapter | Lane 2 | Done | `internal/runtime/docker/adapter.go`, `baseline.go`, `cgroup.go`. |
| §1.2 | BuildKit pinning + build CLI | Lane 3 | Done (binary-edge boundary; cosign Go SDK deferred) | `cmd/midnight-buildkit/`, `internal/build/`. |
| §1.3 | Embedded registry + GC + pin store | Lane 3 | Done | `cmd/midnight-registry/`, `internal/registry/`. |
| §1.4 | Envelope encryption + sealed variables + KEK rotation | Lane 4 | Done | `internal/secrets/`. |
| §1.5 | Traefik file provider + ACME (lego) | Lane 3 | Done (live cosign-keyless verification deferred) | `internal/router/`, `internal/acme/`, `deploy/traefik/`. |
| §1.6 | Custom domains TXT + HTTP-01 | Lane 3 | Deferred — stretch | `internal/acme/domain_diagnostics.go` seam only. |
| §1.7 | Postgres template + `pg_dump` backup hooks | Lane 1 + Lane 3 | Deferred | Beta scope. |
| §1.8 | Live S3-compatible off-host backup destination | Sprint 1 finish (sub-slice B) + Blocker 2 (credential boundary) | Done | `internal/backup/s3_destination.go`, `5800bc4`; credential boundary `internal/backup/credentials.go` + `BACKUP_CREDENTIAL_BOUNDARY.md` (env-var alpha, sealed-variable from `v0.3.0`). |
| §1.9 | Volumes + supersede arbitration + project quotas | Sprint 1 finish (sub-slice C) | Partial — Postgres-side `pg_advisory_xact_lock` wrapper deferred to Lane 1 follow-up | `internal/volume/`, `internal/runtime/docker/volumes.go`, `8eb9b7c`. |
| §1.10 | Signed webhooks + GitHub App scaffolding | Lane 4 → Lane 5 | Done | `internal/webhook/`, `internal/integrations/github/`, Lane 5 wireup `f8541f2` / `ca6d138`. |
| §1.11 | Audit chain export + verify | Lane 4 → Lane 5 | Done | `internal/audit/`, `cmd/midnight/audit_verify.go`, `4cff6ab`. |
| §1.12 | Deploy state machine + advisory locks | Lane 1 + Lane 4 | Partial — single-flight pg advisory lock wrapper deferred (volume sub-slice C TODO) | `internal/promotion/`, `internal/volume/`. |
| §1.13 | Operations + idempotency + cursor pagination | Lane 1 | Done | `internal/api/`, `internal/idempotency/`. |
| §1.14 | Railpack builder integration | Lane 3 | Deferred — separate slice | n/a. |
| §1.15 | Signed releases + SBOM/provenance + image-sig verify | Sprint 1 finish (sub-slice D) + Phase 1 Blocker 3 | Partial — cosign fork-exec posture ratified for alpha + beta per `COSIGN_SDK_BOUNDARY.md`; release workflow and `release-verify` now fail tag releases that lack component images or cosign/SBOM/provenance evidence; first-protected-release custody execution remains a day-of-tag operational follow-up | `.github/workflows/release.yml`, `tools/release-verify/`, `internal/runtime/docker/{signature_verify.go,cosign_exec_verifier.go,cosign_parity_test.go}`, `docs/release-verify.md`, `239e3e3` + Blocker 3 commits. |
| §1.16 | Agent ↔ control-plane HTTPS + HMAC heartbeat | Lane 2 → Lane 5 | Done (mTLS server-side enforcement deferred to Lane 1 follow-up) | `pkg/agentapi/`, `cmd/midnight-agent/internal/control/`, Lane 5 wireup `38a5627`. |
| §1.17 (live doctor + .deb + bootstrap MFA) | Sprint 1 finish (sub-slice A) + durable boot/package follow-up | Partial — `--fix=safe\|all` still emits diagnosis/remediation only; durable service boot now wires `midnight-server serve`, `midnight-worker serve`, BuildKit/registry wrappers, package defaults, cursor key/registry config creation, and `midnight admin migrate` applies embedded Postgres migrations when a DSN is configured | `internal/doctor/live.go`, `cmd/midnight-doctor/`, `cmd/midnight-server/`, `cmd/midnight-worker/`, `cmd/midnight/admin.go`, `cmd/midnight-buildkit/`, `deploy/debian/`, `deploy/systemd/`, `internal/bootstrap/`, `2fa26e4` / `454ec55` / `389ec94` + durable boot/package working-tree changes. |

### 2.2 Sprint 1 exit-criteria status (`v0.1.0-alpha`)

Carried over from `agile/handoff/phase1-subagents/SPRINT1_FINISH_STATUS.md`:

| ID | Criterion | Status | Reason |
|---|---|---|---|
| 0.1-EC1 | Fresh Ubuntu 24.04 VM → `midnight doctor` passes in <10 min | **Pass (CI harness shipped)** | Doctor binary + OR-CHK-DOC registry land on `main` (commit `2fa26e4`); the disposable-VM CI harness ships at `.github/workflows/doctor-vm-ec1.yml` (Phase 1 Blocker 1, branch `phase1/blocker1-vm-ci`). Workflow asserts exit 0, <10 min wall-clock, and all OR-CHK-DOC-001..009 entries `passed=true` on a clean ubuntu-24.04 GitHub-hosted runner; remaining manual step is GREEN-light verification on a real PR. |
| 0.1-EC2 | Fresh Debian 12 VM → `midnight doctor` passes in <10 min | **Pass (CI harness shipped)** | Disposable-VM CI harness ships at `.github/workflows/doctor-vm-ec2.yml` using strategy (i) — `debian:12-slim` Docker container with `--privileged --cgroupns=host` — per the Blocker 1 strategy comparison; same assertion shape as EC1; remaining manual step is GREEN-light verification on a real PR. The strategy upgrade path to a true VM is tracked in the `phase1-blocker1-vm-ci-followup` placeholder for `v0.2.0-alpha`. |
| 0.1-EC3 | cgroup v1 host refused with explicit remediation | **Pass** | OR-CHK-DOC-001 fixture test asserts the panel-locked `add 'systemd.unified_cgroup_hierarchy=1' to the kernel command line and reboot` remediation string on cgroup v1 mountinfo. |
| 0.1-EC4 | `daemon.json` conflict refused with merged-diff output | **Pass** | OR-CHK-DOC-002 fixture test asserts a conflict result; `deploy/debian/postinst` refuses install when the check returns non-zero. |
| 0.1-EC5 | Bootstrap token is one-shot; second `show-bootstrap` returns "already consumed" | **Pass** | `cmd/midnight admin show-bootstrap` test asserts the second-read returns `BOOTSTRAP_TOKEN_CONSUMED` with exit code 2. |
| 0.1-EC6 | `/api/v1alpha/operations/{id}` Operation envelope + Idempotency-Key 409 conflict | **Pass (default lane)** | Lane 1 `internal/api/operations.go` covers the envelope + 409 path; full Schemathesis sweep is a Phase 2 hardening task. |
| 0.1-EC7 | `midnight audit verify` on synthetic 100-row fixture | **Pass** | `cmd/midnight/audit_verify_test.go` plus the Lane 4 `internal/audit/verify_test.go` coverage exercise the 0/1/2/3 outcomes; P0-D017 closure ratifies the exit-code envelope. |
| 0.1-EC8 | No external network calls during install except APT/Docker repo + (optional) telemetry-opt-in | **Pass (by construction)** | `deploy/debian/postinst` invokes local setup, `midnight admin migrate` only when a DSN is configured, and `midnight-doctor`; default `go test ./...` requires no network. The disposable-VM harness records the release evidence on real PR verification. |

## Section 3 — Decisions ratified by this brief

| Decision | One-line summary |
|---|---|
| **P0-D004** — release-key custody | Test keys for `dev` channel CI; offline-or-KMS for `alpha`/`beta`; KMS-required for `stable`/`enterprise-lts`. See `RELEASE_KEY_CUSTODY.md`. |
| **P0-D017** — audit verifier exit codes | `0` healthy / `1` warnings only / `2` chain mismatch / `3` signature failure (panel-locked per ADR-011); `64` `EX_USAGE` / `66` `EX_NOINPUT` (sysexits). See `AUDIT_VERIFY_EXIT_CODES.md`. |
| **P0-D022** — design-partner signoff counts | `v0.1.0-alpha` = 2 install signoffs; `v0.2.0-alpha` = 3 first-product-loop signoffs; `v0.3.0-alpha` = 5 first-product-loop signoffs + ≥1 backup/restore drill. See `DESIGN_PARTNER_SIGNOFF_TEMPLATE.md`. |
| **Backup credential boundary** — env→sealed posture | Env-var posture acceptable for `v0.1.0-alpha` and `v0.2.0-alpha`; sealed-variable posture mandatory for `v0.3.0-alpha` and onward. Implementation: `internal/backup/credentials.go` (`CredentialSource` interface + `Env` / `Sealed` / `Chain` implementations + `VaultSealedResolver` adapter). New typed failure codes: `BACKUP_CREDENTIALS_MISSING`, `BACKUP_CREDENTIALS_UNAUTHORIZED`. See `BACKUP_CREDENTIAL_BOUNDARY.md`. PM-B / Security approval received per product owner / user confirmation on 2026-04-30. |
| **Cosign SDK boundary** — fork-exec posture for alpha + beta | Satisfies greenlight condition (3); PM-B / Security approval received per product owner / user confirmation on 2026-04-30. Fork-exec the pinned `cosign v2.6.3` binary (per-arch SHA-256 digests in `versions.yaml`); the `github.com/sigstore/cosign/v2/...` Go SDK import is binding-deferred to `v1.0.0`. Agent-side `CosignExecVerifier` with the parity-test contract that gates the v1 swap. See `COSIGN_SDK_BOUNDARY.md`. |

## Section 4 — Greenlight Conditions Approval Status for the `v0.1.0-alpha` tag

These are the items PM-B / Security / Release Manager / Product had to
close (or accept-with-tracking) before the `v0.1.0-alpha` tag could be
signed. Per product owner / user confirmation on 2026-04-30, the
governance approvals/signoffs have been received. The notes below still
preserve the underlying evidence state and do not upgrade unverified CI
runs beyond what the cited artifacts prove.

1. **Disposable-VM CI harness for 0.1-EC1 and 0.1-EC2 — shipped.**
   See `.github/workflows/doctor-vm-ec1.yml` (Ubuntu 24.04 GitHub-
   hosted runner) and `.github/workflows/doctor-vm-ec2.yml` (Debian
   12 via `debian:12-slim` Docker container with
   `--privileged --cgroupns=host`, strategy (i) per the workflow
   header). The harness asserts exit 0, total wall-clock duration
   < 10 minutes, and every OR-CHK-DOC-001..009 reports `status="pass"`;
   negative-path matrix entries cover 0.1-EC3 (cgroup v1 refusal via
   the `--hostsource=fixture:` seam in `cmd/midnight-doctor`) and
   0.1-EC4 (`/etc/docker/daemon.json` conflict).
   `tools/vm-harness/` is the assertion harness that the workflows
   invoke; default `go test ./...` covers its parser unit tests, no
   Docker / VM required. Operator runbook at
   `docs/runbooks/disposable-vm-doctor.md` covers Vagrant /
   multipass / lima / Docker reproduction paths. Remaining manual
   step is GREEN-light verification on a real PR — the workflows
   were authored from a Windows worktree (`phase1/blocker1-vm-ci`)
   and have only been YAML-validated locally; approval was received
   with this real-PR verification caveat still tracked, so EC1 / EC2
   are not re-labeled as "verified green" by this update. The strategy upgrade
   path to true VMs (Hetzner / DigitalOcean self-hosted runners or
   `lima`/`qemu`-in-runner) is tracked as
   `phase1-blocker1-vm-ci-followup` and slated for `v0.2.0-alpha`.
2. **Backup credential boundary — shipped.** See `internal/backup/credentials.go` +
   `BACKUP_CREDENTIAL_BOUNDARY.md`; alpha posture is env-var with
   sealed escalation path; PM-B / Security approval was received per
   product owner / user confirmation on 2026-04-30. The implementation lands a `CredentialSource`
   interface with `EnvCredentialSource` (current behavior),
   `SealedCredentialSource` (Lane 4 `KeyProvider`-backed sealed
   variable referenced by `MIDNIGHT_BACKUP_CREDENTIAL_REF`), and a
   `ChainCredentialSource` (sealed-first-then-env composite) wired
   into `internal/backup/s3_destination.go` so existing env-var
   users are unaffected. Two new typed failure codes
   (`BACKUP_CREDENTIALS_MISSING`, `BACKUP_CREDENTIALS_UNAUTHORIZED`)
   surface the missing / unauthorized paths. Audit emit is
   panel-locked at `backup.credentials.resolve` with
   `source: env|sealed` + `reference_id` (sealed only); plaintext
   never appears. Per the boundary doc the env-var posture is
   acceptable for `v0.1.0-alpha` and `v0.2.0-alpha`; the sealed-
   variable posture is mandatory from `v0.3.0-alpha` onward. The
   Vault adapter (HashiCorp) is explicitly Phase 4 scope and is not
   added in this slice. Status: **engineering-complete; PM-B /
   Security approval received per product owner / user confirmation on
   2026-04-30** on the boundary doc.
3. **cosign Go SDK approval — shipped.** See
   [`COSIGN_SDK_BOUNDARY.md`](COSIGN_SDK_BOUNDARY.md) ratifying the
   fork-exec posture for alpha + beta; PM-B / Security approval
   received per product owner / user confirmation on 2026-04-30.
   Concretely: option (b) wins. The
   `github.com/sigstore/cosign/v2/...` Go SDK import is binding-
   deferred to `v1.0.0`; the agent-side `CosignExecVerifier`
   (`internal/runtime/docker/cosign_exec_verifier.go`) fork-execs the
   pinned `cosign v2.6.3` binary (SHA-256 digests in
   `versions.yaml`). The parity test in
   `internal/runtime/docker/cosign_parity_test.go` captures the
   canonical input/output table that the v1 SDK swap must satisfy
   so that PR is verified-equivalent rather than a leap of faith.
   Three new failurecodes ride along:
   `IMAGE_SIGNATURE_VERIFY_BINARY_MISSING`,
   `IMAGE_SIGNATURE_VERIFY_TIMEOUT`, and
   `IMAGE_SIGNATURE_VERIFY_TRANSPARENCY_FAILED`. The Lane 5 wire-up
   commit on this merge folds Blocker 3's `cosign` pin into
   `.github/workflows/release.yml`'s `COSIGN_VERSION` env so the
   supply-chain pin lives in one place (`versions.yaml`).
4. **First-protected-release custody runbook shipped; day-of-tag execution follow-up.** See
   `RELEASE_KEY_CUSTODY.md` runbook (now `decided + runnable`) plus
   the updated `.github/workflows/release.yml` (Fulcio keyless
   `--rekor-url` / `--fulcio-url` flags, post-sign `cosign verify`
   step asserting the canonical identity-regexp / OIDC issuer pair,
   tag-vs-dispatch split for `release-verify` so the GREEN floor
   forbids `--allow-unsigned-deb` on `v*` tags) plus the
   `docs/release/keys/` fingerprint-publication infrastructure
   (placeholder `midnight-release-fingerprint.txt`, README,
   defense-in-depth `.gitignore`). Failure codes
   `RELEASE_FINGERPRINT_MISMATCH` and `RELEASE_REVOKED_KEY` are
   registered in `internal/failurecodes` and documented in
   `docs/error-codes.md`. Remaining manual step is PM-B running the
   operational checklist (`RELEASE_KEY_CUSTODY.md` Step 10) on the
   day of the tag and replacing the placeholder fingerprint with the
   YubiKey-generated value; everything else is engineering-complete.
   Governance approval/signoff was received per product owner / user
   confirmation on 2026-04-30; the day-of-tag fingerprint replacement
   and rc test-tag exercise remain operational execution evidence, not
   claims made by this status update. Until that checklist is co-signed
   by PM-B + Security + Release Manager and the rc test tag
   (`v0.1.0-alpha-rc1`) is exercised end-to-end, the `.deb` ships
   unsigned per the `v0.1.0-alpha-1`
   changelog (the `--allow-unsigned-deb` exception now exists ONLY
   on the dispatch / fork path, never on the GREEN floor).

## Section 5 — Recommended go/no-go conditions

The recommended decision matrix for PM-B at the greenlight meeting:

- **GREEN — ship `v0.1.0-alpha`.** All four greenlight conditions in §4 are
  closed; the design-partner signoff floor (2 install signoffs per
  P0-D022) is met; `go fmt / vet / build / test`, `tools/lint`, and
  `tools/phase0-gate` all exit 0 on the tip; `release-verify` exits 0
  against the rc bundle **without** `--allow-unsigned-deb` (the
  workflow split lands the unsigned exception only on the
  dispatch / fork path; on the GREEN floor it is forbidden — see
  blocker (4)); the `RELEASE_KEY_CUSTODY.md` operational checklist
  (Step 10) is co-signed by PM-B + Security + Release Manager AND
  the placeholder fingerprint in `docs/release/keys/midnight-release-
  fingerprint.txt` is replaced with the YubiKey-generated value; the
  `AUDIT_VERIFY_EXIT_CODES.md` sign-off block is co-signed; the two
  new failure codes (`RELEASE_FINGERPRINT_MISMATCH`,
  `RELEASE_REVOKED_KEY`) are registered and visible in
  `docs/error-codes.md`.
- **YELLOW — ship `v0.1.0-alpha-1` with documented exceptions.** Any
  of the four greenlight conditions in §4 is still open but is tracked with
  an explicit owner + due date + a release-note callout. Specifically
  acceptable for the `-1` revision: condition (1) (disposable-VM CI
  harness) and condition (4) (custody checklist) co-signed, condition
  (3) (cosign Go SDK) accepted with the documented fork-exec boundary;
  condition (2) (backup credential boundary) telegraphed in release notes. Tag
  ships with `--allow-unsigned-deb` honored per the `v0.1.0-alpha-1`
  changelog.
- **RED — do not ship.** Any of the following is true:
  - A panel-locked Phase 0 decision (P0-D001..P0-D022) is reopened or
    its closure document is unsigned by greenlight time.
  - `go test ./...` fails on the tip.
  - `tools/phase0-gate` does not exit 0.
  - The design-partner signoff floor for the tag is **not** met (per
    P0-D022).
  - A signoff with `verdict: fail` cites a panel-locked exit criterion
    and the cited criterion has no fix-in-rc.
  - Any condition in §4 is in `severity: critical` / `status: open` at
    greenlight time **with no telegraphed YELLOW exception**.

## Section 6 — Sign-off block

```yaml
v010_alpha_greenlight_signoff:
  release_tag: v0.1.0-alpha
  release_candidate: <PLACEHOLDER — e.g. v0.1.0-alpha-rc1>
  decision: approvals-received-per-product-owner-confirmation
  confirmation_date: 2026-04-30
  confirmation_source: product-owner/user confirmation
  decisions_ratified: [P0-D004, P0-D017, P0-D022]
  greenlight_conditions_status:
    disposable_vm_ci_harness: approved-with-real-pr-verification-caveat
    backup_credential_boundary: engineering-complete-approved
    cosign_go_sdk_approval:    engineering-complete-approved
    first_protected_release_custody_execution: approved-with-day-of-tag-execution-caveat
  pm_b_acknowledges_runbook_runnable: <PLACEHOLDER — true | false>
  design_partner_signoff_floor_met: true-per-product-owner-confirmation
  release_evidence_packet_path: agile/release-evidence/v0.1.0-alpha/
  signers:
    pm_b:
      name: <PLACEHOLDER>
      date: <PLACEHOLDER — UTC ISO8601>
    security:
      name: <PLACEHOLDER>
      date: <PLACEHOLDER — UTC ISO8601>
    release_manager:
      name: <PLACEHOLDER>
      date: <PLACEHOLDER — UTC ISO8601>
    product:
      name: <PLACEHOLDER>
      date: <PLACEHOLDER — UTC ISO8601>
```

> Status note: approvals/signoffs were received per product owner / user
> confirmation on 2026-04-30. The named signer placeholders are preserved
> as historical evidence fields; this update does not invent signer names,
> timestamps, release-candidate IDs, or unverified CI results.
