# Build Provenance

Midnight has both a live build path and side-effect-free evidence helpers. They
serve different purposes and must not be described as one capability.

The production build worker in `cmd/midnight-buildkit` checks out source, runs
Dockerfile or Railpack builds through BuildKit, pushes the immutable image to
the configured registry, and persists the digest, build logs, SBOM, and
SLSA-style provenance artifact before handing the digest-pinned image to the
deploy queue. `internal/build.BuildctlRunner` is the live BuildKit boundary.

`internal/build.ProvenanceStatement` is the pure model for the in-toto
statement and SLSA v1.0 predicate shape emitted by that path. The artifact is
provenance metadata; Midnight does not claim a SLSA Build Level or that the
build worker automatically signs and publishes it as an OCI attestation.

Build secret provenance records fingerprints only. `internal/build.SecretFingerprint` produces a stable SHA-256 fingerprint scoped by secret name and never stores or returns the plaintext secret.

Release evidence is split into three local JSON artifacts:

1. The release evidence packet rolls up context review, release artifact review, BuildKit cache integrity review, and registry GC review into `clean`, `needs-review`, or `blocked`.
2. The evidence index lists the packet, GC manifest, provenance, and SBOM evidence files with repository-relative paths, review status, optional artifact refs, and optional lowercase SHA-256 checksums.
3. The release evidence bundle stores the packet and index together and validates that their rollup statuses agree.

`internal/build.VerifyEvidenceIndexFiles` is the checksum verification boundary.
It validates the index, confirms each indexed path resolves to a regular file
under the caller-provided base directory, and recomputes checksums for entries
that declare `sha256`. Metadata-only validation intentionally does not read the
filesystem.

The statement, packet, index, bundle, and metadata validators do not contact
BuildKit, a registry, or an attestation store. The build worker supplies their
real artifact inputs through the separately configured live BuildKit and
registry path. The experimental build-isolation planner is also metadata-only;
enabling it does not launch the build worker or prove gVisor, Kata, or microVM
isolation.
