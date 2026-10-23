# Registry Cache

`internal/registry.PlanCache` is the foundation for local registry cache decisions. It is a pure planner: callers provide observed cache entries and policy, then execute returned keep/evict actions through their own backend.

Current behavior:

- Pinned entries are kept.
- Entries older than `MaxAge` are evicted before size pressure is evaluated.
- If kept entries exceed `MaxBytes`, unpinned entries are evicted least-recently-accessed first.
- Action ordering is deterministic by repository and digest.

No registry APIs, blob stores, or filesystem paths are touched by this planner.
