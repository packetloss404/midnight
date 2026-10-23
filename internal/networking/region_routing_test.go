// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package networking

import "testing"

func TestPlanRegionRouteSelectsHealthyWeightedLowLatencyCandidate(t *testing.T) {
	plan, err := PlanRegionRoute([]RegionRouteCandidate{
		{RegionID: "us-west", Residency: "US", Health: RegionHealthHealthy, LatencyMS: 20, Weight: 1},
		{RegionID: "us-east", Residency: "US", Health: RegionHealthHealthy, LatencyMS: 80, Weight: 2},
		{RegionID: "eu-west", Residency: "EU", Health: RegionHealthHealthy, LatencyMS: 5, Weight: 10},
	}, RegionRoutePolicy{AllowedResidency: []string{"us"}, MinimumStandbys: 1})
	if err != nil {
		t.Fatalf("PlanRegionRoute() error = %v", err)
	}
	if !plan.HasPrimary || plan.Primary.RegionID != "us-east" {
		t.Fatalf("primary = %+v", plan.Primary)
	}
	if len(plan.Standbys) != 1 || plan.Standbys[0].RegionID != "us-west" {
		t.Fatalf("standbys = %+v", plan.Standbys)
	}
	if len(plan.Rejected) != 1 || plan.Rejected[0].RegionID != "eu-west" {
		t.Fatalf("rejected = %+v", plan.Rejected)
	}
}

func TestPlanRegionRoutePrefersHealthyOverDegradedDespiteLatency(t *testing.T) {
	plan, err := PlanRegionRoute([]RegionRouteCandidate{
		{RegionID: "us-west", Residency: "us", Health: RegionHealthDegraded, LatencyMS: 1, Weight: 100},
		{RegionID: "us-east", Residency: "us", Health: RegionHealthHealthy, LatencyMS: 100, Weight: 1},
	}, RegionRoutePolicy{RequiredResidency: "us"})
	if err != nil {
		t.Fatalf("PlanRegionRoute() error = %v", err)
	}
	if plan.Primary.RegionID != "us-east" || len(plan.Standbys) != 1 || plan.Standbys[0].RegionID != "us-west" {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanRegionRouteRejectsUnhealthyAndPlansFailover(t *testing.T) {
	plan, err := PlanRegionRoute([]RegionRouteCandidate{
		{RegionID: "eu-a", Residency: "eu", Health: RegionHealthHealthy, LatencyMS: 30},
		{RegionID: "eu-b", Residency: "eu", Health: RegionHealthHealthy, LatencyMS: 35},
		{RegionID: "eu-c", Residency: "eu", Health: RegionHealthUnhealthy, LatencyMS: 10},
	}, RegionRoutePolicy{RequiredResidency: "eu", MinimumStandbys: 1})
	if err != nil {
		t.Fatalf("PlanRegionRoute() error = %v", err)
	}
	if plan.Degraded || plan.Primary.RegionID != "eu-a" {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.FailoverSteps) != 1 || plan.FailoverSteps[0].FromRegionID != "eu-a" || plan.FailoverSteps[0].ToRegionID != "eu-b" {
		t.Fatalf("failover steps = %+v", plan.FailoverSteps)
	}
	if len(plan.Rejected) != 1 || plan.Rejected[0].Reason != "region is unhealthy" {
		t.Fatalf("rejected = %+v", plan.Rejected)
	}
}

func TestPlanRegionRouteMarksNoCompatibleCandidateDegraded(t *testing.T) {
	plan, err := PlanRegionRoute([]RegionRouteCandidate{
		{RegionID: "us-a", Residency: "us", Health: RegionHealthHealthy},
		{RegionID: "eu-a", Residency: "eu", Health: RegionHealthUnhealthy},
	}, RegionRoutePolicy{RequiredResidency: "eu"})
	if err != nil {
		t.Fatalf("PlanRegionRoute() error = %v", err)
	}
	if plan.HasPrimary || !plan.Degraded || plan.Reason == "" {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanRegionRouteValidatesDuplicateCandidates(t *testing.T) {
	_, err := PlanRegionRoute([]RegionRouteCandidate{
		{RegionID: "us-a", Residency: "us"},
		{RegionID: " us-a ", Residency: "us"},
	}, RegionRoutePolicy{})
	if err == nil {
		t.Fatal("PlanRegionRoute() error = nil, want duplicate candidate error")
	}
}
