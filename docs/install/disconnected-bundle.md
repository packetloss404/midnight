# Disconnected Install Bundle

The disconnected install bundle contract is a planning surface for private or air-gapped installs. It validates bundle completeness without mirroring, signing, or installing anything.

## Contract

`internal/release.DisconnectedBundlePlan` emits `midnight.dev/disconnected-install-bundle-plan/v1alpha1` JSON with these required sections:

- APT mirror repository URL, suites, and signing key fingerprint.
- OCI mirror registry and digest-pinned image references; every image reference must include a `sha256` digest.
- Offline docs paths.
- Optional offline vulnerability database metadata; absence is `needs-review`, not `blocked`.
- Manifest SHA-256, cosign bundle path, and GPG detached signature path.

## Non-goals

This contract does not implement multi-region routing, HA control plane behavior, billing, abuse controls, SSO/SAML/SCIM, or build isolation. It only gives release automation and enterprise operators a stable machine-readable readiness check for disconnected install evidence.

## Local API Surface

The OpenAPI contract documents
`POST /api/v1alpha/release/disconnected-bundle/plan`. The router registers the
validation-only route only when `MIDNIGHT_EXPERIMENTAL=1`; it is absent from
the normal product API. When enabled, the handler accepts the same JSON shape
and returns the normalized readiness plan. It rejects unknown fields and
multiple JSON documents, but it does not mirror APT repositories, pull OCI
images, inspect vulnerability databases, create signatures, or read/write
bundle files.

The product dashboard does not expose planner sample endpoints. Experimental
local review can enable and call the planning route; tests and tooling can call
the planner helper directly.

Build isolation evidence is planned separately through the experimental
`POST /api/v1alpha/isolation/build/plan` route and `internal/isolation`. The
disconnected bundle planner can reference offline docs and signatures, while
build evidence keeps provenance/SBOM paths and checksums in a metadata-only
contract that does not read bundle contents.
