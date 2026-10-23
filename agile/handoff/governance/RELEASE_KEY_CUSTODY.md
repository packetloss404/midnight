---
id: GOV-RELEASE-KEY-CUSTODY
decision_ref: P0-D004
status: decided + runnable
owner: PM-B / Security
deciders: [PM-B, Security, Release Manager]
related_decisions: [P0-D022]
related_features: [FEATURE_AUDIT_HASH_CHAIN]
related_adrs: []
date: 2026-04-27
runbook_for: v0.1.0-alpha (first protected release)
---

# Release-Key Custody (P0-D004)

This document closes `PHASE0_DECISION_LOG.md` row **P0-D004 — Release-signing
key custody path**. It is the single source of truth for which signing
material the release pipeline (`.github/workflows/release.yml`) consumes,
how that material is generated, where it lives, who can use it, how it
rotates, and how compromise is contained.

## TL;DR — channel-by-channel custody

The decision: **test keys for `dev` channel CI; offline-or-KMS for
`alpha` / `beta`; KMS-required for `stable` / `enterprise-lts`.**

| Channel | Allowed signing path | Disallowed | Rationale |
|---|---|---|---|
| `dev` | Ephemeral CI test key (in-job key generation, discarded on job exit). `release-verify --allow-unsigned-deb` accepted. | KMS-backed key, YubiKey, production GPG identity. | Nightly `main` builds; not for design partners. Cost-zero, blast radius zero. |
| `alpha` | (i) cosign-keyless (Sigstore Fulcio + GitHub OIDC) **for OCI images**; AND one of (ii) GPG `.deb` signing key on a YubiKey held by the release manager OR (iii) AWS KMS / GCP KMS asymmetric key. `--allow-unsigned-deb` accepted only for `v0.1.0-alpha-1` per the alpha changelog. | Long-lived plaintext GPG key on disk. Same identity as `stable`. | Design-partner gated. Must be cryptographically distinct from `stable` so a compromised alpha pipeline cannot forge a stable `.deb`. |
| `beta` | Same as `alpha`, but the `.deb` MUST be signed (no `--allow-unsigned-deb`). Beta key is **not** the stable key. | Reusing the alpha key. | Public OSS announcement; production-adjacent. |
| `stable` | KMS-backed asymmetric key for the `.deb` signature (operator chooses AWS KMS, GCP KMS, or HashiCorp Vault Transit). cosign-keyless OR cosign with KMS key for OCI images. | Plaintext GPG key on disk anywhere. YubiKey-only without an operator-tested KMS recovery path. | `v1.0.0` and onward; long-lived; recovery from key loss must not require key reissuance to every operator. |
| `enterprise-lts` | KMS-backed key with a documented enterprise rotation/recovery contract; second signature ("witness") from the release manager required for every release tag. | Single-signature releases. | Phase 4 only. Not in scope before Phase 4. |

## Per-`alpha` options

The `alpha` row above lists three concrete options. Until the first
protected `alpha` tag, the operator may choose one option; the choice
must be recorded in this document's "Operational checklist" section
below before the tag is signed.

### Option (i) — cosign-keyless via Sigstore Fulcio + GitHub OIDC

What it is. The release workflow runs in `github.com/midnight/midnight`
under a tag-protected branch. Sigstore Fulcio issues a short-lived (≤10
min) X.509 code-signing cert bound to the GitHub Actions OIDC token, and
the OCI image is signed with that ephemeral cert. The signing material
never persists outside the runner.

Pros.
- **No long-lived key custody.** Compromise window = the runtime of one
  GitHub Actions job.
- **OSS-friendly.** Verifiers only need the Fulcio root + the GitHub
  OIDC issuer URL (`https://token.actions.githubusercontent.com`), which
  is what `docs/release-verify.md` Step 2 already documents.
- **Already wired** in `.github/workflows/release.yml` → `Cosign sign
  container images (keyless via Fulcio)` step.

Cons.
- **Online-only.** Fulcio + Rekor must be reachable at sign time.
- **Identity is GitHub's OIDC token**, which means whoever can push to
  the release-protected branch can sign. The branch protection is the
  identity boundary, not a hardware key.
- **`.deb` signing is separate.** cosign keyless does not sign Debian
  packages; the `.deb` still needs option (ii) or (iii) below.

Recommended default for `alpha` OCI images. **Yes** — the workflow
already implements it; we only need to document the verification surface
(done — `docs/release-verify.md` Step 2).

### Option (ii) — GPG `.deb` signing key on a YubiKey held by the release manager

What it is. A 4096-bit RSA (or Ed25519) GPG key generated on a YubiKey
5-series with a touch policy of `cached-fixed`. The CI workflow
references the public fingerprint via `MIDNIGHT_RELEASE_GPG_KEYID`; the
private material never leaves the YubiKey. Signing happens either (a)
on the release manager's workstation with the YubiKey present, with the
`.deb` then uploaded as a release-attached artifact, or (b) on a
hardened CI runner with YubiKey forwarding (PCSC over a self-hosted
runner — adds attack surface; not preferred).

Pros.
- **Hardware-attested.** Private key cannot be exfiltrated without
  physical access to the YubiKey.
- **Works offline.** No dependency on AWS/GCP/Sigstore at sign time.
- **Cheap.** ~$50 per key; release manager + one backup YubiKey.

Cons.
- **Manual step in the release flow.** The release workflow cannot fully
  automate `.deb` signing; release manager must touch the key.
- **Single-person bottleneck** unless multi-key witness signing is set
  up (recommended: two YubiKeys, both fingerprints published, both can
  sign — quorum of 1).
- **Backup procedure is non-trivial.** Recovery from a lost YubiKey
  requires either a pre-provisioned backup YubiKey or full key reissuance
  (publish new fingerprint, every operator updates `apt-key` / keyring).

Recommended for `alpha` `.deb` signing. **Yes** if (iii) is not yet
provisioned. Plan for two YubiKeys with the fingerprints published in
this document and in `docs/release-verify.md`.

### Option (iii) — AWS KMS / GCP KMS asymmetric key with short-lived signing tokens

What it is. An asymmetric `RSA_4096` (or `EC_P256`) key in AWS KMS
(`KeyUsage=SIGN_VERIFY`) or GCP KMS (`purpose=ASYMMETRIC_SIGN`). The
release workflow assumes a workload-identity role (`role-to-assume`
configured in the workflow), exchanges the GitHub OIDC token for a
short-lived AWS/GCP credential (≤1 hour), invokes
`kms:Sign` / `kms.cryptoKeyVersions.asymmetricSign`, and embeds the
signature in the `.deb` `.changes` envelope. The private key never
leaves KMS.

Pros.
- **No human in the loop.** Tag-driven release fully automated.
- **Cloud-provider audit logs** of every signing operation, with the
  caller identity bound to the GitHub Actions OIDC token.
- **Recovery is trivial.** A new key version is provisioned in KMS; the
  fingerprint update is a doc change, not a key reissuance.

Cons.
- **Requires an AWS/GCP account.** Adds a paid dependency for an OSS
  project that has, until now, depended on no commercial cloud.
- **Verification requires the public key** to be published; consumers
  must trust the published key vs. the live KMS view (mitigated by
  cosign + Sigstore as a transparency layer when KMS is wired through
  cosign, but pure GPG `.deb` signing has no transparency log).
- **Operational complexity.** Workload-identity federation (OIDC trust
  policy, `role-to-assume`, KMS grant) needs ops review.

Recommended posture. **Defer KMS-required path to `stable` / `v1.0.0`.**
At `alpha` we permit (iii) but do not require it; (ii) is acceptable for
`alpha` because the alpha audience is design partners, not the public
OSS surface. KMS becomes mandatory at `stable`.

## Decision criteria

When choosing between (i) / (ii) / (iii) for a given channel, weigh:

1. **OSS reach.** How many independent verifiers must hold the public
   key without operating their own KMS? cosign-keyless (i) wins; KMS
   (iii) requires public-key publication; YubiKey (ii) requires
   fingerprint publication. All three publish a public fingerprint.
2. **Blast radius if compromised.**
   - (i) compromise = a GitHub Actions OIDC token is forged or a
     release-protected branch is pushed to. Window is one job.
   - (ii) compromise = YubiKey is physically stolen and PIN known.
     Window is unbounded until the key is revoked.
   - (iii) compromise = the AWS/GCP IAM role is misused. Window is one
     KMS-token TTL, with cloud audit logs of every misuse.
3. **Recovery time.** (i) recovers in seconds (next workflow run
   re-issues the cert). (ii) recovers in days (publish new fingerprint,
   every operator re-trusts). (iii) recovers in minutes (rotate KMS key
   version, update doc).
4. **Cost.** (i) zero. (ii) hardware ~$50 × 2 + release-manager time.
   (iii) cloud account + KMS monthly + maintenance.
5. **Contributor ergonomics.** (i) zero friction (no tokens to manage).
   (ii) requires the release manager to be physically present at sign
   time. (iii) requires CI/role/OIDC setup.

For the **first protected `alpha` release**, the recommended bundle is:

- OCI images: option (i) — cosign-keyless (already wired).
- `.deb`: option (ii) — YubiKey-held GPG key (set up before tag).
- Stable transition: replace (ii) with (iii) before `v1.0.0`.

## Operational checklist for the first protected signed release

This checklist runs once before `v0.1.0-alpha` (final) is tagged. PM-B
and Release Manager co-sign at the bottom.

1. **Choose channel.** Confirm tag is `alpha` (this checklist) vs. `beta`
   / `stable` (separate checklist).
2. **Choose option set.** Pick option (i) for OCI images; pick option
   (ii) **or** (iii) for `.deb` signing. Record choice in the sign-off
   block below.
3. **Generate keys.**
   - Option (i): nothing to do; Sigstore Fulcio handles ephemeral certs.
   - Option (ii): generate Ed25519 GPG key on YubiKey 1; clone to YubiKey
     2 as backup; export public key only; record fingerprint.
   - Option (iii): provision KMS key; configure GitHub OIDC trust
     policy; record key ARN + public-key SHA-256.
4. **Publish fingerprints.**
   - Update `docs/release-verify.md` "Published key fingerprint" section
     with the actual fingerprint(s).
   - Add the public-key file (or armored block) to
     `docs/release-keys/<channel>-<year>.asc`.
   - Push the public key to `keys.openpgp.org` (option ii only).
5. **Witness signature.**
   - For option (ii) and (iii): a second human on the release team must
     verify the published fingerprint matches the locally exported
     fingerprint, sign a one-line attestation
     (`agile/handoff/governance/attestations/<channel>-<year>-witness.txt`),
     and commit it. The attestation is itself signed by the witness's
     personal GPG key.
6. **Test the path end-to-end.**
   - Tag a `v0.1.0-alpha-rc1` test tag.
   - Run `release.yml` against the rc.
   - On the rc artifact: verify the `.deb` signature locally;
     verify the OCI image with cosign per `docs/release-verify.md`.
   - Run `go run ./tools/release-verify --tag v0.1.0-alpha-rc1` against
     the bundle; assert exit code 0 with the `--allow-unsigned-deb`
     flag **removed**.
   - Delete the rc tag; record the rc tag SHA in the sign-off block.
7. **Configure CI secrets.** (Repository → Settings → Secrets.)
   - `MIDNIGHT_RELEASE_GPG_KEYID` ← published fingerprint.
   - `MIDNIGHT_RELEASE_GPG_KEY` ← armored public-key block (option ii)
     OR omit (option i, iii).
   - For option (iii): `AWS_ROLE_TO_ASSUME` / `GCP_WORKLOAD_IDENTITY_PROVIDER`
     and the KMS key ID.
8. **Revocation procedure dry-run.**
   - Walk the documented revocation steps (publish revocation cert →
     update `docs/release-keys/REVOKED.md` → notify `security@` list →
     change CI secret to a placeholder so future jobs fail closed).
   - Record the dry-run timestamp in the sign-off block.
9. **Rotation cadence.**
   - Option (i): n/a — Fulcio rotates per-job.
   - Option (ii): rotate every **365 days** or on any suspected
     compromise; overlap two valid fingerprints during the rotation
     window for ≥30 days.
   - Option (iii): rotate KMS key version every **365 days**;
     overlap is automatic via KMS versioning.
10. **First-protected-release execution.**
    - This step ratifies that the `v0.1.0-alpha` tag has been signed and
      verified with the chosen option set. Cross-link to `P0-D022` (the
      design-partner signoff governance closure) — the first protected
      release MUST satisfy both P0-D004 and P0-D022 before publication.

## Runbook — alpha first-protected-release execution

This runbook is the **executable companion** to the policy/decision
sections above. It is written so that PM-B (or whoever runs the
release on the day of the tag) can paste each command without further
engineering input. The runbook ratifies the recommended bundle for the
first protected `alpha`:

- OCI images → option (i): cosign-keyless via Sigstore Fulcio + GitHub OIDC.
- `.deb`     → option (ii): YubiKey-resident GPG key held by PM-B / Release Manager.

KMS-backed signing (option (iii)) is permitted but deferred to `stable` per the table above.

### Step 1 — Prerequisites: GitHub repo settings

Goal: enable Sigstore Fulcio keyless signing from `release.yml`.

1. **Enable OIDC for the repository.** OIDC is enabled by default for
   public repos, but the workflow MUST be allowed to request the
   `id-token: write` permission. UI navigation:
   - Repository → **Settings** → **Actions** → **General** → **Workflow
     permissions** section.
   - Confirm "Read and write permissions" OR (preferred) leave default
     "Read repository contents and packages permissions" because the
     workflow declares `permissions:` explicitly per-job.
   - Save.
2. **Configure Actions secrets.** UI navigation:
   - Repository → **Settings** → **Secrets and variables** → **Actions**
     → **New repository secret**.
   - Add the four secrets in the table below. Values shown are the
     Sigstore-public defaults for `COSIGN_REKOR_URL` and
     `COSIGN_FULCIO_URL`; PM-B replaces `DEB_SIGNING_PRIVATE_KEY` /
     `DEB_SIGNING_PASSPHRASE` with values from step 5.

   | Secret name              | Default / origin                                 | Used by                              |
   |---|---|---|
   | `COSIGN_REKOR_URL`       | `https://rekor.sigstore.dev`                     | `cosign sign --rekor-url`            |
   | `COSIGN_FULCIO_URL`      | `https://fulcio.sigstore.dev`                    | `cosign sign --fulcio-url`           |
   | `DEB_SIGNING_PRIVATE_KEY`| Armored ASCII export from PM-B's YubiKey (step 5)| `gpg --import` then `debsign -k…`    |
   | `DEB_SIGNING_PASSPHRASE` | YubiKey PIN-protecting passphrase (step 5)       | `gpg --pinentry-mode loopback`       |

3. **Confirm tag protection.** UI navigation:
   - Repository → **Settings** → **Tags** → **New tag protection rule**.
   - Pattern: `v*`.
   - Save. This ensures only the `release` workflow + the documented
     human signers can push a `v*` tag, which is what binds the Fulcio
     identity to the repo (see Step 3).

**Success looks like:** the four secrets appear in
`Settings → Secrets and variables → Actions` (values masked); the
workflow log for the next `release.yml` run shows `id-token: write`
in its permissions block; the tag protection rule for `v*` is listed.

### Step 2 — Workflow permissions block

The release job MUST declare both `id-token: write` (so Fulcio can
exchange the GitHub OIDC token for a signing cert) and `contents: write`
(so the workflow can attach `.deb` / SBOM / provenance artifacts to the
GitHub Release). The exact YAML block live in `.github/workflows/release.yml`:

```yaml
# Hard-locked least-privilege. id-token:write is required for
# Sigstore Fulcio's keyless flow; contents:write is required for
# attaching release artifacts; attestations:write covers SBOM /
# SLSA-style provenance attachment.
permissions:
  contents: write
  id-token: write
  attestations: write
```

This block is at the **workflow top level** (not job level) so every
job inherits it; if a future PR splits the release into multiple jobs,
each job MUST also keep these three keys.

**Success looks like:** `cat .github/workflows/release.yml | head -30`
shows the block above verbatim; `actionlint` (or the YAML safe_load
fallback in the `workflow-lint` job in `.github/workflows/lint.yml`)
exits 0 against the file.

### Step 3 — Identity: the Fulcio cert subject

When the release workflow runs, GitHub Actions issues an OIDC token
that Fulcio exchanges for a short-lived (≤10 min) X.509 code-signing
certificate. The `subjectAlternativeName` (URI SAN) on that cert is
the workflow path bound to the tag ref:

```
https://github.com/<org>/midnight/.github/workflows/release.yml@refs/tags/v0.1.0-alpha
```

For the `<org>/midnight` repo on GitHub, `<org>` is the GitHub
organization that owns the `midnight` repository (e.g.
`midnight-platform`). The exact subject for the `v0.1.0-alpha` tag is:

```
https://github.com/midnight-platform/midnight/.github/workflows/release.yml@refs/tags/v0.1.0-alpha
```

Operators verify a published OCI image with:

```bash
cosign verify \
  --certificate-identity-regexp '^https://github\.com/.*/midnight/\.github/workflows/release\.yml@refs/tags/v0\.[0-9]+\.[0-9]+(-(alpha|beta))?$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  ghcr.io/midnight-platform/midnight-server:v0.1.0-alpha
```

The regex purposely accepts any future `v<MAJOR>.<MINOR>.<PATCH>` (and
optional `-alpha` / `-beta` suffix) so that operators do not need to
update their verification command when we cut `v0.1.0-alpha-1`,
`v0.2.0-alpha`, etc. The regex anchors `^…$` and escapes literal dots
to avoid wildcard drift.

**Success looks like:** `cosign verify` (above) prints a JSON envelope
where `optional.Subject` matches the canonical subject string and the
issuer is exactly `https://token.actions.githubusercontent.com`. See
[`docs/release-verify.md`](../../../docs/release-verify.md) → "Verify
the alpha release end-to-end" for the full expected JSON shape.

### Step 4 — Transparency log (Rekor)

Every Sigstore signing event is logged to the public Rekor
transparency log (`https://rekor.sigstore.dev`). The Rekor entry
binds together (image digest, signing cert, signature, timestamp) so
that an operator can confirm the signature was issued at a specific
moment in time, by a specific GitHub Actions run, and was not produced
out-of-band against a different image.

After signing, the workflow's verification step (Step 5 below) calls
`cosign verify` which transparently fetches the Rekor entry and
asserts the inclusion proof. Operators can also query Rekor by image
digest:

```bash
# Fetch the digest of the published image.
digest=$(crane digest ghcr.io/midnight-platform/midnight-server:v0.1.0-alpha)

# Search Rekor by sha256 hash of the image digest.
rekor-cli search --sha "${digest#sha256:}"

# Inspect the matching entry.
rekor-cli get --uuid <uuid-from-search>
```

The expected output: a `hashedrekord` entry whose `body.spec.signature`
references the X.509 cert from Step 3 and whose `inclusionProof`
verifies against the current Rekor merkle tree. The Rekor entry UUID
is recorded in the release evidence packet under
`artifacts/release/<version>/rekor-entries.json` for later operator
audit.

**Success looks like:** `rekor-cli search --sha <digest>` returns at
least one UUID; `cosign verify` (Step 3) implicitly succeeds against
the Rekor inclusion proof.

### Step 5 — GPG `.deb` signing (the YubiKey-resident bridge)

`cosign` keyless does not sign Debian packages; the `.deb` still needs
a separate signing path. The alpha posture is **option (ii) —
YubiKey-resident GPG key**.

#### 5a. YubiKey provisioning commands

Run on the release manager's hardened workstation. PM-B owns the
physical YubiKey; backup YubiKey is held by Security as a quorum-of-1
spare per the "Per-`alpha` options" section above.

```bash
# 1. Identify the inserted YubiKey (a 5-series with OpenPGP applet).
gpg --card-status

# 2. Reset OpenPGP applet on a fresh YubiKey (DESTROYS any prior key).
gpg --card-edit
gpg/card> admin
gpg/card> factory-reset
gpg/card> quit

# 3. Generate a fresh release signing key. Choose:
#    - 4096-bit RSA   (broader operator-side gpg compatibility), OR
#    - Ed25519 ECC    (smaller signatures, modern; preferred).
#    The interactive flow asks for: User-ID name, email, comment,
#    expiry. Use "Midnight Release Signing Key", "release@midnight.local",
#    no comment, and a 1-year expiry (rotation cadence per Step 9).
gpg --card-edit
gpg/card> admin
gpg/card> generate
   # answer y to "Make off-card backup?" — answer NO.
   # the key is generated on-card and never leaves the YubiKey.

# 4. Confirm and record the fingerprint.
gpg --card-status | sed -n 's/^Signature key \.\.\.\. : //p'

# 5. Export the ARMORED PUBLIC KEY (private material stays on YubiKey).
gpg --armor --export <FINGERPRINT> > midnight-release-pub.asc

# 6. Repeat steps 1-5 on the backup YubiKey to produce a SECOND
#    fingerprint. Both fingerprints are published (quorum-of-1 signing).
```

#### 5b. Publish the fingerprint into the repo

```bash
# Format: <algorithm> <fingerprint-hex> <created-date> <subject>
echo 'ed25519 <FINGERPRINT_HEX_64> 2026-04-27 Midnight Release Signing Key <release@midnight.local>' \
  > docs/release/keys/midnight-release-fingerprint.txt
git add docs/release/keys/midnight-release-fingerprint.txt
git commit -m "docs(release): publish v0.1.0-alpha release fingerprint"
```

The `<FINGERPRINT_HEX_64>` is the 40-char (RSA) or 64-char (Ed25519)
hex output of `gpg --card-status`. The placeholder file shipped on
this branch carries the schema banner "REPLACE ME WITH REAL
FINGERPRINT" — PM-B replaces the placeholder line with the real
fingerprint as part of the runbook execution and recommits.

#### 5c. Configure the `.deb` signing CI secrets

```bash
# On the workstation that holds the YubiKey, export the armored
# private key for upload into a GitHub Actions secret. Note: the
# YubiKey-resident path means we export the PUBLIC key only and use
# `debsign` with the YubiKey present at sign time. The CI secret is
# only the trust anchor used to import the public key into the
# runner's keyring; the private signing operation runs offline on
# the release manager's workstation, NOT in CI.

gpg --armor --export <FINGERPRINT> > deb-signing-public.asc

# Upload `deb-signing-public.asc` content as the secret
# `DEB_SIGNING_PRIVATE_KEY` (poorly named — historical;
# v0.2 rename ticket tracked separately). The secret is the public
# trust anchor; no private material ever leaves the YubiKey.

# Upload the YubiKey unlock passphrase as `DEB_SIGNING_PASSPHRASE`
# (only relevant if a non-YubiKey fallback path is ever wired;
# unused on the YubiKey-only path documented here).

# In the workflow, the release job runs:
#   echo "$DEB_SIGNING_PRIVATE_KEY" | gpg --batch --import
#   debsign -k$KEYID dist/midnight_*.changes
# When using YubiKey-only signing, debsign is invoked from the
# release manager's workstation and the resulting signed .changes
# is uploaded as a release artifact.
```

**Success looks like:** the new fingerprint file lands on
`phase1/blocker4-key-custody`'s tip; the two GitHub Actions secrets
are populated (values masked); a local `gpg --verify
midnight_<version>_amd64.deb.asc midnight_<version>_amd64.deb`
reports `Good signature from "Midnight Release Signing Key"`.

### Step 6 — Witness signature (second-human signoff)

Before any `v*` tag is pushed, a second human on the release team
MUST sign off via the existing PR review workflow. Concretely:

1. The PR that records the release-evidence packet (under
   `agile/release-evidence/<version>/`) is opened by PM-B.
2. PM-B attaches the relevant local verification output (`make lint`,
   `make test`, `make build`, and `make release-check` when release
   evidence changes). The GitHub Actions workflows are manual-only and are
   not branch-protection required-status checks.
3. Security or the Release Manager reviews the PR and signs the
   approval. GitHub records the reviewer identity in the merge
   commit's `Reviewed-by:` trailer.
4. PM-B merges the PR (squash-merge) and pushes the `v*` tag against
   the merge commit.

The witness signature is the GitHub PR review identity, NOT a separate
GPG signature on the tag. (Tag-side GPG signing is mandatory for
`stable`; for `alpha` it is recommended but not required since the
Fulcio cert + the Rekor inclusion proof already cryptographically bind
the artifacts to the tag commit.)

**Success looks like:** `gh pr view <PR> --json reviews --jq
'.reviews[] | select(.state=="APPROVED")'` returns at least one
APPROVED review from a security-team or release-team member.

### Step 7 — Fingerprint publication (in-repo + remote)

The fingerprint is published in TWO places so operators can defeat a
single point of compromise:

- **In the repo** (this branch's deliverable):
  `docs/release/keys/midnight-release-fingerprint.txt`. Operators clone
  the repo at the tag and read the file.
- **On the project website**:
  `https://midnight.local/release/keys/midnight-release-fingerprint.txt`
  (placeholder URL until the website lands; tracked under
  `docs/release/keys/README.md`). Operators fetch over HTTPS and
  compare the bytes against the in-repo copy.

Operators MUST verify both copies match before trusting any signed
`.deb`:

```bash
diff <(curl -fsSL https://midnight.local/release/keys/midnight-release-fingerprint.txt) \
     docs/release/keys/midnight-release-fingerprint.txt && echo "fingerprints match"
```

If `diff` reports a mismatch, the verifier emits the failure code
`RELEASE_FINGERPRINT_MISMATCH` (registered in `internal/failurecodes`
and documented in `docs/error-codes.md` per this blocker's deliverable
E). The release MUST NOT be deployed; PM-B and Security investigate.

**Success looks like:** the `diff` above is silent (return code 0);
both sources show the identical `<algorithm> <fingerprint-hex>
<created-date> <subject>` line.

### Step 8 — Revocation procedure

If the GPG key is suspected compromised (lost YubiKey, witnessed
shoulder-surf, off-host private-material exfiltration, etc.):

1. **Immediately** generate a revocation certificate against the
   compromised key (PM-B keeps a printed copy of the revocation cert
   in a sealed envelope for exactly this scenario):

   ```bash
   gpg --output midnight-release-revoke.asc --gen-revoke <FINGERPRINT>
   ```

2. **Publish the revocation cert into the repo:**

   ```bash
   cp midnight-release-revoke.asc docs/release/keys/REVOKED-<FINGERPRINT>.txt
   git add docs/release/keys/REVOKED-<FINGERPRINT>.txt
   git commit -m "security(release): revoke <FINGERPRINT> — compromised on $(date -u +%FT%TZ)"
   git push origin main
   ```

3. **Tombstone release.** Tag a `v0.1.0-alpha-revoked` release whose
   only artifact is the revocation certificate. Operators that
   subscribe to release notifications get an unmistakable signal that
   the previous fingerprint is no longer trustworthy.

4. **Notify design partners** via the partner mailing list
   (`design-partners@midnight.local`) and a pinned GitHub Discussions
   post titled `[REVOKED] release fingerprint <FINGERPRINT>`.

5. **Tag-side mitigation.** The Fulcio identity (Step 3) is
   automatically rotated by the next workflow run because the cert is
   re-issued per-run. If the revocation reveals that the workflow path
   itself has been pushed to by an attacker, change the workflow path
   (e.g., move signing into a dedicated `release-sign.yml`) — the
   Fulcio cert subject will then differ from the published expected
   subject, and operators using the regex from Step 3 will fail
   verification on the malicious artifact.

   The verifier (Sprint 1's `tools/release-verify` plus the agent-side
   `internal/runtime/docker/signature_verify.go`, owned by Blocker 3)
   refuses any `.deb` whose signing fingerprint matches a `REVOKED-*.txt`
   file under `docs/release/keys/`; this surfaces as `RELEASE_REVOKED_KEY`.

**Success looks like:** the dry-run of this revocation flow (no real
compromise) is recorded in the PM-B sign-off block under
`revocation_dry_run_at`; in the real-incident case, the tombstone tag
exists, the partner mailing list and GitHub Discussions post are
visible, and the next protected release ships against a fresh
fingerprint.

### Step 9 — Rotation cadence

| Material         | Cadence                          | Trigger                                       |
|---|---|---|
| GPG `.deb` key   | **Annually** (every 365 days)    | Calendar; OR any incident.                    |
| Backup YubiKey   | Same as primary; rotated together| Calendar.                                     |
| Fulcio cert      | **Per workflow run** (≤10 min)   | Implicit; not operator-managed.               |
| Rekor entry      | Permanent (transparency log)     | n/a — entries are append-only by design.      |

**Annual GPG rotation overlap window:** 30 days. Both the outgoing
and incoming fingerprints are published in
`docs/release/keys/midnight-release-fingerprint.txt` (one on each line)
during the window so operators have time to update their keyrings.

The Fulcio path needs no operator-side rotation tracking because the
identity is bound to the workflow path, not a long-lived key. Rotation
of the Fulcio identity is implicit in any workflow path change (e.g.,
splitting the release pipeline into multiple workflow files).

**Success looks like:** PM-B sets a calendar reminder for `now+365d`
to repeat steps 5a → 5c; the calendar reminder lives in the PM-B
sign-off block under `next_rotation_due`.

### Step 10 — Operational checklist (PM-B-runnable, copy-paste)

This is the day-of-tag command sequence. Each item is a check box;
the "what success looks like" assertion is the line below the
command.

```bash
# ------------------------------------------------------------------
# v0.1.0-alpha first protected release — operational checklist
# Read top-to-bottom; do NOT skip steps. Each step has a successful
# assertion that operators tick before moving on.
# ------------------------------------------------------------------

# [ ] 0. Prerequisites: secrets + tag protection (Step 1) are configured.
gh secret list | grep -E '^(COSIGN_REKOR_URL|COSIGN_FULCIO_URL|DEB_SIGNING_PRIVATE_KEY|DEB_SIGNING_PASSPHRASE)\b'
#     success: all four secrets listed; `Updated` column non-empty.

# [ ] 1. Tip is at the merge commit for the release-evidence PR (Step 6).
git log -1 --format='%H %s'
#     success: HEAD is the squash-merge commit, NOT a fork tip.

# [ ] 2. Replace the placeholder fingerprint with the YubiKey output (Step 5b).
$EDITOR docs/release/keys/midnight-release-fingerprint.txt
git diff docs/release/keys/midnight-release-fingerprint.txt
git commit -am "docs(release): publish v0.1.0-alpha fingerprint"
git push origin main
#     success: `git diff` shows the placeholder line replaced by the real
#     <algorithm> <fingerprint-hex> <created-date> <subject> line; CI is green.

# [ ] 3. Cut the rc tag and let release.yml run end-to-end (Step 6 of the
#       per-`alpha` operational checklist higher in this doc).
git tag v0.1.0-alpha-rc1
git push origin v0.1.0-alpha-rc1
gh run watch
#     success: release.yml exits 0 on the rc tag; cosign-verify step
#     reports the matching identity; release-verify smoke step reports
#     overall_status=pass.

# [ ] 4. Locally verify the rc artifacts.
gh release download v0.1.0-alpha-rc1 -D artifacts/release/v0.1.0-alpha-rc1/
gpg --recv-keys "$(awk '{print $2}' docs/release/keys/midnight-release-fingerprint.txt)"
dscverify artifacts/release/v0.1.0-alpha-rc1/midnight_*.changes
cosign verify \
  --certificate-identity-regexp '^https://github\.com/.*/midnight/\.github/workflows/release\.yml@refs/tags/v0\.[0-9]+\.[0-9]+(-(alpha|beta))?$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  ghcr.io/midnight-platform/midnight-server:v0.1.0-alpha-rc1
go run ./tools/release-verify --artifacts-dir artifacts/release/v0.1.0-alpha-rc1 --tag v0.1.0-alpha-rc1
#     success: dscverify says "Verifying signatures"; cosign verify says
#     "Verified OK" with the matching subject; release-verify exits 0
#     WITHOUT --allow-unsigned-deb.

# [ ] 5. Delete the rc tag and record its SHA in the PM-B sign-off block.
rc_sha=$(git rev-parse v0.1.0-alpha-rc1)
git tag -d v0.1.0-alpha-rc1
git push --delete origin v0.1.0-alpha-rc1
echo "rc_test_tag_sha: $rc_sha"
#     success: rc tag is gone from `git tag` and the remote; the SHA is
#     pasted into the sign-off block under `rc_test_tag`.

# [ ] 6. Walk the revocation dry-run (Step 8). Generate, but do NOT
#       publish, the revocation cert; verify it is well-formed.
gpg --output /tmp/midnight-release-revoke.asc --gen-revoke "$(awk '{print $2}' docs/release/keys/midnight-release-fingerprint.txt)"
gpg --import-options show-only --import /tmp/midnight-release-revoke.asc
shred -u /tmp/midnight-release-revoke.asc
#     success: gpg shows the revocation cert with the reason code
#     "no reason specified" (or whichever reason PM-B selected); file
#     is shredded; PM-B records the dry-run timestamp in the sign-off
#     block under `revocation_dry_run_at`.

# [ ] 7. Confirm both fingerprint sources match (Step 7).
diff <(curl -fsSL https://midnight.local/release/keys/midnight-release-fingerprint.txt) \
     docs/release/keys/midnight-release-fingerprint.txt && echo "fingerprints match"
#     success: prints "fingerprints match"; if it does not, STOP and
#     escalate to Security.

# [ ] 8. PM-B + Security + Release Manager fill and commit the sign-off
#       block at the bottom of this document.
$EDITOR agile/handoff/governance/RELEASE_KEY_CUSTODY.md
git diff agile/handoff/governance/RELEASE_KEY_CUSTODY.md
git commit -am "governance(release): co-sign v0.1.0-alpha custody execution"
git push origin main
#     success: `git log -1 --format='%an'` lists PM-B; reviewers added
#     via `gh pr review` for Security and Release Manager.

# [ ] 9. Cut the final tag.
git tag v0.1.0-alpha
git push origin v0.1.0-alpha
gh run watch
#     success: release.yml exits 0; the published GitHub Release page
#     shows the .deb, .changes, .buildinfo, *.sbom.json,
#     *.provenance.json, and the cosign-verify step output.

# [ ] 10. Set the next-rotation calendar reminder (Step 9).
gh issue create --title "rotate Midnight release GPG key (annual)" \
                --body "Due: $(date -u -d '+365 days' +%F). Per RELEASE_KEY_CUSTODY.md Step 9." \
                --label release-rotation
#     success: an issue exists with a due date one year out, labeled
#     `release-rotation`.
```

When all ten boxes are checked, the first protected `alpha` release
is shipped and the runbook is complete. The PM-B sign-off block at
the bottom of this document is the authoritative record.

## Cross-references

- Operator verification flow: [`docs/release-verify.md`](../../../docs/release-verify.md).
  Sections "Step 1 — `.deb` GPG signature" and "Step 2 — OCI image
  signatures (cosign)" are the inverse of the signing path documented
  here.
- CI integration: [`.github/workflows/release.yml`](../../../.github/workflows/release.yml).
  Steps `GPG-sign .deb (if release key configured)` and `Cosign sign
  container images (keyless via Fulcio)` consume the secrets / OIDC
  trust documented above.
- Failure code: `IMAGE_SIGNATURE_REQUIRED` — emitted by the Phase 1
  agent's `StaticSignatureVerifier`
  (`internal/runtime/docker/signature_verify.go`) when an image lacks
  cosign signatures and the runtime has `verify_signatures=true`. See
  Sprint 1 commit `239e3e3` (signed releases + SBOM/provenance + image-
  sig verify) for the in-process verifier.
- Failure code: `RELEASE_KMS_UNAVAILABLE` — emitted by the release
  pipeline if option (iii) is selected and the KMS endpoint is
  unreachable. Documented in `docs/release-verify.md` "Failure-code
  mapping".
- P0-D022 (design-partner signoff) — the **first protected release**
  cannot tag without both P0-D004 (this doc) AND P0-D022
  (`agile/handoff/governance/DESIGN_PARTNER_SIGNOFF_TEMPLATE.md`)
  satisfied. PM-B verifies both before greenlight.
- Sprint 1 deliverable that consumes this decision: the Sub-slice D
  release-pipeline merge (`239e3e3`) with `--allow-unsigned-deb` flagged
  ON until this checklist is co-signed and step 7's CI secrets are
  populated.

## PM-B sign-off block

```yaml
release_key_custody_signoff:
  decision_id: P0-D004
  status: decided + runnable
  channel_chosen_for_first_protected_release: alpha
  option_set:
    oci_images: keyless-fulcio-github-oidc          # option (i)
    deb_packages: yubikey-resident-gpg              # option (ii)
  fingerprints:
    deb_primary: <PLACEHOLDER — replace docs/release/keys/midnight-release-fingerprint.txt at step 5b>
    deb_witness: <PLACEHOLDER — second YubiKey fingerprint, same file, second line>
  fingerprint_published_in_repo_at:    <PLACEHOLDER — UTC ISO8601>
  fingerprint_published_on_website_at: <PLACEHOLDER — UTC ISO8601>
  rc_test_tag:     <PLACEHOLDER — fill at runbook step 5>
  rc_test_tag_sha: <PLACEHOLDER — fill at runbook step 5>
  ci_secrets_configured_at: <PLACEHOLDER — UTC ISO8601>
  revocation_dry_run_at:    <PLACEHOLDER — UTC ISO8601>
  next_rotation_due:        <PLACEHOLDER — UTC ISO8601 (+365d)>
  pm_b_acknowledges_runbook_runnable: <PLACEHOLDER — true | false>
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
  cross_link_p0_d022_satisfied: false
```

> Note: this sign-off block is a placeholder. Agent 4 (the Phase 0
> closure agent) cannot sign on PM-B / Security / Release Manager's
> behalf. The placeholder dates are filled by the named humans before
> `v0.1.0-alpha` (final) is tagged.
