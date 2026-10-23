package supervisor

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestReviewLoopExecutionArtifactReportsReady(t *testing.T) {
	artifact := LoopExecutionArtifact{
		Status: LoopExecutionStatusCompleted,
		ActionOutcomes: []LoopExecutionActionOutcome{
			{Action: LoopActionHeartbeat, Status: LoopExecutionStatusCompleted},
			{Action: LoopActionNoop, Status: LoopExecutionStatusCompleted, Delay: 5 * time.Second},
		},
		NextDelay: 5 * time.Second,
	}

	review := ReviewLoopExecutionArtifact(artifact)

	if review.ReleaseGate != LoopExecutionReleaseGateReady {
		t.Fatalf("release gate = %q, want %q", review.ReleaseGate, LoopExecutionReleaseGateReady)
	}
	wantCounts := []LoopExecutionOutcomeCount{
		{Action: LoopActionHeartbeat, Status: LoopExecutionStatusCompleted, Count: 1},
		{Action: LoopActionNoop, Status: LoopExecutionStatusCompleted, Count: 1},
	}
	if !reflect.DeepEqual(review.ActionOutcomeCounts, wantCounts) {
		t.Fatalf("counts = %+v, want %+v", review.ActionOutcomeCounts, wantCounts)
	}
	if len(review.IssueRollup) != 0 {
		t.Fatalf("issue rollup = %+v", review.IssueRollup)
	}
	wantRetry := LoopExecutionRetryRecommendation{Action: LoopExecutionRetryRecommendationNone}
	if review.RetryRecommendation != wantRetry {
		t.Fatalf("retry recommendation = %+v, want %+v", review.RetryRecommendation, wantRetry)
	}
}

func TestReviewLoopExecutionArtifactReportsNeedsReview(t *testing.T) {
	artifact := LoopExecutionArtifact{
		Status: LoopExecutionStatusPartial,
		ActionOutcomes: []LoopExecutionActionOutcome{
			{Action: LoopActionFetchDesired, Status: LoopExecutionStatusPartial, IssueCode: LoopExecutionIssueMissingFetchDesiredOutcome},
			{Action: LoopActionHeartbeat, Status: LoopExecutionStatusCompleted},
		},
		IssueCodes: []string{LoopExecutionIssueMissingFetchDesiredOutcome},
	}

	review := ReviewLoopExecutionArtifact(artifact)

	if review.ReleaseGate != LoopExecutionReleaseGateNeedsReview {
		t.Fatalf("release gate = %q, want %q", review.ReleaseGate, LoopExecutionReleaseGateNeedsReview)
	}
	wantRollup := []LoopExecutionIssueRollup{
		{IssueCode: LoopExecutionIssueMissingFetchDesiredOutcome, Count: 1, Actions: []string{LoopActionFetchDesired}},
	}
	if !reflect.DeepEqual(review.IssueRollup, wantRollup) {
		t.Fatalf("issue rollup = %+v, want %+v", review.IssueRollup, wantRollup)
	}
	wantRetry := LoopExecutionRetryRecommendation{Action: LoopExecutionRetryRecommendationRetry, IssueCode: LoopExecutionIssueMissingFetchDesiredOutcome}
	if review.RetryRecommendation != wantRetry {
		t.Fatalf("retry recommendation = %+v, want %+v", review.RetryRecommendation, wantRetry)
	}
}

func TestReviewLoopExecutionArtifactReportsBlockedWithBackoff(t *testing.T) {
	artifact := LoopExecutionArtifact{
		Status: LoopExecutionStatusBlocked,
		ActionOutcomes: []LoopExecutionActionOutcome{
			{Action: "rotate_keys", Status: LoopExecutionStatusBlocked, IssueCode: LoopExecutionIssueUnknownAction},
			{Action: LoopActionRegister, Status: LoopExecutionStatusBlocked, IssueCode: "control_plane_unavailable", Delay: 7 * time.Second},
			{Action: LoopActionHeartbeat, Status: LoopExecutionStatusBlocked, IssueCode: "control_plane_unavailable", Delay: 3 * time.Second},
			{Action: LoopActionHeartbeat, Status: LoopExecutionStatusCompleted},
		},
		NextDelay:  7 * time.Second,
		IssueCodes: []string{LoopExecutionIssueUnknownAction, "control_plane_unavailable"},
	}

	review := ReviewLoopExecutionArtifact(artifact)

	if review.ReleaseGate != LoopExecutionReleaseGateBlocked {
		t.Fatalf("release gate = %q, want %q", review.ReleaseGate, LoopExecutionReleaseGateBlocked)
	}
	wantCounts := []LoopExecutionOutcomeCount{
		{Action: LoopActionRegister, Status: LoopExecutionStatusBlocked, Count: 1},
		{Action: LoopActionHeartbeat, Status: LoopExecutionStatusCompleted, Count: 1},
		{Action: LoopActionHeartbeat, Status: LoopExecutionStatusBlocked, Count: 1},
		{Action: "rotate_keys", Status: LoopExecutionStatusBlocked, Count: 1},
	}
	if !reflect.DeepEqual(review.ActionOutcomeCounts, wantCounts) {
		t.Fatalf("counts = %+v, want %+v", review.ActionOutcomeCounts, wantCounts)
	}
	wantRollup := []LoopExecutionIssueRollup{
		{IssueCode: "control_plane_unavailable", Count: 2, Actions: []string{LoopActionRegister, LoopActionHeartbeat}},
		{IssueCode: LoopExecutionIssueUnknownAction, Count: 1, Actions: []string{"rotate_keys"}},
	}
	if !reflect.DeepEqual(review.IssueRollup, wantRollup) {
		t.Fatalf("issue rollup = %+v, want %+v", review.IssueRollup, wantRollup)
	}
	wantRetry := LoopExecutionRetryRecommendation{Action: LoopExecutionRetryRecommendationBackoff, Delay: 7 * time.Second, IssueCode: LoopExecutionIssueUnknownAction}
	if review.RetryRecommendation != wantRetry {
		t.Fatalf("retry recommendation = %+v, want %+v", review.RetryRecommendation, wantRetry)
	}
}

func TestReviewLoopExecutionArtifactUsesStableJSONFields(t *testing.T) {
	artifact := LoopExecutionArtifact{
		Status: LoopExecutionStatusBlocked,
		ActionOutcomes: []LoopExecutionActionOutcome{
			{Action: LoopActionHeartbeat, Status: LoopExecutionStatusBlocked, IssueCode: "heartbeat_rejected"},
		},
		IssueCodes: []string{"heartbeat_rejected"},
	}

	encoded, err := json.Marshal(ReviewLoopExecutionArtifact(artifact))
	if err != nil {
		t.Fatalf("marshal review: %v", err)
	}
	if !json.Valid(encoded) {
		t.Fatalf("review json is invalid: %s", encoded)
	}
	want := `{"release_gate":"blocked","action_outcome_counts":[{"action":"heartbeat","status":"blocked","count":1}],"issue_rollup":[{"issue_code":"heartbeat_rejected","count":1,"actions":["heartbeat"]}],"retry_recommendation":{"action":"retry","delay":0,"issue_code":"heartbeat_rejected"}}`
	if string(encoded) != want {
		t.Fatalf("json = %s, want %s", encoded, want)
	}
}
