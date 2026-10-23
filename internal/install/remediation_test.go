package install

import (
	"encoding/json"
	"testing"

	"github.com/midnight/midnight/internal/preflight"
)

func TestPlanPreflightRemediation(t *testing.T) {
	t.Parallel()

	plan := PlanPreflightRemediation([]preflight.Result{
		{ID: "PRE-CGROUP-001", Status: preflight.StatusFail, Message: "cgroup v2 mount not found"},
		{ID: "PRE-DOCKER-API-001", Status: preflight.StatusFail, Message: "Docker API 1.42 is below required 1.43"},
		{ID: "PRE-DOCKER-DAEMON-001", Status: preflight.StatusWarn, Err: &preflight.DockerDaemonConflictError{Paths: []string{"features.buildkit"}}},
	})

	if len(plan.Steps) != 3 {
		t.Fatalf("len(Steps) = %d, want 3", len(plan.Steps))
	}
	for _, step := range plan.Steps {
		if step.Command == "" || step.Title == "" || step.Risk == "" || step.ApplyMode == "" {
			t.Fatalf("step = %+v, want populated data-only fields", step)
		}
		if !step.RequiresRestart {
			t.Fatalf("step = %+v, want restart requirement", step)
		}
		if step.Safety == "" {
			t.Fatalf("step = %+v, want safety classification", step)
		}
		if step.CheckID == "" {
			t.Fatalf("step = %+v, want check id", step)
		}
	}
	if plan.Steps[0].ApplyMode != ApplyModeManual {
		t.Fatalf("cgroup ApplyMode = %q, want %q", plan.Steps[0].ApplyMode, ApplyModeManual)
	}
	if plan.Steps[2].ApplyMode != ApplyModeManual {
		t.Fatalf("daemon ApplyMode = %q, want %q", plan.Steps[2].ApplyMode, ApplyModeManual)
	}
	if plan.Steps[2].Risk != "operator_config_conflict" {
		t.Fatalf("daemon Risk = %q, want operator_config_conflict", plan.Steps[2].Risk)
	}
	if plan.Steps[2].Safety != SafetyRequiresConfirm {
		t.Fatalf("daemon Safety = %q, want %q", plan.Steps[2].Safety, SafetyRequiresConfirm)
	}
}

func TestPlanPreflightRemediationMarksDaemonMergeSafeFix(t *testing.T) {
	t.Parallel()

	plan := PlanPreflightRemediation([]preflight.Result{
		{ID: "PRE-DOCKER-DAEMON-001", Status: preflight.StatusWarn, Message: "daemon.json requires baseline merge"},
	})
	if len(plan.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(plan.Steps))
	}
	if plan.Steps[0].ApplyMode != ApplyModeSafeFix {
		t.Fatalf("ApplyMode = %q, want %q", plan.Steps[0].ApplyMode, ApplyModeSafeFix)
	}
	if plan.Steps[0].Safety != SafetySafe {
		t.Fatalf("Safety = %q, want %q", plan.Steps[0].Safety, SafetySafe)
	}
}

func TestPlanPreflightRemediationCoversIngressTimeSyncDisk(t *testing.T) {
	t.Parallel()

	plan := PlanPreflightRemediation([]preflight.Result{
		{ID: "PRE-INGRESS-PORTS-001", Status: preflight.StatusFail, Message: "port 80 bound"},
		{ID: "PRE-TIMESYNC-001", Status: preflight.StatusWarn, Message: "drift 2s"},
		{ID: "PRE-DISK-001", Status: preflight.StatusFail, Message: "85%"},
	})
	if len(plan.Steps) != 3 {
		t.Fatalf("len(Steps) = %d, want 3", len(plan.Steps))
	}
	wantTitles := map[string]bool{
		"Free required ingress ports (80/443)":   true,
		"Enable host time synchronization":       true,
		"Reduce disk usage on /var/lib/midnight": true,
	}
	for _, step := range plan.Steps {
		if !wantTitles[step.Title] {
			t.Fatalf("Title = %q, want one of %v", step.Title, wantTitles)
		}
		if step.Safety == "" {
			t.Fatalf("Safety = empty for %q", step.Title)
		}
	}
}

func TestPlanPreflightRemediationTimeSyncWarnIsSafe(t *testing.T) {
	t.Parallel()

	plan := PlanPreflightRemediation([]preflight.Result{
		{ID: "PRE-TIMESYNC-001", Status: preflight.StatusWarn},
	})
	if len(plan.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(plan.Steps))
	}
	if plan.Steps[0].Safety != SafetySafe || plan.Steps[0].ApplyMode != ApplyModeSafeFix {
		t.Fatalf("step = %+v, want safe safe_fix", plan.Steps[0])
	}
}

func TestPlanPreflightRemediationIgnoresHealthyAndUnknownResults(t *testing.T) {
	t.Parallel()

	plan := PlanPreflightRemediation([]preflight.Result{
		{ID: "PRE-CGROUP-001", Status: preflight.StatusPass},
		{ID: "PRE-DOCKER-API-001", Status: preflight.StatusSkip},
		{ID: "PRE-UNKNOWN-001", Status: preflight.StatusFail},
	})
	if len(plan.Steps) != 0 {
		t.Fatalf("Steps = %+v, want none", plan.Steps)
	}
}

func TestMarshalRemediationPlanJSON(t *testing.T) {
	t.Parallel()

	plan := RemediationPlan{Steps: []RemediationStep{{
		Title:           "Resolve Docker daemon.json baseline",
		Command:         "sudoedit /etc/docker/daemon.json && sudo systemctl restart docker",
		Risk:            "operator_config_conflict",
		RequiresRestart: true,
		ApplyMode:       ApplyModeManual,
	}}}
	data, err := MarshalRemediationPlanJSON(plan)
	if err != nil {
		t.Fatalf("MarshalRemediationPlanJSON() error = %v", err)
	}
	var decoded RemediationPlan
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(plan) error = %v", err)
	}
	if len(decoded.Steps) != 1 || decoded.Steps[0].Command != plan.Steps[0].Command {
		t.Fatalf("decoded = %+v, want round-trip", decoded)
	}
	if !json.Valid(data) {
		t.Fatalf("plan JSON is invalid: %s", data)
	}
}
