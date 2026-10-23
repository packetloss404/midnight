# Registry GC

The registry GC CLI contract is represented by `internal/registry.RetentionPlan`.

The current CLI supports dry-run planning and apply-report structures only. Registry API calls, deletion execution, and manifest reachability analysis are deferred.

`internal/registry.PlanGCDryRun` marks candidate digests for deletion only when they are not protected by an active deployment pin, an unexpired rollback-window pin, an explicit keep digest, or the repository's configured last-N pushed tags. The returned plan is always `DryRun: true`.

`internal/registry.NewGCDryRunReport` and `internal/registry.NewGCApplyReport` summarize candidate actions with repository counts, deletion counts, protected/deleted byte totals, and reason counts. `internal/registry.ApplyGCPlan` accepts `GCApplyRequest`; destructive delete actions remain a dry run unless `ConfirmApply` is explicitly set.

`internal/registry.BuildGCManifest` renders the reviewable GC evidence artifact. Apply manifests with deletion actions are invalid unless `confirmApply` is true, which is the current confirm-apply gate. A confirmed apply report or manifest still does not delete registry data; it records the actions that a future registry integration would execute.

GC manifests can be referenced from the build evidence index and included in a release evidence bundle. If the index entry includes a SHA-256 checksum, promotion code should verify the local GC manifest before accepting the bundle.

`internal/registry.ProtectedDigestProbeResult` is the result shape for future registry probes that explain whether a digest is protected and why.

The real registry boundary is intentionally not integrated in the current GC path. Callers provide tag, pin, and rule inputs; no registry API is queried, no manifests are traversed, and no blobs or tags are deleted.
