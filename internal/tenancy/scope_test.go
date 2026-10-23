package tenancy

import "testing"

func TestScopeValidateLabelsAcceptsExactTenantBoundary(t *testing.T) {
	scope := Scope{WorkspaceID: "wrk_acme", ProjectID: "prj_api", EnvironmentID: "env_prod"}
	labels := scope.Labels()
	labels["midnight.service"] = "svc_web"

	if err := scope.ValidateLabels(labels); err != nil {
		t.Fatalf("ValidateLabels() error = %v", err)
	}
}

func TestScopeValidateLabelsRejectsCrossTenantLabel(t *testing.T) {
	scope := Scope{WorkspaceID: "wrk_acme", ProjectID: "prj_api", EnvironmentID: "env_prod"}
	labels := scope.Labels()
	labels[LabelWorkspace] = "wrk_other"

	if err := scope.ValidateLabels(labels); err == nil {
		t.Fatal("ValidateLabels() error = nil, want cross-tenant rejection")
	}
}

func TestScopeKeySeparatesSameProjectAcrossWorkspaces(t *testing.T) {
	left := Scope{WorkspaceID: "wrk_acme", ProjectID: "prj_api", EnvironmentID: "env_prod"}
	right := Scope{WorkspaceID: "wrk_other", ProjectID: "prj_api", EnvironmentID: "env_prod"}

	if left.Key() == right.Key() {
		t.Fatalf("tenant keys collided: %q", left.Key())
	}
}

func TestScopeValidateRejectsUnsafeIDs(t *testing.T) {
	scope := Scope{WorkspaceID: "wrk/acme", ProjectID: "prj_api", EnvironmentID: "env_prod"}

	if err := scope.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unsafe id rejection")
	}
}
