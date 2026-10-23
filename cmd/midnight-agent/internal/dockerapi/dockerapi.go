// Package dockerapi records Docker Engine API metadata without importing Docker SDKs.
// It defines deterministic names, labels, API-version constants, and injected
// seams for future live adapters. This package does not create clients, open
// sockets, or mutate local Docker state directly.
package dockerapi

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

const (
	// MinEngineAPIVersion is the lowest Docker Engine API version this agent will target.
	MinEngineAPIVersion = "1.43"

	ContainerNamePrefix = "midnight"
	LabelNamespace      = "dev.midnight"

	// EnvLiveDockerAPIOptIn is the explicit operator gate required before any
	// injected live Docker Engine source may be called by this package.
	EnvLiveDockerAPIOptIn = "MIDNIGHT_AGENT_LIVE_DOCKER_API" // not-a-failure-code

	LiveReadinessReady   = "ready"
	LiveReadinessBlocked = "blocked"

	LiveIssueOptInRequired     = "live_docker_api_opt_in_required"
	LiveIssueSourceRequired    = "live_docker_api_source_required"
	LiveIssueProbeFailed       = "live_docker_api_probe_failed"
	LiveIssueEngineUnsupported = "docker_engine_unsupported"
)

var nonNameChar = regexp.MustCompile(`[^a-z0-9_.-]+`)

// LiveDockerAPIGate records the explicit operator decision to allow injected
// live Docker Engine calls. The zero value is safely disabled.
type LiveDockerAPIGate struct {
	Enabled bool   `json:"enabled"`
	Source  string `json:"source,omitempty"`
}

// EngineVersion is the read-only subset of Docker Engine /version data needed
// for the runtime readiness handshake.
type EngineVersion struct {
	APIVersion    string `json:"api_version"`
	MinAPIVersion string `json:"min_api_version,omitempty"`
	Version       string `json:"version,omitempty"`
}

// EngineVersionSource is the injected capability seam for a future Docker
// Engine API client. Implementations may open sockets; this package only calls
// them after LiveDockerAPIGate has been explicitly enabled.
type EngineVersionSource interface {
	EngineVersion(context.Context) (EngineVersion, error)
}

// EngineVersionSourceFunc adapts a function into an EngineVersionSource.
type EngineVersionSourceFunc func(context.Context) (EngineVersion, error)

// EngineVersion returns f(ctx).
func (f EngineVersionSourceFunc) EngineVersion(ctx context.Context) (EngineVersion, error) {
	return f(ctx)
}

// LiveReadinessEvidence captures the result of the guarded live Engine API
// readiness handshake without storing credentials or socket paths.
type LiveReadinessEvidence struct {
	Status         string            `json:"status"`
	IssueCode      string            `json:"issue_code,omitempty"`
	Gate           LiveDockerAPIGate `json:"gate"`
	RequiredAPI    string            `json:"required_api"`
	Observed       EngineVersion     `json:"observed,omitempty"`
	APIFloorOK     bool              `json:"api_floor_ok"`
	ProbeAttempted bool              `json:"probe_attempted"`
	Failure        string            `json:"failure,omitempty"`
}

// ContainerName returns a stable Docker container name for a deployment revision.
func ContainerName(deploymentID, revision string) string {
	deploymentID = namePart(deploymentID, "deployment")
	revision = namePart(revision, "revision")
	return ContainerNamePrefix + "-" + deploymentID + "-" + revision
}

// ContainerLabels returns stable ownership labels for future Docker operations.
func ContainerLabels(agentID, deploymentID, revision string) map[string]string {
	return map[string]string{
		LabelNamespace + "/managed-by":    "midnight-agent",
		LabelNamespace + "/agent-id":      strings.TrimSpace(agentID),
		LabelNamespace + "/deployment-id": strings.TrimSpace(deploymentID),
		LabelNamespace + "/revision":      strings.TrimSpace(revision),
	}
}

// LiveDockerAPIGateFromEnv builds a live Docker API gate from an injected getenv
// function. Only the exact value "true" enables live calls.
func LiveDockerAPIGateFromEnv(getenv func(string) string) LiveDockerAPIGate {
	if getenv == nil {
		return LiveDockerAPIGate{}
	}
	return LiveDockerAPIGate{
		Enabled: strings.TrimSpace(getenv(EnvLiveDockerAPIOptIn)) == "true",
		Source:  EnvLiveDockerAPIOptIn,
	}
}

// ProbeLiveEngineReadiness calls the injected EngineVersionSource only when the
// live Docker API gate is enabled. It is the bridge between offline metadata and
// future live Docker Engine adapter readiness evidence.
func ProbeLiveEngineReadiness(ctx context.Context, gate LiveDockerAPIGate, source EngineVersionSource) (LiveReadinessEvidence, error) {
	evidence := LiveReadinessEvidence{
		Status:      LiveReadinessBlocked,
		Gate:        gate,
		RequiredAPI: MinEngineAPIVersion,
	}
	if !gate.Enabled {
		evidence.IssueCode = LiveIssueOptInRequired
		return evidence, fmt.Errorf("live Docker API probe requires %s=true", EnvLiveDockerAPIOptIn)
	}
	if source == nil {
		evidence.IssueCode = LiveIssueSourceRequired
		return evidence, fmt.Errorf("live Docker API version source is required")
	}

	evidence.ProbeAttempted = true
	observed, err := source.EngineVersion(ctx)
	evidence.Observed = observed
	if err != nil {
		evidence.IssueCode = LiveIssueProbeFailed
		evidence.Failure = err.Error()
		return evidence, fmt.Errorf("probe live Docker API version: %w", err)
	}
	apiVersion := strings.TrimSpace(observed.APIVersion)
	ok, err := EngineAPIAtLeast(apiVersion, MinEngineAPIVersion)
	if err != nil {
		evidence.IssueCode = LiveIssueEngineUnsupported
		evidence.Failure = err.Error()
		return evidence, fmt.Errorf("validate Docker Engine API version: %w", err)
	}
	evidence.APIFloorOK = ok
	if !ok {
		evidence.IssueCode = LiveIssueEngineUnsupported
		return evidence, fmt.Errorf("Docker Engine API %q is below required %q", apiVersion, MinEngineAPIVersion)
	}
	evidence.Status = LiveReadinessReady
	return evidence, nil
}

// EngineAPIAtLeast reports whether observed is greater than or equal to
// required. Both inputs must be Docker Engine API major.minor versions.
func EngineAPIAtLeast(observed, required string) (bool, error) {
	oMajor, oMinor, err := parseEngineAPIVersion(observed)
	if err != nil {
		return false, fmt.Errorf("parse observed docker api version: %w", err)
	}
	rMajor, rMinor, err := parseEngineAPIVersion(required)
	if err != nil {
		return false, fmt.Errorf("parse required docker api version: %w", err)
	}
	if oMajor != rMajor {
		return oMajor > rMajor, nil
	}
	return oMinor >= rMinor, nil
}

func namePart(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = nonNameChar.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-_.")
	if value == "" {
		return fallback
	}
	return value
}

func parseEngineAPIVersion(version string) (int, int, error) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" {
		return 0, 0, fmt.Errorf("docker api version is empty")
	}
	parts := strings.Split(version, ".")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("docker api version %q must be major.minor", version)
	}
	major, err := parseNonNegativeInt(parts[0], "major")
	if err != nil {
		return 0, 0, err
	}
	minor, err := parseNonNegativeInt(parts[1], "minor")
	if err != nil {
		return 0, 0, err
	}
	return major, minor, nil
}

func parseNonNegativeInt(value, name string) (int, error) {
	if value == "" {
		return 0, fmt.Errorf("docker api %s is empty", name)
	}
	n := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("docker api %s %q is not numeric", name, value)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}
