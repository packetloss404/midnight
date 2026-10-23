package runtime

import (
	"strings"
	"testing"
)

func TestValidateContainerSpecAcceptsPhase0Defaults(t *testing.T) {
	spec := hardenedSpec()
	spec.CapAdd = []string{" net_bind_service ", "setuid"}

	if err := ValidateContainerSpec(spec); err != nil {
		t.Fatalf("ValidateContainerSpec() error = %v", err)
	}
}

func TestValidateContainerSpecRequiresDropAllCapabilities(t *testing.T) {
	spec := hardenedSpec()
	spec.CapDrop = []string{"NET_RAW"}

	err := ValidateContainerSpec(spec)
	if err == nil || !strings.Contains(err.Error(), "drop all") {
		t.Fatalf("ValidateContainerSpec() error = %v", err)
	}
}

func TestValidateContainerSpecRejectsCapabilityOutsideAllowlist(t *testing.T) {
	spec := hardenedSpec()
	spec.CapAdd = []string{"SYS_ADMIN"}

	err := ValidateContainerSpec(spec)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("ValidateContainerSpec() error = %v", err)
	}
}

func TestValidateContainerSpecRejectsHostNamespaces(t *testing.T) {
	tests := []struct {
		name string
		edit func(*ContainerSpec)
		want string
	}{
		{name: "network", edit: func(s *ContainerSpec) { s.HostNetwork = true }, want: "host network"},
		{name: "pid", edit: func(s *ContainerSpec) { s.HostPID = true }, want: "host pid"},
		{name: "ipc", edit: func(s *ContainerSpec) { s.HostIPC = true }, want: "host ipc"},
		{name: "privileged", edit: func(s *ContainerSpec) { s.Privileged = true }, want: "privileged"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := hardenedSpec()
			tt.edit(&spec)

			err := ValidateContainerSpec(spec)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateContainerSpec() error = %v", err)
			}
		})
	}
}

func TestValidateContainerSpecRequiresInit(t *testing.T) {
	spec := hardenedSpec()
	spec.Init = false

	err := ValidateContainerSpec(spec)
	if err == nil || !strings.Contains(err.Error(), "enable init") {
		t.Fatalf("ValidateContainerSpec() error = %v", err)
	}
}

func TestValidateContainerSpecRequiresAllMidnightLabels(t *testing.T) {
	spec := hardenedSpec()
	delete(spec.Labels, "midnight.deployment")

	err := ValidateContainerSpec(spec)
	if err == nil || !strings.Contains(err.Error(), "midnight.deployment") {
		t.Fatalf("expected missing label error, got %v", err)
	}
}

func TestMissingRequiredLabelsReturnsSortedKeys(t *testing.T) {
	missing := MissingRequiredLabels(map[string]string{"midnight.project": "p"})
	want := []string{"midnight.deployment", "midnight.instance", "midnight.service"}
	if len(missing) != len(want) {
		t.Fatalf("MissingRequiredLabels = %v want %v", missing, want)
	}
	for i := range missing {
		if missing[i] != want[i] {
			t.Fatalf("MissingRequiredLabels[%d] = %s want %s", i, missing[i], want[i])
		}
	}
}

func TestValidateContainerSpecRejectsPrivilegedHostPort(t *testing.T) {
	spec := hardenedSpec()
	spec.Ports = []PortBinding{{HostPort: 80, ContainerPort: 8080}}

	err := ValidateContainerSpec(spec)
	if err == nil || !strings.Contains(err.Error(), "1024") {
		t.Fatalf("expected privileged host port error, got %v", err)
	}
}

func TestValidateContainerSpecAllowsExplicitPrivilegedHostPort(t *testing.T) {
	spec := hardenedSpec()
	spec.Ports = []PortBinding{{HostPort: 80, ContainerPort: 8080}}
	spec.AllowHostPorts = []int{80}

	if err := ValidateContainerSpec(spec); err != nil {
		t.Fatalf("ValidateContainerSpec error: %v", err)
	}
}

func TestValidateContainerSpecPermittedProfileSkipsLabelGate(t *testing.T) {
	spec := hardenedSpec()
	spec.Labels = nil
	spec.Profile = SpecProfilePermitted

	if err := ValidateContainerSpec(spec); err != nil {
		t.Fatalf("permitted profile should skip label gate, got %v", err)
	}
}

func TestValidateContainerSpecAcceptsVolumeMounts(t *testing.T) {
	spec := hardenedSpec()
	spec.Volumes = []VolumeMount{{VolumeID: "vol_data", Target: "/data"}}

	if err := ValidateContainerSpec(spec); err != nil {
		t.Fatalf("ValidateContainerSpec error: %v", err)
	}
}

func TestValidateContainerSpecRejectsInvalidVolumeMounts(t *testing.T) {
	tests := []struct {
		name   string
		volume VolumeMount
		want   string
	}{
		{name: "missing id", volume: VolumeMount{Target: "/data"}, want: "volume id"},
		{name: "relative target", volume: VolumeMount{VolumeID: "vol_data", Target: "data"}, want: "invalid target"},
		{name: "parent target", volume: VolumeMount{VolumeID: "vol_data", Target: "/data/../secret"}, want: "invalid target"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := hardenedSpec()
			spec.Volumes = []VolumeMount{tt.volume}

			err := ValidateContainerSpec(spec)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateContainerSpec() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateContainerSpecRejectsDuplicateVolumeTargets(t *testing.T) {
	spec := hardenedSpec()
	spec.Volumes = []VolumeMount{
		{VolumeID: "vol_data", Target: "/data"},
		{VolumeID: "vol_cache", Target: " /data "},
	}

	err := ValidateContainerSpec(spec)
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("ValidateContainerSpec() error = %v", err)
	}
}

func hardenedSpec() ContainerSpec {
	return ContainerSpec{
		Image:   "example/app:rev1",
		CapDrop: []string{"ALL"},
		Init:    true,
		Labels: map[string]string{
			"midnight.project":    "prj_demo",
			"midnight.service":    "svc_demo",
			"midnight.deployment": "dep_demo",
			"midnight.instance":   "inst_demo",
		},
	}
}
