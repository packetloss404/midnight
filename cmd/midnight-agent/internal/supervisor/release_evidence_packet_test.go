package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/midnight/midnight/cmd/midnight-agent/internal/runtime"
)

func TestBuildAgentReleaseEvidencePacketReportsReady(t *testing.T) {
	decisionArtifact := BuildAgentDecisionArtifact(LoopDecisionSummary{}, nil, agentruntime.RuntimeObservations{})
	packet := BuildAgentReleaseEvidencePacket(
		decisionArtifact,
		ReviewAgentDecisionArtifact(decisionArtifact),
		ReviewLoopExecutionArtifact(LoopExecutionArtifact{Status: LoopExecutionStatusCompleted}),
		ReviewHeartbeatFreshness(2*time.Second, 10*time.Second),
	)

	if packet.Status != AgentReleaseStatusReady {
		t.Fatalf("status = %q, want %q", packet.Status, AgentReleaseStatusReady)
	}
	if len(packet.IssueRollup) != 0 {
		t.Fatalf("issues = %+v", packet.IssueRollup)
	}
	if packet.Reconcile.StepCount != 0 || !packet.RuntimeRoutes.Ready {
		t.Fatalf("reconcile/runtime routes = %+v / %+v", packet.Reconcile, packet.RuntimeRoutes)
	}
}

func TestBuildAgentReleaseEvidencePacketReportsNeedsReview(t *testing.T) {
	decisionArtifact := BuildAgentDecisionArtifact(LoopDecisionSummary{HeartbeatWorkPending: true}, []agentruntime.ReconcileStep{
		{Kind: agentruntime.ReconcilePullImage, DeploymentID: "app"},
		{Kind: agentruntime.ReconcileStartContainer, DeploymentID: "app"},
	}, agentruntime.RuntimeObservations{})
	packet := BuildAgentReleaseEvidencePacket(
		decisionArtifact,
		ReviewAgentDecisionArtifact(decisionArtifact),
		ReviewLoopExecutionArtifact(LoopExecutionArtifact{Status: LoopExecutionStatusPartial}),
		ReviewHeartbeatFreshness(12*time.Second, 10*time.Second),
	)

	if packet.Status != AgentReleaseStatusNeedsReview {
		t.Fatalf("status = %q, want %q", packet.Status, AgentReleaseStatusNeedsReview)
	}
	want := []AgentReleaseIssueRollup{
		{Code: AgentReleaseIssueDecisionPending, Count: 1, Sources: []string{"decision"}},
		{Code: AgentReleaseIssueExecutionNeedsReview, Count: 1, Sources: []string{"execution"}},
		{Code: AgentReleaseIssueHeartbeatStale, Count: 1, Sources: []string{"heartbeat"}},
		{Code: AgentDecisionIssuePendingHeartbeat, Count: 1, Sources: []string{"decision"}},
		{Code: AgentReleaseIssueReconcileStepsPending, Count: 2, Sources: []string{"reconcile"}},
		{Code: HeartbeatFreshnessIssueStale, Count: 1, Sources: []string{"heartbeat"}},
	}
	if !reflect.DeepEqual(packet.IssueRollup, want) {
		t.Fatalf("issues = %+v, want %+v", packet.IssueRollup, want)
	}
	wantKindCounts := []ReconcileStepKindSum{{Kind: "pull_image", Count: 1}, {Kind: "start_container", Count: 1}}
	if !reflect.DeepEqual(packet.Reconcile.StepKindCounts, wantKindCounts) {
		t.Fatalf("step kind counts = %+v, want %+v", packet.Reconcile.StepKindCounts, wantKindCounts)
	}
}

func TestBuildAgentReleaseEvidencePacketReportsBlocked(t *testing.T) {
	decisionArtifact := BuildAgentDecisionArtifact(LoopDecisionSummary{}, []agentruntime.ReconcileStep{
		{Kind: agentruntime.ReconcileUpdateRouteTarget, DeploymentID: "app"},
	}, agentruntime.RuntimeObservations{RouteTargets: []agentruntime.ObservedRouteTarget{{RouteID: "route-a"}}})
	executionReview := ReviewLoopExecutionArtifact(LoopExecutionArtifact{
		Status: LoopExecutionStatusBlocked,
		ActionOutcomes: []LoopExecutionActionOutcome{
			{Action: LoopActionHeartbeat, Status: LoopExecutionStatusBlocked, IssueCode: "heartbeat_rejected"},
		},
		IssueCodes: []string{"heartbeat_rejected"},
	})
	packet := BuildAgentReleaseEvidencePacket(
		decisionArtifact,
		ReviewAgentDecisionArtifact(decisionArtifact),
		executionReview,
		ReviewHeartbeatFreshness(HeartbeatLostAfter+time.Nanosecond, 10*time.Second),
	)

	if packet.Status != AgentReleaseStatusBlocked {
		t.Fatalf("status = %q, want %q", packet.Status, AgentReleaseStatusBlocked)
	}
	want := []AgentReleaseIssueRollup{
		{Code: AgentReleaseIssueDecisionBlocked, Count: 1, Sources: []string{"decision"}},
		{Code: AgentReleaseIssueExecutionBlocked, Count: 1, Sources: []string{"execution"}},
		{Code: AgentReleaseIssueHeartbeatLost, Count: 1, Sources: []string{"heartbeat"}},
		{Code: "heartbeat_rejected", Count: 1, Sources: []string{"execution"}},
		{Code: HeartbeatFreshnessIssueLost, Count: 1, Sources: []string{"heartbeat"}},
		{Code: AgentDecisionIssueMissingRouteTargetReadiness, Count: 1, Sources: []string{"decision"}},
		{Code: AgentReleaseIssueReconcileStepsPending, Count: 1, Sources: []string{"reconcile"}},
		{Code: AgentReleaseIssueRouteTargetsNotReady, Count: 1, Sources: []string{"runtime_routes"}},
	}
	if !reflect.DeepEqual(packet.IssueRollup, want) {
		t.Fatalf("issues = %+v, want %+v", packet.IssueRollup, want)
	}
	if packet.RuntimeRoutes.ObservedRouteTargets != 1 || packet.RuntimeRoutes.RouteTargetUpdatesPending != 1 || packet.RuntimeRoutes.Ready {
		t.Fatalf("runtime routes = %+v", packet.RuntimeRoutes)
	}
}

func TestMarshalAgentReleaseEvidencePacketUsesStableJSONFields(t *testing.T) {
	decisionArtifact := BuildAgentDecisionArtifact(LoopDecisionSummary{}, nil, agentruntime.RuntimeObservations{})
	packet := BuildAgentReleaseEvidencePacket(
		decisionArtifact,
		ReviewAgentDecisionArtifact(decisionArtifact),
		ReviewLoopExecutionArtifact(LoopExecutionArtifact{Status: LoopExecutionStatusCompleted}),
		ReviewHeartbeatFreshness(2*time.Second, 10*time.Second),
	)

	encoded, err := MarshalAgentReleaseEvidencePacket(packet)
	if err != nil {
		t.Fatalf("marshal packet: %v", err)
	}
	if !json.Valid(encoded) {
		t.Fatalf("packet json is invalid: %s", encoded)
	}
	want := `{"status":"ready","decision":{"status":"idle","issues":[]},"execution":{"release_gate":"ready","action_outcome_counts":[],"issue_rollup":[],"retry_recommendation":{"action":"none","delay":0}},"heartbeat":{"status":"fresh","last_heartbeat_age":2000000000,"fresh_within":10000000000,"lost_after":15000000000,"issues":null},"reconcile":{"step_count":0,"step_kind_counts":[]},"runtime_routes":{"ready":true,"observed_route_targets":0,"route_target_updates_pending":0},"diff_plan":{"step_count":0},"issue_rollup":[]}`
	if string(encoded) != want {
		t.Fatalf("json = %s, want %s", encoded, want)
	}
}

func TestValidateAgentReleaseEvidencePacketRejectsInvalidShape(t *testing.T) {
	if err := ValidateAgentReleaseEvidencePacket(AgentReleaseEvidencePacket{Status: "paused"}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("status validation error = %v", err)
	}
	packet := AgentReleaseEvidencePacket{
		Status: AgentReleaseStatusReady,
		IssueRollup: []AgentReleaseIssueRollup{
			{Code: "z", Count: 1, Sources: []string{"decision"}},
			{Code: "a", Count: 1, Sources: []string{"decision"}},
		},
	}
	if err := ValidateAgentReleaseEvidencePacket(packet); err == nil || !strings.Contains(err.Error(), "sorted") {
		t.Fatalf("rollup validation error = %v", err)
	}
}

func TestAgentReleaseEvidencePacketJSONRoundTripUsesDeterministicOutput(t *testing.T) {
	decisionArtifact := BuildAgentDecisionArtifact(LoopDecisionSummary{}, nil, agentruntime.RuntimeObservations{})
	packet := BuildAgentReleaseEvidencePacket(
		decisionArtifact,
		ReviewAgentDecisionArtifact(decisionArtifact),
		ReviewLoopExecutionArtifact(LoopExecutionArtifact{Status: LoopExecutionStatusCompleted}),
		ReviewHeartbeatFreshness(2*time.Second, 10*time.Second),
	)
	path := filepath.Join(t.TempDir(), "release-evidence.json")

	if err := WriteAgentReleaseEvidencePacketJSON(path, packet); err != nil {
		t.Fatalf("write packet json: %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written json: %v", err)
	}
	want, err := MarshalAgentReleaseEvidencePacket(packet)
	if err != nil {
		t.Fatalf("marshal packet: %v", err)
	}
	if string(written) != string(want) {
		t.Fatalf("written json = %s, want %s", written, want)
	}

	decoded, err := ReadAgentReleaseEvidencePacketJSON(path)
	if err != nil {
		t.Fatalf("read packet json: %v", err)
	}
	if !reflect.DeepEqual(decoded, packet) {
		t.Fatalf("decoded packet = %+v, want %+v", decoded, packet)
	}
}

func TestAgentReleaseEvidencePacketJSONRejectsInvalidPaths(t *testing.T) {
	packet := AgentReleaseEvidencePacket{Status: AgentReleaseStatusReady}
	tempDir := t.TempDir()

	if err := WriteAgentReleaseEvidencePacketJSON(filepath.Join(tempDir, "release-evidence.txt"), packet); err == nil || !strings.Contains(err.Error(), ".json") {
		t.Fatalf("write extension validation error = %v", err)
	}
	if _, err := ReadAgentReleaseEvidencePacketJSON(filepath.Join(tempDir, "release-evidence.txt")); err == nil || !strings.Contains(err.Error(), ".json") {
		t.Fatalf("read extension validation error = %v", err)
	}
	if err := WriteAgentReleaseEvidencePacketJSON(filepath.Join(tempDir, "missing", "release-evidence.json"), packet); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("write parent validation error = %v", err)
	}
}

func TestReadAgentReleaseEvidencePacketJSONStrictlyDecodesAndValidates(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "unknown field",
			content: `{"status":"ready","decision":{"status":"idle","issues":[]},"execution":{"release_gate":"ready","action_outcome_counts":[],"issue_rollup":[],"retry_recommendation":{"action":"none","delay":0}},"heartbeat":{"status":"fresh","last_heartbeat_age":0,"fresh_within":1,"lost_after":2,"issues":null},"reconcile":{"step_count":0,"step_kind_counts":[]},"runtime_routes":{"ready":true,"observed_route_targets":0,"route_target_updates_pending":0},"issue_rollup":[],"extra":true}`,
			want:    "unknown field",
		},
		{
			name:    "trailing value",
			content: `{"status":"ready","decision":{"status":"idle","issues":[]},"execution":{"release_gate":"ready","action_outcome_counts":[],"issue_rollup":[],"retry_recommendation":{"action":"none","delay":0}},"heartbeat":{"status":"fresh","last_heartbeat_age":0,"fresh_within":1,"lost_after":2,"issues":null},"reconcile":{"step_count":0,"step_kind_counts":[]},"runtime_routes":{"ready":true,"observed_route_targets":0,"route_target_updates_pending":0},"issue_rollup":[]} {}`,
			want:    "exactly one value",
		},
		{
			name:    "invalid packet",
			content: `{"status":"paused","decision":{"status":"idle","issues":[]},"execution":{"release_gate":"ready","action_outcome_counts":[],"issue_rollup":[],"retry_recommendation":{"action":"none","delay":0}},"heartbeat":{"status":"fresh","last_heartbeat_age":0,"fresh_within":1,"lost_after":2,"issues":null},"reconcile":{"step_count":0,"step_kind_counts":[]},"runtime_routes":{"ready":true,"observed_route_targets":0,"route_target_updates_pending":0},"issue_rollup":[]}`,
			want:    "not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "release-evidence.json")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write test json: %v", err)
			}
			if _, err := ReadAgentReleaseEvidencePacketJSON(path); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("read validation error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
