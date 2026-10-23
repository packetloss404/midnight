package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestPreviewEnvironmentCapabilityIsAuthenticatedScopedAndExplicitlyDisabled(t *testing.T) {
	stack, sessions := productReadDataFixture(t)
	handler := NewRouterWithDataStack(stack)
	alice := loginIdentityForTest(t, sessions, "alice@example.com")

	visible := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/services/svc_a/preview-environments", alice)
	if visible.Code != http.StatusOK ||
		!strings.Contains(visible.Body.String(), `"service_id":"svc_a"`) ||
		!strings.Contains(visible.Body.String(), `"state":"setup_required"`) ||
		!strings.Contains(visible.Body.String(), `"automatic_lifecycle":false`) ||
		!strings.Contains(visible.Body.String(), `"current_disposition":"accepted_but_not_routed"`) ||
		!strings.Contains(visible.Body.String(), `"items":[]`) {
		t.Fatalf("visible capability status=%d body=%s", visible.Code, visible.Body.String())
	}

	crossTenant := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/services/svc_b/preview-environments", alice)
	if crossTenant.Code != http.StatusNotFound || strings.Contains(crossTenant.Body.String(), `"project_id":"prj_b"`) {
		t.Fatalf("cross-tenant status=%d body=%s", crossTenant.Code, crossTenant.Body.String())
	}

	unauthenticated := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/services/svc_a/preview-environments", "")
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
}

func TestPreviewEnvironmentCapabilityRejectsQueriesAndWrites(t *testing.T) {
	stack, sessions := productReadDataFixture(t)
	handler := NewRouterWithDataStack(stack)
	alice := loginIdentityForTest(t, sessions, "alice@example.com")

	query := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/services/svc_a/preview-environments?state=active", alice)
	if query.Code != http.StatusBadRequest {
		t.Fatalf("query status=%d body=%s", query.Code, query.Body.String())
	}
	mutation := authenticatedIdentityRequest(handler, http.MethodPost, v1alphaPrefix+"/services/svc_a/preview-environments", alice)
	if mutation.Code != http.StatusForbidden {
		t.Fatalf("mutation status=%d body=%s", mutation.Code, mutation.Body.String())
	}
}
