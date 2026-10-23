// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/varrefs"
)

// ServiceIDLookup resolves a display/slug name to its canonical service id.
type ServiceIDLookup interface {
	LookupServiceID(ctx context.Context, projectID, environmentID, name string) (string, bool, error)
}

// DecryptAuditSink persists the redaction-safe evidence returned by Vault
// before a decrypted value crosses the runtime/build injection boundary.
type DecryptAuditSink interface {
	RecordDecrypt(ctx context.Context, meta VariableMeta, version int, record AuditDecryptRecord) error
}

// RuntimeLookup adapts encrypted Vault records to varrefs.VariableLookup.
// Plaintext exists only for the duration of a deploy resolution call and is
// never returned by the public metadata API.
type RuntimeLookup struct {
	Vault                *Vault
	Services             ServiceIDLookup
	Audit                DecryptAuditSink
	purpose              ResolvedScope
	auditRequired        bool
	pinned               map[string]varrefs.VariableVersionRef
	pinnedServiceNames   map[string]string
	pinnedAliasesInvalid bool
}

// NewRuntimeLookup constructs the deploy-time variable lookup.
func NewRuntimeLookup(vault *Vault, services ServiceIDLookup) *RuntimeLookup {
	return &RuntimeLookup{Vault: vault, Services: services, purpose: ScopeRuntimeInject}
}

// NewBuildLookup constructs the build-time variable lookup. It shares the
// reference-resolution behavior of RuntimeLookup but authorizes every decrypt
// exclusively as ScopeBuildInject.
func NewBuildLookup(vault *Vault, services ServiceIDLookup) *RuntimeLookup {
	return &RuntimeLookup{Vault: vault, Services: services, purpose: ScopeBuildInject}
}

// NewAuditedRuntimeLookup constructs a production runtime lookup that fails
// closed unless every successful decrypt is durably audited.
func NewAuditedRuntimeLookup(vault *Vault, services ServiceIDLookup, sink DecryptAuditSink) *RuntimeLookup {
	lookup := NewRuntimeLookup(vault, services)
	lookup.Audit = sink
	lookup.auditRequired = true
	return lookup
}

// NewAuditedBuildLookup is the build-injection equivalent of
// NewAuditedRuntimeLookup.
func NewAuditedBuildLookup(vault *Vault, services ServiceIDLookup, sink DecryptAuditSink) *RuntimeLookup {
	lookup := NewBuildLookup(vault, services)
	lookup.Audit = sink
	lookup.auditRequired = true
	return lookup
}

// LookupService decrypts a service-scoped template for runtime injection.
func (l *RuntimeLookup) LookupService(ctx context.Context, projectID, environmentID, serviceID, name string) (string, bool, error) {
	return l.lookup(ctx, projectID, environmentID, serviceID, name)
}

// LookupShared decrypts a project/environment-scoped shared template.
func (l *RuntimeLookup) LookupShared(ctx context.Context, projectID, environmentID, name string) (string, bool, error) {
	return l.lookup(ctx, projectID, environmentID, "", name)
}

func (l *RuntimeLookup) lookup(ctx context.Context, projectID, environmentID, serviceID, name string) (string, bool, error) {
	if l == nil || l.Vault == nil {
		return "", false, errors.New("runtime variable lookup is not configured")
	}
	if l.pinned != nil {
		ref, ok := l.pinned[versionedLookupKey(serviceID, name)]
		if !ok {
			return "", false, nil
		}
		variable, err := l.Vault.GetVersion(ctx, VariableID(ref.ID), ref.Version)
		if err != nil {
			if errors.Is(err, ErrVariableNotFound) {
				return "", false, err
			}
			return "", false, variableInfrastructureError(err)
		}
		meta := variable.Meta()
		if meta.Project != projectID || meta.Environment != environmentID ||
			meta.Service != serviceID || meta.Name != name {
			return "", false, variableInfrastructureError(ErrVariableScopeIntegrity)
		}
		purpose := l.injectionPurpose()
		if purpose != ScopeRuntimeInject && purpose != ScopeBuildInject {
			return "", false, variableInfrastructureError(errors.New("variable lookup injection purpose is invalid"))
		}
		if l.auditRequired && l.Audit == nil {
			return "", false, variableInfrastructureError(errors.New("variable decrypt audit is required"))
		}
		plaintext, record, err := l.Vault.DecryptVersion(ctx, meta.ID, ref.Version, purpose)
		if err != nil {
			if errors.Is(err, ErrVariableNotFound) {
				return "", false, err
			}
			return "", false, variableInfrastructureError(err)
		}
		if err := l.recordDecrypt(ctx, meta, ref.Version, record); err != nil {
			zero(plaintext)
			return "", false, variableInfrastructureError(err)
		}
		value := string(plaintext)
		zero(plaintext)
		return value, true, nil
	}
	variable, err := l.Vault.Find(ctx, projectID, environmentID, serviceID, name)
	if errors.Is(err, ErrVariableNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, variableInfrastructureError(err)
	}
	purpose := l.injectionPurpose()
	if purpose != ScopeRuntimeInject && purpose != ScopeBuildInject {
		return "", false, variableInfrastructureError(errors.New("variable lookup injection purpose is invalid"))
	}
	if l.auditRequired && l.Audit == nil {
		return "", false, variableInfrastructureError(errors.New("variable decrypt audit is required"))
	}
	plaintext, record, err := l.Vault.Decrypt(ctx, variable.meta.ID, purpose)
	if err != nil {
		return "", false, variableInfrastructureError(err)
	}
	if err := l.recordDecrypt(ctx, variable.meta, variable.meta.Version, record); err != nil {
		zero(plaintext)
		return "", false, variableInfrastructureError(err)
	}
	value := string(plaintext)
	zero(plaintext)
	return value, true, nil
}

func (l *RuntimeLookup) recordDecrypt(ctx context.Context, meta VariableMeta, version int, record AuditDecryptRecord) error {
	if l.Audit == nil {
		return nil
	}
	if err := l.Audit.RecordDecrypt(ctx, meta, version, record); err != nil {
		return fmt.Errorf("persist variable decrypt audit: %w", err)
	}
	return nil
}

// LookupServiceByName implements varrefs.VariableLookup.
func (l *RuntimeLookup) LookupServiceByName(ctx context.Context, projectID, environmentID, name string) (string, bool, error) {
	if l == nil {
		return "", false, nil
	}
	if l.pinned != nil {
		if l.pinnedAliasesInvalid {
			return "", false, variableInfrastructureError(ErrVariableScopeIntegrity)
		}
		id, ok := l.pinnedServiceNames[name]
		return id, ok, nil
	}
	if l.Services == nil {
		return "", false, nil
	}
	id, ok, err := l.Services.LookupServiceID(ctx, projectID, environmentID, name)
	if err != nil {
		return "", false, variableInfrastructureError(err)
	}
	return id, ok, nil
}

// LookupBuiltin implements the safe built-ins that are known before route
// promotion. Public domains are deliberately omitted because they are only
// durable after promotion.
func (l *RuntimeLookup) LookupBuiltin(_ context.Context, _, _, serviceID, name string) (string, bool, error) {
	switch name {
	case varrefs.BuiltinPrivateDomain:
		if strings.TrimSpace(serviceID) == "" {
			return "", false, nil
		}
		return serviceID + ".midnight.internal", true, nil
	case varrefs.BuiltinServiceName:
		if strings.TrimSpace(serviceID) == "" {
			return "", false, nil
		}
		return serviceID, true, nil
	case varrefs.BuiltinPublicDomain:
		return "", false, nil
	default:
		return "", false, nil
	}
}

// ListServiceVariables returns only names, never values.
func (l *RuntimeLookup) ListServiceVariables(ctx context.Context, projectID, environmentID, serviceID string) ([]string, error) {
	if l == nil || l.Vault == nil {
		return nil, errors.New("runtime variable lookup is not configured")
	}
	if l.pinned != nil {
		names := make([]string, 0)
		for _, ref := range l.pinned {
			if ref.ServiceID == serviceID {
				names = append(names, ref.Name)
			}
		}
		sort.Strings(names)
		return names, nil
	}
	metas, err := l.Vault.List(ctx)
	if err != nil {
		return nil, variableInfrastructureError(err)
	}
	names := make([]string, 0)
	for _, meta := range metas {
		if meta.Project == projectID && meta.Environment == environmentID && meta.Service == serviceID {
			names = append(names, meta.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// WithVariableVersions returns an isolated immutable-snapshot lookup. The
// active lookup remains unchanged and no plaintext is copied into the pin map.
func (l *RuntimeLookup) WithVariableVersions(refs []varrefs.VariableVersionRef) varrefs.VariableLookup {
	clone := *l
	clone.pinned = make(map[string]varrefs.VariableVersionRef, len(refs))
	type serviceBinding struct {
		slug        string
		displayName string
	}
	bindings := make(map[string]serviceBinding)
	for _, ref := range refs {
		clone.pinned[versionedLookupKey(ref.ServiceID, ref.Name)] = ref
		if ref.ServiceID == "" || (ref.ServiceSlug == "" && ref.ServiceDisplayName == "") {
			continue
		}
		binding := serviceBinding{slug: ref.ServiceSlug, displayName: ref.ServiceDisplayName}
		if prior, exists := bindings[ref.ServiceID]; exists && prior != binding {
			clone.pinnedAliasesInvalid = true
			continue
		}
		bindings[ref.ServiceID] = binding
	}
	serviceIDs := make([]string, 0, len(bindings))
	for serviceID := range bindings {
		serviceIDs = append(serviceIDs, serviceID)
	}
	sort.Strings(serviceIDs)
	clone.pinnedServiceNames = make(map[string]string, len(bindings)*2)
	for _, serviceID := range serviceIDs {
		slug := bindings[serviceID].slug
		if slug == "" {
			continue
		}
		if prior, exists := clone.pinnedServiceNames[slug]; exists && prior != serviceID {
			clone.pinnedAliasesInvalid = true
			continue
		}
		clone.pinnedServiceNames[slug] = serviceID
	}
	for _, serviceID := range serviceIDs {
		displayName := bindings[serviceID].displayName
		if displayName == "" {
			continue
		}
		// Live lookup gives slugs priority over display names and then chooses
		// the lowest service id for duplicate display names. Preserve those
		// semantics from the immutable snapshot without consulting live rows.
		if _, exists := clone.pinnedServiceNames[displayName]; !exists {
			clone.pinnedServiceNames[displayName] = serviceID
		}
	}
	return &clone
}

func (l *RuntimeLookup) injectionPurpose() ResolvedScope {
	purpose := l.purpose
	if purpose == "" {
		// Preserve the safe historical behavior for zero-value lookups used by
		// tests while keeping production constructors explicit.
		purpose = ScopeRuntimeInject
	}
	return purpose
}

func versionedLookupKey(serviceID, name string) string {
	return serviceID + "\x00" + name
}

var _ varrefs.VariableLookup = (*RuntimeLookup)(nil)
var _ varrefs.VersionedVariableLookup = (*RuntimeLookup)(nil)
