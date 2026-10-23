package supervisor

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeLoopActionExecutor struct {
	registerOutcome     LoopInjectedOutcome
	registerErr         error
	heartbeatOutcome    LoopInjectedOutcome
	heartbeatErr        error
	fetchDesiredOutcome LoopInjectedOutcome
	fetchDesiredErr     error
	calls               []string
}

func (f *fakeLoopActionExecutor) Register(context.Context) (LoopInjectedOutcome, error) {
	f.calls = append(f.calls, LoopActionRegister)
	return f.registerOutcome, f.registerErr
}

func (f *fakeLoopActionExecutor) Heartbeat(context.Context) (LoopInjectedOutcome, error) {
	f.calls = append(f.calls, LoopActionHeartbeat)
	return f.heartbeatOutcome, f.heartbeatErr
}

func (f *fakeLoopActionExecutor) FetchDesired(context.Context) (LoopInjectedOutcome, error) {
	f.calls = append(f.calls, LoopActionFetchDesired)
	return f.fetchDesiredOutcome, f.fetchDesiredErr
}

func TestExecutePlannedLoopActionsRecordsSuccessfulInjectedActions(t *testing.T) {
	executor := &fakeLoopActionExecutor{
		registerOutcome:     LoopInjectedOutcome{Message: "registered"},
		heartbeatOutcome:    LoopInjectedOutcome{NextDelay: 2 * time.Second},
		fetchDesiredOutcome: LoopInjectedOutcome{Message: "etag updated"},
	}
	planned := []LoopPlannerDecision{
		{Action: LoopActionRegister, Reason: "agent is not registered"},
		{Action: LoopActionHeartbeat, Reason: "heartbeat is due"},
		{Action: LoopActionFetchDesired, Reason: "desired-state cache is stale"},
		{Action: LoopActionNoop, Reason: "no loop action is due", Delay: 5 * time.Second},
	}

	artifact := ExecutePlannedLoopActions(context.Background(), planned, executor)

	if artifact.Status != LoopExecutionStatusCompleted {
		t.Fatalf("status = %q, want %q", artifact.Status, LoopExecutionStatusCompleted)
	}
	if artifact.NextDelay != 5*time.Second {
		t.Fatalf("next delay = %s", artifact.NextDelay)
	}
	if len(artifact.IssueCodes) != 0 {
		t.Fatalf("issue codes = %+v", artifact.IssueCodes)
	}
	wantCalls := []string{LoopActionRegister, LoopActionHeartbeat, LoopActionFetchDesired}
	if !reflect.DeepEqual(executor.calls, wantCalls) {
		t.Fatalf("calls = %+v, want %+v", executor.calls, wantCalls)
	}
	for _, outcome := range artifact.ActionOutcomes {
		if outcome.Status != LoopExecutionStatusCompleted {
			t.Fatalf("outcome = %+v", outcome)
		}
	}
}

func TestExecutePlannedLoopActionsRecordsFailuresAndSkipsBackoffNoop(t *testing.T) {
	executor := &fakeLoopActionExecutor{
		registerErr: errors.New("control plane unavailable"),
		heartbeatOutcome: LoopInjectedOutcome{
			IssueCode: "heartbeat_rejected",
			NextDelay: 3 * time.Second,
		},
		heartbeatErr: errors.New("sequence rejected"),
	}
	planned := []LoopPlannerDecision{
		{Action: LoopActionBackoff, Reason: "last heartbeat failed", Delay: 10 * time.Second},
		{Action: LoopActionRegister, Reason: "agent is not registered"},
		{Action: LoopActionHeartbeat, Reason: "heartbeat is due"},
		{Action: LoopActionNoop, Reason: "no loop action is due", Delay: time.Second},
	}

	artifact := ExecutePlannedLoopActions(context.Background(), planned, executor)

	if artifact.Status != LoopExecutionStatusBlocked {
		t.Fatalf("status = %q, want %q", artifact.Status, LoopExecutionStatusBlocked)
	}
	if artifact.NextDelay != 10*time.Second {
		t.Fatalf("next delay = %s", artifact.NextDelay)
	}
	wantIssues := []string{"heartbeat_rejected", LoopExecutionIssueRegisterFailed}
	if !reflect.DeepEqual(artifact.IssueCodes, wantIssues) {
		t.Fatalf("issue codes = %+v, want %+v", artifact.IssueCodes, wantIssues)
	}
	wantCalls := []string{LoopActionRegister, LoopActionHeartbeat}
	if !reflect.DeepEqual(executor.calls, wantCalls) {
		t.Fatalf("calls = %+v, want %+v", executor.calls, wantCalls)
	}
	if artifact.ActionOutcomes[0].Status != LoopExecutionStatusCompleted || artifact.ActionOutcomes[3].Status != LoopExecutionStatusCompleted {
		t.Fatalf("backoff/noop outcomes = %+v / %+v", artifact.ActionOutcomes[0], artifact.ActionOutcomes[3])
	}
	if artifact.ActionOutcomes[1].Message != "control plane unavailable" {
		t.Fatalf("register outcome = %+v", artifact.ActionOutcomes[1])
	}
	if artifact.ActionOutcomes[2].IssueCode != "heartbeat_rejected" || artifact.ActionOutcomes[2].Message != "sequence rejected" {
		t.Fatalf("heartbeat outcome = %+v", artifact.ActionOutcomes[2])
	}
}

func TestExecutePlannedLoopActionsWithoutExecutorReturnsPartialArtifact(t *testing.T) {
	artifact := ExecutePlannedLoopActions(context.Background(), []LoopPlannerDecision{{Action: LoopActionFetchDesired}}, nil)

	if artifact.Status != LoopExecutionStatusPartial {
		t.Fatalf("status = %q, want %q", artifact.Status, LoopExecutionStatusPartial)
	}
	wantIssues := []string{LoopExecutionIssueMissingFetchDesiredOutcome}
	if !reflect.DeepEqual(artifact.IssueCodes, wantIssues) {
		t.Fatalf("issue codes = %+v, want %+v", artifact.IssueCodes, wantIssues)
	}
}
