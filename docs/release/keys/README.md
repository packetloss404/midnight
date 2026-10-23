# `docs/release/keys/` — release-signing fingerprint index

This directory is the in-repo publication point for public
fingerprints of keys that sign Midnight `.deb` artifacts.

Trust status: no trusted release-signing fingerprint is currently
published here. `midnight-release-fingerprint.txt` contains only
comments and an explicitly untrusted all-zero example. Operators must
not trust or deploy signed `.deb` artifacts until a real fingerprint is
published here and on the project website.

## Contents

| File | Purpose | Lifecycle |
|---|---|---|
| `midnight-release-fingerprint.txt` | Active release-key fingerprint(s); one signing identity per non-comment line. Currently has no active trusted line. | Created during release-key provisioning; rotated annually or on incident. |
| `REVOKED-<fingerprint>.txt`        | Revocation certificate for a previously trusted fingerprint. | Written immediately on suspected compromise; never deleted (history is the trust signal). |
| `.gitignore`                       | Defense-in-depth: refuse to commit private key material that may accidentally land here. | n/a |
| `README.md` (this file)            | Operator orientation. | Updated when the policy changes. |

## How operators use this directory

1. **Verify the fingerprint** before trusting any signed `.deb`.
   If there is no non-comment fingerprint line, STOP: no operator-trusted
   release key has been published.

   Operators MUST verify the in-repo and remote (project website)
   copies match:

   ```bash
   diff <(curl -fsSL https://deployatmidnight.com/release/keys/midnight-release-fingerprint.txt) \
        docs/release/keys/midnight-release-fingerprint.txt && echo "fingerprints match"
   ```

   If the two copies disagree, the verifier emits
   `RELEASE_FINGERPRINT_MISMATCH`; STOP and engage the security
   review per `SECURITY.md`.

2. **Receive the public key** into the local GPG keyring using the
   fingerprint from the active line:

   ```bash
   fp=$(grep -vE '^(#|[[:space:]]*$)' docs/release/keys/midnight-release-fingerprint.txt | head -1 | awk '{print $2}')
   test -n "$fp"
   gpg --keyserver hkps://keys.openpgp.org --recv-keys "$fp"
   ```

3. **Verify the `.deb`** with `dscverify` and `gpg --verify`:

   ```bash
   dscverify midnight_<version>_amd64.changes
   ```

4. **Check for revocation.** Any `REVOKED-<fingerprint>.txt` file in
   this directory means that fingerprint MUST NOT be trusted, even if
   it appears in `midnight-release-fingerprint.txt` (a stale entry
   during an emergency rotation). The verifier emits
   `RELEASE_REVOKED_KEY` for any artifact signed by a revoked
   fingerprint.

## Where the remote copy lives

The fingerprint file is also published at:

```
https://deployatmidnight.com/release/keys/midnight-release-fingerprint.txt
```

This is the reserved publication path, but the fingerprint resource is not
published yet. It is not operator-trusted until the project website serves the
matching reviewed fingerprint and the in-repo file contains the same active
non-comment value.

The dual-publication contract is the **defense against a single
point of compromise**: if an attacker pushes to the repo, operators
catch the discrepancy with a `diff` against the website; if an
attacker takes over the website, operators catch the discrepancy
with a `diff` against the cloned repo. Both copies update on
fingerprint rotation; both must agree at all times outside of an
in-flight rotation window.

## Lifecycle

```
provision (YubiKey)
    │
    ▼
publish fingerprint  ──►  in-repo (this file) + project website mirror
    │
    ▼
operate              ──►  operators verify repo and website copies match
    │
    ├── annual rotation (365d) ──►  publish overlap window (30d) ──► retire old line
    │
    └── compromise            ──►  publish REVOKED-<fingerprint>.txt
                                   tombstone tag v0.1.0-alpha-revoked
                                   notify partner mailing list + GitHub Discussions
                                   change Fulcio identity (workflow path)
```

The full lifecycle is documented in
[`RELEASE_KEY_CUSTODY.md`](../../../agile/handoff/governance/RELEASE_KEY_CUSTODY.md)
Steps 5–9.

## What MUST NOT be committed here

- **Private GPG material** of any kind (`*.gpg`, `*.asc` for secret
  blocks, `secring.gpg`, etc.). The `.gitignore` in this directory
  refuses these patterns; defense-in-depth.
- **Unapproved fingerprints** that have not completed release-key
  witness review.
- **Revocation certs without a tombstone tag** — every `REVOKED-*.txt`
  has a corresponding `v*-revoked` tag in the repo; the two land in
  the same commit.

## Cross-references

- [`docs/release-verify.md`](../../release-verify.md) — operator-facing
  end-to-end verification flow.
- [`internal/failurecodes/codes.go`](../../../internal/failurecodes/codes.go)
  — `RELEASE_FINGERPRINT_MISMATCH`, `RELEASE_REVOKED_KEY`.
- [`docs/error-codes.md`](../../error-codes.md) — operator-facing
  recovery hints.
