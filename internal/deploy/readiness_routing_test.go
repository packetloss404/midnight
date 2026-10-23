package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/runtime"
)

func TestEvaluateActivationReadinessRoutingReadyToPromote(t *testing.T) {
	check := EvaluateActivationReadinessRouting(readyReadinessRoutingEvidence())

	if !check.ReadyToPromote || !check.RuntimeReady || !check.RoutingReady || !check.RouteReady {
		t.Fatalf("check = %#v, want ready to promote", check)
	}
	if check.ReasonCode != ActivationReadinessRoutingReady {
		t.Fatalf("ReasonCode = %q, want %q", check.ReasonCode, ActivationReadinessRoutingReady)
	}
	if check.HostID != "host-a" || check.RoutingTargetID != "host-a/dep-v2-r0" {
		t.Fatalf("target identity = host:%q target:%q", check.HostID, check.RoutingTargetID)
	}
}

func TestPlanCandidateActivationConsumesReadinessRoutingCheck(t *testing.T) {
	req := readyActivationRequest()
	req.Ready = false
	req.RoutingOK = false
	req.ReadinessRoutingCheck = EvaluateActivationReadinessRouting(readyReadinessRoutingEvidence())

	plan, err := PlanCandidateActivation(req)
	if err != nil {
		t.Fatalf("PlanCandidateActivation() error = %v", err)
	}

	step := findActivationStep(plan, ActivationStepReadinessRoutingCheck)
	if step.Outcome != StepOutcomePlanned {
		t.Fatalf("readiness/routing step = %#v, want planned", step)
	}
	if !plan.ReadinessRoutingCheck.ReadyToPromote {
		t.Fatalf("plan did not retain check result: %#v", plan.ReadinessRoutingCheck)
	}
}

func TestEvaluateActivationReadinessRoutingReasonCodes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ActivationReadinessRoutingEvidence)
		want   ActivationReadinessRoutingReasonCode
	}{
		{
			name: "runtime not ready",
			mutate: func(e *ActivationReadinessRoutingEvidence) {
				e.InstanceStatus.State = runtime.InstanceStateRunning
			},
			want: ActivationReadinessRoutingRuntimeNotReady,
		},
		{
			name: "host mismatch",
			mutate: func(e *ActivationReadinessRoutingEvidence) {
				e.RoutingTarget.HostID = "host-b"
			},
			want: ActivationReadinessRoutingHostMismatch,
		},
		{
			name: "target mismatch",
			mutate: func(e *ActivationReadinessRoutingEvidence) {
				e.RoutingTarget.RoutingTargetID = "host-a/other"
			},
			want: ActivationReadinessRoutingTargetMismatch,
		},
		{
			name: "route pending",
			mutate: func(e *ActivationReadinessRoutingEvidence) {
				e.RouteSummary.Status = router.RouteReconciliationStatusPending
				e.RouteSummary.ActiveRoutes = 0
				e.RouteSummary.PendingRoutes = 1
			},
			want: ActivationReadinessRoutingRoutePending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evidence := readyReadinessRoutingEvidence()
			tt.mutate(&evidence)

			check := EvaluateActivationReadinessRouting(evidence)
			if check.ReadyToPromote {
				t.Fatalf("ReadyToPromote = true, want false")
			}
			if check.ReasonCode != tt.want {
				t.Fatalf("ReasonCode = %q, want %q; check = %#v", check.ReasonCode, tt.want, check)
			}
			req := readyActivationRequest()
			req.ReadinessRoutingCheck = check
			req.Promoted = false
			plan, err := PlanCandidateActivation(req)
			if err != nil {
				t.Fatalf("PlanCandidateActivation() error = %v", err)
			}
			step := findActivationStep(plan, ActivationStepReadinessRoutingCheck)
			if step.Outcome != StepOutcomeBlocked {
				t.Fatalf("readiness/routing step = %#v, want blocked", step)
			}
			if !strings.HasPrefix(step.Reason, string(tt.want)) {
				t.Fatalf("step reason = %q, want reason code prefix %q", step.Reason, tt.want)
			}
		})
	}
}

func TestActivationReadinessRoutingCheckerErrorFlowsBack(t *testing.T) {
	req := ActivationReadinessRoutingRequest{
		DeploymentID:    "dep-v2",
		InstanceID:      "dep-v2-r0",
		HostID:          "host-a",
		RoutingTargetID: "host-a/dep-v2-r0",
		RouteGroup:      "prj-01-prod-api",
	}
	checker := fakeActivationReadinessRoutingChecker{err: errors.New("probe timeout")}
	check, err := checker.CheckActivationReadinessRouting(context.Background(), req)
	if err != nil {
		check = FailedActivationReadinessRoutingCheck(req, err)
	}

	if check.ReasonCode != ActivationReadinessRoutingCheckError || check.Error != "probe timeout" {
		t.Fatalf("check = %#v, want check-error with probe timeout", check)
	}
	activation := readyActivationRequest()
	activation.ReadinessRoutingCheck = check
	activation.Promoted = false
	plan, err := PlanCandidateActivation(activation)
	if err != nil {
		t.Fatalf("PlanCandidateActivation() error = %v", err)
	}
	step := findActivationStep(plan, ActivationStepReadinessRoutingCheck)
	if step.Outcome != StepOutcomeBlocked || step.Reason != "check-error: probe timeout" {
		t.Fatalf("readiness/routing step = %#v", step)
	}
}

type fakeActivationReadinessRoutingChecker struct {
	check ActivationReadinessRoutingCheck
	err   error
}

func (f fakeActivationReadinessRoutingChecker) CheckActivationReadinessRouting(context.Context, ActivationReadinessRoutingRequest) (ActivationReadinessRoutingCheck, error) {
	return f.check, f.err
}

func readyReadinessRoutingEvidence() ActivationReadinessRoutingEvidence {
	candidate := runtime.ScheduleRequest{
		DeploymentID: "dep-v2",
		InstanceID:   "dep-v2-r0",
		ProjectID:    "prj-01",
		ServiceID:    "api",
		Image:        "registry.example.com/api@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Labels: map[string]string{
			"midnight.host": "host-a",
		},
	}
	now := time.Unix(100, 0).UTC()
	return ActivationReadinessRoutingEvidence{
		Candidate:               candidate,
		ExpectedHostID:          "host-a",
		ExpectedRoutingTargetID: "host-a/dep-v2-r0",
		InstanceStatus: runtime.InstanceStatus{
			DeploymentID: candidate.DeploymentID,
			InstanceID:   candidate.InstanceID,
			State:        runtime.InstanceStateReady,
			ObservedAt:   now,
		},
		RoutingTarget: runtime.RoutingTarget{
			DeploymentID:    candidate.DeploymentID,
			InstanceID:      candidate.InstanceID,
			HostID:          "host-a",
			RoutingTargetID: "host-a/dep-v2-r0",
			ContainerID:     "container-1",
			ContainerIP:     "10.0.0.2",
			InternalPort:    8080,
			ProjectID:       candidate.ProjectID,
			Ready:           true,
			ObservedAt:      now,
		},
		RouteSummary: router.RouteReconciliationSummary{
			RouteGroup:     "prj-01-prod-api",
			Status:         router.RouteReconciliationStatusReady,
			DesiredRoutes:  1,
			RenderedRoutes: 1,
			ActiveRoutes:   1,
		},
	}
}
