# Release Keys

Production release keys are not committed to the repository.

Protected releases require two key paths:

- Cosign key material for images and attestations.
- GPG key material for `.deb`, `Release.gpg`, and `InRelease` signing.

The custody model is tracked by the release verification and key-rotation docs.

## Rotation Policy

| Key class | Cadence | Overlap window | Verification |
|---|---|---|---|
| Cosign signing | 12 months | 90 days dual-trust | `cosign verify` resolves to either active key. |
| Cosign root | 36 months | 180 days dual-trust | Public keys published to `https://keys.midnight.dev/`. |
| GPG `.deb` | 12 months | 90 days dual-trust | `apt-key`/`InRelease` accepts either fingerprint. |
| GPG root | 36 months | 180 days dual-trust | Published in the same key bundle. |

The 90-day overlap window is the release-key rotation requirement. The runbook at
[`docs/runbooks/key-rotation.md`](../runbooks/key-rotation.md) walks the
operator through the overlap.

## Failure Modes

When the release pipeline cannot reach the signing KMS or HSM, it must halt
without partially-signed artifacts and surface the
[`RELEASE_KMS_UNAVAILABLE`](../error-codes.md) reason code. The release
operator follows the key-rotation runbook's "fall back to cold standby"
section, then re-runs `make release-check` once the KMS is reachable.

`RELEASE_KMS_UNAVAILABLE` is the canonical halt code for any of:

- Cosign signing aborted because the configured KMS reference resolved to a
  network or auth error.
- GPG signing aborted because the smartcard reader or HSM did not respond.
- Provenance attestation refused to write because the keyless flow could
  not reach the OIDC issuer.

The local release check does not exercise the KMS path; the failure mode is
documented now so operators have one place to read the recovery hint when the
live release pipeline lands.
