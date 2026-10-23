package metering

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/tenancy"
)

func TestAggregateBucketsUsageByTenantAndResource(t *testing.T) {
	start := time.Unix(1_800_000_000, 0).UTC()
	tenantA := tenancy.Scope{WorkspaceID: "w1", ProjectID: "p1", EnvironmentID: "prod"}
	tenantB := tenancy.Scope{WorkspaceID: "w2", ProjectID: "p1", EnvironmentID: "prod"}
	events := []UsageEvent{
		{ID: "e3", Tenant: tenantB, Resource: ResourceRequests, Quantity: 7, ObservedAt: start.Add(3 * time.Minute)},
		{ID: "e1", Tenant: tenantA, Resource: ResourceBuildSeconds, Quantity: 10, ObservedAt: start.Add(time.Minute)},
		{ID: "e2", Tenant: tenantA, Resource: ResourceBuildSeconds, Quantity: 5, ObservedAt: start.Add(2 * time.Minute)},
		{ID: "before", Tenant: tenantA, Resource: ResourceBuildSeconds, Quantity: 99, ObservedAt: start.Add(-time.Second)},
		{ID: "after", Tenant: tenantA, Resource: ResourceBuildSeconds, Quantity: 99, ObservedAt: start.Add(time.Hour)},
	}

	buckets, err := Aggregate(events, start, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("expected 2 buckets, got %#v", buckets)
	}
	if buckets[0].Tenant != tenantA || buckets[0].Resource != ResourceBuildSeconds || buckets[0].Quantity != 15 {
		t.Fatalf("unexpected first bucket: %#v", buckets[0])
	}
	if buckets[0].EventCount != 2 || buckets[0].FirstEventID != "e1" || buckets[0].LastEventID != "e2" {
		t.Fatalf("unexpected event lineage: %#v", buckets[0])
	}
	if buckets[1].Tenant != tenantB || buckets[1].Resource != ResourceRequests || buckets[1].Quantity != 7 {
		t.Fatalf("unexpected second bucket: %#v", buckets[1])
	}
}

func TestAggregateValidatesEventsOutsideWindow(t *testing.T) {
	start := time.Unix(1_800_000_000, 0).UTC()
	_, err := Aggregate([]UsageEvent{{ID: "bad", Tenant: tenancy.Scope{WorkspaceID: "wrk", ProjectID: "prj", EnvironmentID: "prod"}, Resource: ResourceRequests, Quantity: 0, ObservedAt: start.Add(-time.Hour)}}, start, start.Add(time.Hour))
	if !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("expected invalid quantity, got %v", err)
	}
}

func TestAggregateRejectsMissingTenantScope(t *testing.T) {
	start := time.Unix(1_800_000_000, 0).UTC()
	_, err := Aggregate([]UsageEvent{{ID: "bad", Resource: ResourceRequests, Quantity: 1, ObservedAt: start}}, start, start.Add(time.Hour))
	if !errors.Is(err, ErrInvalidTenant) {
		t.Fatalf("expected invalid tenant, got %v", err)
	}
}

func TestAggregateRejectsInvalidWindow(t *testing.T) {
	start := time.Unix(1_800_000_000, 0).UTC()
	_, err := Aggregate(nil, start, start)
	if !errors.Is(err, ErrInvalidWindow) {
		t.Fatalf("expected invalid window, got %v", err)
	}
}

func TestAggregateRejectsQuantityOverflow(t *testing.T) {
	start := time.Unix(1_800_000_000, 0).UTC()
	tenant := tenancy.Scope{WorkspaceID: "wrk", ProjectID: "p1", EnvironmentID: "prod"}
	events := []UsageEvent{
		{ID: "e1", Tenant: tenant, Resource: ResourceRequests, Quantity: math.MaxInt64, ObservedAt: start},
		{ID: "e2", Tenant: tenant, Resource: ResourceRequests, Quantity: 1, ObservedAt: start.Add(time.Second)},
	}

	_, err := Aggregate(events, start, start.Add(time.Hour))
	if !errors.Is(err, ErrQuantityOverflow) {
		t.Fatalf("expected quantity overflow, got %v", err)
	}
}
