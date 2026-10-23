package runtime

import (
	"strings"
	"testing"
)

func TestValidateBridgeNetworkSpecRequiresProjectEnvName(t *testing.T) {
	spec := BridgeNetworkSpec{
		ProjectID:     " project-a ",
		EnvironmentID: " env-a ",
		Name:          "or-net-project-a-env-a",
		IPAM:          []IPAMPool{{Subnet: "10.77.0.0/24"}},
	}

	if err := ValidateBridgeNetworkSpec(spec); err != nil {
		t.Fatalf("ValidateBridgeNetworkSpec() error = %v", err)
	}
}

func TestValidateBridgeNetworkSpecRejectsWrongName(t *testing.T) {
	err := ValidateBridgeNetworkSpec(BridgeNetworkSpec{ProjectID: "project-a", EnvironmentID: "env-a", Name: "bridge"})

	if err == nil || !strings.Contains(err.Error(), "or-net-project-a-env-a") {
		t.Fatalf("ValidateBridgeNetworkSpec() error = %v", err)
	}
}

func TestFindBridgeNetworkConflictDetectsIPAMConflict(t *testing.T) {
	requested := BridgeNetworkSpec{ProjectID: "project-a", EnvironmentID: "env-a", Name: "or-net-project-a-env-a", IPAM: []IPAMPool{{Subnet: "10.77.0.0/24"}}}
	existing := []BridgeNetworkSpec{{ProjectID: "project-b", EnvironmentID: "env-b", Name: "or-net-project-b-env-b", IPAM: []IPAMPool{{Subnet: "10.77.0.0/24"}}}}

	conflict := FindBridgeNetworkConflict(requested, existing)
	if conflict == nil || conflict.Kind != BridgeNetworkIPAMConflict || conflict.RequestedValue != "10.77.0.0/24" {
		t.Fatalf("conflict = %+v", conflict)
	}
}

func TestFindBridgeNetworkConflictAllowsSameNetwork(t *testing.T) {
	requested := BridgeNetworkSpec{ProjectID: "project-a", EnvironmentID: "env-a", Name: "or-net-project-a-env-a", IPAM: []IPAMPool{{Subnet: "10.77.0.0/24"}}}

	if conflict := FindBridgeNetworkConflict(requested, []BridgeNetworkSpec{requested}); conflict != nil {
		t.Fatalf("conflict = %+v", conflict)
	}
}
