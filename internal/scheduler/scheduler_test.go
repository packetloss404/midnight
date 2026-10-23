package scheduler

import (
	"testing"
	"time"
)

func TestPlanRespectsPerProjectQuota(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	policy := Policy{DefaultQuota: Quota{InFlight: 1}}
	requests := []Request{
		{ID: "r1", ProjectID: "p", Class: JobClassDeploy},
		{ID: "r2", ProjectID: "p", Class: JobClassDeploy},
	}
	plan, err := Plan(now, policy, requests)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Decisions[0].Outcome != OutcomeAccepted {
		t.Fatalf("first request should be accepted, got %s", plan.Decisions[0].Outcome)
	}
	if plan.Decisions[1].Outcome != OutcomeQuotaDenied {
		t.Fatalf("second request should be quota-denied, got %s", plan.Decisions[1].Outcome)
	}
}

func TestPlanProjectQuotaOverride(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	policy := Policy{
		DefaultQuota: Quota{InFlight: 1},
		ProjectQuota: map[string]Quota{"vip": {InFlight: 3}},
	}
	requests := []Request{
		{ID: "v1", ProjectID: "vip", Class: JobClassDeploy},
		{ID: "v2", ProjectID: "vip", Class: JobClassDeploy},
		{ID: "v3", ProjectID: "vip", Class: JobClassDeploy},
		{ID: "p1", ProjectID: "p", Class: JobClassDeploy},
		{ID: "p2", ProjectID: "p", Class: JobClassDeploy},
	}
	plan, err := Plan(now, policy, requests)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	accepted := 0
	denied := 0
	for _, decision := range plan.Decisions {
		if decision.Outcome == OutcomeAccepted {
			accepted++
		}
		if decision.Outcome == OutcomeQuotaDenied {
			denied++
		}
	}
	if accepted != 4 || denied != 1 {
		t.Fatalf("expected 4 accepted and 1 denied, got %d/%d", accepted, denied)
	}
}

func TestPlanBackoffTransient(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	policy := Policy{BackoffBase: time.Second, BackoffMax: 10 * time.Second}
	plan, err := Plan(now, policy, []Request{{ID: "r", Class: JobClassDeploy, LastFailure: BackoffClassTransient, Attempt: 2}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Decisions[0].Outcome != OutcomeBackoff {
		t.Fatalf("expected backoff outcome, got %s", plan.Decisions[0].Outcome)
	}
	if plan.Decisions[0].Backoff < 2*time.Second || plan.Decisions[0].Backoff > 10*time.Second {
		t.Fatalf("unexpected backoff %v", plan.Decisions[0].Backoff)
	}
}

func TestPlanPermanentRejected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	plan, err := Plan(now, Policy{}, []Request{{ID: "r", Class: JobClassBuild, LastFailure: BackoffClassPermanent}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Decisions[0].Outcome != OutcomeRejected {
		t.Fatalf("expected rejected, got %s", plan.Decisions[0].Outcome)
	}
}

func TestPlanPriorityOrdering(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	policy := Policy{DefaultQuota: Quota{InFlight: 1}}
	requests := []Request{
		{ID: "low", ProjectID: "p", Class: JobClassDeploy, Priority: 1, SubmittedAt: now},
		{ID: "hi", ProjectID: "p", Class: JobClassDeploy, Priority: 10, SubmittedAt: now.Add(time.Minute)},
	}
	plan, err := Plan(now, policy, requests)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Decisions[0].RequestID != "hi" || plan.Decisions[0].Outcome != OutcomeAccepted {
		t.Fatalf("expected high-priority to win, got %+v", plan.Decisions)
	}
}

func TestPlanRequiresRequestID(t *testing.T) {
	_, err := Plan(time.Now(), Policy{}, []Request{{ID: " "}})
	if err == nil {
		t.Fatal("expected error for empty request id")
	}
}
