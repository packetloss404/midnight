package limits

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/metering"
	"github.com/midnight/midnight/internal/tenancy"
)

// PlanName identifies a commercial or internal service plan.
type PlanName string

const (
	PlanFree     PlanName = "free"
	PlanTeam     PlanName = "team"
	PlanBusiness PlanName = "business"
)

// UsageLimit caps one metered resource. A Max of zero or less means unlimited.
type UsageLimit struct {
	Resource metering.Resource `json:"resource"`
	Max      int64             `json:"max"`
}

// Plan groups usage limits under a stable name.
type Plan struct {
	Name   PlanName     `json:"name"`
	Limits []UsageLimit `json:"limits"`
}

// PlanAssignment attaches a tenant scope to the plan used for limit checks.
type PlanAssignment struct {
	Tenant tenancy.Scope `json:"tenant"`
	Plan   PlanName      `json:"plan"`
}

// UsageSnapshot is the point-in-time value evaluated against a plan.
type UsageSnapshot struct {
	Tenant   tenancy.Scope     `json:"tenant"`
	Resource metering.Resource `json:"resource"`
	Used     int64             `json:"used"`
	Plan     PlanName          `json:"plan"`
}

// Violation is a deterministic plan-limit breach.
type Violation struct {
	Tenant   tenancy.Scope     `json:"tenant"`
	Plan     PlanName          `json:"plan"`
	Resource metering.Resource `json:"resource"`
	Used     int64             `json:"used"`
	Limit    int64             `json:"limit"`
	OverBy   int64             `json:"over_by"`
}

var (
	ErrEmptyPlanName           = errors.New("limits: plan name is required")
	ErrInvalidTenant           = errors.New("limits: tenant scope is required")
	ErrEmptyResource           = errors.New("limits: resource is required")
	ErrNegativeUsage           = errors.New("limits: usage must not be negative")
	ErrDuplicateLimit          = errors.New("limits: duplicate resource limit")
	ErrDuplicatePlanAssignment = errors.New("limits: duplicate tenant plan assignment")
	ErrUnknownPlan             = errors.New("limits: unknown plan")
	ErrPlanNameMismatch        = errors.New("limits: snapshot plan does not match evaluated plan")
	ErrMissingPlan             = errors.New("limits: tenant plan is required")
)

// DefaultCatalog returns the initial Phase 4 plan catalog. Values are small
// enough for tests and demos but the rules are the production contract.
func DefaultCatalog() map[PlanName]Plan {
	return map[PlanName]Plan{
		PlanFree: {
			Name: PlanFree,
			Limits: []UsageLimit{
				{Resource: metering.ResourceBuildSeconds, Max: 3_600},
				{Resource: metering.ResourceCPUSeconds, Max: 7_200},
				{Resource: metering.ResourceMemoryByteSeconds, Max: 16 * 1024 * 1024 * 1024 * 3_600},
				{Resource: metering.ResourceEgressBytes, Max: 10 * 1024 * 1024 * 1024},
				{Resource: metering.ResourceRequests, Max: 100_000},
			},
		},
		PlanTeam: {
			Name: PlanTeam,
			Limits: []UsageLimit{
				{Resource: metering.ResourceBuildSeconds, Max: 36_000},
				{Resource: metering.ResourceCPUSeconds, Max: 72_000},
				{Resource: metering.ResourceMemoryByteSeconds, Max: 256 * 1024 * 1024 * 1024 * 3_600},
				{Resource: metering.ResourceEgressBytes, Max: 100 * 1024 * 1024 * 1024},
				{Resource: metering.ResourceRequests, Max: 1_000_000},
			},
		},
		PlanBusiness: {
			Name: PlanBusiness,
			Limits: []UsageLimit{
				{Resource: metering.ResourceBuildSeconds, Max: 360_000},
				{Resource: metering.ResourceCPUSeconds, Max: 720_000},
				{Resource: metering.ResourceMemoryByteSeconds, Max: 2 * 1024 * 1024 * 1024 * 1024 * 3_600},
				{Resource: metering.ResourceEgressBytes, Max: 1_000 * 1024 * 1024 * 1024},
				{Resource: metering.ResourceRequests, Max: 10_000_000},
			},
		},
	}
}

// SnapshotsFromBuckets converts metering aggregates into plan-scoped limit
// snapshots without applying enforcement side effects.
func SnapshotsFromBuckets(buckets []metering.Bucket, assignments []PlanAssignment) ([]UsageSnapshot, error) {
	planIndex, err := planAssignmentIndex(assignments)
	if err != nil {
		return nil, err
	}
	snapshots := make([]UsageSnapshot, 0, len(buckets))
	for i, bucket := range buckets {
		if err := bucket.Tenant.Validate(); err != nil {
			return nil, fmt.Errorf("bucket[%d]: %w: %v", i, ErrInvalidTenant, err)
		}
		planName := planIndex[bucket.Tenant.Key()]
		if strings.TrimSpace(string(planName)) == "" {
			return nil, fmt.Errorf("bucket[%d]: %w: %s/%s/%s", i, ErrMissingPlan, bucket.Tenant.WorkspaceID, bucket.Tenant.ProjectID, bucket.Tenant.EnvironmentID)
		}
		snapshot := UsageSnapshot{Tenant: bucket.Tenant, Resource: bucket.Resource, Used: bucket.Quantity, Plan: planName}
		if err := snapshot.validate(planName); err != nil {
			return nil, fmt.Errorf("bucket[%d]: %w", i, err)
		}
		snapshots = append(snapshots, snapshot)
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].Tenant.Key() != snapshots[j].Tenant.Key() {
			return snapshots[i].Tenant.Key() < snapshots[j].Tenant.Key()
		}
		return snapshots[i].Resource < snapshots[j].Resource
	})
	return snapshots, nil
}

// Evaluate returns every limit violation for snapshots belonging to plan.
func Evaluate(plan Plan, snapshots []UsageSnapshot) ([]Violation, error) {
	limits, err := limitMap(plan)
	if err != nil {
		return nil, err
	}

	violations := make([]Violation, 0)
	for i, snapshot := range snapshots {
		if err := snapshot.validate(plan.Name); err != nil {
			return nil, fmt.Errorf("snapshot[%d]: %w", i, err)
		}
		max, ok := limits[snapshot.Resource]
		if !ok || max <= 0 || snapshot.Used <= max {
			continue
		}
		violations = append(violations, Violation{
			Tenant:   snapshot.Tenant,
			Plan:     plan.Name,
			Resource: snapshot.Resource,
			Used:     snapshot.Used,
			Limit:    max,
			OverBy:   snapshot.Used - max,
		})
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Tenant.Key() != violations[j].Tenant.Key() {
			return violations[i].Tenant.Key() < violations[j].Tenant.Key()
		}
		return violations[i].Resource < violations[j].Resource
	})
	return violations, nil
}

// EvaluateCatalog looks up planName and evaluates snapshots against it.
func EvaluateCatalog(catalog map[PlanName]Plan, planName PlanName, snapshots []UsageSnapshot) ([]Violation, error) {
	plan, ok := catalog[planName]
	if !ok {
		return nil, ErrUnknownPlan
	}
	return Evaluate(plan, snapshots)
}

func limitMap(plan Plan) (map[metering.Resource]int64, error) {
	if strings.TrimSpace(string(plan.Name)) == "" {
		return nil, ErrEmptyPlanName
	}
	out := make(map[metering.Resource]int64, len(plan.Limits))
	for _, limit := range plan.Limits {
		if strings.TrimSpace(string(limit.Resource)) == "" {
			return nil, ErrEmptyResource
		}
		if _, exists := out[limit.Resource]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateLimit, limit.Resource)
		}
		out[limit.Resource] = limit.Max
	}
	return out, nil
}

func planAssignmentIndex(assignments []PlanAssignment) (map[string]PlanName, error) {
	out := make(map[string]PlanName, len(assignments))
	for i, assignment := range assignments {
		if err := assignment.Tenant.Validate(); err != nil {
			return nil, fmt.Errorf("plan_assignment[%d]: %w: %v", i, ErrInvalidTenant, err)
		}
		if strings.TrimSpace(string(assignment.Plan)) == "" {
			return nil, fmt.Errorf("plan_assignment[%d]: %w", i, ErrEmptyPlanName)
		}
		key := assignment.Tenant.Key()
		if _, exists := out[key]; exists {
			return nil, fmt.Errorf("plan_assignment[%d]: %w", i, ErrDuplicatePlanAssignment)
		}
		out[key] = assignment.Plan
	}
	return out, nil
}

func (s UsageSnapshot) validate(planName PlanName) error {
	if err := s.Tenant.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTenant, err)
	}
	if strings.TrimSpace(string(s.Resource)) == "" {
		return ErrEmptyResource
	}
	if s.Used < 0 {
		return ErrNegativeUsage
	}
	if s.Plan != "" && s.Plan != planName {
		return ErrPlanNameMismatch
	}
	return nil
}
