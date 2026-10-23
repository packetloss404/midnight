package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/runtime"
)

// RoutePromotionEvidence is the compact, support-safe deploy-level summary for
// one route promotion attempt. It excludes raw hosts, service URLs, rendered
// artifacts, and filesystem paths.
type RoutePromotionEvidence struct {
	RouteGroup     string                                     `json:"route_group,omitempty"`
	RouteName      string                                     `json:"route_name,omitempty"`
	HostHash       string                                     `json:"host_hash,omitempty"`
	ServiceURLHash string                                     `json:"service_url_hash,omitempty"`
	ArtifactStatus router.RouteArtifactPublicationApplyStatus `json:"artifact_status,omitempty"`
	ArtifactHash   string                                     `json:"artifact_hash,omitempty"`
	Writes         int                                        `json:"writes"`
	Skips          int                                        `json:"skips"`
	Errors         int                                        `json:"errors"`
	Diagnostics    []string                                   `json:"diagnostics,omitempty"`
}

func routePromotionEvidenceForRoute(routeGroup string, route router.RouteInput) RoutePromotionEvidence {
	return RoutePromotionEvidence{
		RouteGroup:     strings.TrimSpace(routeGroup),
		RouteName:      strings.TrimSpace(route.Name),
		HostHash:       routePromotionSupportHash(strings.ToLower(strings.TrimSpace(route.Host))),
		ServiceURLHash: routePromotionSupportHash(strings.TrimSpace(route.ServiceURL)),
		ArtifactHash:   routePromotionArtifactHash(route),
	}
}

func routePromotionEvidenceForValidationFailure(target runtime.RoutingTarget, metadata RoutePromotionMetadata, err error) RoutePromotionEvidence {
	routeName := deriveRouteName(target, metadata)
	if router.ValidateRouteName(routeName) != nil {
		routeName = ""
	}
	routeGroup := deriveRouteGroup(target, metadata)
	if router.ValidateRouteGroup(routeGroup) != nil {
		routeGroup = ""
	}
	host := strings.TrimSpace(metadata.Host)
	if host == "" {
		baseDomain := strings.Trim(strings.TrimSpace(metadata.BaseDomain), ".")
		if baseDomain != "" && strings.TrimSpace(routeName) != "" {
			host = dnsLabel(routeName) + "." + baseDomain
		}
	}
	return RoutePromotionEvidence{
		RouteGroup:     routeGroup,
		RouteName:      routeName,
		HostHash:       routePromotionSupportHash(strings.ToLower(strings.TrimSpace(host))),
		ArtifactStatus: router.RouteArtifactPublicationApplyStatusRefused,
		Errors:         1,
		Diagnostics:    []string{routePromotionDiagnostic(err)},
	}
}

func routePromotionEvidenceForPublisherMissing() RoutePromotionEvidence {
	return RoutePromotionEvidence{
		ArtifactStatus: router.RouteArtifactPublicationApplyStatusError,
		Errors:         1,
		Diagnostics:    []string{"code=route_promotion_publisher_missing"},
	}
}

func (e RoutePromotionEvidence) withApplyEvidence(apply router.RouteArtifactPublicationApplyEvidence) RoutePromotionEvidence {
	e.ArtifactStatus = apply.Status
	e.Writes = apply.Writes
	e.Skips = apply.Skips
	e.Errors = apply.Errors
	e.Diagnostics = append([]string(nil), apply.Diagnostics...)
	return e
}

func (e RoutePromotionEvidence) withPublisherFailure() RoutePromotionEvidence {
	if e.ArtifactStatus == "" {
		e.ArtifactStatus = router.RouteArtifactPublicationApplyStatusError
	}
	if e.Errors == 0 {
		e.Errors = 1
	}
	e.Diagnostics = append(e.Diagnostics, "code=route_promotion_publish_failed")
	return e
}

func routePromotionArtifactHash(route router.RouteInput) string {
	rendered, err := router.RenderTraefikDynamicYAML([]router.RouteInput{route})
	if err != nil {
		return ""
	}
	return router.RenderedConfigHash(rendered)
}

func routePromotionSupportHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func routePromotionDiagnostic(err error) string {
	if errors.Is(err, ErrRoutePromotionInvalidTarget) {
		message := err.Error()
		switch {
		case strings.Contains(message, "deployment id is required"):
			return "code=route_promotion_deployment_id_required"
		case strings.Contains(message, "instance id is required"):
			return "code=route_promotion_instance_id_required"
		case strings.Contains(message, "target is not ready"):
			return "code=route_promotion_target_not_ready"
		case strings.Contains(message, "route group is required"):
			return "code=route_promotion_route_group_required"
		case strings.Contains(message, "route name is required"):
			return "code=route_promotion_route_name_required"
		case strings.Contains(message, "host or base domain is required"):
			return "code=route_promotion_host_required"
		case strings.Contains(message, "internal port is required"):
			return "code=route_promotion_internal_port_required"
		default:
			return "code=route_promotion_validation_failed"
		}
	}
	if errors.Is(err, ErrRoutePromotionPublishFailed) {
		return "code=route_promotion_publish_failed"
	}
	return "code=route_promotion_failed"
}
