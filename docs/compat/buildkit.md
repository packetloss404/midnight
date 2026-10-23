# BuildKit Compatibility

Midnight pins BuildKit to `v0.28.1` in `versions.yaml`, matching Railpack
v0.23.0's upstream BuildKit dependency.

`internal/build.ValidateBuildKitVersion` is the current contract boundary. It returns a `BuildKitVersionMismatchError` when the discovered BuildKit version differs from the pin. Callers must fail closed on that mismatch before accepting release evidence.

`internal/build.BuildctlRunner` is the opt-in live runner for direct BuildKit solves. It can push to a registry and write digest, SBOM, provenance, and log artifacts when a caller supplies a prepared local context, Dockerfile, output directory, and destination image.

The runner is wired through `cmd/midnight-buildkit`. Source deploy intake
creates canonical deployment and outbox truth, the build worker checks out the
source and executes the selected Dockerfile or Railpack solve, persists build
artifacts, and enqueues the digest-pinned image for the deploy worker. The
deploy worker still rejects a hand-authored deploy job that contains only a
source reference; source must pass through the build queue so the candidate
image digest and artifact evidence exist before runtime activation.

Registry compatibility is similarly a contract placeholder in `versions.yaml` as `distribution-placeholder`. There is no live registry integration in the current GC path; registry GC code builds dry-run/apply manifests and reports from caller-supplied tag and pin data only.
