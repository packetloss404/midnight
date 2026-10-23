package preflight

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunFileBackedSkipsMissingInputs(t *testing.T) {
	t.Parallel()

	report := RunFileBacked(context.Background(), FileInputs{})
	if report.Status != StatusSkip {
		t.Fatalf("Status = %q, want %q", report.Status, StatusSkip)
	}
	if len(report.Checks) != 3 {
		t.Fatalf("len(Checks) = %d, want 3", len(report.Checks))
	}
	for _, check := range report.Checks {
		if check.Status != StatusSkip || check.Reason == "" {
			t.Fatalf("check = %+v, want skipped with reason", check)
		}
	}
}

func TestRunFileBackedUsesExplicitFixturePaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mountinfo := filepath.Join(dir, "mountinfo")
	dockerVersion := filepath.Join(dir, "docker-version.txt")
	daemonJSON := filepath.Join(dir, "daemon.json")
	writeFixture(t, mountinfo, "27 23 0:26 / /sys/fs/cgroup rw - cgroup2 cgroup rw\n")
	writeFixture(t, dockerVersion, "Client:\n API version: 1.42\nServer:\n API version: 1.44\n")
	writeFixture(t, daemonJSON, DockerDaemonBaselineJSON)

	report := RunFileBacked(context.Background(), FileInputs{
		CgroupMountInfoPath: mountinfo,
		DockerVersionPath:   dockerVersion,
		DaemonJSONPath:      daemonJSON,
	})
	if report.Status != StatusPass {
		t.Fatalf("Status = %q, want %q; report = %+v", report.Status, StatusPass, report)
	}
	if report.Severity != ReportSeverityHealthy || report.ExitCode != 0 {
		t.Fatalf("Severity/ExitCode = %q/%d, want healthy/0", report.Severity, report.ExitCode)
	}
	for _, check := range report.Checks {
		if check.Status != StatusPass {
			t.Fatalf("check = %+v, want pass", check)
		}
	}
	if _, err := MarshalReportJSON(report); err != nil {
		t.Fatalf("MarshalReportJSON(report) error = %v", err)
	}
	if _, err := json.Marshal(report); err != nil {
		t.Fatalf("json.Marshal(report) error = %v", err)
	}
}

func TestRunSourceBackedUsesInjectedReader(t *testing.T) {
	t.Parallel()

	report := RunSourceBacked(context.Background(), HostSource{
		Reader: fakeFileReader{
			"mountinfo":          []byte("27 23 0:26 / /sys/fs/cgroup rw - cgroup2 cgroup rw\n"),
			"docker-version.txt": []byte("Server:\n API version: 1.44\n"),
			"daemon.json":        []byte(DockerDaemonBaselineJSON),
		},
		Paths: HostPaths{
			MountInfo:        "mountinfo",
			DockerAPIVersion: "docker-version.txt",
			DockerDaemonJSON: "daemon.json",
		},
	})
	if report.Status != StatusPass {
		t.Fatalf("Status = %q, want %q; report = %+v", report.Status, StatusPass, report)
	}
	if len(report.Checks) != 3 {
		t.Fatalf("len(Checks) = %d, want 3", len(report.Checks))
	}
	for _, check := range report.Checks {
		if check.Status != StatusPass {
			t.Fatalf("check = %+v, want pass", check)
		}
	}
}

func TestSourceBackedCheckResults(t *testing.T) {
	t.Parallel()

	source := HostSource{
		Reader: fakeFileReader{
			"mountinfo":          []byte("28 23 0:27 / /sys/fs/cgroup/cpu rw - cgroup cgroup rw,cpu\n"),
			"docker-version.txt": []byte("Server:\n API version: 1.42\n"),
			"daemon.json":        []byte(`{"features":{"buildkit":false}}`),
		},
		Paths: HostPaths{
			MountInfo:        "mountinfo",
			DockerAPIVersion: "docker-version.txt",
			DockerDaemonJSON: "daemon.json",
		},
	}

	checks := []struct {
		name string
		got  Result
		want Status
	}{
		{name: "cgroup v1 only", got: CheckCgroupV2(context.Background(), source), want: StatusFail},
		{name: "Docker API below minimum", got: CheckDockerAPI(context.Background(), source), want: StatusFail},
		{name: "daemon baseline conflict", got: CheckDockerDaemonJSONBaseline(source), want: StatusWarn},
	}
	for _, check := range checks {
		if check.got.Status != check.want {
			t.Fatalf("%s status = %q, want %q; result = %+v", check.name, check.got.Status, check.want, check.got)
		}
	}
}

func TestRunSourceBackedUsesInjectedRunner(t *testing.T) {
	t.Parallel()

	runner := fakeCommandRunner{
		"stat -fc %T /sys/fs/cgroup":                     []byte("cgroup2fs\n"),
		"docker version --format {{.Server.APIVersion}}": []byte("1.44\n"),
	}
	report := RunSourceBacked(context.Background(), HostSource{
		Reader: fakeFileReader{"daemon.json": []byte(DockerDaemonBaselineJSON)},
		Runner: runner,
		Paths:  HostPaths{DockerDaemonJSON: "daemon.json"},
	})
	if report.Status != StatusPass {
		t.Fatalf("Status = %q, want %q; report = %+v", report.Status, StatusPass, report)
	}
	for _, check := range report.Checks {
		if check.Status != StatusPass {
			t.Fatalf("check = %+v, want pass", check)
		}
	}
}

func TestNewReportUsesErrorAsReason(t *testing.T) {
	t.Parallel()

	report := NewReport([]Result{{ID: "PRE-X", Status: StatusFail, Err: os.ErrNotExist}})
	if report.Status != StatusFail {
		t.Fatalf("Status = %q, want %q", report.Status, StatusFail)
	}
	if report.Checks[0].Reason == "" {
		t.Fatal("Reason is empty, want error text")
	}
}

func TestCombineReports(t *testing.T) {
	t.Parallel()

	pass := NewReport([]Result{{ID: "PRE-ONE", Status: StatusPass, Message: "ok"}})
	warn := NewReport([]Result{{ID: "PRE-TWO", Status: StatusWarn, Message: "warn"}})
	combined := CombineReports(pass, warn)
	if combined.Status != StatusWarn {
		t.Fatalf("Status = %q, want %q", combined.Status, StatusWarn)
	}
	if combined.Severity != ReportSeverityWarn || combined.ExitCode != 1 {
		t.Fatalf("Severity/ExitCode = %q/%d, want warn/1", combined.Severity, combined.ExitCode)
	}
	if len(combined.Checks) != 2 || combined.Checks[0].ID != "PRE-ONE" || combined.Checks[1].ID != "PRE-TWO" {
		t.Fatalf("Checks = %+v, want concatenated source checks", combined.Checks)
	}
}

func TestCombineReportsEmpty(t *testing.T) {
	t.Parallel()

	combined := CombineReports()
	if combined.Status != StatusSkip {
		t.Fatalf("Status = %q, want %q", combined.Status, StatusSkip)
	}
	if combined.Severity != ReportSeverityHealthy || combined.ExitCode != 0 {
		t.Fatalf("Severity/ExitCode = %q/%d, want healthy/0", combined.Severity, combined.ExitCode)
	}
}

func TestLoadFixtureBundleReportsConfigError(t *testing.T) {
	t.Parallel()

	report := LoadFixtureBundle(context.Background(), FileInputs{})
	if report.Status != StatusConfigError {
		t.Fatalf("Status = %q, want %q", report.Status, StatusConfigError)
	}
	if report.Severity != ReportSeverityConfigError || report.ExitCode != 3 {
		t.Fatalf("Severity/ExitCode = %q/%d, want config_error/3", report.Severity, report.ExitCode)
	}
	if len(report.Checks) != 1 || report.Checks[0].Reason == "" {
		t.Fatalf("Checks = %+v, want config error reason", report.Checks)
	}
}

func TestExitCodeForStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status Status
		want   int
	}{
		{status: StatusPass, want: 0},
		{status: StatusSkip, want: 0},
		{status: StatusWarn, want: 1},
		{status: StatusFail, want: 2},
		{status: StatusConfigError, want: 3},
	}
	for _, tt := range tests {
		if got := ExitCodeForStatus(tt.status); got != tt.want {
			t.Fatalf("ExitCodeForStatus(%q) = %d, want %d", tt.status, got, tt.want)
		}
	}
}

func writeFixture(t *testing.T, path, text string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

type fakeFileReader map[string][]byte

func (r fakeFileReader) ReadFile(path string) ([]byte, error) {
	data, ok := r[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

type fakeCommandRunner map[string][]byte

func (r fakeCommandRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name
	for _, arg := range args {
		key += " " + arg
	}
	data, ok := r[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}
