package tcpproxy

import (
	"errors"
	"testing"
)

func TestPlanEndpointDisabledByDefaultDoesNotAllocate(t *testing.T) {
	plan, err := PlanEndpoint(PlanInput{Request: EndpointRequest{BackendHost: "10.0.0.7", BackendPort: 5432}})
	if err != nil {
		t.Fatalf("PlanEndpoint() error = %v", err)
	}
	if plan.Enabled || plan.Host != "" || plan.ExternalPort != 0 {
		t.Fatalf("PlanEndpoint() = %#v, want disabled plan without endpoint", plan)
	}
}

func TestPlanEndpointGeneratesHostAndAllocatesExternalPort(t *testing.T) {
	plan, err := PlanEndpoint(PlanInput{
		Request: EndpointRequest{
			Enabled:        true,
			ProjectID:      "Project_A",
			EnvironmentID:  "prod",
			ServiceID:      "postgres-primary",
			BaseDomain:     "example.test",
			BackendHost:    "10.0.0.7",
			BackendPort:    5432,
			SourceIPPolicy: SourceIPPolicy{AllowedCIDRs: []string{"203.0.113.10/32"}},
		},
		Existing: []ExistingEndpoint{{Host: "other.tcp.example.test", ExternalPort: DefaultExternalPortStart}},
	})
	if err != nil {
		t.Fatalf("PlanEndpoint() error = %v", err)
	}
	if !plan.Enabled {
		t.Fatal("PlanEndpoint() disabled, want enabled")
	}
	if plan.Host != "postgres-primary--prod--project-a.tcp.example.test" {
		t.Fatalf("host = %q", plan.Host)
	}
	if plan.ExternalPort != DefaultExternalPortStart+1 {
		t.Fatalf("external port = %d, want %d", plan.ExternalPort, DefaultExternalPortStart+1)
	}
	if len(plan.SourceIPPolicy.AllowedCIDRs) != 1 || plan.SourceIPPolicy.AllowedCIDRs[0] != "203.0.113.10/32" {
		t.Fatalf("source IP policy = %#v", plan.SourceIPPolicy)
	}
}

func TestPlanEndpointCanonicalizesSourceIPPolicy(t *testing.T) {
	plan, err := PlanEndpoint(PlanInput{Request: EndpointRequest{
		Enabled:        true,
		ProjectID:      "prj",
		EnvironmentID:  "env",
		ServiceID:      "postgres",
		BaseDomain:     "example.test",
		BackendHost:    "10.0.0.7",
		BackendPort:    5432,
		SourceIPPolicy: SourceIPPolicy{AllowedCIDRs: []string{"203.0.113.128/25", "203.0.113.10/32"}},
	}})
	if err != nil {
		t.Fatalf("PlanEndpoint() error = %v", err)
	}
	want := []string{"203.0.113.10/32", "203.0.113.128/25"}
	if len(plan.SourceIPPolicy.AllowedCIDRs) != len(want) {
		t.Fatalf("source IP policy = %#v", plan.SourceIPPolicy)
	}
	for i := range want {
		if plan.SourceIPPolicy.AllowedCIDRs[i] != want[i] {
			t.Fatalf("source IP policy = %#v, want %#v", plan.SourceIPPolicy.AllowedCIDRs, want)
		}
	}
}

func TestPlanEndpointUsesRequestedPortWhenAvailable(t *testing.T) {
	plan, err := PlanEndpoint(PlanInput{
		Request: EndpointRequest{
			Enabled:               true,
			ProjectID:             "prj",
			EnvironmentID:         "env",
			ServiceID:             "redis",
			BaseDomain:            "example.test",
			RequestedExternalPort: 26000,
			BackendHost:           "10.0.0.8",
			BackendPort:           6379,
			SourceIPPolicy:        SourceIPPolicy{AllowedCIDRs: []string{"2001:db8::1/128"}},
		},
	})
	if err != nil {
		t.Fatalf("PlanEndpoint() error = %v", err)
	}
	if plan.ExternalPort != 26000 {
		t.Fatalf("external port = %d, want 26000", plan.ExternalPort)
	}
}

func TestPlanEndpointAllowsExplicitPublicSourcePolicy(t *testing.T) {
	plan, err := PlanEndpoint(PlanInput{Request: EndpointRequest{
		Enabled:       true,
		ProjectID:     "prj",
		EnvironmentID: "env",
		ServiceID:     "mysql",
		BaseDomain:    "example.test",
		BackendHost:   "10.0.0.9",
		BackendPort:   3306,
	}})
	if err != nil {
		t.Fatalf("PlanEndpoint() error = %v", err)
	}
	if plan.SourceIPPolicy.AllowedCIDRs == nil || len(plan.SourceIPPolicy.AllowedCIDRs) != 0 {
		t.Fatalf("source IP policy = %#v, want explicit unrestricted slice", plan.SourceIPPolicy)
	}
	if err := ValidateSourceIPPolicy(SourceIPPolicy{AllowedCIDRs: []string{"0.0.0.0/0", "::/0"}}); err != nil {
		t.Fatalf("catch-all source policy should be valid: %v", err)
	}
}

func TestValidateSourceIPPolicyRejectsUnsafePolicies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy SourceIPPolicy
	}{
		{name: "malformed", policy: SourceIPPolicy{AllowedCIDRs: []string{"203.0.113.10"}}},
		{name: "duplicate", policy: SourceIPPolicy{AllowedCIDRs: []string{"203.0.113.10/32", "203.0.113.10/32"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSourceIPPolicy(tc.policy); !errors.Is(err, ErrInvalidSourceIPPolicy) {
				t.Fatalf("ValidateSourceIPPolicy() error = %v, want ErrInvalidSourceIPPolicy", err)
			}
		})
	}
}

func TestProductContractReturnsStablePublicShape(t *testing.T) {
	plan, err := PlanEndpoint(PlanInput{Request: EndpointRequest{
		Enabled:        true,
		ProjectID:      "prj",
		EnvironmentID:  "prod",
		ServiceID:      "postgres",
		BaseDomain:     "example.test",
		BackendHost:    " 10.0.0.7 ",
		BackendPort:    5432,
		SourceIPPolicy: SourceIPPolicy{AllowedCIDRs: []string{"203.0.113.128/25", "203.0.113.10/32"}},
	}})
	if err != nil {
		t.Fatalf("PlanEndpoint() error = %v", err)
	}

	contract, err := ProductContract(plan)
	if err != nil {
		t.Fatalf("ProductContract() error = %v", err)
	}
	if contract.Kind != "tcp-proxy" {
		t.Fatalf("Kind = %q", contract.Kind)
	}
	if contract.PublicAddress != "postgres--prod--prj.tcp.example.test:20000" {
		t.Fatalf("PublicAddress = %q", contract.PublicAddress)
	}
	if contract.BackendHost != "10.0.0.7" || contract.BackendPort != 5432 {
		t.Fatalf("backend = %q:%d", contract.BackendHost, contract.BackendPort)
	}
	want := []string{"203.0.113.10/32", "203.0.113.128/25"}
	for i := range want {
		if contract.AllowedSourceCIDRs[i] != want[i] || contract.SourceIPPolicy.AllowedCIDRs[i] != want[i] {
			t.Fatalf("source policy = %#v / %#v, want %#v", contract.AllowedSourceCIDRs, contract.SourceIPPolicy.AllowedCIDRs, want)
		}
	}
}

func TestProductContractRejectsDisabledPlan(t *testing.T) {
	if _, err := ProductContract(EndpointPlan{}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("ProductContract() error = %v, want ErrDisabled", err)
	}
}

func TestAllocateExternalPortReportsExhaustion(t *testing.T) {
	_, err := AllocateExternalPort(0, PortRange{Start: 20000, End: 20001}, []ExistingEndpoint{{ExternalPort: 20000}, {ExternalPort: 20001}})
	if !errors.Is(err, ErrNoExternalPortsAvailable) {
		t.Fatalf("AllocateExternalPort() error = %v, want ErrNoExternalPortsAvailable", err)
	}
}
