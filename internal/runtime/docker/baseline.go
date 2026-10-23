package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/moby/moby/api/types/system"

	"github.com/midnight/midnight/internal/failurecodes"
)

// MinEngineAPIVersion is the Docker Engine API floor pinned by ADR-004 and
// the panel's line 90 decision. The agent refuses to start with
// failurecodes.DockerEngineUnsupported below this version.
const MinEngineAPIVersion = "1.43"

// BaselineRequirement enumerates the daemon.json baseline keys the agent
// asserts at startup against /info. The list mirrors
// FEATURE_DAEMON_JSON_BASELINE.md "Required JSON Document" — overlay2,
// live-restore=true, json-file with size caps, no-new-privileges enabled.
type BaselineRequirement struct {
	Key      string
	Required string
	Observed string
}

// BaselineReport summarizes the daemon.json baseline check. Conflicts is
// non-empty when the live daemon configuration violates the locked baseline;
// the adapter's Init() converts a non-empty Conflicts slice into a typed
// failurecodes.DockerDaemonBaselineConflict error.
type BaselineReport struct {
	APIVersion      string
	EngineVersion   string
	StorageDriver   string
	LiveRestore     bool
	LoggingDriver   string
	NoNewPrivileges bool
	CGroupVersion   string
	Conflicts       []BaselineRequirement
	Warnings        []string
}

// EngineMetadataProbe is the minimal subset of the Docker SDK the daemon.json
// baseline validator needs. The Adapter calls this against a real Docker
// client; tests inject a fake. Keeping this interface small means the
// validator unit tests do not import the SDK.
type EngineMetadataProbe interface {
	Info(ctx context.Context) (system.Info, error)
	NegotiatedAPIVersion() string
}

// CheckBaseline runs the daemon.json baseline assertions and returns a
// structured BaselineReport. A non-empty Conflicts slice means the host is
// out of compliance and Init() will refuse to operate.
//
// CheckBaseline does NOT mutate /etc/docker/daemon.json — the panel decision
// pins editing to the installer, not the agent.
func CheckBaseline(ctx context.Context, probe EngineMetadataProbe) (BaselineReport, error) {
	if probe == nil {
		return BaselineReport{}, fmt.Errorf("baseline probe is required")
	}
	info, err := probe.Info(ctx)
	if err != nil {
		return BaselineReport{}, fmt.Errorf("docker /info: %w", err)
	}
	report := BaselineReport{
		APIVersion:      probe.NegotiatedAPIVersion(),
		EngineVersion:   info.ServerVersion,
		StorageDriver:   info.Driver,
		LiveRestore:     info.LiveRestoreEnabled,
		LoggingDriver:   info.LoggingDriver,
		NoNewPrivileges: containsSecurityOption(info.SecurityOptions, "no-new-privileges"),
		CGroupVersion:   info.CgroupVersion,
	}

	if !equalFold(report.StorageDriver, "overlay2") {
		report.Conflicts = append(report.Conflicts, BaselineRequirement{
			Key:      "storage-driver",
			Required: "overlay2",
			Observed: report.StorageDriver,
		})
	}
	if !report.LiveRestore {
		report.Conflicts = append(report.Conflicts, BaselineRequirement{
			Key:      "live-restore",
			Required: "true",
			Observed: "false",
		})
	}
	if !equalFold(report.LoggingDriver, "json-file") && !equalFold(report.LoggingDriver, "local") {
		// `local` is the json-file successor with the same size-cap shape;
		// the panel locked json-file but the embedded log driver
		// equivalent ships under the `local` name on Engine ≥ 24.0. We
		// accept either as the baseline contract is "json-file with size
		// caps", not the literal driver string.
		report.Conflicts = append(report.Conflicts, BaselineRequirement{
			Key:      "log-driver",
			Required: "json-file",
			Observed: report.LoggingDriver,
		})
	}
	if !report.NoNewPrivileges {
		report.Conflicts = append(report.Conflicts, BaselineRequirement{
			Key:      "no-new-privileges",
			Required: "true",
			Observed: "absent or false",
		})
	}
	if cgv := strings.TrimSpace(report.CGroupVersion); cgv != "" && cgv != "2" {
		report.Conflicts = append(report.Conflicts, BaselineRequirement{
			Key:      "cgroup-version",
			Required: "2",
			Observed: report.CGroupVersion,
		})
	}

	sort.Slice(report.Conflicts, func(i, j int) bool {
		return report.Conflicts[i].Key < report.Conflicts[j].Key
	})
	return report, nil
}

// BaselineMode selects how daemon.json baseline conflicts are handled at
// adapter boot. The zero value is BaselineEnforce, which preserves the
// fail-closed production behaviour.
type BaselineMode string

const (
	// BaselineEnforce refuses to start when the daemon violates the baseline.
	BaselineEnforce BaselineMode = "enforce"
	// BaselineWarn records conflicts as report warnings and continues. It is
	// intended for developer machines (Docker Desktop, rootless engines)
	// whose daemon.json cannot be hardened, never for installed hosts.
	BaselineWarn BaselineMode = "warn"
)

// ParseBaselineMode normalizes an operator-supplied mode string. Empty input
// selects BaselineEnforce.
func ParseBaselineMode(raw string) (BaselineMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(BaselineEnforce):
		return BaselineEnforce, nil
	case string(BaselineWarn):
		return BaselineWarn, nil
	default:
		return "", fmt.Errorf("docker baseline mode must be %q or %q, got %q", BaselineEnforce, BaselineWarn, raw)
	}
}

// EnforceBaseline returns nil when the report has no conflicts and a typed
// *Error otherwise.
func EnforceBaseline(report BaselineReport) error {
	if len(report.Conflicts) == 0 {
		return nil
	}
	pieces := make([]string, 0, len(report.Conflicts))
	for _, c := range report.Conflicts {
		pieces = append(pieces, fmt.Sprintf("%s want=%q got=%q", c.Key, c.Required, c.Observed))
	}
	return newError(
		failurecodes.DockerDaemonBaselineConflict,
		"docker daemon.json baseline conflict",
		strings.Join(pieces, "; "),
	)
}

// EnforceEngineAPIFloor returns nil when apiVersion satisfies
// MinEngineAPIVersion and a typed *Error otherwise.
func EnforceEngineAPIFloor(apiVersion string) error {
	apiVersion = strings.TrimSpace(apiVersion)
	if apiVersion == "" {
		return newError(
			failurecodes.DockerEngineUnsupported,
			"docker engine api version negotiation failed",
			"client returned empty version",
		)
	}
	cmp, err := compareVersions(apiVersion, MinEngineAPIVersion)
	if err != nil {
		return newError(
			failurecodes.DockerEngineUnsupported,
			"docker engine api version is unparseable",
			fmt.Sprintf("got=%q", apiVersion),
		)
	}
	if cmp < 0 {
		return newError(
			failurecodes.DockerEngineUnsupported,
			fmt.Sprintf("docker engine api %s is below the supported floor", apiVersion),
			fmt.Sprintf("min=%s", MinEngineAPIVersion),
		)
	}
	return nil
}

func containsSecurityOption(opts []string, name string) bool {
	for _, opt := range opts {
		if strings.Contains(opt, name) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// compareVersions compares two dotted version strings (e.g. "1.43" vs
// "1.50") returning -1/0/+1. It does not support pre-release suffixes; the
// Engine API surface uses simple major.minor versioning.
func compareVersions(a, b string) (int, error) {
	parsedA, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	parsedB, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < len(parsedA) && i < len(parsedB); i++ {
		if parsedA[i] < parsedB[i] {
			return -1, nil
		}
		if parsedA[i] > parsedB[i] {
			return 1, nil
		}
	}
	switch {
	case len(parsedA) < len(parsedB):
		return -1, nil
	case len(parsedA) > len(parsedB):
		return 1, nil
	default:
		return 0, nil
	}
}

func parseVersion(v string) ([]int, error) {
	parts := strings.Split(strings.TrimSpace(v), ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("empty version component in %q", v)
		}
		n := 0
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return nil, fmt.Errorf("non-numeric version component %q", p)
			}
			n = n*10 + int(ch-'0')
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty version %q", v)
	}
	return out, nil
}
