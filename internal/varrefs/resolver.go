package varrefs

import (
	"context"
	"strings"
)

// VariableLookup is the boundary between the resolver and the variable
// vault. Implementations return the raw template string for a variable
// in the supplied project/environment/service scope, plus a boolean ok
// flag (false signals "no such variable").
//
// LookupShared returns the template string of a project-scoped shared
// variable. LookupBuiltin resolves a `${{this.*}}` reference for the
// service that owns the template. LookupServiceByName resolves a
// service name to a service-id; the resolver consults this to translate
// the user-facing name in `${{ServiceName.VAR}}` into the canonical id
// used by LookupService.
type VariableLookup interface {
	// LookupService returns the template for a variable on the supplied
	// service. Implementations MUST scope by (projectID, envID,
	// serviceID, name).
	LookupService(ctx context.Context, projectID, envID, serviceID, name string) (string, bool, error)
	// LookupShared returns the template for a project-scoped shared
	// variable.
	LookupShared(ctx context.Context, projectID, envID, name string) (string, bool, error)
	// LookupServiceByName resolves a user-supplied service name (the
	// portion before the `.` in `${{ServiceName.VAR}}`) into a canonical
	// service id. Implementations SHOULD scope by (projectID, envID).
	LookupServiceByName(ctx context.Context, projectID, envID, name string) (string, bool, error)
	// LookupBuiltin returns the value of a `${{this.*}}` built-in for
	// the supplied service. The full set is fixed; see the constants in
	// this package.
	LookupBuiltin(ctx context.Context, projectID, envID, serviceID, name string) (string, bool, error)
	// ListServiceVariables returns the names of all variables defined on
	// the supplied service. The resolver uses this to know which vars to
	// resolve into the final env map.
	ListServiceVariables(ctx context.Context, projectID, envID, serviceID string) ([]string, error)
}

// VariableVersionRef pins one variable identity/version in an immutable
// deployment snapshot. It contains metadata only.
type VariableVersionRef struct {
	ID                 string
	ServiceID          string
	ServiceSlug        string
	ServiceDisplayName string
	Name               string
	Version            int
}

// VersionedVariableLookup can derive a lookup that resolves only the supplied
// immutable versions. Missing versions must fail closed as unknown references.
type VersionedVariableLookup interface {
	WithVariableVersions(refs []VariableVersionRef) VariableLookup
}

// Built-in reference names auto-injected on every service via `${{this.*}}`.
const (
	BuiltinPublicDomain  = "MIDNIGHT_PUBLIC_DOMAIN"
	BuiltinPrivateDomain = "MIDNIGHT_PRIVATE_DOMAIN"
	BuiltinServiceName   = "MIDNIGHT_SERVICE_NAME"
)

// Builtins returns the set of supported `${{this.*}}` built-in names.
// Callers use this to know which built-ins are available without poking
// at the resolver internals.
func Builtins() []string {
	return []string{BuiltinPublicDomain, BuiltinPrivateDomain, BuiltinServiceName}
}

// IsBuiltin reports whether name is one of the supported `${{this.*}}`
// built-ins.
func IsBuiltin(name string) bool {
	switch name {
	case BuiltinPublicDomain, BuiltinPrivateDomain, BuiltinServiceName:
		return true
	}
	return false
}

// Resolver renders templates into final string values.
type Resolver struct {
	Lookup VariableLookup
}

// NewResolver constructs a Resolver wrapping the supplied lookup.
func NewResolver(lookup VariableLookup) *Resolver { return &Resolver{Lookup: lookup} }

// Render walks the supplied template, recursively resolving every
// reference, and returns the final concatenated string. projectID,
// envID, serviceID identify the scope the template was found in (used
// for RefLocal and RefBuiltin) — for RefServiceVar the resolver looks
// the target service up by name via Lookup.LookupServiceByName.
func (r *Resolver) Render(ctx context.Context, projectID, envID, serviceID, name, template string) (string, error) {
	visit := visitStack{}
	return r.render(ctx, projectID, envID, serviceID, name, template, &visit)
}

// Resolve produces the final env map for the supplied service. Every
// variable defined on the service is rendered; the map keys are the
// variable names and the values are the resolved strings.
func (r *Resolver) Resolve(ctx context.Context, projectID, envID, serviceID string) (map[string]string, error) {
	names, err := r.Lookup.ListServiceVariables(ctx, projectID, envID, serviceID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(names)+len(Builtins()))
	for _, name := range names {
		template, ok, err := r.Lookup.LookupService(ctx, projectID, envID, serviceID, name)
		if err != nil {
			return nil, err
		}
		if !ok {
			// Concurrent delete — surface as missing rather than crash.
			return nil, ErrUnknownReference{Kind: RefLocal, Name: name}
		}
		rendered, err := r.Render(ctx, projectID, envID, serviceID, name, template)
		if err != nil {
			return nil, err
		}
		out[name] = rendered
	}
	// Inject built-ins so a service that does not reference them can
	// still depend on them downstream. Skip on lookup error so legacy
	// callers without route plumbing don't break.
	for _, builtin := range Builtins() {
		if _, exists := out[builtin]; exists {
			continue
		}
		value, ok, err := r.Lookup.LookupBuiltin(ctx, projectID, envID, serviceID, builtin)
		if err != nil {
			return nil, err
		}
		if ok {
			out[builtin] = value
		}
	}
	return out, nil
}

// ResolveAtVersions resolves an immutable deployment snapshot. It never falls
// back to a newer active value when a pinned version is missing.
func (r *Resolver) ResolveAtVersions(ctx context.Context, projectID, envID, serviceID string, refs []VariableVersionRef) (map[string]string, error) {
	versioned, ok := r.Lookup.(VersionedVariableLookup)
	if !ok {
		return nil, ErrUnknownReference{Kind: RefLocal, Name: "versioned variable lookup unavailable"}
	}
	return NewResolver(versioned.WithVariableVersions(refs)).Resolve(ctx, projectID, envID, serviceID)
}

func (r *Resolver) render(ctx context.Context, projectID, envID, serviceID, name, template string, visit *visitStack) (string, error) {
	key := serviceID + "." + name
	if visit.contains(key) {
		path := append(visit.copy(), key)
		return "", ErrReferenceCycle{Path: path}
	}
	visit.push(key)
	defer visit.pop()

	parsed, err := Parse(template)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, seg := range parsed.Segments {
		if seg.Ref == nil {
			out.WriteString(seg.Literal)
			continue
		}
		resolved, err := r.resolveOne(ctx, projectID, envID, serviceID, seg.Ref, visit)
		if err != nil {
			return "", err
		}
		out.WriteString(resolved)
	}
	return out.String(), nil
}

func (r *Resolver) resolveOne(ctx context.Context, projectID, envID, serviceID string, ref *Reference, visit *visitStack) (string, error) {
	switch ref.Kind {
	case RefBuiltin:
		if !IsBuiltin(ref.Name) {
			return "", ErrUnknownReference{Kind: RefBuiltin, Name: ref.Name}
		}
		value, ok, err := r.Lookup.LookupBuiltin(ctx, projectID, envID, serviceID, ref.Name)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ErrUnknownReference{Kind: RefBuiltin, Name: ref.Name}
		}
		// Built-ins are scalar — they never recurse, so just return them.
		return value, nil
	case RefLocal:
		template, ok, err := r.Lookup.LookupService(ctx, projectID, envID, serviceID, ref.Name)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ErrUnknownReference{Kind: RefLocal, Name: ref.Name}
		}
		return r.render(ctx, projectID, envID, serviceID, ref.Name, template, visit)
	case RefShared:
		template, ok, err := r.Lookup.LookupShared(ctx, projectID, envID, ref.Name)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ErrUnknownReference{Kind: RefShared, Name: ref.Name}
		}
		// Shared variables are resolved in the shared "service id" sentinel
		// so cycles between shared and per-service refs are tracked.
		return r.render(ctx, projectID, envID, sharedScopeID, ref.Name, template, visit)
	case RefServiceVar:
		targetID, ok, err := r.Lookup.LookupServiceByName(ctx, projectID, envID, ref.Service)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ErrUnknownReference{Kind: RefServiceVar, Service: ref.Service, Name: ref.Name}
		}
		template, ok, err := r.Lookup.LookupService(ctx, projectID, envID, targetID, ref.Name)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ErrUnknownReference{Kind: RefServiceVar, Service: ref.Service, Name: ref.Name}
		}
		return r.render(ctx, projectID, envID, targetID, ref.Name, template, visit)
	default:
		return "", ErrInvalidSyntax{Pos: ref.Start, Reason: "unsupported reference kind"}
	}
}

// sharedScopeID is the sentinel used for the shared-scope visit stack
// entries. It is intentionally not a valid service id so it cannot
// collide with a real one.
const sharedScopeID = "@shared"

// visitStack tracks the current resolution path so we can detect cycles.
// We use a slice keyed by "scope.name" tokens; the resolver pushes on
// entry and pops on exit.
type visitStack struct {
	stack []string
}

func (v *visitStack) push(key string) { v.stack = append(v.stack, key) }
func (v *visitStack) pop()            { v.stack = v.stack[:len(v.stack)-1] }
func (v *visitStack) contains(key string) bool {
	for _, k := range v.stack {
		if k == key {
			return true
		}
	}
	return false
}
func (v *visitStack) copy() []string {
	out := make([]string, len(v.stack))
	copy(out, v.stack)
	return out
}
