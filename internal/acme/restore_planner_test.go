package acme

import (
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestPlanRestoreReissuePrioritizesAndThrottles(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	candidates := []RestoreCandidate{
		{Domain: "h1.example.test", RegisteredDomain: "example.test", Priority: RestorePriorityHealthy, ExpiresAt: now.Add(24 * time.Hour)},
		{Domain: "expired.example.test", RegisteredDomain: "example.test", Priority: RestorePriorityExpired, ExpiresAt: now.Add(-time.Hour)},
		{Domain: "soon.example.test", RegisteredDomain: "example.test", Priority: RestorePriorityExpiringSoon, ExpiresAt: now.Add(2 * time.Hour)},
	}

	plan, err := PlanRestoreReissue(RestorePlanInput{
		Now:        now,
		Candidates: candidates,
	})
	if err != nil {
		t.Fatalf("PlanRestoreReissue: %v", err)
	}
	if len(plan.Slots) != 3 || plan.Scheduled != 3 || plan.BudgetExhausted != 0 {
		t.Fatalf("plan counts unexpected: %#v", plan)
	}

	want := []string{"expired.example.test", "soon.example.test", "h1.example.test"}
	for i, name := range want {
		if plan.Slots[i].Domain != name {
			t.Fatalf("slot %d = %q, want %q", i, plan.Slots[i].Domain, name)
		}
		if plan.Slots[i].Slot != i {
			t.Fatalf("slot %d index = %d, want %d", i, plan.Slots[i].Slot, i)
		}
		if plan.Slots[i].NotBefore != now {
			t.Fatalf("slot %d NotBefore = %s, want %s (within first hour)", i, plan.Slots[i].NotBefore, now)
		}
	}
}

func TestPlanRestoreReissueGroupsHourlyByCertsPerHour(t *testing.T) {
	now := time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)
	candidates := make([]RestoreCandidate, 25)
	for i := range candidates {
		candidates[i] = RestoreCandidate{
			Domain:           string(rune('a'+i)) + ".example.test",
			RegisteredDomain: "example.test",
			Priority:         RestorePriorityHealthy,
			ExpiresAt:        now.Add(24 * time.Hour),
		}
	}
	plan, err := PlanRestoreReissue(RestorePlanInput{
		Now:          now,
		Candidates:   candidates,
		CertsPerHour: 10,
	})
	if err != nil {
		t.Fatalf("PlanRestoreReissue: %v", err)
	}
	if plan.Scheduled != 25 || plan.BudgetExhausted != 0 {
		t.Fatalf("plan counts unexpected: %#v", plan)
	}
	if got := plan.Slots[0].NotBefore; !got.Equal(now) {
		t.Fatalf("slot 0 NotBefore = %s, want %s", got, now)
	}
	if got := plan.Slots[10].NotBefore; !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("slot 10 NotBefore = %s, want %s", got, now.Add(time.Hour))
	}
	if got := plan.Slots[20].NotBefore; !got.Equal(now.Add(2 * time.Hour)) {
		t.Fatalf("slot 20 NotBefore = %s, want %s", got, now.Add(2*time.Hour))
	}
}

func TestPlanRestoreReissueRefusesWhenWeeklyBudgetExceeded(t *testing.T) {
	now := time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)
	candidates := []RestoreCandidate{
		{Domain: "a.example.test", RegisteredDomain: "example.test", Priority: RestorePriorityHealthy, ExpiresAt: now.Add(24 * time.Hour)},
		{Domain: "b.example.test", RegisteredDomain: "example.test", Priority: RestorePriorityHealthy, ExpiresAt: now.Add(24 * time.Hour)},
	}
	plan, err := PlanRestoreReissue(RestorePlanInput{
		Now:                   now,
		Candidates:            candidates,
		WeeklyBudget:          1,
		AlreadyIssuedThisWeek: map[string]int{"example.test": 1},
	})
	if err != nil {
		t.Fatalf("PlanRestoreReissue: %v", err)
	}
	if plan.Scheduled != 0 || plan.BudgetExhausted != 2 {
		t.Fatalf("expected all candidates refused, got %#v", plan)
	}
	for _, slot := range plan.Slots {
		if slot.Status != RestoreSlotBudgetExhausted {
			t.Fatalf("status = %s, want %s", slot.Status, RestoreSlotBudgetExhausted)
		}
		if slot.Reason != failurecodes.ACMERestoreBudgetExhausted {
			t.Fatalf("reason = %s, want %s", slot.Reason, failurecodes.ACMERestoreBudgetExhausted)
		}
		if slot.RecoveryHint == "" {
			t.Fatal("expected non-empty recovery hint")
		}
	}
}

func TestPlanRestoreReissueRequiresDomain(t *testing.T) {
	if _, err := PlanRestoreReissue(RestorePlanInput{Candidates: []RestoreCandidate{{}}}); err == nil {
		t.Fatal("expected error when candidate domain missing")
	}
}

func TestPlanRestoreReissueDoesNotMutateInput(t *testing.T) {
	now := time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)
	already := map[string]int{"example.test": 0}
	original := map[string]int{"example.test": 0}
	if _, err := PlanRestoreReissue(RestorePlanInput{
		Now: now,
		Candidates: []RestoreCandidate{
			{Domain: "a.example.test", RegisteredDomain: "example.test", ExpiresAt: now.Add(time.Hour)},
		},
		AlreadyIssuedThisWeek: already,
	}); err != nil {
		t.Fatalf("PlanRestoreReissue: %v", err)
	}
	if already["example.test"] != original["example.test"] {
		t.Fatalf("AlreadyIssuedThisWeek mutated: %v", already)
	}
}
