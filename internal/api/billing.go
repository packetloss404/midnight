package api

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/midnight/midnight/internal/abuse"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/limits"
	"github.com/midnight/midnight/internal/metering"
)

const billingMeteringLimitsAbusePlanSchemaVersion = "midnight.dev/billing-metering-limits-abuse-plan/v1alpha1"

type billingMeteringLimitsAbusePlanRequest struct {
	WindowStart     time.Time               `json:"window_start"`
	WindowEnd       time.Time               `json:"window_end"`
	Events          []metering.UsageEvent   `json:"events"`
	PlanAssignments []limits.PlanAssignment `json:"plan_assignments"`
	Activities      []abuse.Activity        `json:"activities,omitempty"`
}

type billingMeteringLimitsAbusePlan struct {
	SchemaVersion     string                 `json:"schema_version"`
	Status            string                 `json:"status"`
	WindowStart       time.Time              `json:"window_start"`
	WindowEnd         time.Time              `json:"window_end"`
	Buckets           []metering.Bucket      `json:"buckets"`
	Snapshots         []limits.UsageSnapshot `json:"snapshots"`
	Violations        []limits.Violation     `json:"violations"`
	AbuseDecisions    []abuse.Decision       `json:"abuse_decisions"`
	OfflineGuarantees []string               `json:"offline_guarantees"`
}

func billingMeteringLimitsAbusePlanHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	var request billingMeteringLimitsAbusePlanRequest
	if !decodeStrictJSONRequest(w, r, &request, "invalid billing metering limits abuse planning request") {
		return
	}
	plan, err := buildBillingMeteringLimitsAbusePlan(request)
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	WriteJSON(w, http.StatusOK, plan)
}

func buildBillingMeteringLimitsAbusePlan(request billingMeteringLimitsAbusePlanRequest) (billingMeteringLimitsAbusePlan, error) {
	buckets, err := metering.Aggregate(request.Events, request.WindowStart, request.WindowEnd)
	if err != nil {
		return billingMeteringLimitsAbusePlan{}, err
	}
	snapshots, err := limits.SnapshotsFromBuckets(buckets, request.PlanAssignments)
	if err != nil {
		return billingMeteringLimitsAbusePlan{}, err
	}
	violations, err := evaluateSnapshotsByPlan(snapshots)
	if err != nil {
		return billingMeteringLimitsAbusePlan{}, err
	}
	decisions, err := abuse.DecideBatch(abuse.DefaultPolicy(), request.Activities)
	if err != nil {
		return billingMeteringLimitsAbusePlan{}, err
	}
	return billingMeteringLimitsAbusePlan{
		SchemaVersion:     billingMeteringLimitsAbusePlanSchemaVersion,
		Status:            billingPlanStatus(violations, decisions),
		WindowStart:       request.WindowStart,
		WindowEnd:         request.WindowEnd,
		Buckets:           buckets,
		Snapshots:         snapshots,
		Violations:        violations,
		AbuseDecisions:    decisions,
		OfflineGuarantees: billingMeteringLimitsAbuseOfflineGuarantees(),
	}, nil
}

func evaluateSnapshotsByPlan(snapshots []limits.UsageSnapshot) ([]limits.Violation, error) {
	byPlan := map[limits.PlanName][]limits.UsageSnapshot{}
	planNames := make([]limits.PlanName, 0)
	for _, snapshot := range snapshots {
		if _, ok := byPlan[snapshot.Plan]; !ok {
			planNames = append(planNames, snapshot.Plan)
		}
		byPlan[snapshot.Plan] = append(byPlan[snapshot.Plan], snapshot)
	}
	sort.Slice(planNames, func(i, j int) bool { return planNames[i] < planNames[j] })

	violations := make([]limits.Violation, 0)
	catalog := limits.DefaultCatalog()
	for _, planName := range planNames {
		planViolations, err := limits.EvaluateCatalog(catalog, planName, byPlan[planName])
		if err != nil {
			return nil, fmt.Errorf("plan %s: %w", planName, err)
		}
		violations = append(violations, planViolations...)
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Tenant.Key() != violations[j].Tenant.Key() {
			return violations[i].Tenant.Key() < violations[j].Tenant.Key()
		}
		return violations[i].Resource < violations[j].Resource
	})
	return violations, nil
}

func billingPlanStatus(violations []limits.Violation, decisions []abuse.Decision) string {
	if len(violations) > 0 {
		return "needs-review"
	}
	for _, decision := range decisions {
		if decision.Action != abuse.ActionAllow {
			return "needs-review"
		}
	}
	return "ready"
}

func billingMeteringLimitsAbuseOfflineGuarantees() []string {
	return []string{
		"no payment provider, invoice, customer portal, or customer identity calls",
		"no cross-workspace project aggregation, durable metering store, replay cursor, database, or filesystem writes",
		"no API middleware enforcement, scheduler admission, runtime hook, or external abuse-provider calls",
	}
}
