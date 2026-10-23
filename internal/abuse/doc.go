// Package abuse contains deterministic abuse-control policy evaluation. It does
// not block traffic itself; API, scheduler, or runtime callers translate the
// returned decision into headers, queue denial, or operator review.
package abuse
