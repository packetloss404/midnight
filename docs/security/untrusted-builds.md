# Untrusted Builds

Midnight has a live source-build path through the BuildKit worker. The stronger
tenant-isolation profiles described here are not live execution backends.
Running an ordinary source build is therefore not evidence that gVisor, Kata,
microVM, tenant-cache, or no-egress isolation has been enforced.

The live worker treats build inputs as untrusted, fails closed on BuildKit
version mismatch, and applies its configured build and secret-handling
boundaries. Stronger sandbox, tenant-cache, and egress enforcement remains a
separate integration gate.

`internal/isolation.PlanBuild` is the side-effect-free planning boundary for stronger build isolation. Untrusted plans default to gVisor with private tenant cache and no network egress. Isolated plans default to a microVM backend in plan-only mode; Kata and microVM launchers remain future integration work.

Secrets supplied to untrusted build inputs must be represented by fingerprints in snapshots or provenance records. Plaintext secret material is not persisted by the build helpers.

Release artifact review blocks fully untrusted builds unless the caller classifies the build as trusted or isolated. Isolated untrusted builds still require human review through the release evidence packet before promotion.

Evidence for untrusted builds must be carried through the packet/index/bundle flow. The evidence index may include SHA-256 checksums for local provenance, SBOM, packet, or GC manifest files; callers should run checksum verification before accepting the bundle. The current contract only validates these artifacts and never launches a sandboxed build, pulls arbitrary build contexts, or deletes registry data.

## Local API Surface

The OpenAPI contract documents
`POST /api/v1alpha/isolation/build/plan`. The router registers this
validation-only route only when `MIDNIGHT_EXPERIMENTAL=1`; it is absent from
the normal product API. The handler wraps `internal/isolation.PlanBuild` with
build-isolation evidence metadata. Untrusted and isolated build plans require
provenance and SBOM evidence references before the plan can report `ready`;
missing evidence returns `needs-review` rather than probing the filesystem.

Evidence references are metadata-only: kind, safe relative path, SHA-256 checksum, and review status. Secret inputs must be represented as `sha256:` fingerprints. The endpoint does not launch BuildKit, containers, gVisor, Kata, or microVMs, pull images, fetch build contexts, read evidence files, call networks, or mutate runtime state.
