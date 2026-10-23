package router

import (
	"fmt"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/timeline"
)

// ObservedRouteTarget is the last target state reported by the ingress data plane.
type ObservedRouteTarget struct {
	RouteName  string
	ServiceURL string
	Freshness  RouteTargetFreshness
}

type RouteArtifact struct {
	RouteGroup string
	Format     string
	Rendered   string
	Hash       string
}

type RouteReconciliationInput struct {
	DeploymentID  string
	TenantID      string
	RouteGroup    string
	Routes        []RouteInput
	Observed      []ObservedRouteTarget
	Now           time.Time
	FirstEventSeq int64
}

type RouteReconciliationPlan struct {
	Artifact RouteArtifact
	Events   []timeline.Event
	Summary  RouteReconciliationSummary
}

type RouteReconciliationStatus string

const (
	RouteReconciliationStatusReady   RouteReconciliationStatus = "ready"
	RouteReconciliationStatusPending RouteReconciliationStatus = "pending"
)

// RouteReconciliationSummary is the compact status-UI view of the most recent
// reconciliation result. It intentionally excludes rendered config content.
type RouteReconciliationSummary struct {
	RouteGroup     string                    `json:"routeGroup,omitempty"`
	ArtifactHash   string                    `json:"artifactHash,omitempty"`
	Status         RouteReconciliationStatus `json:"status"`
	DesiredRoutes  int                       `json:"desiredRoutes"`
	RenderedRoutes int                       `json:"renderedRoutes"`
	ActiveRoutes   int                       `json:"activeRoutes"`
	PendingRoutes  int                       `json:"pendingRoutes"`
	StaleRoutes    int                       `json:"staleRoutes"`
	UnknownRoutes  int                       `json:"unknownRoutes"`
	Mismatches     int                       `json:"mismatches"`
}

func (summary RouteReconciliationSummary) CompactString() string {
	parts := []string{
		fmt.Sprintf("status=%s", summary.Status),
		fmt.Sprintf("routes=%d/%d", summary.ActiveRoutes, summary.DesiredRoutes),
		fmt.Sprintf("pending=%d", summary.PendingRoutes),
	}
	if summary.StaleRoutes > 0 {
		parts = append(parts, fmt.Sprintf("stale=%d", summary.StaleRoutes))
	}
	if summary.UnknownRoutes > 0 {
		parts = append(parts, fmt.Sprintf("unknown=%d", summary.UnknownRoutes))
	}
	if summary.Mismatches > 0 {
		parts = append(parts, fmt.Sprintf("mismatches=%d", summary.Mismatches))
	}
	if summary.ArtifactHash != "" {
		parts = append(parts, "hash="+summary.ArtifactHash)
	}
	if summary.RouteGroup != "" {
		parts = append(parts, "group="+summary.RouteGroup)
	}
	return strings.Join(parts, " ")
}

// PlanRouteReconciliation renders the desired route artifact and records the
// timeline events implied by the desired routes and fresh observed targets.
func PlanRouteReconciliation(input RouteReconciliationInput) (RouteReconciliationPlan, error) {
	rendered, err := RenderTraefikDynamicYAML(input.Routes)
	if err != nil {
		return RouteReconciliationPlan{}, err
	}

	observed := make(map[string]ObservedRouteTarget, len(input.Observed))
	for _, target := range input.Observed {
		observed[target.RouteName] = target
	}

	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nextSeq := input.FirstEventSeq
	if nextSeq <= 0 {
		nextSeq = 1
	}

	plan := RouteReconciliationPlan{Artifact: RouteArtifact{
		RouteGroup: input.RouteGroup,
		Format:     "traefik.dynamic.yaml",
		Rendered:   rendered,
		Hash:       RenderedConfigHash(rendered),
	}}
	plan.Summary = RouteReconciliationSummary{
		RouteGroup:     input.RouteGroup,
		ArtifactHash:   plan.Artifact.Hash,
		Status:         RouteReconciliationStatusReady,
		DesiredRoutes:  len(input.Routes),
		RenderedRoutes: len(input.Routes),
	}

	for _, route := range input.Routes {
		route = normalizeRoute(route)
		for _, eventType := range []timeline.EventType{timeline.EventIngressRouteRendered, timeline.EventIngressRouteArtifactWritten} {
			plan.Events = append(plan.Events, timeline.Event{
				DeploymentID: input.DeploymentID,
				EventSeq:     nextSeq,
				Type:         eventType,
				TenantID:     input.TenantID,
				RouteName:    route.Name,
				Host:         route.Host,
				At:           now,
				Message:      routeEventMessage(eventType, route.Name),
			})
			nextSeq++
		}

		target, ok := observed[route.Name]
		if !ok {
			plan.Summary.UnknownRoutes++
			continue
		}
		serviceURL := route.ServiceURL
		if serviceURL == "" && len(route.ServiceURLs) > 0 {
			serviceURL = route.ServiceURLs[0]
		}
		if target.ServiceURL != serviceURL {
			plan.Summary.Mismatches++
			continue
		}
		if target.Freshness.Status != RouteTargetFreshnessFresh {
			if target.Freshness.Status == RouteTargetFreshnessStale {
				plan.Summary.StaleRoutes++
			} else {
				plan.Summary.UnknownRoutes++
			}
			continue
		}
		plan.Summary.ActiveRoutes++
		for _, eventType := range []timeline.EventType{timeline.EventACMEDNSPending, timeline.EventACMECertificateReady, timeline.EventIngressRouteActive} {
			plan.Events = append(plan.Events, timeline.Event{
				DeploymentID: input.DeploymentID,
				EventSeq:     nextSeq,
				Type:         eventType,
				TenantID:     input.TenantID,
				RouteName:    route.Name,
				Host:         route.Host,
				At:           now,
				Message:      routeEventMessage(eventType, route.Name),
			})
			nextSeq++
		}
	}
	plan.Summary.PendingRoutes = plan.Summary.DesiredRoutes - plan.Summary.ActiveRoutes
	if plan.Summary.PendingRoutes > 0 {
		plan.Summary.Status = RouteReconciliationStatusPending
	}

	return plan, nil
}

func routeEventMessage(eventType timeline.EventType, routeName string) string {
	switch eventType {
	case timeline.EventIngressRouteRendered:
		return fmt.Sprintf("rendered route %q", routeName)
	case timeline.EventIngressRouteArtifactWritten:
		return fmt.Sprintf("wrote route artifact for %q", routeName)
	case timeline.EventACMEDNSPending:
		return fmt.Sprintf("ACME DNS pending for route %q", routeName)
	case timeline.EventACMECertificateReady:
		return fmt.Sprintf("ACME certificate ready for route %q", routeName)
	case timeline.EventIngressRouteActive:
		return fmt.Sprintf("route %q active", routeName)
	default:
		return fmt.Sprintf("route %q event %q", routeName, eventType)
	}
}
