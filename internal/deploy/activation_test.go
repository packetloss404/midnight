package deploy

import (
	"errors"
	"testing"

	"github.com/midnight/midnight/internal/runtime"
)

func TestPlanCandidateActivationOrdersPromotionBeforeDrainPrevious(t *testing.T) {
	plan, err := PlanCandidateActivation(readyActivationRequest())
	if err != nil {
		t.Fatalf("PlanCandidateActivation() error = %v", err)
	}

	want := []string{
		ActivationStepPullVerify,
		ActivationStepSchedule,
		ActivationStepStart,
		ActivationStepReadinessRoutingCheck,
		ActivationStepPromote,
		ActivationStepDrainPrevious,
	}
	if len(plan.Steps) != len(want) {
		t.Fatalf("steps = %#v, want %d steps", plan.Steps, len(want))
	}
	for i, name := range want {
		if plan.Steps[i].Name != name || plan.Steps[i].Outcome != StepOutcomePlanned {
			t.Fatalf("step[%d] = %#v, want planned %s", i, plan.Steps[i], name)
		}
	}
	if !plan.ReadyToDrainPrevious || !plan.ReadyForPreviousShutdown || plan.PreviousActiveProtected {
		t.Fatalf("previous flags = protected:%t drain:%t shutdown:%t", plan.PreviousActiveProtected, plan.ReadyToDrainPrevious, plan.ReadyForPreviousShutdown)
	}
}

func TestPlanCandidateActivationProtectsPreviousWhenCandidateFailsBeforePromotion(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CandidateActivationRequest)
	}{
		{
			name: "pull verify",
			mutate: func(req *CandidateActivationRequest) {
				req.PullVerify = runtime.PullVerifyResult{Image: req.Candidate.Image, Verified: false, Trusted: false, Reason: "signature missing"}
				req.Scheduled = false
				req.Started = false
				req.Ready = false
				req.RoutingOK = false
				req.Promoted = false
			},
		},
		{
			name: "schedule",
			mutate: func(req *CandidateActivationRequest) {
				req.Scheduled = false
				req.Started = false
				req.Ready = false
				req.RoutingOK = false
				req.Promoted = false
			},
		},
		{
			name: "start",
			mutate: func(req *CandidateActivationRequest) {
				req.Started = false
				req.Ready = false
				req.RoutingOK = false
				req.Promoted = false
			},
		},
		{
			name: "readiness",
			mutate: func(req *CandidateActivationRequest) {
				req.Ready = false
				req.RoutingOK = true
				req.Promoted = false
			},
		},
		{
			name: "routing",
			mutate: func(req *CandidateActivationRequest) {
				req.Ready = true
				req.RoutingOK = false
				req.Promoted = false
			},
		},
		{
			name: "promote",
			mutate: func(req *CandidateActivationRequest) {
				req.Promoted = false
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := readyActivationRequest()
			tt.mutate(&req)
			plan, err := PlanCandidateActivation(req)
			if err != nil {
				t.Fatalf("PlanCandidateActivation() error = %v", err)
			}
			if !plan.PreviousActiveProtected {
				t.Fatalf("PreviousActiveProtected = false, steps = %#v", plan.Steps)
			}
			if plan.ReadyToDrainPrevious || plan.ReadyForPreviousShutdown {
				t.Fatalf("previous version is eligible for drain/shutdown too early: %#v", plan)
			}
			drain := findActivationStep(plan, ActivationStepDrainPrevious)
			if drain.Outcome != StepOutcomeSkipped {
				t.Fatalf("drain previous step = %#v, want skipped", drain)
			}
		})
	}
}

func TestPlanCandidateActivationSkipsDrainWithoutPreviousActive(t *testing.T) {
	req := readyActivationRequest()
	req.PreviousActive = ActiveInstanceRef{}

	plan, err := PlanCandidateActivation(req)
	if err != nil {
		t.Fatalf("PlanCandidateActivation() error = %v", err)
	}
	drain := findActivationStep(plan, ActivationStepDrainPrevious)
	if drain.Outcome != StepOutcomeSkipped || drain.Reason != "no previous active version" {
		t.Fatalf("drain previous step = %#v", drain)
	}
	if plan.PreviousActiveProtected || plan.ReadyToDrainPrevious || plan.ReadyForPreviousShutdown {
		t.Fatalf("previous flags = protected:%t drain:%t shutdown:%t", plan.PreviousActiveProtected, plan.ReadyToDrainPrevious, plan.ReadyForPreviousShutdown)
	}
}

func TestPlanCandidateActivationValidatesCandidate(t *testing.T) {
	req := readyActivationRequest()
	req.Candidate.Image = " "

	_, err := PlanCandidateActivation(req)
	if !errors.Is(err, ErrActivationCandidateImageRequired) {
		t.Fatalf("PlanCandidateActivation() error = %v, want ErrActivationCandidateImageRequired", err)
	}
}

func readyActivationRequest() CandidateActivationRequest {
	candidate := runtime.ScheduleRequest{
		DeploymentID: "dep-v2",
		InstanceID:   "dep-v2-r0",
		ProjectID:    "prj-01",
		ServiceID:    "api",
		Image:        "registry.example.com/api@sha256:0000000000000000000000000000000000000000000000000000000000000000",
	}
	return CandidateActivationRequest{
		Candidate:      candidate,
		PreviousActive: ActiveInstanceRef{DeploymentID: "dep-v1", InstanceID: "dep-v1-r0"},
		PullVerify: runtime.PullVerifyResult{
			Image:    candidate.Image,
			Verified: true,
			Trusted:  true,
		},
		Scheduled: true,
		Started:   true,
		Ready:     true,
		RoutingOK: true,
		Promoted:  true,
	}
}

func findActivationStep(plan CandidateActivationPlan, name string) Step {
	for _, step := range plan.Steps {
		if step.Name == name {
			return step
		}
	}
	return Step{}
}
