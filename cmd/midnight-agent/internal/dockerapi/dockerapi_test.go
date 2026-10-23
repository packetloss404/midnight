package dockerapi

import (
	"context"
	"errors"
	"testing"
)

func TestMinEngineAPIVersion(t *testing.T) {
	if MinEngineAPIVersion != "1.43" {
		t.Fatalf("MinEngineAPIVersion = %q", MinEngineAPIVersion)
	}
}

func TestContainerNameIsDeterministicAndSanitized(t *testing.T) {
	name := ContainerName(" Deploy_01 ", " Rev:2 ")

	if name != "midnight-deploy_01-rev-2" {
		t.Fatalf("name = %q", name)
	}
}

func TestContainerNameUsesFallbacks(t *testing.T) {
	name := ContainerName("...", "")

	if name != "midnight-deployment-revision" {
		t.Fatalf("name = %q", name)
	}
}

func TestContainerLabels(t *testing.T) {
	labels := ContainerLabels(" agent-a ", " deploy-1 ", " rev-1 ")

	if labels[LabelNamespace+"/managed-by"] != "midnight-agent" {
		t.Fatalf("managed-by label = %q", labels[LabelNamespace+"/managed-by"])
	}
	if labels[LabelNamespace+"/agent-id"] != "agent-a" {
		t.Fatalf("agent label = %q", labels[LabelNamespace+"/agent-id"])
	}
	if labels[LabelNamespace+"/deployment-id"] != "deploy-1" {
		t.Fatalf("deployment label = %q", labels[LabelNamespace+"/deployment-id"])
	}
	if labels[LabelNamespace+"/revision"] != "rev-1" {
		t.Fatalf("revision label = %q", labels[LabelNamespace+"/revision"])
	}
}

func TestLiveDockerAPIGateFromEnvRequiresExactTrue(t *testing.T) {
	gate := LiveDockerAPIGateFromEnv(func(name string) string {
		if name != EnvLiveDockerAPIOptIn {
			t.Fatalf("unexpected env name %q", name)
		}
		return "true"
	})
	if !gate.Enabled || gate.Source != EnvLiveDockerAPIOptIn {
		t.Fatalf("gate = %+v", gate)
	}

	gate = LiveDockerAPIGateFromEnv(func(string) string { return "TRUE" })
	if gate.Enabled {
		t.Fatalf("uppercase opt-in must not enable live Docker API")
	}
}

func TestProbeLiveEngineReadinessRequiresOptInAndDoesNotCallSource(t *testing.T) {
	called := false
	evidence, err := ProbeLiveEngineReadiness(context.Background(), LiveDockerAPIGate{}, EngineVersionSourceFunc(func(context.Context) (EngineVersion, error) {
		called = true
		return EngineVersion{APIVersion: "1.45"}, nil
	}))
	if err == nil {
		t.Fatalf("expected opt-in error")
	}
	if called {
		t.Fatalf("source was called without opt-in")
	}
	if evidence.Status != LiveReadinessBlocked || evidence.IssueCode != LiveIssueOptInRequired || evidence.ProbeAttempted {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func TestProbeLiveEngineReadinessRequiresSource(t *testing.T) {
	evidence, err := ProbeLiveEngineReadiness(context.Background(), LiveDockerAPIGate{Enabled: true, Source: "test"}, nil)
	if err == nil {
		t.Fatalf("expected source error")
	}
	if evidence.IssueCode != LiveIssueSourceRequired || evidence.ProbeAttempted {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func TestProbeLiveEngineReadinessAcceptsCompliantVersion(t *testing.T) {
	evidence, err := ProbeLiveEngineReadiness(context.Background(), LiveDockerAPIGate{Enabled: true, Source: "test"}, EngineVersionSourceFunc(func(context.Context) (EngineVersion, error) {
		return EngineVersion{APIVersion: "1.45", Version: "24.0.6"}, nil
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evidence.Status != LiveReadinessReady || !evidence.APIFloorOK || !evidence.ProbeAttempted {
		t.Fatalf("evidence = %+v", evidence)
	}
	if evidence.RequiredAPI != MinEngineAPIVersion || evidence.Observed.APIVersion != "1.45" {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func TestProbeLiveEngineReadinessRejectsUnsupportedVersion(t *testing.T) {
	evidence, err := ProbeLiveEngineReadiness(context.Background(), LiveDockerAPIGate{Enabled: true, Source: "test"}, EngineVersionSourceFunc(func(context.Context) (EngineVersion, error) {
		return EngineVersion{APIVersion: "1.42"}, nil
	}))
	if err == nil {
		t.Fatalf("expected unsupported version error")
	}
	if evidence.Status != LiveReadinessBlocked || evidence.IssueCode != LiveIssueEngineUnsupported || evidence.APIFloorOK {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func TestProbeLiveEngineReadinessRecordsProbeFailure(t *testing.T) {
	probeErr := errors.New("socket unavailable")
	evidence, err := ProbeLiveEngineReadiness(context.Background(), LiveDockerAPIGate{Enabled: true, Source: "test"}, EngineVersionSourceFunc(func(context.Context) (EngineVersion, error) {
		return EngineVersion{}, probeErr
	}))
	if !errors.Is(err, probeErr) {
		t.Fatalf("error = %v, want wrapped %v", err, probeErr)
	}
	if evidence.IssueCode != LiveIssueProbeFailed || evidence.Failure != probeErr.Error() || !evidence.ProbeAttempted {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func TestEngineAPIAtLeastComparesVersions(t *testing.T) {
	cases := []struct {
		observed string
		required string
		want     bool
		wantErr  bool
	}{
		{observed: "1.43", required: "1.43", want: true},
		{observed: "v1.45", required: "1.43", want: true},
		{observed: "1.42", required: "1.43", want: false},
		{observed: "2.0", required: "1.43", want: true},
		{observed: "1.x", required: "1.43", wantErr: true},
	}
	for _, tc := range cases {
		got, err := EngineAPIAtLeast(tc.observed, tc.required)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("EngineAPIAtLeast(%q, %q): expected error", tc.observed, tc.required)
			}
			continue
		}
		if err != nil {
			t.Fatalf("EngineAPIAtLeast(%q, %q): %v", tc.observed, tc.required, err)
		}
		if got != tc.want {
			t.Fatalf("EngineAPIAtLeast(%q, %q) = %v, want %v", tc.observed, tc.required, got, tc.want)
		}
	}
}
