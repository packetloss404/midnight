package supervisor

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/midnight/midnight/cmd/midnight-agent/internal/protocol"
)

type plannerFixedRandom float64

func (r plannerFixedRandom) Float64() float64 { return float64(r) }

func TestPlanNextLoopActionRegistersWhenMissingState(t *testing.T) {
	decision := PlanNextLoopAction(LoopPlannerInput{})

	if decision.Action != LoopActionRegister || decision.Delay != 0 || decision.Reason == "" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestPlanNextLoopActionBacksOffAfterHeartbeatFailure(t *testing.T) {
	decision := PlanNextLoopAction(LoopPlannerInput{
		Config: LoopPlannerConfig{HeartbeatBackoff: protocol.RetryBackoff{Base: time.Second, Max: 10 * time.Second, JitterFraction: 0.2}},
		Registration: RegistrationState{
			Registered: true,
			AgentID:    "agent-a",
		},
		Heartbeat: HeartbeatOutcome{Attempted: true, OK: false, RetryAttempt: 1},
		Random:    plannerFixedRandom(0.5),
	})

	if decision.Action != LoopActionBackoff || decision.Delay != 2*time.Second || decision.Reason == "" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestPlanNextLoopActionHeartbeatsBeforeDesiredFetch(t *testing.T) {
	decision := PlanNextLoopAction(LoopPlannerInput{
		Config: LoopPlannerConfig{HeartbeatInterval: 10 * time.Second, DesiredMaxAge: time.Minute},
		Registration: RegistrationState{
			Registered: true,
			AgentID:    "agent-a",
		},
		DesiredCacheAge: 2 * time.Minute,
		Heartbeat:       HeartbeatOutcome{Attempted: true, OK: true, LastSuccessAge: 10 * time.Second},
	})

	if decision.Action != LoopActionHeartbeat || decision.Delay != 0 {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestPlanNextLoopActionFetchesStaleDesiredState(t *testing.T) {
	decision := PlanNextLoopAction(LoopPlannerInput{
		Config: LoopPlannerConfig{HeartbeatInterval: 10 * time.Second, DesiredMaxAge: time.Minute},
		Registration: RegistrationState{
			Registered: true,
			AgentID:    "agent-a",
		},
		DesiredCacheAge: time.Minute,
		Heartbeat:       HeartbeatOutcome{Attempted: true, OK: true, LastSuccessAge: time.Second},
	})

	if decision.Action != LoopActionFetchDesired || decision.Delay != 0 || decision.Reason == "" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestPlanNextLoopActionSuppressesDesiredFetchDuringMaintenance(t *testing.T) {
	decision := PlanNextLoopAction(LoopPlannerInput{
		Config: LoopPlannerConfig{HeartbeatInterval: 10 * time.Second, DesiredMaxAge: time.Minute},
		Registration: RegistrationState{
			Registered: true,
			AgentID:    "agent-a",
		},
		DesiredCacheAge: time.Minute,
		Heartbeat:       HeartbeatOutcome{Attempted: true, OK: true, LastSuccessAge: time.Second},
		MaintenanceMode: true,
	})

	if decision.Action != LoopActionNoop || decision.Delay != 9*time.Second || decision.Reason != "agent is in maintenance mode" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestPlanNextLoopActionStillHeartbeatsDuringMaintenance(t *testing.T) {
	decision := PlanNextLoopAction(LoopPlannerInput{
		Config: LoopPlannerConfig{HeartbeatInterval: 10 * time.Second, DesiredMaxAge: time.Minute},
		Registration: RegistrationState{
			Registered: true,
			AgentID:    "agent-a",
		},
		DesiredCacheAge: time.Minute,
		Heartbeat:       HeartbeatOutcome{Attempted: true, OK: true, LastSuccessAge: 10 * time.Second},
		DrainMode:       true,
	})

	if decision.Action != LoopActionHeartbeat || decision.Reason == "" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestPlanNextLoopActionNoopsUntilNextDueAction(t *testing.T) {
	decision := PlanNextLoopAction(LoopPlannerInput{
		Config: LoopPlannerConfig{HeartbeatInterval: 30 * time.Second, DesiredMaxAge: time.Minute},
		Registration: RegistrationState{
			Registered: true,
			AgentID:    "agent-a",
		},
		DesiredCacheAge: 10 * time.Second,
		Heartbeat:       HeartbeatOutcome{Attempted: true, OK: true, LastSuccessAge: 5 * time.Second},
	})

	if decision.Action != LoopActionNoop || decision.Delay != 25*time.Second || decision.Reason == "" {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestSummarizeLoopDecisionsCountsAndPendingWork(t *testing.T) {
	summary := SummarizeLoopDecisions([]LoopPlannerDecision{
		{Action: LoopActionNoop, Delay: time.Second},
		{Action: LoopActionHeartbeat},
		{Action: LoopActionBackoff, Delay: 5 * time.Second},
		{Action: LoopActionFetchDesired, Delay: 2 * time.Second},
		{Action: LoopActionRegister},
		{Action: LoopActionHeartbeat, Delay: 3 * time.Second},
	})

	wantCounts := []LoopActionCount{
		{Action: LoopActionRegister, Count: 1},
		{Action: LoopActionHeartbeat, Count: 2},
		{Action: LoopActionFetchDesired, Count: 1},
		{Action: LoopActionBackoff, Count: 1},
		{Action: LoopActionNoop, Count: 1},
	}
	if !reflect.DeepEqual(summary.Counts, wantCounts) {
		t.Fatalf("counts = %+v, want %+v", summary.Counts, wantCounts)
	}
	if summary.NextDelayMax != 5*time.Second {
		t.Fatalf("next delay max = %s", summary.NextDelayMax)
	}
	if !summary.RegistrationWorkPending || !summary.DesiredStateWorkPending || !summary.HeartbeatWorkPending {
		t.Fatalf("pending flags = %+v", summary)
	}
}

func TestSummarizeLoopDecisionsEmptySequence(t *testing.T) {
	summary := SummarizeLoopDecisions(nil)

	if len(summary.Counts) != 0 || summary.NextDelayMax != 0 || summary.RegistrationWorkPending || summary.DesiredStateWorkPending || summary.HeartbeatWorkPending {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestSummarizeLoopDecisionsForModeAnnotatesMaintenanceAndDrain(t *testing.T) {
	summary := SummarizeLoopDecisionsForMode([]LoopPlannerDecision{{Action: LoopActionNoop}}, false, true)

	if !summary.MaintenanceMode || !summary.DrainMode || summary.DesiredStateWorkPending {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestSummarizeLoopDecisionsSortsUnknownActionsAfterKnownActions(t *testing.T) {
	summary := SummarizeLoopDecisions([]LoopPlannerDecision{
		{Action: "zeta"},
		{Action: LoopActionNoop},
		{Action: "alpha"},
		{Action: "zeta"},
	})

	wantCounts := []LoopActionCount{
		{Action: LoopActionNoop, Count: 1},
		{Action: "alpha", Count: 1},
		{Action: "zeta", Count: 2},
	}
	if !reflect.DeepEqual(summary.Counts, wantCounts) {
		t.Fatalf("counts = %+v, want %+v", summary.Counts, wantCounts)
	}
}

func TestLoopDecisionSummaryJSONUsesStableFieldNames(t *testing.T) {
	summary := SummarizeLoopDecisions([]LoopPlannerDecision{
		{Action: LoopActionHeartbeat, Delay: 2 * time.Second},
	})

	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	want := `{"counts":[{"action":"heartbeat","count":1}],"next_delay_max":2000000000,"registration_work_pending":false,"desired_state_work_pending":false,"heartbeat_work_pending":true}`
	if string(encoded) != want {
		t.Fatalf("json = %s, want %s", encoded, want)
	}
}
