package secrets

import (
	"context"
	"errors"
	"fmt"
)

// ErrReferenceForbidden is returned when a principal does not have
// read access on a referenced variable's scope.
var ErrReferenceForbidden = errors.New("midnight: reference variable cross-scope authorization denied")

// ErrReferenceCycle is returned when a reference chain forms a cycle.
var ErrReferenceCycle = errors.New("midnight: reference variable cycle detected")

// ErrReferenceCrossTier is returned when a cross-tier reference is
// forbidden by the panel-locked tier matrix (Sprint 9 VAR-905).
var ErrReferenceCrossTier = errors.New("midnight: reference variable cross-tier transition denied")

// Principal identifies the actor (or the consuming deploy principal) for
// authorization checks.
type Principal struct {
	ID    string
	Type  string // "user" | "service_account" | "system"
	Scope string // workspace/project/environment/service for object-level checks
}

// Tier classifies the environment (panel: Sprint 9 VAR-905).
type Tier string

const (
	TierProd    Tier = "prod"
	TierStaging Tier = "staging"
	TierPreview Tier = "preview"
	TierDev     Tier = "dev"
)

// Authorizer is the boundary between Lane 4's Vault and the eventual
// RBAC-908 middleware. The implementation returns nil when the action is
// allowed and an error otherwise. Callers MUST construct an Authorizer
// that maps to their RBAC policy; AllowAll exists for unauthenticated
// tests only.
type Authorizer interface {
	// CanRead reports whether principal may read the supplied variable
	// metadata (object-level permission).
	CanRead(ctx context.Context, principal Principal, target VariableMeta) error
	// AllowsTierEdge reports whether a reference from the source tier to
	// the destination tier is permitted under any policy override.
	AllowsTierEdge(ctx context.Context, principal Principal, src, dst Tier) error
}

// AllowAll is the trivial Authorizer used by tests. It permits every
// read and every tier edge.
type AllowAll struct{}

// CanRead implements Authorizer.
func (AllowAll) CanRead(context.Context, Principal, VariableMeta) error { return nil }

// AllowsTierEdge implements Authorizer.
func (AllowAll) AllowsTierEdge(context.Context, Principal, Tier, Tier) error { return nil }

// DenyByDefault implements Authorizer with a deny-by-default semantics
// plus an explicit per-(actor, scope) allowlist. Useful for tests of the
// negative path.
type DenyByDefault struct {
	Reads map[string]bool // key: principal.ID + "::" + scope
	Edges map[string]bool // key: principal.ID + "::" + src + "->" + dst
}

// CanRead implements Authorizer.
func (a DenyByDefault) CanRead(_ context.Context, principal Principal, target VariableMeta) error {
	if a.Reads[principal.ID+"::"+scopeOf(target)] {
		return nil
	}
	return fmt.Errorf("%w: principal=%s target=%s", ErrReferenceForbidden, principal.ID, target.ID)
}

// AllowsTierEdge implements Authorizer.
func (a DenyByDefault) AllowsTierEdge(_ context.Context, principal Principal, src, dst Tier) error {
	// Panel-locked: preview -> prod is permanently denied, no override.
	if src == TierPreview && dst == TierProd {
		return fmt.Errorf("%w: %s->%s is permanently denied", ErrReferenceCrossTier, src, dst)
	}
	if a.Edges[principal.ID+"::"+string(src)+"->"+string(dst)] {
		return nil
	}
	return fmt.Errorf("%w: principal=%s %s->%s", ErrReferenceCrossTier, principal.ID, src, dst)
}

func scopeOf(m VariableMeta) string {
	if m.Service != "" {
		return "svc:" + m.Service
	}
	if m.Environment != "" {
		return "env:" + m.Environment
	}
	if m.Project != "" {
		return "prj:" + m.Project
	}
	return "wsp:" + m.Workspace
}

// CreateReference creates a Variable that references another Variable's
// plaintext. The creating actor MUST have CanRead access on the target
// at creation time (panel-locked VAR-905).
func (vlt *Vault) CreateReference(ctx context.Context, in VariableInput, target VariableID, creator Principal, srcTier, dstTier Tier) (*Variable, error) {
	vlt.mu.Lock()
	defer vlt.mu.Unlock()
	tgt, ok := vlt.byID[target]
	if !ok {
		return nil, ErrVariableNotFound
	}
	// Tier check first: a forbidden tier edge is structural and SHOULD
	// surface even when the target is otherwise readable. Read check
	// second; both are panel-locked at creation time (Sprint 9 VAR-905).
	if err := vlt.auth.AllowsTierEdge(ctx, creator, srcTier, dstTier); err != nil {
		return nil, err
	}
	if err := vlt.auth.CanRead(ctx, creator, tgt.meta); err != nil {
		return nil, err
	}
	id, err := nextVariableID(in.Scope, in.Workspace, in.Project, in.Environment, in.Service, in.Name)
	if err != nil {
		return nil, err
	}
	now := vlt.clock().UTC()
	fp, err := vlt.kp.Fingerprint(ctx)
	if err != nil {
		return nil, err
	}
	meta := VariableMeta{
		ID:             id,
		Scope:          tgt.meta.Scope, // secrecy propagates: a sealed/secret target stays sealed/secret
		Workspace:      in.Workspace,
		Project:        in.Project,
		Environment:    in.Environment,
		Service:        in.Service,
		Name:           in.Name,
		Version:        1,
		LastRotatedAt:  now,
		LengthBucket:   tgt.meta.LengthBucket,
		KEKFingerprint: fp,
	}
	v := &Variable{meta: meta, refTo: target}
	vlt.byID[id] = v
	key := variableKey(meta.Scope, meta.Workspace, meta.Project, meta.Environment, meta.Service, meta.Name)
	vlt.byName[key] = id
	return v, nil
}

// ResolveReference walks the reference chain originating at id, applying
// CanRead at every hop using the supplied consumer principal. Cycles are
// detected and returned as ErrReferenceCycle.
//
// The injection scope MUST be one of the panel-locked injection contexts
// because the resolver may unseal a sealed leaf for runtime injection.
func (vlt *Vault) ResolveReference(ctx context.Context, id VariableID, consumer Principal, scope ResolvedScope) ([]byte, AuditDecryptRecord, error) {
	if !isInjectionScope(scope) {
		return nil, AuditDecryptRecord{}, ErrSealedRevealed
	}
	visited := map[VariableID]bool{}
	cur := id
	for {
		if visited[cur] {
			return nil, AuditDecryptRecord{}, ErrReferenceCycle
		}
		visited[cur] = true
		v, err := vlt.Get(ctx, cur)
		if err != nil {
			return nil, AuditDecryptRecord{}, err
		}
		if err := vlt.auth.CanRead(ctx, consumer, v.meta); err != nil {
			return nil, AuditDecryptRecord{}, err
		}
		if !v.IsReference() {
			return vlt.Decrypt(ctx, cur, scope)
		}
		cur = v.refTo
	}
}
