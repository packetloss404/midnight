# Key Rotation Runbook

Status: operator procedure draft. These steps describe the intended release-key rotation flow, but they are not operator-ready until a real release-signing fingerprint, key publication location, and KMS/HSM reference are published.

## Preconditions

- A trusted current release-signing fingerprint is published in `docs/release/keys/midnight-release-fingerprint.txt` and on the project website.
- New public key material is published through the same dual-publication path.
- Old and new keys have a planned overlap window of at least 90 days.
- The release workflow has a documented signing backend or KMS/HSM reference.

## Steps

1. Freeze release signing.
2. Publish new public keys to the documented project key location and update
   the bundle referenced from [`docs/security/keys.md`](../security/keys.md).
3. Enable dual verification for 90 days. During this window the runtime
   agent and `apt`/`cosign` clients accept either the old or new key.
4. Rotate signing jobs to new private keys. Update the KMS reference in the
   release workflow secrets.
5. Verify `.deb`, image, and provenance signatures resolve cleanly against
   the new key bundle.
6. Retire old keys after the overlap window. Move the old public keys to
   the archived bundle and remove them from the verifying clients only when
   the alpha install base has rotated past the overlap.

## Verification

Release artifacts must verify against the published key bundle and the active
fingerprint in `docs/release/keys/midnight-release-fingerprint.txt`. Run
`make release-check` from a clean worktree after the rotation so the local
gate confirms that the new evidence-emitting commands still succeed.

## KMS Unavailable Fallback

If the release pipeline halts with [`RELEASE_KMS_UNAVAILABLE`](../error-codes.md):

1. Stop the release workflow run; do **not** retry until the KMS is
   reachable. A retry that lands a partial artifact set must be quarantined.
2. Page the on-call security engineer using the contact in `MAINTAINERS.md`.
3. Confirm whether the KMS outage affects the active or the overlap key
   (or both). Single-key outages can fall back to the overlap key for the
   remaining 90 days; double-key outages require freezing all releases.
4. Once the KMS is reachable, rerun `make release-check` from the same
   commit and only then re-trigger the release workflow.
