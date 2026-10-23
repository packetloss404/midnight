# Deployment Snapshot

Deployment snapshots capture image digests and build metadata needed to redeploy without rebuilding.

Rollback by digest is a no-build contract: if every image entry has a digest, `DeploymentSnapshot.RequiresBuild` returns false. Missing digests require a build before deployment can proceed.

`DeploymentSnapshot.ImagePins` exposes the digest-only deployment model used by rollback and registry retention. Pins include image name, tag, digest, and rollback reference; entries without digests are excluded because they cannot be redeployed without a build.

Snapshot drafts validate provenance and SBOM artifact references as immutable SHA-256 digests before a build plan is accepted.

Promotion evidence is external to the snapshot but uses the same digest-first model. A release evidence packet explains the review result, an evidence index points at the packet plus provenance/SBOM/registry-GC artifacts, and a release evidence bundle ties the packet and index together so the control plane can check a single status before promotion.

When an evidence index includes `sha256` values, callers should verify the indexed files before applying a snapshot. This confirms the local evidence files still match the reviewed packet/index/bundle and prevents a digest-only deployment from being promoted with stale or replaced evidence.
