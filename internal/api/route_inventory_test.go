package api

import (
	"net/http"
	"testing"
)

func TestRuntimeRouteMuxFlagsUndocumentedV1AlphaRegistration(t *testing.T) {
	mux := newRuntimeRouteMux()
	mux.Handle("/api/v1alpha/undocumented", http.NotFoundHandler())
	got := mux.inventory.UnclassifiedRegistrations()
	if len(got) != 1 || got[0] != "/api/v1alpha/undocumented" {
		t.Fatalf("unclassified registrations = %v", got)
	}
}

func TestRuntimeRouteMuxInventoriesDispatcherOperations(t *testing.T) {
	mux := newRuntimeRouteMux()
	mux.Handle(v1alphaPrefix+"/operations/", http.NotFoundHandler())
	want := map[string]bool{
		"GET /api/v1alpha/operations/{id}":         false,
		"POST /api/v1alpha/operations/{id}/cancel": false,
		"GET /api/v1alpha/operations/{id}/events":  false,
	}
	for _, operation := range mux.inventory.Operations() {
		key := operation.Method + " " + operation.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for operation, found := range want {
		if !found {
			t.Errorf("dispatcher inventory missing %s", operation)
		}
	}
}

func TestNonRESTMountsStayOutsideV1AlphaInventory(t *testing.T) {
	// Static UI assets, GraphQL, MCP, OAuth, GraphiQL, and well-known discovery
	// are separate protocols or presentation mounts. They are intentionally not
	// represented as REST/OpenAPI operations and live outside /api/v1alpha.
	mux := newRuntimeRouteMux()
	for _, pattern := range []string{
		"/",
		"/dashboard/",
		"/graphql/v2",
		"/graphiql",
		"/mcp/v1",
		"/.well-known/mcp",
		"/auth/oauth/token",
	} {
		mux.Handle(pattern, http.NotFoundHandler())
	}
	if got := mux.inventory.UnclassifiedRegistrations(); len(got) != 0 {
		t.Fatalf("non-REST mounts entered v1alpha inventory: %v", got)
	}
	if got := mux.inventory.Operations(); len(got) != 0 {
		t.Fatalf("non-REST mounts declared REST operations: %v", got)
	}
}
