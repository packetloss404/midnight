---
id: GOV-BACKUP-CREDENTIAL-BOUNDARY
release_tag: v0.1.0-alpha
status: approved-per-product-owner-confirmation
owner: PM-B / Security
deciders: [PM-B, Security]
related_decisions: [P0-D004]
related_features: [FEATURE_OFFHOST_BACKUP]
related_failurecodes: [BACKUP_CREDENTIALS_MISSING, BACKUP_CREDENTIALS_UNAUTHORIZED, BACKUP_OFFHOST_MISSING, BACKUP_STALE]
date: 2026-04-27
status_updated: 2026-04-30
---

# Backup Credential Boundary

This document closes the `v0.1.0-alpha` greenlight blocker (2)
"backup credential boundary" by ratifying the credential-loading
posture for off-host backup destinations. It is the single PM-B /
Security sign-off package for the env-var → sealed-variable
migration path the implementation in `internal/backup/credentials.go`
encodes.

## TL;DR — channel-by-channel posture

The decision: **env-var posture acceptable for `v0.1.0-alpha` and
`v0.2.0-alpha`; sealed-variable posture mandatory for
`v0.3.0-alpha` and onward.**

| Tag | Posture | `MIDNIGHT_BACKUP_AWS_*` accepted? | `MIDNIGHT_BACKUP_CREDENTIAL_REF` accepted? | Notes |
|---|---|---|---|---|
| `v0.1.0-alpha` | env-var default; sealed escalation opt-in | YES | YES (operator may opt in) | Documented in release notes; `BACKUP_CREDENTIAL_BOUNDARY.md` co-signed. |
| `v0.2.0-alpha` | env-var default; sealed escalation opt-in | YES | YES | Same posture; doctor surfaces a soft warning when env path is in use. |
| `v0.3.0-alpha` | sealed-variable mandatory | **REFUSED** at startup | YES (required) | Doctor refuses the install when env path is the only configured source. |
| `v0.4.0-beta` | sealed-variable mandatory + KMS-backed KEK | **REFUSED** | YES (required) | KEK custody hardened (P0-D004 stable channel posture). |

Rationale. Alpha-channel users are operators with shell access on
the host already; the marginal security improvement of forcing them
through the sealed-variable workflow is small relative to the
operational cost of demanding the bootstrap-admin + KEK rotation
paths be production-grade on day one. Those paths are panel-locked
to land in Lane 4 (KEK rotation) and Sub-slice A (bootstrap admin
first-login MFA — `389ec94`); both ship in `v0.1.0-alpha` but have
not yet been exercised against the multi-operator rotation
scenarios the sealed-variable workflow needs (KEK rotation grace,
multi-vault unseal, audit emit on every reveal). The
`v0.3.0-alpha` cutover gives Lane 4 + Sub-slice A two release
windows to harden those paths before the sealed posture is
mandatory.

## Threat model

### Who can read `MIDNIGHT_BACKUP_AWS_*`?

The env-var posture exposes the credential triple to:

1. **Root on the host** — `/proc/<pid>/environ` is `0400 root:root`
   for the `midnight-server` process under the panel-locked
   systemd unit. Root is already in the trusted compute base
   (TCB) for any local-adapter install; this is not a new
   exposure.
2. **The systemd unit's `EnvironmentFile=`** — Sub-slice A's
   packaged unit reads
   `/etc/midnight/backup-credentials.env` with mode `0600
   root:midnight`. The `midnight` group has read-only access; no
   other local user can read the file.
3. **The control-plane process memory** — once env vars are read
   into the Go runtime they live in `os.Environ()` until the
   process exits. The `internal/backup` package zeros the
   resolved `Credentials` struct after the MinIO client has
   consumed it, but Go's GC may copy the underlying buffers
   freely, so plaintext is treated as short-lived but not
   guaranteed-zero per the Lane 4 panel decision (line 1178).
4. **Crash dumps and core files** — disabled by default in the
   `midnight-server` systemd unit (`LimitCORE=0`). If the
   operator has overridden this knob, env vars surface in any
   produced core file.

### Blast radius if compromised

A leaked `MIDNIGHT_BACKUP_AWS_ACCESS_KEY_ID` /
`MIDNIGHT_BACKUP_AWS_SECRET_ACCESS_KEY` triple grants the holder
exactly the IAM scope of the operator-issued key. **The IAM
template below restricts that scope to `s3:PutObject`,
`s3:GetObject`, `s3:DeleteObject`, and `s3:ListBucket` on the
configured bucket only.** Worst case: an attacker can read every
backup that has ever been written to the bucket, write fraudulent
backups, or delete every existing backup. They cannot escalate to
other AWS resources because the policy denies `s3:*` wildcards and
denies `s3:ListAllMyBuckets`.

### Mitigations applied

- **Least-privileged IAM policy template** (next section). Operators
  who copy a `s3:*` wildcard policy out of an AWS quickstart guide
  are explicitly out-of-policy for Midnight.
- **Restic per-project encryption.** Per `FEATURE_OFFHOST_BACKUP.md`
  acceptance criterion 3 + 4, every project gets its own restic
  repository whose password is sealed by the platform KEK. An
  attacker with S3 read access still cannot decrypt the backup
  contents; the worst case is metadata exposure (snapshot tags
  surface project / service / volume ids) plus availability damage
  (deletion).
- **Audit emit on every credential resolution.** The
  `backup.credentials.resolve` audit row is written to Lane 4's
  hash-chained `audit_logs` table on every off-host backup write.
  An attacker with credentials but without audit-chain write access
  cannot suppress the trail; an attacker with both is already
  inside the TCB (re-entry into P0-D004 / `RELEASE_KEY_CUSTODY.md`).
- **`BACKUP_OFFHOST_MISSING` doctor warning.** `internal/backup/
  offhost_alert.go` fires the warning at `severity=warn` whenever
  the destination URI is unset, so operators who forget to
  configure off-host writes hear about it from `midnight doctor`
  rather than silently losing the redundancy.

## IAM policy template (S3-compatible)

The panel-locked least-privilege policy. Operators MUST scope this
to a single bucket per Midnight install. Wildcards on `Resource` are
acceptable as long as they remain bucket-scoped; wildcards on
`Action` are forbidden.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "MidnightBackupReadWriteWithinBucket",
      "Effect": "Allow",
      "Action": [
        "s3:PutObject",
        "s3:GetObject",
        "s3:DeleteObject"
      ],
      "Resource": "arn:aws:s3:::midnight-backups-<install-id>/*"
    },
    {
      "Sid": "MidnightBackupListBucket",
      "Effect": "Allow",
      "Action": [
        "s3:ListBucket"
      ],
      "Resource": "arn:aws:s3:::midnight-backups-<install-id>"
    },
    {
      "Sid": "MidnightBackupDenyEscalation",
      "Effect": "Deny",
      "Action": [
        "s3:ListAllMyBuckets",
        "s3:GetBucketPolicy",
        "s3:PutBucketPolicy",
        "s3:DeleteBucket"
      ],
      "Resource": "*"
    }
  ]
}
```

S3-compatible vendors. Translation guide for the panel-locked
destinations:

- **MinIO**: same policy via `mc admin policy add midnight
  midnight-backup.json` then `mc admin policy attach midnight
  midnight-backup --user backup-bot`.
- **Cloudflare R2**: scope an R2 token to a single bucket with
  Object Read/Write + List permissions. R2 does not honor IAM
  Deny statements; the equivalent posture is the absence of
  `Admin Read & Write` on the token.
- **Wasabi / Backblaze B2 with S3 API**: same JSON shape; both
  vendors document `iam.amazonaws.com:CreatePolicy`-equivalent
  surfaces.

## Migration path (env → sealed)

Operators move from env-var to sealed in three steps. The migration
is zero-downtime: the chain credential source supports both modes
simultaneously, with the sealed path winning when
`MIDNIGHT_BACKUP_CREDENTIAL_REF` is set.

### Step 1 — Enroll the credential as a sealed variable

```bash
# Use the workspace scope so the variable can be referenced from any
# project the workspace owns. Use `--scope=project` when isolation
# is required.
midnight admin variable set \
  --sealed \
  --scope=workspace \
  --workspace=wsp_<your_workspace_id> \
  --name=backup-credentials \
  --plaintext-stdin

# Paste the JSON payload, then Ctrl-D:
{
  "access_key_id":     "AKIA...",
  "secret_access_key": "...",
  "session_token":     ""
}
```

The CLI returns a `var_<ulid>` reference id. Record it; the next
step references it.

### Step 2 — Point the off-host backup at the sealed reference

```bash
# Add to /etc/midnight/backup-credentials.env (mode 0600 root:midnight):
MIDNIGHT_BACKUP_CREDENTIAL_REF=var_<the-ulid-from-step-1>
```

Reload the unit:

```bash
sudo systemctl reload midnight-server
```

The `internal/backup.ChainCredentialSource` notices the env ref is
set and switches to the sealed path. Every subsequent backup write
emits a `backup.credentials.resolve` audit row with `source: sealed`
and `reference_id: var_<ulid>`.

### Step 3 — Remove the legacy env vars

After confirming the next scheduled backup completes successfully
(check `midnight admin backup-coordinator status` or wait for the
`BACKUP_STALE` alert window — default 26h):

```bash
# Edit /etc/midnight/backup-credentials.env to remove:
#   MIDNIGHT_BACKUP_AWS_ACCESS_KEY_ID=...
#   MIDNIGHT_BACKUP_AWS_SECRET_ACCESS_KEY=...
#   MIDNIGHT_BACKUP_AWS_SESSION_TOKEN=...
sudo systemctl reload midnight-server
```

The chain source now has only the sealed path available; if
`MIDNIGHT_BACKUP_CREDENTIAL_REF` is unset, the backup write fails
fast with `BACKUP_CREDENTIALS_MISSING`.

### Rollback

If the sealed path misbehaves (Vault unreachable, KEK rotation
in-progress, etc.), roll back to env in two steps:

1. Unset `MIDNIGHT_BACKUP_CREDENTIAL_REF` in the env file.
2. Restore the legacy `MIDNIGHT_BACKUP_AWS_*` vars.

The chain source falls back to env automatically; no Midnight
restart is required beyond the `systemctl reload`.

## Operator runbook — rotate credentials with zero downtime

### Sealed-variable posture (v0.3.0+)

1. **Provision the new key in the IAM provider** (AWS / MinIO / R2)
   alongside the old one. Both keys remain valid.
2. **Update the sealed variable** via
   `midnight admin variable rotate --id=var_<ulid> --plaintext-stdin`.
   Lane 4's `Vault.Rotate` re-seals the new payload under the
   active KEK without ever re-revealing the previous plaintext
   (`secrets.SealedVariable` is panel-locked write-only).
3. **Wait for the next backup write.** The chain source resolves
   the sealed variable on every `NewS3DestinationCtx` call, so the
   next scheduled backup picks up the new credential. Monitor the
   `backup.credentials.resolve` audit row; the `reference_id`
   stays the same, but the underlying envelope `version` increments
   from N to N+1.
4. **Revoke the old IAM key** once the next successful backup
   verifies (typically within one scheduled-backup period).

### Env-var posture (v0.1.0-alpha / v0.2.0-alpha)

1. Provision the new key alongside the old one.
2. Edit `/etc/midnight/backup-credentials.env` to point at the new
   key. (`mode=0600 root:midnight`.)
3. `sudo systemctl reload midnight-server`. The reload re-reads
   the env file; the next backup write uses the new key.
4. Revoke the old IAM key once the next successful backup verifies.

### Audit chain expectation

Both rotation paths produce exactly one `backup.credentials.resolve`
audit row per credential resolve, identical in shape to a steady-
state backup write. Auditors detecting an unexpected gap or
duplicate row should follow `docs/runbooks/audit-chain-break.md`
(panel-locked `AUDIT_CHAIN_BROKEN`).

## Failure-code surface

The implementation registers two new typed failure codes
(see `internal/failurecodes/codes.go` + `docs/error-codes.md`):

| Code | Trigger | Recovery |
|---|---|---|
| `BACKUP_CREDENTIALS_MISSING` | Neither env-var nor sealed reference is configured. | Set `MIDNIGHT_BACKUP_AWS_ACCESS_KEY_ID`/`MIDNIGHT_BACKUP_AWS_SECRET_ACCESS_KEY`, or configure a sealed credential variable and point `MIDNIGHT_BACKUP_CREDENTIAL_REF` at its `var_<ulid>` id. |
| `BACKUP_CREDENTIALS_UNAUTHORIZED` | Sealed reference exists but the consumer principal lacks read scope on the referenced variable, or the reference target is missing. | Grant the backup principal read scope on the referenced sealed credential, or rotate `MIDNIGHT_BACKUP_CREDENTIAL_REF` to a variable in the consumer's allowed scope. |

Both codes wrap the `failurecodes.FailureError` envelope; callers
extract the structured `(code, recovery_hint)` pair via
`failurecodes.ReasonOf`. Audit emit is **fail-soft per Phase 1
default** (warn + continue per ADR-011 §"Operational Posture" item
3); the typed failure surfaces through the `internal/backup` API
boundary regardless.

## Cross-references

- **Implementation:** `internal/backup/credentials.go` —
  `CredentialSource` interface, `EnvCredentialSource`,
  `SealedCredentialSource`, `ChainCredentialSource`, and
  `VaultSealedResolver` adapter. Wired through
  `internal/backup/s3_destination.go`.
- **Tests:**
  - Unit: `internal/backup/credentials_test.go` (env / sealed /
    chain matrix; audit-row plaintext sweep; one-record-per-
    resolve cardinality).
  - Integration (`//go:build integration`):
    `internal/backup/credentials_integration_test.go` — Lane 4
    Vault → sealed reference → MinIO Put/Get round-trip.
- **Lane 4 substrate:**
  - `KeyProvider` interface — `internal/secrets/keyprovider.go`
    (consumed unchanged).
  - `Vault` + sealed-variable contract — `internal/secrets/vault.go`,
    `internal/secrets/sealed.go`, `internal/secrets/reference.go`.
  - Authorizer-driven `CanRead` on the consumer principal —
    `internal/secrets/reference.go`.
- **Audit chain:** every resolve emits a `backup.credentials.resolve`
  row through Lane 5's chain wireup
  (`internal/api/audit_emit.go`); chain math is
  `internal/audit/chain.go`.
- **Failure codes:** `internal/failurecodes/codes.go` rows
  `BackupCredentialsMissing` / `BackupCredentialsUnauthorized`;
  `docs/error-codes.md` rows `BACKUP_CREDENTIALS_MISSING` /
  `BACKUP_CREDENTIALS_UNAUTHORIZED`.
- **Off-host alert:** `internal/backup/offhost_alert.go` continues
  to surface `BACKUP_OFFHOST_MISSING` (warn) and `BACKUP_STALE`
  (critical); the credential failure codes are orthogonal — they
  fire at resolve time, before the destination is even contacted.
- **KEK custody:** [`RELEASE_KEY_CUSTODY.md`](RELEASE_KEY_CUSTODY.md)
  P0-D004 closure document. The sealed-variable workflow this
  boundary depends on inherits the KEK custody posture from
  P0-D004; the `v0.3.0-alpha` mandatory cutover for the sealed
  posture is gated on the P0-D004 "first-protected-release custody
  execution" checklist being co-signed.
- **Greenlight brief:**
  [`V010_ALPHA_GREENLIGHT_BRIEF.md`](V010_ALPHA_GREENLIGHT_BRIEF.md)
  §3 ratified-decisions row + §4 blocker (2). This document is the
  binding scope for the blocker (2) "shipped — PM-B / Security
  approval received per product owner / user confirmation on
  2026-04-30" status update.
- **Current operator contract:**
  [`docs/runbooks/backup-restore.md`](../../../docs/runbooks/backup-restore.md)
  — supported destination behavior, encryption boundaries, restore workflow,
  and failure recovery. The older `FEATURE_OFFHOST_BACKUP.md` planning spec was
  removed when the feature dossiers were consolidated.

## Out of scope

- **HashiCorp Vault adapter.** The Vault adapter is a Phase 4
  concern; Phase 1 ships the panel-locked `KeyProvider` boundary
  and consumes Lane 4's in-memory + file-backed providers only.
  No `github.com/hashicorp/vault/api` dependency is added in this
  slice.
- **AWS KMS / GCP KMS adapters.** Same — Phase 4 work; the
  `KeyProvider` interface is the seam.
- **GCS / SFTP / rsync destination support.** This boundary
  document defines the credential resolution path for the
  panel-locked S3-compatible destination only. The other three
  destination kinds (GCS, SFTP, rsync) are MVP Beta scope per
  `FEATURE_OFFHOST_BACKUP.md` and will reuse the `CredentialSource`
  interface when they land.
- **IAM role / OIDC federation.** Some operators run Midnight on
  hosts with EC2 instance profiles or GCP workload identity. The
  alpha posture requires explicit credentials; the
  `CredentialSource` interface is open for an
  `IAMRoleCredentialSource` implementation in MVP Beta but does
  not ship in `v0.1.0-alpha`.
- **Cosign Go SDK.** Out of scope here; tracked under blocker (3)
  in `V010_ALPHA_GREENLIGHT_BRIEF.md` §4.

## PM-B / Security sign-off block

Status update: PM-B / Security approval for this boundary was received
per product owner / user confirmation on 2026-04-30. The placeholder
signer fields below remain unmodified because this document does not
contain named signer evidence.

```yaml
backup_credential_boundary_signoff:
  decision_id: GOV-BACKUP-CREDENTIAL-BOUNDARY
  release_tag: v0.1.0-alpha
  posture_alpha: env-var default; sealed escalation opt-in
  posture_alpha_2: env-var default; sealed escalation opt-in
  posture_alpha_3: sealed-variable mandatory
  posture_beta:    sealed-variable mandatory + KMS-backed KEK
  iam_policy_template_reviewed: <PLACEHOLDER — true | false>
  threat_model_reviewed:        <PLACEHOLDER — true | false>
  migration_path_reviewed:      <PLACEHOLDER — true | false>
  rotation_runbook_reviewed:    <PLACEHOLDER — true | false>
  cross_link_p0_d004_satisfied: <PLACEHOLDER — true | false>
  signers:
    pm_b:
      name: <PLACEHOLDER>
      date: <PLACEHOLDER — UTC ISO8601>
    security:
      name: <PLACEHOLDER>
      date: <PLACEHOLDER — UTC ISO8601>
```

> Status note: approval was received per product owner / user
> confirmation on 2026-04-30. The placeholder dates are preserved as
> historical evidence fields and should only be replaced by named signer
> evidence.
