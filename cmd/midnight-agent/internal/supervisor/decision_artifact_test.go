package supervisor

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	agentruntime "github.com/midnight/midnight/cmd/midnight-agent/internal/runtime"
)

func TestBuildAgentDecisionArtifactAggregatesPureDiagnostics(t *testing.T) {
	loop := SummarizeLoopDecisions([]LoopPlannerDecision{
		{Action: LoopActionHeartbeat, Delay: 2 * time.Second},
		{Action: LoopActionFetchDesired},
	})
	steps := []agentruntime.ReconcileStep{
		{Kind: agentruntime.ReconcileStartContainer, DeploymentID: "app"},
		{Kind: agentruntime.ReconcileUpdateRouteTarget, DeploymentID: "app"},
		{Kind: agentruntime.ReconcileCreateContainer, DeploymentID: "app"},
		{Kind: agentruntime.ReconcileStartContainer, DeploymentID: "worker"},
	}
	observations := agentruntime.RuntimeObservations{
		Networks:     []agentruntime.ObservedNetwork{{Name: "net-a"}},
		Images:       []agentruntime.ObservedImage{{Image: "example/app:1"}, {Image: "example/worker:1"}},
		Containers:   []agentruntime.ObservedContainer{{DeploymentID: "app", Revision: "1", Created: true}},
		RouteTargets: []agentruntime.ObservedRouteTarget{{RouteID: "route-a", DeploymentID: "app", Revision: "1"}},
	}

	artifact := BuildAgentDecisionArtifact(loop, steps, observations)

	wantKinds := []string{"create_container", "start_container", "update_route_target"}
	if !reflect.DeepEqual(artifact.Reconcile.StepKinds, wantKinds) {
		t.Fatalf("step kinds = %+v, want %+v", artifact.Reconcile.StepKinds, wantKinds)
	}
	wantKindCounts := []ReconcileStepKindSum{
		{Kind: "create_container", Count: 1},
		{Kind: "start_container", Count: 2},
		{Kind: "update_route_target", Count: 1},
	}
	if !reflect.DeepEqual(artifact.Reconcile.StepKindCounts, wantKindCounts) {
		t.Fatalf("step kind counts = %+v, want %+v", artifact.Reconcile.StepKindCounts, wantKindCounts)
	}
	if artifact.Reconcile.StepCount != 4 || artifact.Reconcile.RouteTargetsReady || artifact.Reconcile.RouteTargetUpdatesPending != 1 {
		t.Fatalf("reconcile = %+v", artifact.Reconcile)
	}
	if artifact.Observations != (RuntimeObservationCounts{Networks: 1, Images: 2, Containers: 1, RouteTargets: 1}) {
		t.Fatalf("observations = %+v", artifact.Observations)
	}
	if !artifact.PendingWork.Any || artifact.PendingWork.Registration || !artifact.PendingWork.DesiredState || !artifact.PendingWork.Heartbeat || !artifact.PendingWork.Reconcile || !artifact.PendingWork.RouteTarget {
		t.Fatalf("pending work = %+v", artifact.PendingWork)
	}
}

func TestBuildAgentDecisionArtifactReportsReadyWhenNoRouteTargetWork(t *testing.T) {
	artifact := BuildAgentDecisionArtifact(LoopDecisionSummary{}, []agentruntime.ReconcileStep{
		{Kind: agentruntime.ReconcileWaitHealth, DeploymentID: "app"},
	}, agentruntime.RuntimeObservations{})

	if !artifact.Reconcile.RouteTargetsReady || artifact.Reconcile.RouteTargetUpdatesPending != 0 {
		t.Fatalf("reconcile = %+v", artifact.Reconcile)
	}
	if !artifact.PendingWork.Any || !artifact.PendingWork.Reconcile || artifact.PendingWork.RouteTarget {
		t.Fatalf("pending work = %+v", artifact.PendingWork)
	}
}

func TestBuildAgentDecisionArtifactClonesLoopSummary(t *testing.T) {
	loop := LoopDecisionSummary{Counts: []LoopActionCount{{Action: LoopActionRegister, Count: 1}}, RegistrationWorkPending: true}
	artifact := BuildAgentDecisionArtifact(loop, nil, agentruntime.RuntimeObservations{})
	loop.Counts[0].Count = 99

	if artifact.Loop.Counts[0].Count != 1 {
		t.Fatalf("artifact loop counts mutated: %+v", artifact.Loop.Counts)
	}
}

func TestMarshalAgentDecisionArtifactUsesStableJSONFields(t *testing.T) {
	artifact := BuildAgentDecisionArtifact(LoopDecisionSummary{}, []agentruntime.ReconcileStep{
		{Kind: agentruntime.ReconcilePullImage, DeploymentID: "app"},
	}, agentruntime.RuntimeObservations{})

	encoded, err := MarshalAgentDecisionArtifact(artifact)
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	if !json.Valid(encoded) {
		t.Fatalf("artifact json is invalid: %s", encoded)
	}
	want := `{"loop":{"counts":null,"next_delay_max":0,"registration_work_pending":false,"desired_state_work_pending":false,"heartbeat_work_pending":false},"reconcile":{"step_count":1,"step_kinds":["pull_image"],"step_kind_counts":[{"kind":"pull_image","count":1}],"route_targets_ready":true,"route_target_updates_pending":0},"observations":{"networks":0,"images":0,"containers":0,"route_targets":0},"pending_work":{"any":true,"registration":false,"desired_state":false,"heartbeat":false,"reconcile":true,"route_target":false}}`
	if string(encoded) != want {
		t.Fatalf("json = %s, want %s", encoded, want)
	}
}

func TestReviewAgentDecisionArtifactReportsIdle(t *testing.T) {
	artifact := BuildAgentDecisionArtifact(LoopDecisionSummary{}, nil, agentruntime.RuntimeObservations{})

	review := ReviewAgentDecisionArtifact(artifact)

	if review.Status != AgentDecisionStatusIdle || len(review.Issues) != 0 {
		t.Fatalf("review = %+v", review)
	}
}

func TestReviewAgentDecisionArtifactReportsPendingWork(t *testing.T) {
	artifact := BuildAgentDecisionArtifact(LoopDecisionSummary{
		RegistrationWorkPending: true,
		HeartbeatWorkPending:    true,
	}, nil, agentruntime.RuntimeObservations{})

	review := ReviewAgentDecisionArtifact(artifact)

	if review.Status != AgentDecisionStatusPending {
		t.Fatalf("status = %q, want %q", review.Status, AgentDecisionStatusPending)
	}
	want := []AgentDecisionReviewIssue{
		{Code: AgentDecisionIssuePendingHeartbeat},
		{Code: AgentDecisionIssuePendingRegistration},
	}
	if !reflect.DeepEqual(review.Issues, want) {
		t.Fatalf("issues = %+v, want %+v", review.Issues, want)
	}
}

func TestReviewAgentDecisionArtifactReportsBlockedAndSortsIssues(t *testing.T) {
	artifact := BuildAgentDecisionArtifact(LoopDecisionSummary{HeartbeatWorkPending: true}, []agentruntime.ReconcileStep{
		{Kind: agentruntime.ReconcileUpdateRouteTarget, DeploymentID: "app"},
	}, agentruntime.RuntimeObservations{})
	artifact.Reconcile.Errors = []string{"z reconcile failed", "a reconcile failed"}

	review := ReviewAgentDecisionArtifact(artifact)

	if review.Status != AgentDecisionStatusBlocked {
		t.Fatalf("status = %q, want %q", review.Status, AgentDecisionStatusBlocked)
	}
	want := []AgentDecisionReviewIssue{
		{Code: AgentDecisionIssueMissingRouteTargetReadiness},
		{Code: AgentDecisionIssuePendingHeartbeat},
		{Code: AgentDecisionIssueReconcileError, Message: "a reconcile failed"},
		{Code: AgentDecisionIssueReconcileError, Message: "z reconcile failed"},
	}
	if !reflect.DeepEqual(review.Issues, want) {
		t.Fatalf("issues = %+v, want %+v", review.Issues, want)
	}
}

func TestMarshalAgentDecisionReviewUsesStableJSONFields(t *testing.T) {
	review := ReviewAgentDecisionArtifact(AgentDecisionArtifact{
		Reconcile: ReconcileDiagnosticArtifact{RouteTargetsReady: true},
		PendingWork: AgentPendingWork{
			Any:          true,
			Registration: true,
		},
	})

	encoded, err := MarshalAgentDecisionReview(review)
	if err != nil {
		t.Fatalf("marshal review: %v", err)
	}
	if !json.Valid(encoded) {
		t.Fatalf("review json is invalid: %s", encoded)
	}
	want := `{"status":"pending","issues":[{"code":"pending_registration"}]}`
	if string(encoded) != want {
		t.Fatalf("json = %s, want %s", encoded, want)
	}
}
