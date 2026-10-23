// Package doctor defines runtime health checks with stable identifiers.
package doctor

import (
	"context"
	"fmt"
	"strings"
)

// Status is the normalized outcome for a doctor check.
type Status string

const (
	StatusPass        Status = "pass"
	StatusWarn        Status = "warn"
	StatusFail        Status = "fail"
	StatusSkip        Status = "skip"
	StatusConfigError Status = "config_error"
)

// Result captures the executable outcome for a doctor check.
type Result struct {
	ID          string
	Description string
	Severity    string
	DocsURL     string
	Status      Status
	Message     string
	Err         error
}

// SafeFixMetadata describes whether a check has an automated safe fix.
// Phase 0 only exposes the stable JSON placeholder; fixes are wired later.
type SafeFixMetadata struct {
	Available bool   `json:"available"`
	Summary   string `json:"summary,omitempty"`
}

// CheckFunc executes a doctor check.
type CheckFunc func(context.Context) Result

// Check describes a diagnostic validation for an installed Midnight host.
type Check struct {
	ID          string
	Description string
	Severity    string
	DocsURL     string
	SafeFix     SafeFixMetadata
	Run         CheckFunc
}

// Checks returns the Phase 0 doctor registry in stable execution order.
func Checks() []Check {
	return []Check{
		{ID: "DOC-SYSTEMD-001", Description: "Midnight systemd units are installed and loadable", Severity: "fatal", DocsURL: "docs/doctor/checks.md#doc-systemd-001"},
		{ID: "DOC-SERVICE-001", Description: "server, worker, agent, BuildKit, and registry services report healthy state", Severity: "fatal", DocsURL: "docs/doctor/checks.md#doc-service-001"},
		{ID: "DOC-JOURNAL-001", Description: "recent Midnight journal entries are readable", Severity: "warn", DocsURL: "docs/doctor/checks.md#doc-journal-001"},
		{ID: "DOC-REGISTRY-001", Description: "local OCI registry responds on configured loopback address", Severity: "fatal", DocsURL: "docs/doctor/checks.md#doc-registry-001"},
		{ID: "DOC-BUILDKIT-001", Description: "BuildKit socket exists and accepts client connections", Severity: "fatal", DocsURL: "docs/doctor/checks.md#doc-buildkit-001"},
		{ID: "DOC-FIREWALL-001", Description: "firewall profile allows the configured public server port", Severity: "warn", DocsURL: "docs/doctor/checks.md#doc-firewall-001"},
		{ID: "DOC-DISK-001", Description: "host data disk is below the doctor critical pressure threshold", Severity: "warn", DocsURL: "docs/doctor/checks.md#doc-disk-001"},
		{ID: "DOC-MEMORY-001", Description: "host memory headroom is above the doctor warning threshold", Severity: "warn", DocsURL: "docs/doctor/checks.md#doc-memory-001"},
		{ID: "DOC-TIMESYNC-001", Description: "host clock drift is below the doctor critical threshold", Severity: "warn", DocsURL: "docs/doctor/checks.md#doc-timesync-001"},
	}
}

// ValidateChecks rejects empty and duplicate doctor check IDs before execution.
func ValidateChecks(checks []Check) error {
	seen := make(map[string]struct{}, len(checks))
	for index, check := range checks {
		id := strings.TrimSpace(check.ID)
		if id == "" {
			return fmt.Errorf("doctor check at index %d has empty ID", index)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate doctor check ID %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// RunChecks validates and executes doctor checks in registry order.
func RunChecks(ctx context.Context, checks []Check) ([]Result, error) {
	if err := ValidateChecks(checks); err != nil {
		return nil, err
	}

	results := make([]Result, 0, len(checks))
	for _, check := range checks {
		result := Result{ID: check.ID, Description: check.Description, Severity: check.Severity, DocsURL: check.DocsURL}
		if check.Run == nil {
			result.Status = StatusConfigError
			result.Message = "check has no runner"
			results = append(results, result)
			continue
		}

		result = check.Run(ctx)
		if result.ID == "" {
			result.ID = check.ID
		}
		if result.Description == "" {
			result.Description = check.Description
		}
		if result.Severity == "" {
			result.Severity = check.Severity
		}
		if result.DocsURL == "" {
			result.DocsURL = check.DocsURL
		}
		if result.Status == "" {
			result.Status = StatusFail
			result.Err = fmt.Errorf("check %s returned empty status", check.ID)
		}
		results = append(results, result)
	}
	return results, nil
}
