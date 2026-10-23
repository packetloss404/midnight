package runtime

import (
	"fmt"
	"sort"
	"strings"
)

var allowedCapAdd = map[string]struct{}{
	"CHOWN":            {},
	"DAC_OVERRIDE":     {},
	"FOWNER":           {},
	"FSETID":           {},
	"KILL":             {},
	"NET_BIND_SERVICE": {},
	"SETGID":           {},
	"SETUID":           {},
	"SETPCAP":          {},
	"SYS_CHROOT":       {},
}

// RequiredLabelKeys are the panel-locked Midnight ownership labels every
// managed container MUST carry. Container specs that omit any one fail
// validation with a CONTAINER_LABEL_MISSING-shaped error.
var RequiredLabelKeys = []string{
	"midnight.project",
	"midnight.service",
	"midnight.deployment",
	"midnight.instance",
}

// PrivilegedHostPortFloor is the smallest unprivileged host port. Specs
// that bind to a host port below the floor must allowlist explicitly.
const PrivilegedHostPortFloor = 1024

// PortBinding is one host-port to container-port mapping inside a spec.
type PortBinding struct {
	HostPort      int
	ContainerPort int
	Protocol      string
}

// SpecProfile selects the validation profile applied to a spec.
// The default profile rejects host-network, privileged, and
// privileged-port bindings unless explicitly allowed.
type SpecProfile string

const (
	SpecProfileDefault   SpecProfile = "default"
	SpecProfilePermitted SpecProfile = "permitted"
)

// ContainerSpec is the agent-owned runtime container shape used for pure validation and planning.
type ContainerSpec struct {
	Image          string
	Command        []string
	Env            map[string]string
	Labels         map[string]string
	Health         []HealthCheckSpec
	Volumes        []VolumeMount
	Ports          []PortBinding
	CapAdd         []string
	CapDrop        []string
	HostNetwork    bool
	HostPID        bool
	HostIPC        bool
	Privileged     bool
	Init           bool
	Profile        SpecProfile
	AllowHostPorts []int
}

// VolumeMount describes one Midnight-managed volume mount requested by a
// container spec. The runtime planner only emits attach/detach steps when the
// observed backend reports volume support.
type VolumeMount struct {
	VolumeID string
	Target   string
	ReadOnly bool
}

// ValidateContainerSpec enforces Phase 1 container hardening defaults.
// The default profile refuses host namespaces, privileged mode, capability
// requests outside the operator allowlist, host-port bindings below 1024
// without explicit allowlisting, and missing midnight ownership labels.
func ValidateContainerSpec(spec ContainerSpec) error {
	profile := normalizeProfile(spec.Profile)
	if !containsNormalized(spec.CapDrop, "ALL") {
		return fmt.Errorf("container spec must drop all capabilities")
	}
	for _, capability := range spec.CapAdd {
		normalized := normalizeCapability(capability)
		if _, ok := allowedCapAdd[normalized]; !ok {
			return fmt.Errorf("container spec capability %q is not allowed", capability)
		}
	}
	if spec.HostNetwork {
		return fmt.Errorf("container spec must not use host network namespace")
	}
	if spec.HostPID {
		return fmt.Errorf("container spec must not use host pid namespace")
	}
	if spec.HostIPC {
		return fmt.Errorf("container spec must not use host ipc namespace")
	}
	if spec.Privileged {
		return fmt.Errorf("container spec must not run privileged")
	}
	if !spec.Init {
		return fmt.Errorf("container spec must enable init")
	}
	if profile == SpecProfileDefault {
		if err := validateRequiredLabels(spec.Labels); err != nil {
			return err
		}
	}
	if err := validatePortBindings(profile, spec); err != nil {
		return err
	}
	if err := ValidateHealthChecks(spec.Health); err != nil {
		return err
	}
	if err := validateVolumeMounts(spec.Volumes); err != nil {
		return err
	}
	return nil
}

// MissingRequiredLabels reports the set of required labels missing from a
// label map. The result is sorted for deterministic diagnostics.
func MissingRequiredLabels(labels map[string]string) []string {
	missing := make([]string, 0, len(RequiredLabelKeys))
	for _, key := range RequiredLabelKeys {
		value, ok := labels[key]
		if !ok || strings.TrimSpace(value) == "" {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}

func validateRequiredLabels(labels map[string]string) error {
	missing := MissingRequiredLabels(labels)
	if len(missing) > 0 {
		return fmt.Errorf("container spec missing required labels: %s", strings.Join(missing, ","))
	}
	return nil
}

func validatePortBindings(profile SpecProfile, spec ContainerSpec) error {
	allow := make(map[int]struct{}, len(spec.AllowHostPorts))
	for _, port := range spec.AllowHostPorts {
		allow[port] = struct{}{}
	}
	for _, binding := range spec.Ports {
		if binding.HostPort < 0 || binding.HostPort > 65535 {
			return fmt.Errorf("container spec host port %d is out of range", binding.HostPort)
		}
		if binding.ContainerPort <= 0 || binding.ContainerPort > 65535 {
			return fmt.Errorf("container spec container port %d is out of range", binding.ContainerPort)
		}
		if profile != SpecProfileDefault {
			continue
		}
		if binding.HostPort > 0 && binding.HostPort < PrivilegedHostPortFloor {
			if _, ok := allow[binding.HostPort]; !ok {
				return fmt.Errorf("container spec host port %d is below %d and not in AllowHostPorts", binding.HostPort, PrivilegedHostPortFloor)
			}
		}
	}
	return nil
}

func normalizeProfile(profile SpecProfile) SpecProfile {
	switch profile {
	case SpecProfilePermitted:
		return SpecProfilePermitted
	default:
		return SpecProfileDefault
	}
}

func containsNormalized(values []string, want string) bool {
	for _, value := range values {
		if normalizeCapability(value) == want {
			return true
		}
	}
	return false
}

func normalizeCapability(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func normalizedSpec(spec ContainerSpec) ContainerSpec {
	spec.Command = append([]string(nil), spec.Command...)
	spec.CapAdd = normalizeCapabilityList(spec.CapAdd)
	spec.CapDrop = normalizeCapabilityList(spec.CapDrop)
	spec.Health = NormalizeHealthChecks(spec.Health)
	spec.Volumes = normalizeVolumeMounts(spec.Volumes)
	spec.Env = cloneStringMap(spec.Env)
	spec.Labels = cloneStringMap(spec.Labels)
	return spec
}

func validateVolumeMounts(volumes []VolumeMount) error {
	seen := map[string]struct{}{}
	for _, volume := range volumes {
		volumeID := strings.TrimSpace(volume.VolumeID)
		target := strings.TrimSpace(volume.Target)
		if volumeID == "" {
			return fmt.Errorf("container spec volume id is required")
		}
		if target == "" || !strings.HasPrefix(target, "/") || strings.Contains(target, "..") {
			return fmt.Errorf("container spec volume %s has invalid target %q", volumeID, volume.Target)
		}
		if _, ok := seen[target]; ok {
			return fmt.Errorf("container spec volume target %q is duplicated", target)
		}
		seen[target] = struct{}{}
	}
	return nil
}

func normalizeVolumeMounts(volumes []VolumeMount) []VolumeMount {
	normalized := append([]VolumeMount(nil), volumes...)
	for i := range normalized {
		normalized[i].VolumeID = strings.TrimSpace(normalized[i].VolumeID)
		normalized[i].Target = strings.TrimSpace(normalized[i].Target)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Target != normalized[j].Target {
			return normalized[i].Target < normalized[j].Target
		}
		return normalized[i].VolumeID < normalized[j].VolumeID
	})
	return normalized
}

func normalizeCapabilityList(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		normalized = append(normalized, normalizeCapability(value))
	}
	sort.Strings(normalized)
	return normalized
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
