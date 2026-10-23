package supervisor

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestBuildLoopExecutionArtifactReportsCompletedActions(t *testing.T) {
	artifact := BuildLoopExecutionArtifact([]LoopPlannerDecision{
		{Action: LoopActionRegister, Reason: "agent is not registered"},
		{Action: LoopActionHeartbeat, Reason: "heartbeat is due"},
		{Action: LoopActionFetchDesired, Reason: "desired-state cache is stale"},
		{Action: LoopActionNoop, Reason: "no loop action is due", Delay: 5 * time.Second},
	}, LoopInjectedOutcomes{
		Register:     &LoopInjectedOutcome{Completed: true},
		Heartbeat:    &LoopInjectedOutcome{Completed: true, NextDelay: 3 * time.Second},
		FetchDesired: &LoopInjectedOutcome{Completed: true},
	})

	if artifact.Status != LoopExecutionStatusCompleted {
		t.Fatalf("status = %q, want %q", artifact.Status, LoopExecutionStatusCompleted)
	}
	if artifact.NextDelay != 5*time.Second {
		t.Fatalf("next delay = %s", artifact.NextDelay)
	}
	if len(artifact.IssueCodes) != 0 {
		t.Fatalf("issue codes = %+v", artifact.IssueCodes)
	}
	wantStatuses := []string{
		LoopExecutionStatusCompleted,
		LoopExecutionStatusCompleted,
		LoopExecutionStatusCompleted,
		LoopExecutionStatusCompleted,
	}
	gotStatuses := make([]string, 0, len(artifact.ActionOutcomes))
	for _, outcome := range artifact.ActionOutcomes {
		gotStatuses = append(gotStatuses, outcome.Status)
	}
	if !reflect.DeepEqual(gotStatuses, wantStatuses) {
		t.Fatalf("action statuses = %+v, want %+v", gotStatuses, wantStatuses)
	}
}

func TestBuildLoopExecutionArtifactReportsPartialForMissingInjectedOutcome(t *testing.T) {
	artifact := BuildLoopExecutionArtifact([]LoopPlannerDecision{
		{Action: LoopActionRegister},
		{Action: LoopActionHeartbeat},
	}, LoopInjectedOutcomes{
		Register: &LoopInjectedOutcome{Completed: true},
	})

	if artifact.Status != LoopExecutionStatusPartial {
		t.Fatalf("status = %q, want %q", artifact.Status, LoopExecutionStatusPartial)
	}
	wantIssues := []string{LoopExecutionIssueMissingHeartbeatOutcome}
	if !reflect.DeepEqual(artifact.IssueCodes, wantIssues) {
		t.Fatalf("issue codes = %+v, want %+v", artifact.IssueCodes, wantIssues)
	}
	wantOutcome := LoopExecutionActionOutcome{Action: LoopActionHeartbeat, Status: LoopExecutionStatusPartial, IssueCode: LoopExecutionIssueMissingHeartbeatOutcome}
	if artifact.ActionOutcomes[1] != wantOutcome {
		t.Fatalf("heartbeat outcome = %+v, want %+v", artifact.ActionOutcomes[1], wantOutcome)
	}
}

func TestBuildLoopExecutionArtifactReportsBlockedForFailedInjectedOutcome(t *testing.T) {
	artifact := BuildLoopExecutionArtifact([]LoopPlannerDecision{
		{Action: LoopActionHeartbeat, Reason: "heartbeat is due"},
		{Action: LoopActionFetchDesired, Reason: "desired-state cache is stale"},
	}, LoopInjectedOutcomes{
		Heartbeat:    &LoopInjectedOutcome{Completed: false, IssueCode: "control_plane_unavailable", Message: " dial failed ", NextDelay: 7 * time.Second},
		FetchDesired: &LoopInjectedOutcome{Completed: false},
	})

	if artifact.Status != LoopExecutionStatusBlocked {
		t.Fatalf("status = %q, want %q", artifact.Status, LoopExecutionStatusBlocked)
	}
	if artifact.NextDelay != 7*time.Second {
		t.Fatalf("next delay = %s", artifact.NextDelay)
	}
	wantIssues := []string{"control_plane_unavailable", LoopExecutionIssueFetchDesiredFailed}
	if !reflect.DeepEqual(artifact.IssueCodes, wantIssues) {
		t.Fatalf("issue codes = %+v, want %+v", artifact.IssueCodes, wantIssues)
	}
	if artifact.ActionOutcomes[0].Message != "dial failed" || artifact.ActionOutcomes[0].IssueCode != "control_plane_unavailable" {
		t.Fatalf("heartbeat outcome = %+v", artifact.ActionOutcomes[0])
	}
	if artifact.ActionOutcomes[1].IssueCode != LoopExecutionIssueFetchDesiredFailed {
		t.Fatalf("fetch desired outcome = %+v", artifact.ActionOutcomes[1])
	}
}

func TestBuildLoopExecutionArtifactReportsUnknownActionAsBlocked(t *testing.T) {
	artifact := BuildLoopExecutionArtifact([]LoopPlannerDecision{{Action: "rotate_keys"}}, LoopInjectedOutcomes{})

	if artifact.Status != LoopExecutionStatusBlocked {
		t.Fatalf("status = %q, want %q", artifact.Status, LoopExecutionStatusBlocked)
	}
	wantIssues := []string{LoopExecutionIssueUnknownAction}
	if !reflect.DeepEqual(artifact.IssueCodes, wantIssues) {
		t.Fatalf("issue codes = %+v, want %+v", artifact.IssueCodes, wantIssues)
	}
}

func TestMarshalLoopExecutionArtifactUsesStableJSONFields(t *testing.T) {
	artifact := BuildLoopExecutionArtifact([]LoopPlannerDecision{
		{Action: LoopActionHeartbeat, Reason: "heartbeat is due", Delay: time.Second},
	}, LoopInjectedOutcomes{
		Heartbeat: &LoopInjectedOutcome{Completed: false, Message: "timeout"},
	})

	encoded, err := MarshalLoopExecutionArtifact(artifact)
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	if !json.Valid(encoded) {
		t.Fatalf("artifact json is invalid: %s", encoded)
	}
	want := `{"status":"blocked","action_outcomes":[{"action":"heartbeat","status":"blocked","reason":"heartbeat is due","issue_code":"heartbeat_failed","message":"timeout","delay":1000000000}],"next_delay":1000000000,"issue_codes":["heartbeat_failed"]}`
	if string(encoded) != want {
		t.Fatalf("json = %s, want %s", encoded, want)
	}
}
