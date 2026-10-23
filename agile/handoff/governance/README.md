# Governance closures (Phase 0 → Phase 1)

This directory holds the formal governance closures for the three
deferred Phase 0 decisions plus the consolidated `v0.1.0-alpha`
greenlight brief. They are the inputs PM-B / Security / Release Manager
/ Product review before the first protected `alpha` release ships.

Status update (2026-04-30): Phase 0 Alpha approvals and design-partner
signoffs were received per product owner / user confirmation. This index
records governance status only; named signer placeholders and technical
verification caveats remain in the source documents unless backed by
separate evidence.

| File | Closes / Produces | One-line summary |
|---|---|---|
| [`RELEASE_KEY_CUSTODY.md`](RELEASE_KEY_CUSTODY.md) | P0-D004 | Per-channel signing-key custody (test/dev → ephemeral CI key; alpha/beta → cosign-keyless OR YubiKey OR KMS; stable/enterprise-lts → KMS-required) plus the operational checklist for the first protected signed release. |
| [`AUDIT_VERIFY_EXIT_CODES.md`](AUDIT_VERIFY_EXIT_CODES.md) | P0-D017 | Ratifies Lane 4's audit-verifier exit-code envelope: `0` healthy / `1` warnings / `2` chain mismatch / `3` signature failure (panel-locked per ADR-011), `64` `EX_USAGE` / `66` `EX_NOINPUT` (sysexits). Includes SIEM consumer guidance. |
| [`DESIGN_PARTNER_SIGNOFF_TEMPLATE.md`](DESIGN_PARTNER_SIGNOFF_TEMPLATE.md) | P0-D022 | Per-tag signoff floors (v0.1=2 install / v0.2=3 first-product-loop / v0.3=5 first-product-loop + ≥1 backup-restore drill) plus a yaml-fronted markdown signoff template, aggregation contract, reviewer matrix, and 90-day stale-signoff lifecycle. |
| [`BACKUP_CREDENTIAL_BOUNDARY.md`](BACKUP_CREDENTIAL_BOUNDARY.md) | `v0.1.0-alpha` greenlight condition (2) | Per-tag credential posture (env-var acceptable for `v0.1.0` / `v0.2.0`; sealed-variable mandatory from `v0.3.0`); IAM least-privilege template; env→sealed migration path; rotation runbook; failure-code surface (`BACKUP_CREDENTIALS_MISSING` / `BACKUP_CREDENTIALS_UNAUTHORIZED`); PM-B / Security sign-off block. |
| [`COSIGN_SDK_BOUNDARY.md`](COSIGN_SDK_BOUNDARY.md) | `v0.1.0-alpha` greenlight condition (3) | Ratifies fork-exec of the pinned `cosign` binary as the alpha + beta posture; defers the `github.com/sigstore/cosign/v2` Go SDK import to a `v1.0.0` follow-up; pins `cosign v2.6.3` + linux-amd64 / linux-arm64 SHA-256 digests in `versions.yaml`; records operational requirements + parity-test contract for the v1 swap. |
| [`V010_ALPHA_GREENLIGHT_BRIEF.md`](V010_ALPHA_GREENLIGHT_BRIEF.md) | Single PM-B sign-off package for `v0.1.0-alpha` | Phase 0 closure recap, Phase 1 § coverage table, Sprint 1 exit-criteria table, decisions ratified, greenlight approval status, go/no-go conditions, and the consolidated PM-B / Security / Release Manager / Product sign-off block. |

## How these documents are consumed

- `PHASE0_DECISION_LOG.md` rows P0-D004 / P0-D017 / P0-D022 are now all
  `decided`; the recommendation column links here.
- The `v0.1.0-alpha` greenlight meeting reads
  `V010_ALPHA_GREENLIGHT_BRIEF.md` first; the three per-decision docs
  are the supporting evidence.
- The 2026-04-30 product owner / user confirmation records Phase 0 Alpha
  approvals and design-partner signoffs as received; operational
  day-of-tag evidence is still recorded through the relevant runbooks.
- The release pipeline (`.github/workflows/release.yml`) and the
  release-verify harness (`tools/release-verify`) reference
  `RELEASE_KEY_CUSTODY.md` for which CI secrets are populated and what
  `--allow-unsigned-deb` exception is in effect for which alpha
  revision.
- Design-partner signoffs land under `agile/release-evidence/v0.X.0-alpha/
  signoffs/` per the contract in `DESIGN_PARTNER_SIGNOFF_TEMPLATE.md`.
- Audit verifier exit codes are panel-locked across alpha → beta →
  stable; SIEM dashboards alert on `2` and `3`, treat `1` as
  informational, ignore `64` / `66` unless they spike.
