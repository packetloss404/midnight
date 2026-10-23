package scheduler

import (
	"errors"
	"testing"
)

func TestPlanStatelessServiceSpreadsAcrossReadyHosts(t *testing.T) {
	plan, err := PlanStatelessService(StatelessServiceRequest{
		ID:        "req-1",
		ProjectID: "project-1",
		ServiceID: "svc-1",
		Replicas:  3,
		Stateless: true,
	}, []Host{
		{ID: "host-b", Status: HostStatusReady, Capacity: 2},
		{ID: "host-a", Status: HostStatusReady, Capacity: 2},
		{ID: "host-c", Status: HostStatusDraining, Capacity: 10},
	})
	if err != nil {
		t.Fatalf("PlanStatelessService: %v", err)
	}

	wantHosts := []string{"host-a", "host-b", "host-a"}
	for i, want := range wantHosts {
		if plan.Placements[i].Outcome != OutcomeAccepted || plan.Placements[i].HostID != want {
			t.Fatalf("placement[%d] = %+v, want host %q accepted", i, plan.Placements[i], want)
		}
	}
}

func TestPlanStatelessServiceHonorsLabelsAndCapacity(t *testing.T) {
	plan, err := PlanStatelessService(StatelessServiceRequest{
		ID:             "req-1",
		ServiceID:      "svc-1",
		Replicas:       2,
		Stateless:      true,
		RequiredLabels: map[string]string{"pool": "web"},
	}, []Host{
		{ID: "host-a", Status: HostStatusReady, Capacity: 1, Labels: map[string]string{"pool": "web"}},
		{ID: "host-b", Status: HostStatusReady, Capacity: 3, Labels: map[string]string{"pool": "worker"}},
	})
	if err != nil {
		t.Fatalf("PlanStatelessService: %v", err)
	}
	if plan.Placements[0].HostID != "host-a" || plan.Placements[0].Outcome != OutcomeAccepted {
		t.Fatalf("first placement = %+v, want host-a accepted", plan.Placements[0])
	}
	if plan.Placements[1].Outcome != OutcomeQuotaDenied {
		t.Fatalf("second placement = %+v, want quota denied", plan.Placements[1])
	}
	if len(plan.Notes) != 1 || plan.Notes[0].Code != "scheduler.multihost_capacity" {
		t.Fatalf("notes = %+v, want capacity note", plan.Notes)
	}
}

func TestPlanStatelessServiceAccountsForExistingHostLoad(t *testing.T) {
	plan, err := PlanStatelessService(StatelessServiceRequest{
		ID:        "req-1",
		ServiceID: "svc-1",
		Replicas:  2,
		Stateless: true,
	}, []Host{
		{ID: "host-a", Status: HostStatusReady, Capacity: 5, Used: 4},
		{ID: "host-b", Status: HostStatusReady, Capacity: 5},
	})
	if err != nil {
		t.Fatalf("PlanStatelessService: %v", err)
	}

	for i, placement := range plan.Placements {
		if placement.HostID != "host-b" || placement.Outcome != OutcomeAccepted {
			t.Fatalf("placement[%d] = %+v, want host-b accepted", i, placement)
		}
	}
}

func TestPlanStatelessServiceRejectsStateful(t *testing.T) {
	_, err := PlanStatelessService(StatelessServiceRequest{ID: "req-1", ServiceID: "svc-1", Replicas: 1}, nil)
	if !errors.Is(err, ErrStatefulServiceUnsupported) {
		t.Fatalf("error = %v, want ErrStatefulServiceUnsupported", err)
	}
}

func TestPlanAgentCanaryRolloutSelectsDeterministicBatch(t *testing.T) {
	plan, err := PlanAgentCanaryRollout(CanaryPolicy{TargetVersion: "v1.5.0", MaxBatch: 2, MaxUnavailable: 2}, []Host{
		{ID: "host-c", AgentVersion: "v1.4.0", Status: HostStatusReady},
		{ID: "host-a", AgentVersion: "v1.4.0", Status: HostStatusReady},
		{ID: "host-b", AgentVersion: "v1.5.0", Status: HostStatusReady},
		{ID: "host-d", AgentVersion: "v1.4.0", Status: HostStatusDraining},
	})
	if err != nil {
		t.Fatalf("PlanAgentCanaryRollout: %v", err)
	}
	if len(plan.Updates) != 1 || plan.Updates[0].HostID != "host-a" || plan.Updates[0].ToVersion != "v1.5.0" {
		t.Fatalf("updates = %+v, want only host-a within remaining unavailable budget", plan.Updates)
	}
	if len(plan.Holdouts) != 2 || plan.Holdouts[0].HostID != "host-d" || plan.Holdouts[1].HostID != "host-c" {
		t.Fatalf("holdouts = %+v, want draining then outside-batch holdouts", plan.Holdouts)
	}
}

func TestPlanAgentCanaryRolloutHoldsWhenBudgetExhausted(t *testing.T) {
	plan, err := PlanAgentCanaryRollout(CanaryPolicy{TargetVersion: "v1.5.0", MaxBatch: 2, MaxUnavailable: 1}, []Host{
		{ID: "host-a", AgentVersion: "v1.4.0", Status: HostStatusReady},
		{ID: "host-b", AgentVersion: "v1.4.0", Status: HostStatusOffline},
	})
	if err != nil {
		t.Fatalf("PlanAgentCanaryRollout: %v", err)
	}
	if len(plan.Updates) != 0 {
		t.Fatalf("updates = %+v, want none", plan.Updates)
	}
	if len(plan.Notes) != 1 || plan.Notes[0].Code != "scheduler.canary_budget_exhausted" {
		t.Fatalf("notes = %+v, want budget exhausted note", plan.Notes)
	}
}

func TestPlanAgentCanaryRolloutHoldsHostsWithoutIDs(t *testing.T) {
	plan, err := PlanAgentCanaryRollout(CanaryPolicy{TargetVersion: "v1.5.0", MaxBatch: 2, MaxUnavailable: 2}, []Host{
		{AgentVersion: "v1.4.0", Status: HostStatusReady},
		{ID: "host-a", AgentVersion: "v1.4.0", Status: HostStatusReady},
	})
	if err != nil {
		t.Fatalf("PlanAgentCanaryRollout: %v", err)
	}
	if len(plan.Updates) != 1 || plan.Updates[0].HostID != "host-a" {
		t.Fatalf("updates = %+v, want host-a only", plan.Updates)
	}
	if len(plan.Holdouts) != 1 || plan.Holdouts[0].Reason != "host id is required for canary" {
		t.Fatalf("holdouts = %+v, want missing-id holdout", plan.Holdouts)
	}
}
