package router

import (
	"testing"
	"time"

	"github.com/midnight/midnight/internal/timeline"
)

func TestPlanRouteReconciliationRendersArtifactAndEvents(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	plan, err := PlanRouteReconciliation(RouteReconciliationInput{
		DeploymentID:  "dep-1",
		TenantID:      "tenant-1",
		RouteGroup:    "group-a",
		FirstEventSeq: 10,
		Now:           now,
		Routes: []RouteInput{{
			Name:       "app",
			Host:       "app.example.test",
			ServiceURL: "http://127.0.0.1:8080",
			TLS:        true,
		}},
		Observed: []ObservedRouteTarget{{
			RouteName:  "app",
			ServiceURL: "http://127.0.0.1:8080",
			Freshness:  RouteTargetFreshness{Status: RouteTargetFreshnessFresh},
		}},
	})
	if err != nil {
		t.Fatalf("PlanRouteReconciliation returned error: %v", err)
	}

	if plan.Artifact.RouteGroup != "group-a" || plan.Artifact.Format != "traefik.dynamic.yaml" {
		t.Fatalf("unexpected artifact metadata: %#v", plan.Artifact)
	}
	if plan.Artifact.Rendered == "" || plan.Artifact.Hash != RenderedConfigHash(plan.Artifact.Rendered) {
		t.Fatalf("unexpected artifact render/hash: %#v", plan.Artifact)
	}
	if plan.Summary.Status != RouteReconciliationStatusReady || plan.Summary.ActiveRoutes != 1 || plan.Summary.PendingRoutes != 0 || plan.Summary.ArtifactHash != plan.Artifact.Hash {
		t.Fatalf("unexpected summary: %#v", plan.Summary)
	}

	wantTypes := []timeline.EventType{
		timeline.EventIngressRouteRendered,
		timeline.EventIngressRouteArtifactWritten,
		timeline.EventACMEDNSPending,
		timeline.EventACMECertificateReady,
		timeline.EventIngressRouteActive,
	}
	if len(plan.Events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d", len(plan.Events), len(wantTypes))
	}
	for i, want := range wantTypes {
		if plan.Events[i].Type != want {
			t.Fatalf("event %d type = %q, want %q", i, plan.Events[i].Type, want)
		}
		if plan.Events[i].EventSeq != int64(10+i) {
			t.Fatalf("event %d seq = %d, want %d", i, plan.Events[i].EventSeq, 10+i)
		}
		if plan.Events[i].At != now || plan.Events[i].DeploymentID != "dep-1" || plan.Events[i].TenantID != "tenant-1" {
			t.Fatalf("event %d has unexpected common fields: %#v", i, plan.Events[i])
		}
	}
	if err := timeline.ValidateEventSequence(plan.Events); err != nil {
		t.Fatalf("planned events are out of sequence: %v", err)
	}
}

func TestPlanRouteReconciliationSkipsActiveWhenObservedTargetIsStale(t *testing.T) {
	plan, err := PlanRouteReconciliation(RouteReconciliationInput{
		Routes: []RouteInput{{Name: "app", Host: "app.example.test", ServiceURL: "http://127.0.0.1:8080"}},
		Observed: []ObservedRouteTarget{{
			RouteName:  "app",
			ServiceURL: "http://127.0.0.1:8080",
			Freshness:  RouteTargetFreshness{Status: RouteTargetFreshnessStale},
		}},
	})
	if err != nil {
		t.Fatalf("PlanRouteReconciliation returned error: %v", err)
	}
	if len(plan.Events) != 2 {
		t.Fatalf("event count = %d, want rendered and artifact events only", len(plan.Events))
	}
	if plan.Summary.Status != RouteReconciliationStatusPending || plan.Summary.StaleRoutes != 1 || plan.Summary.PendingRoutes != 1 {
		t.Fatalf("unexpected stale summary: %#v", plan.Summary)
	}
}

func TestPlanRouteReconciliationSummarizesUnknownAndMismatchedRoutes(t *testing.T) {
	plan, err := PlanRouteReconciliation(RouteReconciliationInput{
		Routes: []RouteInput{
			{Name: "missing", Host: "missing.example.test", ServiceURL: "http://127.0.0.1:8080"},
			{Name: "changed", Host: "changed.example.test", ServiceURL: "http://127.0.0.1:8081"},
		},
		Observed: []ObservedRouteTarget{{
			RouteName:  "changed",
			ServiceURL: "http://127.0.0.1:8080",
			Freshness:  RouteTargetFreshness{Status: RouteTargetFreshnessFresh},
		}},
	})
	if err != nil {
		t.Fatalf("PlanRouteReconciliation returned error: %v", err)
	}

	if plan.Summary.Status != RouteReconciliationStatusPending || plan.Summary.DesiredRoutes != 2 || plan.Summary.RenderedRoutes != 2 {
		t.Fatalf("unexpected common summary fields: %#v", plan.Summary)
	}
	if plan.Summary.UnknownRoutes != 1 || plan.Summary.Mismatches != 1 || plan.Summary.ActiveRoutes != 0 || plan.Summary.PendingRoutes != 2 {
		t.Fatalf("unexpected route result counts: %#v", plan.Summary)
	}
}

func TestRouteReconciliationSummaryCompactString(t *testing.T) {
	summary := RouteReconciliationSummary{
		RouteGroup:    "group-a",
		ArtifactHash:  "abc123",
		Status:        RouteReconciliationStatusPending,
		DesiredRoutes: 3,
		ActiveRoutes:  1,
		PendingRoutes: 2,
		StaleRoutes:   1,
		Mismatches:    1,
	}

	got := summary.CompactString()
	want := "status=pending routes=1/3 pending=2 stale=1 mismatches=1 hash=abc123 group=group-a"
	if got != want {
		t.Fatalf("CompactString() = %q, want %q", got, want)
	}
}
