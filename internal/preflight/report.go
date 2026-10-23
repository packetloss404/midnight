package preflight

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ReportSeverity is the process-level severity derived from check results.
type ReportSeverity string

const (
	ReportSeverityHealthy     ReportSeverity = "healthy"
	ReportSeverityWarn        ReportSeverity = "warn"
	ReportSeverityFail        ReportSeverity = "fail"
	ReportSeverityConfigError ReportSeverity = "config_error"
)

// FileInputs points preflight checks at explicit fixture or probe output files.
// Empty paths are skipped so the default runner never inspects the live host.
type FileInputs struct {
	CgroupMountInfoPath string
	DockerVersionPath   string
	DaemonJSONPath      string
}

// Report is a JSON-serializable aggregate preflight result.
type Report struct {
	Status   Status         `json:"status"`
	Severity ReportSeverity `json:"severity"`
	ExitCode int            `json:"exit_code"`
	Checks   []ReportCheck  `json:"checks"`
}

// ReportCheck is the stable public shape for one preflight check result.
type ReportCheck struct {
	ID     string `json:"id"`
	Status Status `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// RunFileBacked executes only explicit file-backed checks and returns a JSON-ready report.
func RunFileBacked(ctx context.Context, inputs FileInputs) Report {
	return NewReport(RunChecks(ctx, FileBackedChecks(inputs)))
}

// RunSourceBacked executes checks against an injected source and returns a JSON-ready report.
func RunSourceBacked(ctx context.Context, source HostSource) Report {
	return NewReport(RunChecks(ctx, SourceBackedChecks(source)))
}

// LoadFixtureBundle validates explicit fixture paths and returns a preflight report.
func LoadFixtureBundle(ctx context.Context, inputs FileInputs) Report {
	if err := ValidateFixtureBundle(inputs); err != nil {
		return NewConfigErrorReport(err)
	}
	return RunFileBacked(ctx, inputs)
}

// ValidateFixtureBundle rejects missing or unreadable fixture paths before checks run.
func ValidateFixtureBundle(inputs FileInputs) error {
	paths := map[string]string{
		"cgroup mountinfo": inputs.CgroupMountInfoPath,
		"Docker version":   inputs.DockerVersionPath,
		"daemon.json":      inputs.DaemonJSONPath,
	}
	for name, path := range paths {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("%s fixture path is required", name)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("%s fixture path %q: %w", name, path, err)
		}
		if info.IsDir() {
			return fmt.Errorf("%s fixture path %q is a directory", name, path)
		}
	}
	return nil
}

// FileBackedChecks builds fixture-backed checks without reading any live host paths by default.
func FileBackedChecks(inputs FileInputs) []RunnableCheck {
	return SourceBackedChecks(HostSource{
		Reader: OSFileReader{},
		Paths: HostPaths{
			MountInfo:        inputs.CgroupMountInfoPath,
			DockerAPIVersion: inputs.DockerVersionPath,
			DockerDaemonJSON: inputs.DaemonJSONPath,
		},
	})
}

// SourceBackedChecks builds checks from injected host fact readers.
func SourceBackedChecks(source HostSource) []RunnableCheck {
	return []RunnableCheck{
		{
			ID:          "PRE-CGROUP-001",
			Description: "cgroup v2 mount is present",
			Severity:    "fatal",
			Run: func(ctx context.Context) Result {
				return CheckCgroupV2(ctx, source)
			},
		},
		{
			ID:          "PRE-DOCKER-API-001",
			Description: "Docker API version supports Phase 0",
			Severity:    "fatal",
			Run: func(ctx context.Context) Result {
				return CheckDockerAPI(ctx, source)
			},
		},
		{
			ID:          "PRE-DOCKER-DAEMON-001",
			Description: "Docker daemon configuration is readable and mergeable",
			Severity:    "warn",
			Run: func(context.Context) Result {
				return CheckDockerDaemonJSONBaseline(source)
			},
		},
	}
}

// CheckCgroupV2 checks injected mountinfo text or stat output for cgroup v2.
func CheckCgroupV2(ctx context.Context, source HostSource) Result {
	if strings.TrimSpace(source.Paths.MountInfo) == "" {
		if source.Runner == nil {
			return Result{Status: StatusSkip, Message: "cgroup mountinfo path or stat command runner not provided"}
		}
		ok, err := HasCgroupV2FSTypeFromSource(ctx, source)
		if err != nil {
			return Result{Status: StatusFail, Err: err}
		}
		if !ok {
			return Result{Status: StatusFail, Message: "cgroup v2 filesystem type not found"}
		}
		return Result{Status: StatusPass, Message: "cgroup v2 filesystem type found"}
	}
	ok, err := HasCgroupV2FromSource(source)
	if err != nil {
		return Result{Status: StatusFail, Err: err}
	}
	if !ok {
		return Result{Status: StatusFail, Message: "cgroup v2 mount not found"}
	}
	return Result{Status: StatusPass, Message: "cgroup v2 mount found"}
}

// CheckDockerAPI checks injected Docker version text for minimum API support.
func CheckDockerAPI(ctx context.Context, source HostSource) Result {
	if strings.TrimSpace(source.Paths.DockerAPIVersion) == "" && source.Runner == nil {
		return Result{Status: StatusSkip, Message: "Docker version path or command runner not provided"}
	}
	version, err := DockerAPIVersionFromSourceContext(ctx, source)
	if err != nil {
		return Result{Status: StatusFail, Err: err}
	}
	ok, err := DockerAPISupportsPhase0(version)
	if err != nil {
		return Result{Status: StatusFail, Err: err}
	}
	if !ok {
		return Result{Status: StatusFail, Message: fmt.Sprintf("Docker API %s is below required %s", version, minimumDockerAPIVersion)}
	}
	return Result{Status: StatusPass, Message: fmt.Sprintf("Docker API %s satisfies required %s", version, minimumDockerAPIVersion)}
}

// CheckDockerDaemonJSONBaseline checks whether injected daemon.json can accept the Midnight baseline.
func CheckDockerDaemonJSONBaseline(source HostSource) Result {
	if strings.TrimSpace(source.Paths.DockerDaemonJSON) == "" {
		return Result{Status: StatusSkip, Message: "daemon.json path not provided"}
	}
	result, err := MergeDockerDaemonBaselineFromSource(source)
	if err != nil {
		return Result{Status: StatusWarn, Err: err}
	}
	if result.Changed {
		return Result{Status: StatusWarn, Message: "daemon.json requires baseline merge"}
	}
	return Result{Status: StatusPass, Message: "daemon.json already satisfies baseline"}
}

// NewReport converts internal results to a JSON-serializable aggregate shape.
func NewReport(results []Result) Report {
	report := Report{Status: StatusPass, Checks: make([]ReportCheck, 0, len(results))}
	for _, result := range results {
		report.Checks = append(report.Checks, ReportCheck{ID: result.ID, Status: result.Status, Reason: resultReason(result)})
		report.Status = aggregateStatus(report.Status, result.Status)
	}
	if len(results) == 0 {
		report.Status = StatusSkip
	}
	report.Severity = SeverityForStatus(report.Status)
	report.ExitCode = ExitCodeForStatus(report.Status)
	return report
}

// CombineReports merges explicit report sources without running additional checks.
func CombineReports(reports ...Report) Report {
	combined := Report{Status: StatusPass}
	for _, report := range reports {
		combined.Checks = append(combined.Checks, report.Checks...)
		combined.Status = aggregateStatus(combined.Status, report.Status)
	}
	if len(reports) == 0 {
		combined.Status = StatusSkip
	}
	combined.Severity = SeverityForStatus(combined.Status)
	combined.ExitCode = ExitCodeForStatus(combined.Status)
	return combined
}

// NewConfigErrorReport builds a report for invalid caller-supplied configuration.
func NewConfigErrorReport(err error) Report {
	return NewReport([]Result{{ID: "PRE-CONFIG-001", Status: StatusConfigError, Err: err}})
}

// MarshalReportJSON emits the stable JSON representation used by installer callers.
func MarshalReportJSON(report Report) ([]byte, error) {
	return json.Marshal(report)
}

// SeverityForStatus maps an aggregate status to process-level severity.
func SeverityForStatus(status Status) ReportSeverity {
	switch status {
	case StatusConfigError:
		return ReportSeverityConfigError
	case StatusFail:
		return ReportSeverityFail
	case StatusWarn:
		return ReportSeverityWarn
	default:
		return ReportSeverityHealthy
	}
}

// ExitCodeForStatus maps aggregate status to the installer/doctor contract.
func ExitCodeForStatus(status Status) int {
	switch status {
	case StatusConfigError:
		return 3
	case StatusFail:
		return 2
	case StatusWarn:
		return 1
	default:
		return 0
	}
}

func resultReason(result Result) string {
	if result.Message != "" {
		return result.Message
	}
	if result.Err != nil {
		return result.Err.Error()
	}
	return ""
}

func aggregateStatus(current, next Status) Status {
	if current == StatusConfigError || next == StatusConfigError {
		return StatusConfigError
	}
	if current == StatusFail || next == StatusFail {
		return StatusFail
	}
	if current == StatusWarn || next == StatusWarn {
		return StatusWarn
	}
	if current == StatusSkip || next == StatusSkip {
		return StatusSkip
	}
	return StatusPass
}
