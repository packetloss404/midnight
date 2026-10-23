// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

// PrincipalKind discriminates the calling identity attached to an
// agent turn or MCP session. The set is intentionally narrow — v1
// has no users / teams. Once teams land, "user" and "team_member"
// extend the set without changing the interfaces.
type PrincipalKind string

const (
	// PrincipalKindOperator is the holder of the operator bearer
	// token (single-tenant default).
	PrincipalKindOperator PrincipalKind = "operator"
	// PrincipalKindAgent is the agent itself acting on behalf of a
	// caller (the chat-backend forwards the upstream principal).
	PrincipalKindAgent PrincipalKind = "agent"
	// PrincipalKindMCPClient is an external MCP client session.
	PrincipalKindMCPClient PrincipalKind = "mcp_client"
	// PrincipalKindSystem is an internal-only caller (notably the
	// build-failure diagnosis path). System principals bypass
	// project scope but still record audit rows.
	PrincipalKindSystem PrincipalKind = "system"
)

// Principal identifies the calling identity. ID is the canonical
// short id (e.g. token id, mcp session id, conversation id); the
// optional Subject string carries the value of an `X-Midnight-
// Principal` header when set so per-user audit attribution still
// works in a single-tenant deploy.
type Principal struct {
	Kind    PrincipalKind
	ID      string
	Subject string
}

// SystemPrincipal returns the canonical privileged-internal caller
// used by the diagnosis track and by background reconcilers.
func SystemPrincipal(id string) Principal {
	if strings.TrimSpace(id) == "" {
		id = "system"
	}
	return Principal{Kind: PrincipalKindSystem, ID: id}
}

// ProjectScope enumerates the project ids a principal can see. The
// shape carries the "all" flag separately so v1 callers (operator
// bearer → all projects) need not materialize the full project
// list on every request.
type ProjectScope struct {
	// All is true when the principal has read access to every
	// project in the instance. v1 default for the operator bearer.
	All bool
	// IDs is the explicit set of project ids the principal can see
	// when All is false. Empty + !All means "no access".
	IDs []string
}

// Allows reports whether project id is inside the scope.
func (s ProjectScope) Allows(id string) bool {
	if s.All {
		return true
	}
	id = strings.TrimSpace(id)
	if id == "" {
		// An empty id means "no project context" (an instance-wide
		// read). Only the full scope can satisfy that. v1 default.
		return false
	}
	for _, candidate := range s.IDs {
		if candidate == id {
			return true
		}
	}
	return false
}

// Equal compares two scopes for structural equality. The ID order
// is canonicalized first.
func (s ProjectScope) Equal(o ProjectScope) bool {
	if s.All != o.All {
		return false
	}
	left := append([]string(nil), s.IDs...)
	right := append([]string(nil), o.IDs...)
	sort.Strings(left)
	sort.Strings(right)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// ResourceKindProject is the canonical kind string the chat
// backend uses when asking the authorizer about a project. The
// authorizer accepts any kind string but recognizes this one as
// the project-level scope check.
const (
	ResourceKindProject     = "project"
	ResourceKindService     = "service"
	ResourceKindEnvironment = "environment"
	ResourceKindVariable    = "variable"
	ResourceKindDeployment  = "deployment"
)

// ProjectScopeAuthorizer is the central authorization seam. The
// chat backend and MCP server both consume it before forwarding a
// tool call to the underlying API. v1 ships OpenAuthorizer (bearer
// → all projects) and DenyAllAuthorizer (test scaffold); a
// PostgresACLAuthorizer skeleton is gated by a feature flag so
// per-project ACLs can be introduced without a wire change.
type ProjectScopeAuthorizer interface {
	// AllowedProjects returns the set of project ids the principal
	// may read. v1 default returns all projects for any
	// operator-bearer principal.
	AllowedProjects(ctx context.Context, principal Principal) (ProjectScope, error)
	// CanRead reports whether principal may read the named
	// resource. kind is one of the ResourceKind* constants; id is
	// the canonical resource id (project_id for kind="project",
	// service_id for kind="service", etc).
	CanRead(ctx context.Context, principal Principal, kind, id string) (bool, error)
	// CanMutate reports whether principal may issue a mutating
	// operation against the named resource. v1 default mirrors
	// CanRead for operator-bearer callers; the mutation gate is
	// the per-message brake — see MutationGate.
	CanMutate(ctx context.Context, principal Principal, kind, id string) (bool, error)
}

// ErrAuthorizerUnavailable is returned by stubs (e.g. the Postgres
// authorizer when its feature flag is off) so callers can surface
// a clean 501 rather than a generic 500.
var ErrAuthorizerUnavailable = errors.New("agent authorizer: not available")

// OpenAuthorizer is the v1 default — any caller with a valid
// operator-bearer principal can read and mutate every project.
// System principals also satisfy every check (they bypass scope
// per the track contract but still record audit rows downstream).
//
// OpenAuthorizer is safe for concurrent use.
type OpenAuthorizer struct{}

// NewOpenAuthorizer constructs the v1 default authorizer.
func NewOpenAuthorizer() *OpenAuthorizer { return &OpenAuthorizer{} }

// AllowedProjects always reports the full scope.
func (OpenAuthorizer) AllowedProjects(ctx context.Context, principal Principal) (ProjectScope, error) {
	if !principalAllowed(principal) {
		return ProjectScope{}, nil
	}
	return ProjectScope{All: true}, nil
}

// CanRead always returns true for non-empty principals.
func (OpenAuthorizer) CanRead(ctx context.Context, principal Principal, kind, id string) (bool, error) {
	return principalAllowed(principal), nil
}

// CanMutate always returns true for non-empty principals. The
// per-message brake lives in MutationGate.
func (OpenAuthorizer) CanMutate(ctx context.Context, principal Principal, kind, id string) (bool, error) {
	return principalAllowed(principal), nil
}

func principalAllowed(p Principal) bool {
	if strings.TrimSpace(string(p.Kind)) == "" {
		return false
	}
	return true
}

// DenyAllAuthorizer is a test scaffold that rejects every call.
// Used in handler tests that want to assert the deny path without
// constructing a Postgres-backed ACL.
type DenyAllAuthorizer struct{}

// NewDenyAllAuthorizer constructs the deny scaffold.
func NewDenyAllAuthorizer() *DenyAllAuthorizer { return &DenyAllAuthorizer{} }

// AllowedProjects always returns an empty (no-access) scope.
func (DenyAllAuthorizer) AllowedProjects(ctx context.Context, principal Principal) (ProjectScope, error) {
	return ProjectScope{}, nil
}

// CanRead always returns false.
func (DenyAllAuthorizer) CanRead(ctx context.Context, principal Principal, kind, id string) (bool, error) {
	return false, nil
}

// CanMutate always returns false.
func (DenyAllAuthorizer) CanMutate(ctx context.Context, principal Principal, kind, id string) (bool, error) {
	return false, nil
}

// PostgresACLAuthorizer is the per-project ACL skeleton. v1 boots
// it with the feature flag disabled and falls back to
// OpenAuthorizer; the table schema and migration are deferred
// until teams / users land. Until the flag flips, calls return
// ErrAuthorizerUnavailable so the caller can fall back cleanly.
type PostgresACLAuthorizer struct {
	// Enabled, when false, makes every method return
	// ErrAuthorizerUnavailable. Default zero value is "disabled"
	// so a stack that wires this authorizer in without setting the
	// flag fails closed in tests but the production stack can
	// safely default to OpenAuthorizer when Enabled is false.
	Enabled bool

	// LookupACL is the seam tests use to inject a fake ACL
	// snapshot. Production startup leaves this nil — the schema
	// hasn't landed yet — and the seam exists purely so the
	// interface contract is testable today.
	LookupACL func(ctx context.Context, principal Principal) (ProjectScope, error)

	mu        sync.RWMutex
	scopes    map[string]ProjectScope
	mutations map[string]struct{}
}

// NewPostgresACLAuthorizer constructs the disabled skeleton.
// Callers flip Enabled=true once the ACL schema lands.
func NewPostgresACLAuthorizer() *PostgresACLAuthorizer {
	return &PostgresACLAuthorizer{
		scopes:    map[string]ProjectScope{},
		mutations: map[string]struct{}{},
	}
}

// SetScope wires a per-principal scope for tests / preview
// deployments. Production callers use the LookupACL hook instead.
func (a *PostgresACLAuthorizer) SetScope(principal Principal, scope ProjectScope) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.scopes[principalKey(principal)] = scope
}

// AllowMutation marks a (principal, resource) pair as
// mutation-eligible. Defaults to false.
func (a *PostgresACLAuthorizer) AllowMutation(principal Principal, kind, id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mutations[mutationKey(principal, kind, id)] = struct{}{}
}

// AllowedProjects returns the per-principal scope. When LookupACL
// is set, it takes precedence; otherwise the in-memory map serves.
func (a *PostgresACLAuthorizer) AllowedProjects(ctx context.Context, principal Principal) (ProjectScope, error) {
	if !a.Enabled {
		return ProjectScope{}, ErrAuthorizerUnavailable
	}
	if a.LookupACL != nil {
		return a.LookupACL(ctx, principal)
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if scope, ok := a.scopes[principalKey(principal)]; ok {
		return scope, nil
	}
	return ProjectScope{}, nil
}

// CanRead returns true when the principal's scope allows the
// project id supplied (or, for non-project resources, when the
// scope is "all").
func (a *PostgresACLAuthorizer) CanRead(ctx context.Context, principal Principal, kind, id string) (bool, error) {
	scope, err := a.AllowedProjects(ctx, principal)
	if err != nil {
		return false, err
	}
	if scope.All {
		return true, nil
	}
	if kind == ResourceKindProject {
		return scope.Allows(id), nil
	}
	// For non-project resources, v1 cannot resolve the project
	// owner without a schema join the operator hasn't approved
	// yet; fail closed.
	return false, nil
}

// CanMutate returns true when the principal scope allows the
// project AND the mutation map has been populated for this
// (principal, kind, id).
func (a *PostgresACLAuthorizer) CanMutate(ctx context.Context, principal Principal, kind, id string) (bool, error) {
	ok, err := a.CanRead(ctx, principal, kind, id)
	if err != nil || !ok {
		return false, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok = a.mutations[mutationKey(principal, kind, id)]
	return ok, nil
}

func principalKey(p Principal) string {
	return string(p.Kind) + "\x00" + p.ID + "\x00" + p.Subject
}

func mutationKey(p Principal, kind, id string) string {
	return principalKey(p) + "\x00" + kind + "\x00" + id
}
