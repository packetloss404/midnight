package api

import (
	"net/http"
	"sort"
	"strings"
	"sync"
)

// RuntimeRouteOperation is one REST operation deliberately exposed by the
// production router. Path is the canonical templated URL, while Pattern is the
// net/http registration that owns it. Keeping both fields makes subtree
// dispatchers (agents, services, operations, webhooks, and legacy item
// families) visible to conformance checks instead of treating them as opaque.
type RuntimeRouteOperation struct {
	Method       string
	Path         string
	Pattern      string
	Experimental bool
}

// RuntimeRouteInventory is populated by the same registrar that builds the
// production ServeMux. It is read-only after router construction.
type RuntimeRouteInventory struct {
	mu           sync.Mutex
	operations   map[string]RuntimeRouteOperation
	unclassified map[string]struct{}
}

func newRuntimeRouteInventory() *RuntimeRouteInventory {
	return &RuntimeRouteInventory{
		operations:   make(map[string]RuntimeRouteOperation),
		unclassified: make(map[string]struct{}),
	}
}

// Operations returns the runtime-declared REST surface in stable order.
func (i *RuntimeRouteInventory) Operations() []RuntimeRouteOperation {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]RuntimeRouteOperation, 0, len(i.operations))
	for _, operation := range i.operations {
		out = append(out, operation)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Path == out[b].Path {
			return out[a].Method < out[b].Method
		}
		return out[a].Path < out[b].Path
	})
	return out
}

// UnclassifiedRegistrations returns v1alpha registrations that did not claim
// any explicit REST operation. Such a registration is either an undocumented
// REST surface or needs a narrowly documented non-REST exclusion.
func (i *RuntimeRouteInventory) UnclassifiedRegistrations() []string {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]string, 0, len(i.unclassified))
	for pattern := range i.unclassified {
		out = append(out, pattern)
	}
	sort.Strings(out)
	return out
}

type runtimeRouteMux struct {
	*http.ServeMux
	inventory *RuntimeRouteInventory
	catalog   []RuntimeRouteOperation
}

func newRuntimeRouteMux() *runtimeRouteMux {
	return &runtimeRouteMux{
		ServeMux:  http.NewServeMux(),
		inventory: newRuntimeRouteInventory(),
		catalog:   canonicalRuntimeRESTRoutes,
	}
}

func (m *runtimeRouteMux) Handle(pattern string, handler http.Handler) {
	m.record(pattern)
	m.ServeMux.Handle(pattern, handler)
}

func (m *runtimeRouteMux) HandleFunc(pattern string, handler http.HandlerFunc) {
	m.record(pattern)
	m.ServeMux.HandleFunc(pattern, handler)
}

func (m *runtimeRouteMux) record(pattern string) {
	if m == nil || m.inventory == nil || !strings.HasPrefix(pattern, v1alphaPrefix) {
		return
	}
	matched := false
	m.inventory.mu.Lock()
	defer m.inventory.mu.Unlock()
	for _, operation := range m.catalog {
		if operation.Experimental && !experimentalEnabled() {
			continue
		}
		if !runtimeRegistrationOwns(pattern, operation.Pattern) {
			continue
		}
		matched = true
		key := operation.Method + " " + operation.Path
		m.inventory.operations[key] = operation
	}
	if !matched {
		m.inventory.unclassified[pattern] = struct{}{}
	}
}

func runtimeRegistrationOwns(actual, declared string) bool {
	if normalizeRuntimeRoutePattern(actual) == normalizeRuntimeRoutePattern(declared) {
		return true
	}
	// Four provider-specific subtree registrations implement the single
	// provider-templated inbound-webhook REST operation.
	return strings.HasPrefix(actual, WebhookRoutePrefix) &&
		strings.HasPrefix(declared, WebhookRoutePrefix)
}

func normalizeRuntimeRoutePattern(pattern string) string {
	var result strings.Builder
	for cursor := 0; cursor < len(pattern); {
		openOffset := strings.IndexByte(pattern[cursor:], '{')
		if openOffset < 0 {
			result.WriteString(pattern[cursor:])
			break
		}
		open := cursor + openOffset
		result.WriteString(pattern[cursor:open])
		closeOffset := strings.IndexByte(pattern[open:], '}')
		if closeOffset < 0 {
			result.WriteString(pattern[open:])
			break
		}
		result.WriteString("{}")
		cursor = open + closeOffset + 1
	}
	return result.String()
}

// handlerRegistrar is the narrow registration seam shared by router modules.
// Both http.ServeMux (isolated handler tests) and runtimeRouteMux (production
// construction plus inventory recording) implement it.
type handlerRegistrar interface {
	Handle(pattern string, handler http.Handler)
}
