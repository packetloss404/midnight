// Package preflight defines installer preflight checks with stable identifiers.
package preflight

// Check describes a pre-install validation that can block installation.
type Check struct {
	ID          string
	Description string
	Severity    string
}

// Checks returns the Phase 0 preflight registry in stable execution order.
func Checks() []Check {
	return []Check{
		{ID: "PRE-SYSTEMD-001", Description: "systemd is present and usable", Severity: "fatal"},
		{ID: "PRE-USER-001", Description: "midnight system user and group can be created", Severity: "fatal"},
		{ID: "PRE-DISK-001", Description: "required state and log directories have sufficient free space", Severity: "fatal"},
		{ID: "PRE-PORT-001", Description: "public server port is available or explicitly overridden", Severity: "fatal"},
		{ID: "PRE-DOCKER-001", Description: "Docker daemon configuration is readable and mergeable", Severity: "warn"},
		{ID: "PRE-BUILDKIT-001", Description: "BuildKit binary or managed package dependency is available", Severity: "fatal"},
	}
}
