package varrefs

import (
	"context"
	"sort"
	"sync"
)

// MemoryLookup is an in-memory implementation of VariableLookup used by
// tests and by the default Phase 1 router wiring. It is safe for
// concurrent use.
type MemoryLookup struct {
	mu sync.RWMutex
	// service variables keyed by (project, env, service, name)
	service map[string]string
	// shared variables keyed by (project, env, name)
	shared map[string]string
	// service id by (project, env, name)
	serviceIDs map[string]string
	// builtin values keyed by (project, env, service, name)
	builtins map[string]string
}

// NewMemoryLookup constructs a zero-valued in-memory lookup.
func NewMemoryLookup() *MemoryLookup {
	return &MemoryLookup{
		service:    map[string]string{},
		shared:     map[string]string{},
		serviceIDs: map[string]string{},
		builtins:   map[string]string{},
	}
}

func svcKey(project, env, service, name string) string {
	return project + "\x00" + env + "\x00" + service + "\x00" + name
}
func sharedKey(project, env, name string) string {
	return project + "\x00" + env + "\x00" + name
}
func idKey(project, env, name string) string {
	return project + "\x00" + env + "\x00" + name
}

// SetService stores a template for a service variable.
func (m *MemoryLookup) SetService(project, env, service, name, template string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.service[svcKey(project, env, service, name)] = template
}

// SetShared stores a template for a project-scoped shared variable.
func (m *MemoryLookup) SetShared(project, env, name, template string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shared[sharedKey(project, env, name)] = template
}

// SetServiceName registers a user-facing name -> service-id mapping.
func (m *MemoryLookup) SetServiceName(project, env, name, serviceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.serviceIDs[idKey(project, env, name)] = serviceID
}

// SetBuiltin stores a built-in value for a service.
func (m *MemoryLookup) SetBuiltin(project, env, service, name, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.builtins[svcKey(project, env, service, name)] = value
}

// LookupService implements VariableLookup.
func (m *MemoryLookup) LookupService(_ context.Context, projectID, envID, serviceID, name string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.service[svcKey(projectID, envID, serviceID, name)]
	return v, ok, nil
}

// LookupShared implements VariableLookup.
func (m *MemoryLookup) LookupShared(_ context.Context, projectID, envID, name string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.shared[sharedKey(projectID, envID, name)]
	return v, ok, nil
}

// LookupServiceByName implements VariableLookup.
func (m *MemoryLookup) LookupServiceByName(_ context.Context, projectID, envID, name string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.serviceIDs[idKey(projectID, envID, name)]
	return v, ok, nil
}

// LookupBuiltin implements VariableLookup.
//
// When no explicit value has been recorded, LookupBuiltin synthesizes a
// reasonable default for MIDNIGHT_PRIVATE_DOMAIN ("<service>.midnight.internal")
// and MIDNIGHT_SERVICE_NAME (the service id itself). MIDNIGHT_PUBLIC_DOMAIN
// is returned as "" + false when unset because public domains are
// per-route and only meaningful after route promotion.
func (m *MemoryLookup) LookupBuiltin(_ context.Context, projectID, envID, serviceID, name string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.builtins[svcKey(projectID, envID, serviceID, name)]; ok {
		return v, true, nil
	}
	switch name {
	case BuiltinPrivateDomain:
		if serviceID == "" {
			return "", false, nil
		}
		return serviceID + ".midnight.internal", true, nil
	case BuiltinServiceName:
		if serviceID == "" {
			return "", false, nil
		}
		return serviceID, true, nil
	case BuiltinPublicDomain:
		return "", false, nil
	default:
		return "", false, nil
	}
}

// ListServiceVariables implements VariableLookup.
func (m *MemoryLookup) ListServiceVariables(_ context.Context, projectID, envID, serviceID string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	prefix := projectID + "\x00" + envID + "\x00" + serviceID + "\x00"
	out := make([]string, 0)
	for k := range m.service {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k[len(prefix):])
		}
	}
	sort.Strings(out)
	return out, nil
}
