package identity

import "context"

// ClaimStatusReader reports whether the migration default workspace already has
// a human administrator.
//
// It is deliberately a narrow, optional interface rather than a method on
// AccessStore: the answer is a single boolean that the unauthenticated console
// setup probe needs, and widening AccessStore would force every fake in the
// test suite to grow a method that has nothing to do with tenant membership.
type ClaimStatusReader interface {
	// DefaultWorkspaceClaimed returns true once a non-migration, enabled,
	// accepted admin holds the default workspace. It must never return any
	// other detail — the caller exposes the result on an unauthenticated route.
	DefaultWorkspaceClaimed(ctx context.Context) (bool, error)
}
