package runtime

import (
	"fmt"
	"strings"
)

type BridgeNetworkSpec struct {
	ProjectID     string
	EnvironmentID string
	Name          string
	IPAM          []IPAMPool
}

type IPAMPool struct {
	Subnet  string
	Gateway string
}

type BridgeNetworkConflictKind string

const (
	BridgeNetworkNameConflict BridgeNetworkConflictKind = "bridge_name_conflict"
	BridgeNetworkIPAMConflict BridgeNetworkConflictKind = "ipam_conflict"
)

// BridgeNetworkConflict is a pure placeholder model for future Docker network reconciliation.
type BridgeNetworkConflict struct {
	Kind                  BridgeNetworkConflictKind
	ProjectID             string
	EnvironmentID         string
	BridgeName            string
	ExistingProjectID     string
	ExistingEnvironmentID string
	ExistingBridgeName    string
	RequestedValue        string
	ExistingValue         string
}

func DefaultBridgeNetworkName(projectID, environmentID string) string {
	return "or-net-" + strings.TrimSpace(projectID) + "-" + strings.TrimSpace(environmentID)
}

func ValidateBridgeNetworkSpec(spec BridgeNetworkSpec) error {
	spec = normalizeBridgeNetworkSpec(spec)
	if spec.ProjectID == "" {
		return fmt.Errorf("bridge network project id is required")
	}
	if spec.EnvironmentID == "" {
		return fmt.Errorf("bridge network environment id is required")
	}
	if spec.Name != DefaultBridgeNetworkName(spec.ProjectID, spec.EnvironmentID) {
		return fmt.Errorf("bridge network name must be %q", DefaultBridgeNetworkName(spec.ProjectID, spec.EnvironmentID))
	}
	seenSubnets := make(map[string]struct{}, len(spec.IPAM))
	for _, pool := range spec.IPAM {
		if pool.Subnet == "" {
			return fmt.Errorf("bridge network ipam subnet is required")
		}
		if _, ok := seenSubnets[pool.Subnet]; ok {
			return fmt.Errorf("bridge network ipam subnet %q must be unique", pool.Subnet)
		}
		seenSubnets[pool.Subnet] = struct{}{}
	}
	return nil
}

func FindBridgeNetworkConflict(requested BridgeNetworkSpec, existing []BridgeNetworkSpec) *BridgeNetworkConflict {
	requested = normalizeBridgeNetworkSpec(requested)
	for _, candidate := range existing {
		candidate = normalizeBridgeNetworkSpec(candidate)
		if candidate.Name == requested.Name && (candidate.ProjectID != requested.ProjectID || candidate.EnvironmentID != requested.EnvironmentID) {
			return &BridgeNetworkConflict{
				Kind:                  BridgeNetworkNameConflict,
				ProjectID:             requested.ProjectID,
				EnvironmentID:         requested.EnvironmentID,
				BridgeName:            requested.Name,
				ExistingProjectID:     candidate.ProjectID,
				ExistingEnvironmentID: candidate.EnvironmentID,
				ExistingBridgeName:    candidate.Name,
				RequestedValue:        requested.Name,
				ExistingValue:         candidate.Name,
			}
		}
		for _, requestedPool := range requested.IPAM {
			for _, candidatePool := range candidate.IPAM {
				if requestedPool.Subnet != "" && requestedPool.Subnet == candidatePool.Subnet && candidate.Name != requested.Name {
					return &BridgeNetworkConflict{
						Kind:                  BridgeNetworkIPAMConflict,
						ProjectID:             requested.ProjectID,
						EnvironmentID:         requested.EnvironmentID,
						BridgeName:            requested.Name,
						ExistingProjectID:     candidate.ProjectID,
						ExistingEnvironmentID: candidate.EnvironmentID,
						ExistingBridgeName:    candidate.Name,
						RequestedValue:        requestedPool.Subnet,
						ExistingValue:         candidatePool.Subnet,
					}
				}
			}
		}
	}
	return nil
}

func normalizeBridgeNetworkSpec(spec BridgeNetworkSpec) BridgeNetworkSpec {
	spec.ProjectID = strings.TrimSpace(spec.ProjectID)
	spec.EnvironmentID = strings.TrimSpace(spec.EnvironmentID)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.IPAM = append([]IPAMPool(nil), spec.IPAM...)
	for i := range spec.IPAM {
		spec.IPAM[i].Subnet = strings.TrimSpace(spec.IPAM[i].Subnet)
		spec.IPAM[i].Gateway = strings.TrimSpace(spec.IPAM[i].Gateway)
	}
	return spec
}
