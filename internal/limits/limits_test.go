package limits

import (
	"errors"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/metering"
	"github.com/midnight/midnight/internal/tenancy"
)

func TestEvaluateReportsDeterministicViolations(t *testing.T) {
	tenantA := tenancy.Scope{WorkspaceID: "w1", ProjectID: "p1", EnvironmentID: "prod"}
	tenantB := tenancy.Scope{WorkspaceID: "w2", ProjectID: "p1", EnvironmentID: "prod"}
	plan := Plan{
		Name: PlanFree,
		Limits: []UsageLimit{
			{Resource: metering.ResourceRequests, Max: 10},
			{Resource: metering.ResourceBuildSeconds, Max: 20},
		},
	}
	snapshots := []UsageSnapshot{
		{Tenant: tenantB, Plan: PlanFree, Resource: metering.ResourceRequests, Used: 11},
		{Tenant: tenantA, Plan: PlanFree, Resource: metering.ResourceBuildSeconds, Used: 21},
		{Tenant: tenantA, Plan: PlanFree, Resource: metering.ResourceRequests, Used: 10},
	}

	violations, err := Evaluate(plan, snapshots)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(violations) != 2 {
		t.Fatalf("expected 2 violations, got %#v", violations)
	}
	if violations[0].Tenant != tenantA || violations[0].Resource != metering.ResourceBuildSeconds || violations[0].OverBy != 1 {
		t.Fatalf("unexpected first violation: %#v", violations[0])
	}
	if violations[1].Tenant != tenantB || violations[1].Resource != metering.ResourceRequests || violations[1].OverBy != 1 {
		t.Fatalf("unexpected second violation: %#v", violations[1])
	}
}

func TestEvaluateRejectsDuplicateLimits(t *testing.T) {
	_, err := Evaluate(Plan{Name: PlanFree, Limits: []UsageLimit{{Resource: metering.ResourceRequests, Max: 1}, {Resource: metering.ResourceRequests, Max: 2}}}, nil)
	if !errors.Is(err, ErrDuplicateLimit) {
		t.Fatalf("expected duplicate limit, got %v", err)
	}
}

func TestEvaluateCatalogRejectsUnknownPlan(t *testing.T) {
	_, err := EvaluateCatalog(DefaultCatalog(), PlanName("missing"), nil)
	if !errors.Is(err, ErrUnknownPlan) {
		t.Fatalf("expected unknown plan, got %v", err)
	}
}

func TestEvaluateRejectsPlanMismatch(t *testing.T) {
	_, err := Evaluate(Plan{Name: PlanFree, Limits: []UsageLimit{{Resource: metering.ResourceRequests, Max: 1}}}, []UsageSnapshot{{Tenant: tenancy.Scope{WorkspaceID: "wrk", ProjectID: "prj", EnvironmentID: "prod"}, Plan: PlanTeam, Resource: metering.ResourceRequests}})
	if !errors.Is(err, ErrPlanNameMismatch) {
		t.Fatalf("expected plan mismatch, got %v", err)
	}
}

func TestSnapshotsFromBucketsConvertsDeterministically(t *testing.T) {
	start := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	tenantA := tenancy.Scope{WorkspaceID: "w1", ProjectID: "p1", EnvironmentID: "prod"}
	tenantB := tenancy.Scope{WorkspaceID: "w2", ProjectID: "p1", EnvironmentID: "prod"}
	buckets := []metering.Bucket{
		{Tenant: tenantB, Resource: metering.ResourceRequests, Quantity: 20, WindowStart: start, WindowEnd: start.Add(time.Hour)},
		{Tenant: tenantA, Resource: metering.ResourceCPUSeconds, Quantity: 10, WindowStart: start, WindowEnd: start.Add(time.Hour)},
		{Tenant: tenantA, Resource: metering.ResourceBuildSeconds, Quantity: 5, WindowStart: start, WindowEnd: start.Add(time.Hour)},
	}

	snapshots, err := SnapshotsFromBuckets(buckets, []PlanAssignment{{Tenant: tenantA, Plan: PlanFree}, {Tenant: tenantB, Plan: PlanTeam}})
	if err != nil {
		t.Fatalf("SnapshotsFromBuckets: %v", err)
	}
	if len(snapshots) != 3 {
		t.Fatalf("expected 3 snapshots, got %#v", snapshots)
	}
	if snapshots[0] != (UsageSnapshot{Tenant: tenantA, Resource: metering.ResourceBuildSeconds, Used: 5, Plan: PlanFree}) {
		t.Fatalf("unexpected first snapshot: %#v", snapshots[0])
	}
	if snapshots[2] != (UsageSnapshot{Tenant: tenantB, Resource: metering.ResourceRequests, Used: 20, Plan: PlanTeam}) {
		t.Fatalf("unexpected last snapshot: %#v", snapshots[2])
	}
}

func TestSnapshotsFromBucketsRejectsMissingProjectPlan(t *testing.T) {
	_, err := SnapshotsFromBuckets([]metering.Bucket{{Tenant: tenancy.Scope{WorkspaceID: "wrk", ProjectID: "p1", EnvironmentID: "prod"}, Resource: metering.ResourceRequests, Quantity: 1}}, nil)
	if !errors.Is(err, ErrMissingPlan) {
		t.Fatalf("expected missing plan, got %v", err)
	}
}

func TestSnapshotsFromBucketsRejectsDuplicatePlanAssignment(t *testing.T) {
	tenant := tenancy.Scope{WorkspaceID: "wrk", ProjectID: "p1", EnvironmentID: "prod"}
	_, err := SnapshotsFromBuckets(nil, []PlanAssignment{{Tenant: tenant, Plan: PlanFree}, {Tenant: tenant, Plan: PlanTeam}})
	if !errors.Is(err, ErrDuplicatePlanAssignment) {
		t.Fatalf("expected duplicate plan assignment, got %v", err)
	}
}

func TestDefaultCatalogCoversDocumentedMeteredResources(t *testing.T) {
	want := []metering.Resource{metering.ResourceBuildSeconds, metering.ResourceCPUSeconds, metering.ResourceMemoryByteSeconds, metering.ResourceEgressBytes, metering.ResourceRequests}
	for name, plan := range DefaultCatalog() {
		seen := map[metering.Resource]bool{}
		for _, limit := range plan.Limits {
			seen[limit.Resource] = true
		}
		for _, resource := range want {
			if !seen[resource] {
				t.Fatalf("plan %s missing limit for %s", name, resource)
			}
		}
	}
}
