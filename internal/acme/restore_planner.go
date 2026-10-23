package acme

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

// RestoreCandidatePriority enumerates the panel-locked priority bands used to
// order the restore reissue queue. Higher priority bands (lower numeric value)
// are scheduled first.
type RestoreCandidatePriority int

const (
	// RestorePriorityExpired marks certificates that are already past NotAfter.
	RestorePriorityExpired RestoreCandidatePriority = 0
	// RestorePriorityExpiringSoon marks certificates within the renew-soon window.
	RestorePriorityExpiringSoon RestoreCandidatePriority = 1
	// RestorePriorityHealthy marks certificates with no urgent issue.
	RestorePriorityHealthy RestoreCandidatePriority = 2
)

// RestoreCandidate is one certificate restore request fed to the planner.
type RestoreCandidate struct {
	// Domain is the FQDN being restored. It is normalized to lowercase.
	Domain string
	// RegisteredDomain is the eTLD+1 used for weekly budget accounting.
	RegisteredDomain string
	// ExpiresAt is the certificate's NotAfter timestamp.
	ExpiresAt time.Time
	// Priority is computed by the caller using ClassifyCertificateExpiry or
	// any other policy. Healthy is the default.
	Priority RestoreCandidatePriority
}

// RestorePlanInput describes one batch planning call.
type RestorePlanInput struct {
	// Now is the planner's reference time. When zero, time.Now in UTC is used.
	Now time.Time
	// Candidates is the unsorted set of restore requests for this batch.
	Candidates []RestoreCandidate
	// AlreadyIssuedThisWeek records the per-registered-domain count consumed
	// before the batch starts; the planner adds to this number.
	AlreadyIssuedThisWeek map[string]int
	// CertsPerHour overrides RestoreCertificatesPerHour when positive.
	CertsPerHour int
	// WeeklyBudget overrides RegisteredDomainCertificatesPerWeek when positive.
	WeeklyBudget int
}

// RestoreSlotPlan is the planner's per-candidate decision.
type RestoreSlotPlan struct {
	// Domain is the candidate's normalized FQDN.
	Domain string `json:"domain"`
	// RegisteredDomain is the eTLD+1 used for budget accounting.
	RegisteredDomain string `json:"registeredDomain"`
	// Priority echoes the candidate's priority band.
	Priority RestoreCandidatePriority `json:"priority"`
	// Slot is the zero-based ordering within the throttled queue.
	Slot int `json:"slot"`
	// NotBefore is when the reconciler is allowed to reissue the certificate.
	NotBefore time.Time `json:"notBefore"`
	// Status reports whether the slot is scheduled, throttled by weekly
	// budget, or refused entirely.
	Status RestoreSlotStatus `json:"status"`
	// Reason is the registered failure code emitted when Status != scheduled.
	Reason failurecodes.Code `json:"reason,omitempty"`
	// RecoveryHint is the canonical hint emitted with Reason.
	RecoveryHint string `json:"recoveryHint,omitempty"`
}

// RestoreSlotStatus enumerates the three planner outcomes.
type RestoreSlotStatus string

const (
	// RestoreSlotScheduled marks a slot allowed to proceed.
	RestoreSlotScheduled RestoreSlotStatus = "scheduled"
	// RestoreSlotBudgetExhausted marks a slot blocked by the weekly per-domain budget.
	RestoreSlotBudgetExhausted RestoreSlotStatus = "budget-exhausted"
)

// RestorePlan is the planner output. It carries the per-slot plan, the new
// per-registered-domain counts after applying scheduled slots, and aggregate
// counters useful for operator-facing summaries.
type RestorePlan struct {
	// Slots is the per-candidate plan, sorted by (priority, expiresAt, domain).
	Slots []RestoreSlotPlan `json:"slots"`
	// IssuedAfterPlan is the per-registered-domain count after applying the
	// scheduled slots (already-issued + scheduled count).
	IssuedAfterPlan map[string]int `json:"issuedAfterPlan"`
	// Scheduled is the number of slots with status RestoreSlotScheduled.
	Scheduled int `json:"scheduled"`
	// BudgetExhausted is the number of slots refused by the weekly budget.
	BudgetExhausted int `json:"budgetExhausted"`
}

// PlanRestoreReissue computes a deterministic restore plan: certs are sorted
// by priority then expiry then domain, hourly slots are assigned at
// CertsPerHour pace, and the per-registered-domain weekly budget is enforced.
// Slots that would breach the weekly budget are emitted with status
// budget-exhausted and reason ACME_RESTORE_BUDGET_EXHAUSTED.
func PlanRestoreReissue(input RestorePlanInput) (RestorePlan, error) {
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	certsPerHour := input.CertsPerHour
	if certsPerHour <= 0 {
		certsPerHour = RestoreCertificatesPerHour
	}
	weeklyBudget := input.WeeklyBudget
	if weeklyBudget <= 0 {
		weeklyBudget = RegisteredDomainCertificatesPerWeek
	}

	candidates := append([]RestoreCandidate(nil), input.Candidates...)
	for i, c := range candidates {
		candidates[i].Domain = normalizeDomain(c.Domain)
		candidates[i].RegisteredDomain = normalizeRegisteredDomain(c.RegisteredDomain)
		if candidates[i].RegisteredDomain == "" && candidates[i].Domain != "" {
			candidates[i].RegisteredDomain = candidates[i].Domain
		}
		if candidates[i].Domain == "" {
			return RestorePlan{}, errors.New("restore candidate domain is required")
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		if !candidates[i].ExpiresAt.Equal(candidates[j].ExpiresAt) {
			return candidates[i].ExpiresAt.Before(candidates[j].ExpiresAt)
		}
		return strings.Compare(candidates[i].Domain, candidates[j].Domain) < 0
	})

	issued := make(map[string]int, len(input.AlreadyIssuedThisWeek)+len(candidates))
	for k, v := range input.AlreadyIssuedThisWeek {
		issued[normalizeRegisteredDomain(k)] = v
	}

	plan := RestorePlan{Slots: make([]RestoreSlotPlan, 0, len(candidates)), IssuedAfterPlan: issued}
	scheduledIndex := 0
	for _, candidate := range candidates {
		slot := RestoreSlotPlan{
			Domain:           candidate.Domain,
			RegisteredDomain: candidate.RegisteredDomain,
			Priority:         candidate.Priority,
		}
		if issued[candidate.RegisteredDomain] >= weeklyBudget {
			slot.Status = RestoreSlotBudgetExhausted
			slot.Reason = failurecodes.ACMERestoreBudgetExhausted
			slot.RecoveryHint = failurecodes.RecoveryHint(failurecodes.ACMERestoreBudgetExhausted)
			plan.BudgetExhausted++
			plan.Slots = append(plan.Slots, slot)
			continue
		}
		slot.Status = RestoreSlotScheduled
		slot.Slot = scheduledIndex
		hours := scheduledIndex / certsPerHour
		slot.NotBefore = now.Add(time.Duration(hours) * time.Hour)
		issued[candidate.RegisteredDomain]++
		scheduledIndex++
		plan.Scheduled++
		plan.Slots = append(plan.Slots, slot)
	}

	return plan, nil
}
