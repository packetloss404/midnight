// Package scheduler owns placement and host capability matching. It
// applies per-(project, class) fairness quotas, classifies failures into
// transient/permanent backoff classes, plans stateless multihost service
// placement and agent canary rollout batches, and emits deterministic DTOs
// for evidence and execution.
package scheduler
