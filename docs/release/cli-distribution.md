# CLI Distribution

GoReleaser is the only producer of Midnight CLI binaries. The release workflow
verifies its five native archives, derives every package-channel artifact from
those archives and their SHA-256 manifest, and signs both the archive and
distribution checksum manifests with Sigstore on protected tag runs.

`tools/distribution-manifests` creates:

- one Homebrew formula for macOS and Linux on amd64 and arm64;
- one Scoop manifest for Windows amd64;
- five OS/CPU-scoped npm packages containing only the native binary; and
- one npm dispatcher package with exact optional dependencies and no install,
  postinstall, or network lifecycle script.

The generated kit is evidence, not publication. Public package channels remain
disabled until an operator proves ownership and configures the following
repository variables:

When publication is enabled, its separate GitHub-hosted job verifies both
Sigstore bundles and every checksum entry, then sends the exact checksum-bound
npm tarballs, formula, and Scoop manifest to their channels. It does not
rebuild or repack those assets after verification.

| Variable | Contract |
|---|---|
| `MIDNIGHT_NPM_PACKAGE` | Approved public npm package name. Its five platform package names are formed by appending `-darwin-x64`, `-darwin-arm64`, `-linux-x64`, `-linux-arm64`, and `-win32-x64`. |
| `MIDNIGHT_HOMEBREW_TAP` | Existing GitHub repository in `owner/homebrew-name` form. The workflow owns only `Formula/midnight.rb`. |
| `MIDNIGHT_SCOOP_BUCKET` | Existing GitHub repository in `owner/repository` form. The workflow owns only the root `midnight.json`. |
| `MIDNIGHT_DISTRIBUTION_PUBLISH` | Must remain unset until every prerequisite below passes; set to `true` to enable the post-release publication job. |

The Homebrew and Scoop repositories use separate least-privilege contents-write
secrets, `MIDNIGHT_HOMEBREW_TAP_TOKEN` and `MIDNIGHT_SCOOP_BUCKET_TOKEN`.
Neither token belongs in the source repository or release artifact.

## npm bootstrap and trusted publishing

npm trusted publishing requires each package to exist before its GitHub Actions
publisher can be configured. Bootstrap all six exact names once using a
short-lived granular token with 2FA bypass, stored temporarily as
`MIDNIGHT_NPM_BOOTSTRAP_TOKEN`. Configure each package's trusted publisher for
`packetloss404/midnight`, workflow `release.yml`, action `npm publish`, then
delete the bootstrap secret and revoke the token. Subsequent releases use OIDC
from the GitHub-hosted `publish-distribution` job.

The source repository is currently private. npm trusted publishing supports the
OIDC release path, but npm provenance is not generated for private source
repositories. The packages therefore do not force the provenance setting or
claim a provenance attestation. If the repository becomes public later, npm's
trusted-publishing flow generates provenance automatically; no package rewrite
or `--provenance` flag is required.

Do not enable publication from a self-hosted runner: npm trusted publishing and
provenance require a supported cloud-hosted runner. Platform packages publish
before the dispatcher. Reruns preserve an already-published immutable
name/version and continue with missing packages.

## Operator acceptance

Before setting `MIDNIGHT_DISTRIBUTION_PUBLISH=true`:

1. Complete release-key custody, protected tag controls, and the real signing
   identity in `MID-010`.
2. Run `release.yml` by `workflow_dispatch`. It must build the publication kit,
   pack six npm tarballs, install the runner's native package with
   `--ignore-scripts`, and
   prove `midnight version` without publishing.
3. Review the generated formula and Scoop manifest against the approved tap and
   bucket. Confirm their URLs and hashes point to the same GitHub Release.
4. Bootstrap and bind all six npm trusted publishers, then revoke the bootstrap
   token.
5. Grant each repository token access to only its one tap or bucket. Protect
   their default branches and retain review/audit evidence.
6. Push the protected release tag. Verify the GitHub assets and both Sigstore
   bundles before accepting the npm, Homebrew, or Scoop update.
7. On clean hosts, install through each channel and compare `midnight version`
   with the tag. Retain the package-manager logs and resolved artifact hashes.

No public channel should be documented as available until this clean-host proof
is complete. The immutable GitHub archive remains the recovery distribution
surface if a package channel later drifts or is compromised.
