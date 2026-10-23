package router

import (
	"testing"
	"time"
)

func TestEvaluateRouteTargetFreshness(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	tests := map[string]struct {
		observedAt time.Time
		maxAge     time.Duration
		wantStatus RouteTargetFreshnessStatus
		wantAge    time.Duration
	}{
		"fresh within explicit max age": {observedAt: now.Add(-5 * time.Second), maxAge: 10 * time.Second, wantStatus: RouteTargetFreshnessFresh, wantAge: 5 * time.Second},
		"stale past explicit max age":   {observedAt: now.Add(-11 * time.Second), maxAge: 10 * time.Second, wantStatus: RouteTargetFreshnessStale, wantAge: 11 * time.Second},
		"unknown without observation":   {wantStatus: RouteTargetFreshnessUnknown},
		"future observation clamps age": {observedAt: now.Add(5 * time.Second), maxAge: 10 * time.Second, wantStatus: RouteTargetFreshnessFresh},
		"uses default max age":          {observedAt: now.Add(-DefaultRouteTargetFreshnessMaxAge), wantStatus: RouteTargetFreshnessFresh, wantAge: DefaultRouteTargetFreshnessMaxAge},
	}

	for name, test := range tests {
		got := EvaluateRouteTargetFreshness(now, test.observedAt, test.maxAge)
		if got.Status != test.wantStatus {
			t.Fatalf("%s: status = %q, want %q", name, got.Status, test.wantStatus)
		}
		if got.Age != test.wantAge {
			t.Fatalf("%s: age = %s, want %s", name, got.Age, test.wantAge)
		}
	}
}
