package supervisor

import (
	"encoding/json"
	"sort"

	agentruntime "github.com/midnight/midnight/cmd/midnight-agent/internal/runtime"
)

// AgentDecisionArtifact is a JSON-friendly diagnostic snapshot of pure agent decisions.
type AgentDecisionArtifact struct {
	Loop         LoopDecisionSummary         `json:"loop"`
	Reconcile    ReconcileDiagnosticArtifact `json:"reconcile"`
	Observations RuntimeObservationCounts    `json:"observations"`
	PendingWork  AgentPendingWork            `json:"pending_work"`
}

// ReconcileDiagnosticArtifact summarizes pending runtime reconcile work without executing it.
type ReconcileDiagnosticArtifact struct {
	StepCount                 int                    `json:"step_count"`
	StepKinds                 []string               `json:"step_kinds"`
	StepKindCounts            []ReconcileStepKindSum `json:"step_kind_counts"`
	RouteTargetsReady         bool                   `json:"route_targets_ready"`
	RouteTargetUpdatesPending int                    `json:"route_target_updates_pending"`
	Errors                    []string               `json:"errors,omitempty"`
}

const (
	AgentDecisionStatusIdle    = "idle"
	AgentDecisionStatusPending = "pending"
	AgentDecisionStatusBlocked = "blocked"

	AgentDecisionIssueMissingRouteTargetReadiness = "missing_route_target_readiness"
	AgentDecisionIssuePendingRegistration         = "pending_registration"
	AgentDecisionIssuePendingHeartbeat            = "pending_heartbeat"
	AgentDecisionIssueReconcileError              = "reconcile_error"
)

// AgentDecisionReview is a JSON-friendly status derived from an agent decision artifact.
type AgentDecisionReview struct {
	Status string                     `json:"status"`
	Issues []AgentDecisionReviewIssue `json:"issues"`
}

// AgentDecisionReviewIssue identifies one deterministic reason for a non-idle status.
type AgentDecisionReviewIssue struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// ReconcileStepKindSum is a deterministic count for one reconcile step kind.
type ReconcileStepKindSum struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// RuntimeObservationCounts summarizes observed runtime inputs used by reconciliation.
type RuntimeObservationCounts struct {
	Networks     int `json:"networks"`
	Images       int `json:"images"`
	Containers   int `json:"containers"`
	RouteTargets int `json:"route_targets"`
}

// AgentPendingWork exposes coarse booleans for deciding whether the agent has work to do.
type AgentPendingWork struct {
	Any          bool `json:"any"`
	Registration bool `json:"registration"`
	DesiredState bool `json:"desired_state"`
	Heartbeat    bool `json:"heartbeat"`
	Reconcile    bool `json:"reconcile"`
	RouteTarget  bool `json:"route_target"`
}

// BuildAgentDecisionArtifact combines pure loop, reconcile, and observation facts for diagnostics.
// It performs no HTTP, Docker, or filesystem I/O.
func BuildAgentDecisionArtifact(loop LoopDecisionSummary, steps []agentruntime.ReconcileStep, observations agentruntime.RuntimeObservations) AgentDecisionArtifact {
	reconcile := summarizeReconcileSteps(steps)
	pending := AgentPendingWork{
		Registration: loop.RegistrationWorkPending,
		DesiredState: loop.DesiredStateWorkPending,
		Heartbeat:    loop.HeartbeatWorkPending,
		Reconcile:    len(steps) > 0,
		RouteTarget:  reconcile.RouteTargetUpdatesPending > 0,
	}
	pending.Any = pending.Registration || pending.DesiredState || pending.Heartbeat || pending.Reconcile || pending.RouteTarget

	return AgentDecisionArtifact{
		Loop:         cloneLoopDecisionSummary(loop),
		Reconcile:    reconcile,
		Observations: countRuntimeObservations(observations),
		PendingWork:  pending,
	}
}

// MarshalAgentDecisionArtifact marshals the artifact with stable field names for transport or logs.
func MarshalAgentDecisionArtifact(artifact AgentDecisionArtifact) ([]byte, error) {
	return json.Marshal(artifact)
}

// ReviewAgentDecisionArtifact derives a compact status from an existing decision artifact.
// It performs no HTTP, Docker, or filesystem I/O.
func ReviewAgentDecisionArtifact(artifact AgentDecisionArtifact) AgentDecisionReview {
	issues := make([]AgentDecisionReviewIssue, 0)
	blocked := false
	if !artifact.Reconcile.RouteTargetsReady {
		blocked = true
		issues = append(issues, AgentDecisionReviewIssue{Code: AgentDecisionIssueMissingRouteTargetReadiness})
	}
	if artifact.PendingWork.Registration {
		issues = append(issues, AgentDecisionReviewIssue{Code: AgentDecisionIssuePendingRegistration})
	}
	if artifact.PendingWork.Heartbeat {
		issues = append(issues, AgentDecisionReviewIssue{Code: AgentDecisionIssuePendingHeartbeat})
	}

	reconcileErrors := append([]string(nil), artifact.Reconcile.Errors...)
	sort.Strings(reconcileErrors)
	for _, reconcileError := range reconcileErrors {
		blocked = true
		issues = append(issues, AgentDecisionReviewIssue{Code: AgentDecisionIssueReconcileError, Message: reconcileError})
	}
	sortAgentDecisionReviewIssues(issues)

	status := AgentDecisionStatusIdle
	if blocked {
		status = AgentDecisionStatusBlocked
	} else if artifact.PendingWork.Any || len(issues) > 0 {
		status = AgentDecisionStatusPending
	}

	return AgentDecisionReview{Status: status, Issues: issues}
}

// MarshalAgentDecisionReview marshals a derived review with stable field names for transport or logs.
func MarshalAgentDecisionReview(review AgentDecisionReview) ([]byte, error) {
	return json.Marshal(review)
}

func summarizeReconcileSteps(steps []agentruntime.ReconcileStep) ReconcileDiagnosticArtifact {
	counts := make(map[string]int, len(steps))
	for _, step := range steps {
		kind := string(step.Kind)
		counts[kind]++
	}

	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)

	stepKindCounts := make([]ReconcileStepKindSum, 0, len(kinds))
	for _, kind := range kinds {
		stepKindCounts = append(stepKindCounts, ReconcileStepKindSum{Kind: kind, Count: counts[kind]})
	}

	routeTargetUpdatesPending := counts[string(agentruntime.ReconcileUpdateRouteTarget)]
	return ReconcileDiagnosticArtifact{
		StepCount:                 len(steps),
		StepKinds:                 kinds,
		StepKindCounts:            stepKindCounts,
		RouteTargetsReady:         routeTargetUpdatesPending == 0,
		RouteTargetUpdatesPending: routeTargetUpdatesPending,
	}
}

func countRuntimeObservations(observations agentruntime.RuntimeObservations) RuntimeObservationCounts {
	return RuntimeObservationCounts{
		Networks:     len(observations.Networks),
		Images:       len(observations.Images),
		Containers:   len(observations.Containers),
		RouteTargets: len(observations.RouteTargets),
	}
}

func cloneLoopDecisionSummary(summary LoopDecisionSummary) LoopDecisionSummary {
	summary.Counts = append([]LoopActionCount(nil), summary.Counts...)
	return summary
}

func sortAgentDecisionReviewIssues(issues []AgentDecisionReviewIssue) {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Code != issues[j].Code {
			return issues[i].Code < issues[j].Code
		}
		return issues[i].Message < issues[j].Message
	})
}
