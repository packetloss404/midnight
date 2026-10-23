package deploy

import (
	"errors"
	"strings"

	"github.com/midnight/midnight/internal/runtime"
)

const (
	ActivationStepPullVerify            = "pull-verify"
	ActivationStepSchedule              = "schedule"
	ActivationStepStart                 = "start"
	ActivationStepReadinessRoutingCheck = "readiness-routing-check"
	ActivationStepPromote               = "promote"
	ActivationStepDrainPrevious         = "drain-previous"
)

type ActiveInstanceRef struct {
	DeploymentID string `json:"deployment_id,omitempty"`
	InstanceID   string `json:"instance_id,omitempty"`
}

type CandidateActivationRequest struct {
	Candidate      runtime.ScheduleRequest
	PreviousActive ActiveInstanceRef

	PullVerify            runtime.PullVerifyResult
	Scheduled             bool
	Started               bool
	Ready                 bool
	RoutingOK             bool
	ReadinessRoutingCheck ActivationReadinessRoutingCheck
	Promoted              bool
}

type CandidateActivationPlan struct {
	CandidateDeploymentID    string
	CandidateInstanceID      string
	PreviousActive           ActiveInstanceRef
	Steps                    []Step
	ReadinessRoutingCheck    ActivationReadinessRoutingCheck
	PreviousActiveProtected  bool
	ReadyToDrainPrevious     bool
	ReadyForPreviousShutdown bool
}

var (
	ErrActivationCandidateDeploymentRequired = errors.New("activation planner: candidate deployment id is required")
	ErrActivationCandidateInstanceRequired   = errors.New("activation planner: candidate instance id is required")
	ErrActivationCandidateImageRequired      = errors.New("activation planner: candidate image is required")
)

func PlanCandidateActivation(req CandidateActivationRequest) (CandidateActivationPlan, error) {
	req.Candidate.DeploymentID = strings.TrimSpace(req.Candidate.DeploymentID)
	req.Candidate.InstanceID = strings.TrimSpace(req.Candidate.InstanceID)
	req.Candidate.Image = strings.TrimSpace(req.Candidate.Image)
	req.PreviousActive.DeploymentID = strings.TrimSpace(req.PreviousActive.DeploymentID)
	req.PreviousActive.InstanceID = strings.TrimSpace(req.PreviousActive.InstanceID)
	if err := validateCandidateActivation(req); err != nil {
		return CandidateActivationPlan{}, err
	}

	plan := CandidateActivationPlan{
		CandidateDeploymentID: req.Candidate.DeploymentID,
		CandidateInstanceID:   req.Candidate.InstanceID,
		PreviousActive:        req.PreviousActive,
		ReadinessRoutingCheck: req.ReadinessRoutingCheck,
	}
	blocked := false
	addGate := func(name string, ok bool, reason string) {
		switch {
		case blocked:
			plan.Steps = append(plan.Steps, Step{Name: name, Outcome: StepOutcomeSkipped, Reason: "blocked by earlier activation gate"})
		case ok:
			plan.Steps = append(plan.Steps, Step{Name: name, Outcome: StepOutcomePlanned})
		default:
			plan.Steps = append(plan.Steps, Step{Name: name, Outcome: StepOutcomeBlocked, Reason: reason})
			blocked = true
		}
	}

	addGate(ActivationStepPullVerify, req.PullVerify.Verified && req.PullVerify.Trusted, pullVerifyActivationReason(req.PullVerify))
	addGate(ActivationStepSchedule, req.Scheduled, "candidate has not been scheduled")
	addGate(ActivationStepStart, req.Started, "candidate has not been started")
	readinessRoutingOK, readinessRoutingReason := activationReadinessRoutingGate(req)
	addGate(ActivationStepReadinessRoutingCheck, readinessRoutingOK, readinessRoutingReason)
	addGate(ActivationStepPromote, req.Promoted, "candidate has not been promoted")

	hasPrevious := req.PreviousActive.DeploymentID != "" && req.PreviousActive.InstanceID != ""
	switch {
	case blocked:
		plan.Steps = append(plan.Steps, Step{Name: ActivationStepDrainPrevious, Outcome: StepOutcomeSkipped, Reason: "candidate not promoted"})
		plan.PreviousActiveProtected = hasPrevious
	case !hasPrevious:
		plan.Steps = append(plan.Steps, Step{Name: ActivationStepDrainPrevious, Outcome: StepOutcomeSkipped, Reason: "no previous active version"})
	default:
		plan.Steps = append(plan.Steps, Step{Name: ActivationStepDrainPrevious, Outcome: StepOutcomePlanned})
		plan.ReadyToDrainPrevious = true
		plan.ReadyForPreviousShutdown = true
	}

	return plan, nil
}

func validateCandidateActivation(req CandidateActivationRequest) error {
	if req.Candidate.DeploymentID == "" {
		return ErrActivationCandidateDeploymentRequired
	}
	if req.Candidate.InstanceID == "" {
		return ErrActivationCandidateInstanceRequired
	}
	if req.Candidate.Image == "" {
		return ErrActivationCandidateImageRequired
	}
	return nil
}

func pullVerifyActivationReason(result runtime.PullVerifyResult) string {
	if result.Reason != "" {
		return result.Reason
	}
	if !result.Verified && !result.Trusted {
		return "candidate image is not verified or trusted"
	}
	if !result.Verified {
		return "candidate image is not verified"
	}
	return "candidate image is not trusted"
}

func readinessRoutingActivationReason(ready, routingOK bool) string {
	switch {
	case !ready && !routingOK:
		return "candidate is not ready and has no routable target"
	case !ready:
		return "candidate is not ready"
	default:
		return "candidate has no routable target"
	}
}

func activationReadinessRoutingGate(req CandidateActivationRequest) (bool, string) {
	if req.ReadinessRoutingCheck.Evaluated {
		if req.ReadinessRoutingCheck.ReadyToPromote {
			return true, ""
		}
		return false, req.ReadinessRoutingCheck.BlockReason()
	}
	return req.Ready && req.RoutingOK, readinessRoutingActivationReason(req.Ready, req.RoutingOK)
}
