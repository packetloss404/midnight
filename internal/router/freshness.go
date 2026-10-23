package router

import "time"

const DefaultRouteTargetFreshnessMaxAge = 30 * time.Second

type RouteTargetFreshnessStatus string

const (
	RouteTargetFreshnessFresh   RouteTargetFreshnessStatus = "fresh"
	RouteTargetFreshnessStale   RouteTargetFreshnessStatus = "stale"
	RouteTargetFreshnessUnknown RouteTargetFreshnessStatus = "unknown"
)

type RouteTargetFreshness struct {
	Status RouteTargetFreshnessStatus
	Age    time.Duration
}

// EvaluateRouteTargetFreshness classifies whether the last observed route
// target state is recent enough for control-plane decisions.
func EvaluateRouteTargetFreshness(now, observedAt time.Time, maxAge time.Duration) RouteTargetFreshness {
	if maxAge <= 0 {
		maxAge = DefaultRouteTargetFreshnessMaxAge
	}
	if observedAt.IsZero() {
		return RouteTargetFreshness{Status: RouteTargetFreshnessUnknown}
	}

	age := now.Sub(observedAt)
	if age < 0 {
		age = 0
	}
	if age > maxAge {
		return RouteTargetFreshness{Status: RouteTargetFreshnessStale, Age: age}
	}
	return RouteTargetFreshness{Status: RouteTargetFreshnessFresh, Age: age}
}
