# Release Verification

This document is the operator-facing companion to the
`tools/release-verify/` harness. It walks through the panel-locked
checks every operator MUST run after downloading a Midnight release
artifact.

## TL;DR

```bash
# 1. Verify the .deb GPG signature against the published key.
gpg --verify midnight_<version>_amd64.deb.asc midnight_<version>_amd64.deb

# 2. Verify the standalone CLI checksum manifest before using its hashes.
cosign verify-blob midnight_<version>_SHA256SUMS \
    --bundle midnight_<version>_SHA256SUMS.sigstore.json \
    --certificate-identity-regexp '^https://github\.com/packetloss404/midnight/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$' \
    --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'

# 3. Verify the OCI image signatures with cosign.
cosign verify ghcr.io/midnight-platform/midnight-server:<version> \
    --certificate-identity-regexp '^https://github\.com/packetloss404/midnight/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$' \
    --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'

# 4. Verify the SBOM + provenance attestations.
cosign verify-attestation --type cyclonedx ghcr.io/midnight-platform/midnight-server:<version>
cosign verify-attestation --type slsaprovenance ghcr.io/midnight-platform/midnight-server:<version>

# 5. End-to-end automated smoke (CI + reproducible local run):
go run ./tools/release-verify --artifacts-dir artifacts/release --tag <version>
```

## Published key fingerprint

Trust status: no trusted Midnight release-signing fingerprint is
currently published in `docs/release/keys/midnight-release-fingerprint.txt`.
The file contains only comments and an explicitly untrusted all-zero
example. Operators must stop before any `.deb` trust decision until a
real fingerprint is published in both locations below and the two copies
match.

The active release-signing fingerprint(s) are published in two places
that operators MUST cross-check before trusting any signed `.deb`:

1. **In the repo:** [`docs/release/keys/midnight-release-fingerprint.txt`](release/keys/midnight-release-fingerprint.txt)
   (one signing identity per non-comment line, format `<algorithm>
   <fingerprint-hex> <created-date> <subject>`).
2. **On the project website:**
   `https://deployatmidnight.com/release/keys/midnight-release-fingerprint.txt`.
   This is the reserved path and is not published yet; signed releases remain
   untrusted until it serves the reviewed fingerprint.

The two copies MUST match byte-for-byte:

```bash
diff <(curl -fsSL https://deployatmidnight.com/release/keys/midnight-release-fingerprint.txt) \
     docs/release/keys/midnight-release-fingerprint.txt && echo "fingerprints match"
```

A mismatch surfaces as `RELEASE_FINGERPRINT_MISMATCH`; STOP and
escalate per `SECURITY.md`.

The release pipeline (`.github/workflows/release.yml`) consumes the
`DEB_SIGNING_PRIVATE_KEY` and `DEB_SIGNING_PASSPHRASE` Actions
secrets configured per
[`RELEASE_KEY_CUSTODY.md`](../agile/handoff/governance/RELEASE_KEY_CUSTODY.md)
Step 5c. On a `v*` tag, the workflow refuses to publish if the
secret is unset (fail-closed); on `workflow_dispatch` test runs the
alpha exception still applies via `--allow-unsigned-deb` so forks
can build PRs without signing material.

> The `release-verify` harness refuses to declare success on an
> unsigned `.deb` UNLESS `--allow-unsigned-deb` is explicitly passed.
> The flag is REMOVED from the GREEN-floor verification step in
> `release.yml` per the `v0.1.0-alpha` greenlight blocker (4); it
> remains available only for fork / dispatch test runs and for
> historical compatibility.

## Step 1 — `.deb` GPG signature

`midnight_<version>_<arch>.deb` is signed with the operator-trusted
release key. Debian package revisions and product versions are separate:
`0.1.0~alpha-6` is package revision 6 of binaries that report
`0.1.0-alpha`. The private maintenance package is not a public release until
the signing workflow passes. The CI workflow attaches:

| File | Purpose |
|---|---|
| `midnight_<version>_<arch>.deb` | The package itself. |
| `midnight-dbgsym_<version>_<arch>.deb` (Debian) or `.ddeb` (Ubuntu) | Debug symbols referenced by the Debian manifests. Exactly one host-native suffix is retained. |
| `midnight_<version>_<arch>.deb.asc` | Detached GPG signature (or `.changes` carrying one). |
| `midnight_<version>_<arch>.changes` | Debian build manifest signed by the maintainer. |
| `midnight_<version>_<arch>.buildinfo` | dpkg `--print-architecture` reproducible-build manifest. |

Build the package only from a pristine detached worktree at the reviewed
commit. The package gate rejects untracked or ignored `web-next/.env*` files
and ambient `VITE_*` variables; otherwise Vite could compile unreviewed
behavior while the Go binaries still truthfully report `vcs.modified=false`.
Docker Engine 29 publishes its supported Go SDK as the separate
`github.com/moby/moby/api` and `github.com/moby/moby/client` modules. Release
builds pin API `v1.55.0`, client `v0.5.1`, and `github.com/moby/go-archive
v0.3.3`. The archive floor must remain at least `v0.3.0`, which fixes
GO-2026-6253; the newer `v0.3.3` also includes the subsequent hard-link
compatibility repair. Neither the retired `github.com/docker/docker` module nor
the engine-only `github.com/moby/moby/v2` root module may appear in the selected
build list.

Before accepting the `.deb`, extract its control archive and inspect the
generated `prerm` and `postinst`. They must preserve the custom exact-active
upgrade snapshot, contain no fresh-install or unconditional enable path, and
contain no generated runtime activation or stop-on-upgrade path. With
`--no-enable`, debhelper deliberately preserves boot enablement on upgrade:
its marker-delimited block may call `deb-systemd-helper enable` only when the
same literal unit is guarded by both `debian-installed` and `was-enabled`.
That state preservation must never be used to decide which services restart;
only the root-owned exact-active snapshot may do that. The artifact verifier
matches those guarded blocks for the fixed six-unit allowlist and rejects any
other helper enable, every post-install `deb-systemd-invoke`/`invoke-rc.d`, and
every generated prerm service invocation. Direct activation is limited to the
custom exact-active snapshot restart; custom prerm owns removal and upgrade
stops, with no enablement-state fallback.
The source `dh_installsystemd` invocation must include `--no-enable --no-start
--no-stop-on-upgrade`.

For the first private `0.1.0~alpha-2` to `0.1.0~alpha-3` VM upgrade, transfer
`deploy/debian/upgrade-alpha2-bridge` with the package and run the bridge as
root with the package path as its sole argument. It captures the exact-active
snapshot and immediately replaces itself with `dpkg`, closing the race between
the two operations. This is a one-transition compatibility step because
alpha-2's installed `prerm` cannot create alpha-3's snapshot. Do not substitute
enabled-unit inference, and do not overwrite a snapshot retained from a failed
attempt.

Run:

```bash
gpg --keyserver hkps://keys.openpgp.org \
    --recv-keys <PUBLISHED_FINGERPRINT_HERE>
gpg --verify midnight_0.1.0~alpha-6_amd64.deb.asc midnight_0.1.0~alpha-6_amd64.deb
```

`gpg` must report `Good signature from "Midnight Release ..."`. If it
reports `BAD signature` STOP and consult the security advisory on
the private reporting path in `SECURITY.md`.

## Step 2 — standalone CLI manifest signature (cosign)

Before checking an archive hash, verify the standalone CLI manifest's Sigstore
bundle. The bundle binds the exact checksum bytes to the protected tag execution
of `.github/workflows/release.yml`; dispatch builds intentionally do not produce
one.

```bash
cosign verify-blob midnight_<version>_SHA256SUMS \
  --bundle midnight_<version>_SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/packetloss404/midnight/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
sha256sum --check midnight_<version>_SHA256SUMS --ignore-missing
```

Only after both commands pass should an operator extract a standalone archive.
The matching `DISTRIBUTION_SHA256SUMS` bundle covers the generated npm tarballs,
Homebrew formula, Scoop manifest, and publication manifest.

## Step 3 — OCI image signatures (cosign)

The release pipeline signs every OCI image keyless via Sigstore
Fulcio. The certificate identity is locked to the GitHub Actions
OIDC token of the `packetloss404/midnight` repo:

```bash
cosign verify ghcr.io/midnight-platform/midnight-server:<version> \
    --certificate-identity-regexp '^https://github\.com/packetloss404/midnight/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$' \
    --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
```

The output MUST include the line `Verified OK`. Any other text means
the image was either signed by a different identity or not signed at
all; either case is `IMAGE_SIGNATURE_INVALID` and the release should
NOT be deployed.

## Step 4 — SBOM + SLSA-style provenance

Each release artifact in `artifacts/release/` includes one
`<component>.sbom.json` (CycloneDX 1.5) and one
`<component>.provenance.json` (SLSA v1 / in-toto v1) emitted by
Lane 3's BuildKit pipeline. They can be verified against the
configured cosign identity:

```bash
cosign verify-attestation --type cyclonedx \
    --certificate-identity-regexp '^https://github\.com/packetloss404/midnight/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$' \
    --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
    ghcr.io/midnight-platform/midnight-server:<version>

cosign verify-attestation --type slsaprovenance \
    --certificate-identity-regexp '^https://github\.com/packetloss404/midnight/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$' \
    --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
    ghcr.io/midnight-platform/midnight-server:<version>
```

> ⚠️ Public claim language: Midnight emits **SLSA-style** provenance.
> No SLSA Build Level claim is made until an external audit closes
> Phase 4. The provenance is faithful to the in-toto v1 schema and
> records the BuildKit version digest, every resolved base-image
> digest, and the canonical Dockerfile hash.

## Step 5 — `release-verify` automation

Every release artifact tree is checked end-to-end by:

```bash
go run ./tools/release-verify --artifacts-dir artifacts/release --tag <version>
```

The harness emits a JSON report and exits non-zero when any finding
is `fail`. Findings are categorized:

| Category | What it asserts |
|---|---|
| `deb-signature` | At least one `.deb.asc` (or `.changes`) per `.deb`. |
| `image-digest` | `image-digests.txt` records every required OCI component: `server`, `worker`, `agent`, `buildkit`, and `registry`. |
| `cosign` | Each required OCI component has `cosign/<component>.verify.json` or `cosign/<component>.sig`. |
| `sbom` | At least one `*.sbom.json` per release. |
| `provenance` | At least one `*.provenance.json` per release. |

Run with `--allow-unsigned-deb` only for unsigned alpha smoke bundles.
Run with `--allow-missing-supplychain` only for `workflow_dispatch`
or fork smoke runs where GHCR signing and attestation publication are
intentionally unavailable. Protected tag releases must omit both flags
for the GREEN-floor verification.

## Verify the alpha release end-to-end

Use this section as the operator-facing companion to the runbook in
[`RELEASE_KEY_CUSTODY.md`](../agile/handoff/governance/RELEASE_KEY_CUSTODY.md)
Step 10. The six steps below are what an operator runs **after** a
`v0.1.0-alpha` tag is published, to gain confidence the release was
produced by Midnight's release workflow against the documented
fingerprint and is safe to deploy.

### 1. Download the signed `.deb` and its `.changes`

```bash
gh release download v0.1.0-alpha -D artifacts/release/v0.1.0-alpha/
ls artifacts/release/v0.1.0-alpha/
# success: midnight_<version>_amd64.deb,
#          midnight_<version>_amd64.changes,
#          midnight_<version>_amd64.buildinfo,
#          image-digests.txt,
#          cosign/{server,worker,agent,buildkit,registry}.verify.json,
#          *.sbom.json, *.provenance.json present.
```

### 2. Receive the published key into the local keyring

```bash
fp=$(grep -vE '^(#|[[:space:]]*$)' docs/release/keys/midnight-release-fingerprint.txt | head -1 | awk '{print $2}')
test -n "$fp"
gpg --keyserver hkps://keys.openpgp.org --recv-keys "$fp"
# success: gpg reports "1 imported" or "unchanged" (already trusted).
```

The fingerprint is read from
[`docs/release/keys/midnight-release-fingerprint.txt`](release/keys/midnight-release-fingerprint.txt);
operators ALWAYS cross-check against the project-website mirror per
the `diff` command higher in this document before proceeding.

### 3. Verify the `.deb` signature with `dscverify`

```bash
dscverify artifacts/release/v0.1.0-alpha/midnight_*.changes
# success: dscverify prints
#   "midnight_<version>_amd64.changes:
#      Verifying signatures...
#      All files validated successfully."
```

If `dscverify` reports `BAD signature`, STOP — the artifact was
tampered with after the release manager signed it; engage the
security-review path. If `dscverify` reports a different signing
fingerprint than the published one, the verifier emits
`RELEASE_FINGERPRINT_MISMATCH`.

### 4. Verify the standalone CLI checksum manifest

```bash
cosign verify-blob artifacts/release/v0.1.0-alpha/midnight_0.1.0-alpha_SHA256SUMS \
  --bundle artifacts/release/v0.1.0-alpha/midnight_0.1.0-alpha_SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/packetloss404/midnight/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
```

Only use the checksum lines after this identity-bound verification succeeds.

### 5. Verify the OCI image signature with `cosign`

```bash
cosign verify \
  --certificate-identity-regexp '^https://github\.com/packetloss404/midnight/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  ghcr.io/midnight-platform/midnight-server:v0.1.0-alpha
```

Success means cosign prints a JSON envelope (see "Examples" below)
where every entry's `optional.Subject` matches the expected workflow
path and the issuer is exactly
`https://token.actions.githubusercontent.com`. The expected JSON
shape is in the [Examples](#examples) subsection so operators can
diff field-by-field.

### 6. Run `tools/release-verify` against the bundle

```bash
go run ./tools/release-verify \
    --artifacts-dir artifacts/release/v0.1.0-alpha \
    --tag v0.1.0-alpha
# success: exit 0 with overall_status="pass". The harness asserts:
#   - every .deb has a .changes (or .deb.asc)
#   - image-digests.txt includes server, worker, agent, buildkit, registry
#   - every required OCI component has cosign verification evidence
#   - SBOM (CycloneDX) attached
#   - SLSA-style provenance attached
# Note: --allow-unsigned-deb and --allow-missing-supplychain are
# INTENTIONALLY OMITTED. On the GREEN floor both are forbidden; if either
# appears here, the release-verify greenlight blocker is regressed.
```

When all five steps pass, the release is verified and safe to deploy.
Record the verification timestamp and the operator name in the
deployment audit trail per `SECURITY.md`.

### Examples

A known-good `cosign verify` output for `v0.1.0-alpha` looks like
this (sanitized; replace `<...>` placeholders with the actual values
from the published artifact):

```json
[
  {
    "critical": {
      "identity": {
        "docker-reference": "ghcr.io/midnight-platform/midnight-server"
      },
      "image": {
        "docker-manifest-digest": "sha256:<64-char-hex>"
      },
      "type": "cosign container image signature"
    },
    "optional": {
      "1.3.6.1.4.1.57264.1.1": "https://token.actions.githubusercontent.com",
      "1.3.6.1.4.1.57264.1.2": "push",
      "1.3.6.1.4.1.57264.1.3": "<commit-sha>",
      "1.3.6.1.4.1.57264.1.4": "release",
      "1.3.6.1.4.1.57264.1.5": "midnight-platform/midnight",
      "1.3.6.1.4.1.57264.1.6": "refs/tags/v0.1.0-alpha",
      "Bundle": {
        "SignedEntryTimestamp": "<base64-rfc3161-timestamp>",
        "Payload": {
          "body": "<base64-hashedrekord>",
          "integratedTime": <unix-seconds>,
          "logIndex": <rekor-log-index>,
          "logID": "<rekor-log-id>"
        }
      },
      "Issuer": "https://token.actions.githubusercontent.com",
      "Subject": "https://github.com/packetloss404/midnight/.github/workflows/release.yml@refs/tags/v0.1.0-alpha"
    }
  }
]
```

The values an operator MUST diff field-by-field against the expected:

| JSON path                                | Expected (for `v0.1.0-alpha`)                                                                                              |
|---|---|
| `[0].critical.identity.docker-reference` | `ghcr.io/midnight-platform/midnight-<component>`                                                                           |
| `[0].critical.type`                      | `cosign container image signature`                                                                                         |
| `[0].optional.Issuer`                    | `https://token.actions.githubusercontent.com`                                                                              |
| `[0].optional.Subject`                   | `https://github.com/packetloss404/midnight/.github/workflows/release.yml@refs/tags/v0.1.0-alpha`                          |
| `[0].optional.Bundle.Payload.logIndex`   | non-zero integer (the Rekor entry index — non-zero confirms transparency-log inclusion)                                    |
| `[0].optional.1.3.6.1.4.1.57264.1.6`     | A SemVer tag under `refs/tags/v`; the Sigstore source-ref OID must match the exact release tag and workflow identity policy. |

Any mismatch means the artifact was NOT produced by the documented
release workflow against the documented tag; treat it as
`IMAGE_SIGNATURE_INVALID` and STOP.

## Failure-code mapping

| Code | When it fires |
|---|---|
| `IMAGE_SIGNATURE_INVALID` | cosign verify reports the image was signed by a non-trusted identity. |
| `IMAGE_SIGNATURE_REQUIRED` | Agent runtime is configured with `verify_signatures=true` and the requested image carries no signature at all. |
| `IMAGE_SIGNATURE_VERIFY_BINARY_MISSING` | Agent has `verify_signatures=true` but the cosign binary is not at `cosign_binary_path`. The agent refuses to silently downgrade enforcement (set the path or disable enforcement). |
| `IMAGE_SIGNATURE_VERIFY_TIMEOUT` | `cosign verify` exec exceeded the per-call timeout budget (default 30s; tunable via `cosign_verify_timeout_seconds`). |
| `IMAGE_SIGNATURE_VERIFY_TRANSPARENCY_FAILED` | Sigstore Rekor / Fulcio transparency-log failure (split out from `IMAGE_SIGNATURE_INVALID` so SIEMs can page security on transparency-log breaches separately from "wrong signing key" failures). |
| `RELEASE_FINGERPRINT_MISMATCH` | The on-disk (`docs/release/keys/midnight-release-fingerprint.txt`) and remote (project website) fingerprint copies disagree, OR `dscverify` reports a fingerprint other than the published one. |
| `RELEASE_KMS_UNAVAILABLE` | The release pipeline could not reach the signing KMS / HSM; halt and follow `docs/runbooks/key-rotation.md`. |
| `RELEASE_REVOKED_KEY` | The verifier finds the signing fingerprint in a `REVOKED-<fingerprint>.txt` file in `docs/release/keys/`; even a valid signature MUST NOT be trusted. |

## Cosign binary pin

The cosign binary version + per-architecture SHA-256 digests live in
[`versions.yaml`](../versions.yaml) and are loaded by the
`release-verify` harness at run time. The pin is binding for `alpha`
and `beta` per
[`agile/handoff/governance/COSIGN_SDK_BOUNDARY.md`](../agile/handoff/governance/COSIGN_SDK_BOUNDARY.md);
the Go SDK import is deferred to `v1.0.0`. Use `--versions-file`
to point `release-verify` at a non-default `versions.yaml` (useful
when verifying a release tarball).

The release workflow (`.github/workflows/release.yml`) reads
`COSIGN_VERSION` from `versions.yaml` so the supply-chain pin has a
single source of truth.
