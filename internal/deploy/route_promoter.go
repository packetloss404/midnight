package deploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/midnight/midnight/internal/domain"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/runtime"
)

var (
	ErrRoutePromotionInvalidTarget = errors.New("route promoter: invalid routing target")
	ErrRoutePromotionPublishFailed = errors.New("route promoter: publish failed")
)

// RouteGroupPublisher is the small publication boundary RoutePromoter needs.
// router.TraefikDynamicPublicationDirectory satisfies this interface.
type RouteGroupPublisher interface {
	PublishRouteGroup(routeGroup string, routes []router.RouteInput, existingHash string) (router.RouteArtifactPublicationApplyEvidence, error)
}

// RoutePromotionMetadata carries route-level data that is not produced by the
// runtime adapter. Explicit fields win; otherwise deterministic names are
// derived from the routing target and service metadata.
type RoutePromotionMetadata struct {
	RouteGroup   string
	RouteName    string
	ServiceID    string
	Host         string
	BaseDomain   string
	PathPrefix   string
	Public       bool
	TLS          bool
	CertResolver string
	Middlewares  []string
	ExistingHash string
	InternalPort int
	Scheme       string
}

// TraefikRoutePromoter converts a runtime RoutingTarget into a router
// RouteInput and publishes one Traefik file-provider route group artifact.
type TraefikRoutePromoter struct {
	Publisher RouteGroupPublisher
	Metadata  RoutePromotionMetadata
}

var (
	_ RoutePromoter       = TraefikRoutePromoter{}
	_ RouteGroupPublisher = (*router.TraefikDynamicPublicationDirectory)(nil)
)

func (p TraefikRoutePromoter) Promote(ctx context.Context, target runtime.RoutingTarget, metadata RoutePromotionMetadata) error {
	_, err := p.PromoteWithEvidence(ctx, target, metadata)
	return err
}

// PromoteWithEvidence publishes the route group and returns deploy-level
// support-safe evidence that callers can attach to telemetry or release docs.
// Promote keeps the existing RoutePromoter interface stable for the executor.
func (p TraefikRoutePromoter) PromoteWithEvidence(ctx context.Context, target runtime.RoutingTarget, metadata RoutePromotionMetadata) (RoutePromotionEvidence, error) {
	if err := ctx.Err(); err != nil {
		return RoutePromotionEvidence{}, err
	}
	// Publication is a per-deploy opt-in. Worker defaults configure where a
	// route would be written, but they must never turn a private deploy public.
	if !metadata.Public {
		return RoutePromotionEvidence{
			ArtifactStatus: router.RouteArtifactPublicationApplyStatusReady,
			Skips:          1,
			Diagnostics:    []string{"code=route_promotion_private"},
		}, nil
	}
	if p.Publisher == nil {
		evidence := routePromotionEvidenceForPublisherMissing()
		return evidence, fmt.Errorf("%w: publisher is required", ErrRoutePromotionPublishFailed)
	}
	routeMetadata := mergeRoutePromotionMetadata(p.Metadata, metadata)
	routeGroup, route, err := BuildRoutePromotion(target, routeMetadata)
	if err != nil {
		return routePromotionEvidenceForValidationFailure(target, routeMetadata, err), err
	}
	evidence := routePromotionEvidenceForRoute(routeGroup, route)
	applyEvidence, err := p.Publisher.PublishRouteGroup(routeGroup, []router.RouteInput{route}, routeMetadata.ExistingHash)
	evidence = evidence.withApplyEvidence(applyEvidence)
	if err != nil {
		evidence = evidence.withPublisherFailure()
		return evidence, fmt.Errorf("%w: %w", ErrRoutePromotionPublishFailed, err)
	}
	if applyEvidence.Status != router.RouteArtifactPublicationApplyStatusReady {
		return evidence, fmt.Errorf("%w: status=%s errors=%d diagnostics=%s",
			ErrRoutePromotionPublishFailed,
			applyEvidence.Status,
			applyEvidence.Errors,
			strings.Join(applyEvidence.Diagnostics, ","),
		)
	}
	return evidence, nil
}

func mergeRoutePromotionMetadata(base, override RoutePromotionMetadata) RoutePromotionMetadata {
	merged := base
	if override.RouteGroup != "" {
		merged.RouteGroup = override.RouteGroup
	}
	if override.RouteName != "" {
		merged.RouteName = override.RouteName
	}
	if override.ServiceID != "" {
		merged.ServiceID = override.ServiceID
	}
	if override.Host != "" {
		merged.Host = override.Host
	}
	if override.BaseDomain != "" {
		merged.BaseDomain = override.BaseDomain
	}
	if override.PathPrefix != "" {
		merged.PathPrefix = override.PathPrefix
	}
	if override.Public {
		merged.Public = true
	}
	if override.TLS {
		merged.TLS = true
	}
	if override.CertResolver != "" {
		merged.CertResolver = override.CertResolver
	}
	if len(override.Middlewares) > 0 {
		merged.Middlewares = append([]string(nil), override.Middlewares...)
	}
	if value := strings.TrimSpace(override.ExistingHash); value != "" {
		merged.ExistingHash = value
	}
	if override.InternalPort > 0 {
		merged.InternalPort = override.InternalPort
	}
	if override.Scheme != "" {
		merged.Scheme = override.Scheme
	}
	return merged
}

// BuildRoutePromotion is the pure conversion from runtime target plus route
// metadata to the route group and RouteInput expected by the router package.
func BuildRoutePromotion(target runtime.RoutingTarget, metadata RoutePromotionMetadata) (string, router.RouteInput, error) {
	if err := validatePromotionTarget(target); err != nil {
		return "", router.RouteInput{}, err
	}
	routeGroup := deriveRouteGroup(target, metadata)
	if err := router.ValidateRouteGroup(routeGroup); err != nil {
		return "", router.RouteInput{}, fmt.Errorf("%w: %v", ErrRoutePromotionInvalidTarget, err)
	}
	routeName := deriveRouteName(target, metadata)
	if routeName == "" {
		return "", router.RouteInput{}, fmt.Errorf("%w: route name is required", ErrRoutePromotionInvalidTarget)
	}
	host := metadata.Host
	if host == "" {
		baseDomain := metadata.BaseDomain
		if baseDomain == "" {
			return "", router.RouteInput{}, fmt.Errorf("%w: host or base domain is required", ErrRoutePromotionInvalidTarget)
		}
		host = dnsLabel(routeName) + "." + baseDomain
	}
	serviceURL, err := serviceURLForTarget(target, metadata)
	if err != nil {
		return "", router.RouteInput{}, err
	}
	route := router.RouteInput{
		Name:         routeName,
		Host:         host,
		PathPrefix:   metadata.PathPrefix,
		ServiceURL:   serviceURL,
		TLS:          metadata.TLS,
		CertResolver: metadata.CertResolver,
		Middlewares:  append([]string(nil), metadata.Middlewares...),
	}
	if err := router.ValidateRouteInput(route); err != nil {
		return "", router.RouteInput{}, fmt.Errorf("%w: %v", ErrRoutePromotionInvalidTarget, err)
	}
	return routeGroup, route, nil
}

func validatePromotionTarget(target runtime.RoutingTarget) error {
	if strings.TrimSpace(target.DeploymentID) == "" {
		return fmt.Errorf("%w: deployment id is required", ErrRoutePromotionInvalidTarget)
	}
	if strings.TrimSpace(target.InstanceID) == "" {
		return fmt.Errorf("%w: instance id is required", ErrRoutePromotionInvalidTarget)
	}
	if !target.Ready {
		return fmt.Errorf("%w: target is not ready", ErrRoutePromotionInvalidTarget)
	}
	return nil
}

func deriveRouteGroup(target runtime.RoutingTarget, metadata RoutePromotionMetadata) string {
	if metadata.RouteGroup != "" {
		return metadata.RouteGroup
	}
	projectID := target.ProjectID
	environmentID := target.EnvironmentID
	if projectID != "" && environmentID != "" {
		return "project-" + projectID + "-environment-" + environmentID
	}
	if environmentID != "" {
		return "environment-" + environmentID
	}
	if projectID != "" {
		return "project-" + projectID
	}
	return "deployment-" + strings.TrimSpace(target.DeploymentID)
}

func deriveRouteName(target runtime.RoutingTarget, metadata RoutePromotionMetadata) string {
	for _, candidate := range []string{
		metadata.RouteName,
		metadata.ServiceID,
		target.DeploymentID,
		target.InstanceID,
	} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func serviceURLForTarget(target runtime.RoutingTarget, metadata RoutePromotionMetadata) (string, error) {
	port := target.InternalPort
	if port <= 0 {
		port = metadata.InternalPort
	}
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("%w: internal port is required", ErrRoutePromotionInvalidTarget)
	}
	host := strings.TrimSpace(target.ContainerIP)
	if host == "" {
		host = "127.0.0.1"
	}
	scheme := metadata.Scheme
	if scheme == "" {
		scheme = "http"
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// dnsLabel produces the ASCII-only, length-capped left label used when a
// generated host is assembled from a route name plus a base domain. It
// delegates to internal/domain so non-ASCII input is transliterated to LDH
// characters (never emitted as raw multibyte UTF-8) and the label is capped
// at the DNS 63-octet limit with trailing hyphens trimmed. An empty or
// all-invalid seed falls back to the shared default label ("app").
func dnsLabel(value string) string {
	return domain.DNSLabel(value)
}
