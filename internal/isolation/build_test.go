package isolation

import (
	"testing"

	"github.com/midnight/midnight/internal/tenancy"
)

func TestPlanBuildDefaultsUntrustedToGVisorWithPrivateCache(t *testing.T) {
	plan, err := PlanBuild(BuildRequest{
		Tenant:  fixtureTenant(),
		BuildID: "bld_123",
		Risk:    RiskUntrusted,
	})
	if err != nil {
		t.Fatalf("PlanBuild() error = %v", err)
	}
	if plan.Backend != BackendGVisor {
		t.Fatalf("Backend = %q, want %q", plan.Backend, BackendGVisor)
	}
	if plan.CacheMode != CachePrivate || plan.CacheNamespace != "workspace:wrk_acme|project:prj_api|environment:env_prod|build:bld_123" {
		t.Fatalf("cache isolation = %q/%q", plan.CacheMode, plan.CacheNamespace)
	}
	if plan.NetworkEgress {
		t.Fatal("untrusted build should disable network egress")
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestPlanBuildDefaultsIsolatedToMicroVMPlanOnly(t *testing.T) {
	plan, err := PlanBuild(BuildRequest{Tenant: fixtureTenant(), BuildID: "bld_123", Risk: RiskIsolated})
	if err != nil {
		t.Fatalf("PlanBuild() error = %v", err)
	}
	if plan.Backend != BackendMicroVM {
		t.Fatalf("Backend = %q, want %q", plan.Backend, BackendMicroVM)
	}
	if !plan.PlanOnly {
		t.Fatal("expected VM isolation backend to remain plan-only")
	}
	if len(plan.IsolationWarnings) == 0 {
		t.Fatal("expected plan-only warning for microvm backend")
	}
}

func TestPlanBuildRejectsContainerForIsolatedRisk(t *testing.T) {
	_, err := PlanBuild(BuildRequest{
		Tenant:           fixtureTenant(),
		BuildID:          "bld_123",
		Risk:             RiskIsolated,
		RequestedBackend: BackendContainer,
	})
	if err == nil {
		t.Fatal("PlanBuild() error = nil, want isolated container rejection")
	}
}

func TestPlanBuildRejectsUnsafeBuildID(t *testing.T) {
	_, err := PlanBuild(BuildRequest{Tenant: fixtureTenant(), BuildID: "bld/123", Risk: RiskTrusted})
	if err == nil {
		t.Fatal("PlanBuild() error = nil, want unsafe build id rejection")
	}
}

func TestBuildPlanValidateRejectsCrossTenantLabels(t *testing.T) {
	plan, err := PlanBuild(BuildRequest{Tenant: fixtureTenant(), BuildID: "bld_123", Risk: RiskTrusted})
	if err != nil {
		t.Fatalf("PlanBuild() error = %v", err)
	}
	plan.RequiredLabels[tenancy.LabelProject] = "prj_other"

	if err := plan.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want cross-tenant label rejection")
	}
}

func TestPlanBuildCacheNamespaceDoesNotCollideOnDottedIDs(t *testing.T) {
	first, err := PlanBuild(BuildRequest{
		Tenant:  tenancy.Scope{WorkspaceID: "wrk.a", ProjectID: "prj", EnvironmentID: "env"},
		BuildID: "bld",
		Risk:    RiskUntrusted,
	})
	if err != nil {
		t.Fatalf("PlanBuild(first) error = %v", err)
	}
	second, err := PlanBuild(BuildRequest{
		Tenant:  tenancy.Scope{WorkspaceID: "wrk", ProjectID: "a.prj", EnvironmentID: "env"},
		BuildID: "bld",
		Risk:    RiskUntrusted,
	})
	if err != nil {
		t.Fatalf("PlanBuild(second) error = %v", err)
	}

	if first.CacheNamespace == second.CacheNamespace {
		t.Fatalf("cache namespaces collided: %q", first.CacheNamespace)
	}
}

func fixtureTenant() tenancy.Scope {
	return tenancy.Scope{WorkspaceID: "wrk_acme", ProjectID: "prj_api", EnvironmentID: "env_prod"}
}
