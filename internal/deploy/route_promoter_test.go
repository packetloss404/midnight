package deploy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/runtime"
)

func TestTraefikRoutePromoterPublishesRouteInput(t *testing.T) {
	publisher := &fakeRouteGroupPublisher{
		evidence: router.RouteArtifactPublicationApplyEvidence{
			Status: router.RouteArtifactPublicationApplyStatusReady,
			Writes: 1,
		},
	}
	promoter := TraefikRoutePromoter{
		Publisher: publisher,
		Metadata: RoutePromotionMetadata{
			ServiceID:    "api",
			BaseDomain:   "apps.example.test",
			PathPrefix:   "/v1",
			TLS:          true,
			CertResolver: "letsencrypt",
			Middlewares:  []string{"secure-headers"},
			ExistingHash: "old-hash",
		},
	}

	evidence, err := promoter.PromoteWithEvidence(context.Background(), runtime.RoutingTarget{
		DeploymentID:  "dep-2",
		InstanceID:    "dep-2-r0",
		ContainerIP:   "10.42.0.23",
		InternalPort:  8080,
		ProjectID:     "prj-1",
		EnvironmentID: "prod",
		Ready:         true,
	}, RoutePromotionMetadata{Public: true})
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if publisher.routeGroup != "project-prj-1-environment-prod" {
		t.Fatalf("route group = %q, want project-prj-1-environment-prod", publisher.routeGroup)
	}
	if publisher.existingHash != "old-hash" {
		t.Fatalf("existing hash = %q, want old-hash", publisher.existingHash)
	}
	want := []router.RouteInput{{
		Name:         "api",
		Host:         "api.apps.example.test",
		PathPrefix:   "/v1",
		ServiceURL:   "http://10.42.0.23:8080",
		TLS:          true,
		CertResolver: "letsencrypt",
		Middlewares:  []string{"secure-headers"},
	}}
	if !reflect.DeepEqual(publisher.routes, want) {
		t.Fatalf("routes = %#v, want %#v", publisher.routes, want)
	}
	if evidence.RouteGroup != "project-prj-1-environment-prod" || evidence.RouteName != "api" {
		t.Fatalf("evidence route identity = %#v, want route group/name", evidence)
	}
	if evidence.ArtifactStatus != router.RouteArtifactPublicationApplyStatusReady || evidence.Writes != 1 || evidence.Skips != 0 || evidence.Errors != 0 {
		t.Fatalf("evidence apply summary = %#v, want ready write", evidence)
	}
	if evidence.HostHash == "" || evidence.ServiceURLHash == "" || evidence.HostHash == "api.apps.example.test" || strings.Contains(evidence.ServiceURLHash, "10.42") {
		t.Fatalf("evidence hashes are not support-safe: %#v", evidence)
	}
	if evidence.ArtifactHash != routePromotionArtifactHash(want[0]) {
		t.Fatalf("artifact hash = %q, want rendered route artifact hash", evidence.ArtifactHash)
	}
	if len(evidence.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", evidence.Diagnostics)
	}
}

func TestTraefikRoutePromoterEvidenceCapturesSkip(t *testing.T) {
	publisher := &fakeRouteGroupPublisher{
		evidence: router.RouteArtifactPublicationApplyEvidence{
			Status: router.RouteArtifactPublicationApplyStatusReady,
			Skips:  1,
		},
	}
	promoter := TraefikRoutePromoter{
		Publisher: publisher,
		Metadata: RoutePromotionMetadata{
			RouteName:    "api",
			Host:         "api.example.test",
			InternalPort: 8080,
			ExistingHash: "same-hash",
		},
	}

	evidence, err := promoter.PromoteWithEvidence(context.Background(), runtime.RoutingTarget{
		DeploymentID: "dep-2",
		InstanceID:   "dep-2-r0",
		ContainerIP:  "10.42.0.23",
		Ready:        true,
	}, RoutePromotionMetadata{Public: true})
	if err != nil {
		t.Fatalf("PromoteWithEvidence() error = %v", err)
	}
	if evidence.ArtifactStatus != router.RouteArtifactPublicationApplyStatusReady || evidence.Writes != 0 || evidence.Skips != 1 || evidence.Errors != 0 {
		t.Fatalf("evidence = %#v, want ready skip", evidence)
	}
	if evidence.ArtifactHash == "" {
		t.Fatalf("artifact hash is empty in skip evidence: %#v", evidence)
	}
	if publisher.existingHash != "same-hash" {
		t.Fatalf("existing hash = %q, want same-hash", publisher.existingHash)
	}
}

func TestTraefikRoutePromoterUsesDeployRouteMetadataOverride(t *testing.T) {
	publisher := &fakeRouteGroupPublisher{
		evidence: router.RouteArtifactPublicationApplyEvidence{Status: router.RouteArtifactPublicationApplyStatusReady},
	}
	promoter := TraefikRoutePromoter{
		Publisher: publisher,
		Metadata: RoutePromotionMetadata{
			RouteGroup:   "worker-default",
			RouteName:    "default",
			Host:         "default.example.test",
			PathPrefix:   "/",
			InternalPort: 8080,
		},
	}

	err := promoter.Promote(context.Background(), runtime.RoutingTarget{
		DeploymentID: "dep-2",
		InstanceID:   "dep-2-r0",
		ContainerIP:  "10.42.0.23",
		Ready:        true,
	}, RoutePromotionMetadata{
		RouteGroup: "job-group",
		RouteName:  "job-route",
		Host:       "job.example.test",
		PathPrefix: "/job",
		Public:     true,
	})
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if publisher.routeGroup != "job-group" {
		t.Fatalf("route group = %q, want job-group", publisher.routeGroup)
	}
	if len(publisher.routes) != 1 || publisher.routes[0].Name != "job-route" || publisher.routes[0].Host != "job.example.test" || publisher.routes[0].PathPrefix != "/job" {
		t.Fatalf("routes = %#v, want job override metadata", publisher.routes)
	}
}

func TestBuildRoutePromotionUsesLocalhostFallback(t *testing.T) {
	routeGroup, route, err := BuildRoutePromotion(runtime.RoutingTarget{
		DeploymentID:  "dep-3",
		InstanceID:    "dep-3-r0",
		InternalPort:  3000,
		EnvironmentID: "preview",
		Ready:         true,
	}, RoutePromotionMetadata{
		RouteName:  "web",
		BaseDomain: "apps.example.test",
	})
	if err != nil {
		t.Fatalf("BuildRoutePromotion() error = %v", err)
	}
	if routeGroup != "environment-preview" {
		t.Fatalf("route group = %q, want environment-preview", routeGroup)
	}
	if route.ServiceURL != "http://127.0.0.1:3000" {
		t.Fatalf("service URL = %q, want localhost fallback", route.ServiceURL)
	}
}

func TestTraefikRoutePromoterRejectsInvalidTarget(t *testing.T) {
	publisher := &fakeRouteGroupPublisher{}
	promoter := TraefikRoutePromoter{
		Publisher: publisher,
		Metadata:  RoutePromotionMetadata{RouteName: "api", Host: "api.example.test"},
	}

	evidence, err := promoter.PromoteWithEvidence(context.Background(), runtime.RoutingTarget{
		DeploymentID: "dep-2",
		InstanceID:   "dep-2-r0",
		Ready:        true,
	}, RoutePromotionMetadata{Public: true})
	if !errors.Is(err, ErrRoutePromotionInvalidTarget) {
		t.Fatalf("Promote() error = %v, want ErrRoutePromotionInvalidTarget", err)
	}
	if publisher.calls != 0 {
		t.Fatalf("publisher calls = %d, want 0", publisher.calls)
	}
	if evidence.ArtifactStatus != router.RouteArtifactPublicationApplyStatusRefused || evidence.Errors != 1 {
		t.Fatalf("validation evidence = %#v, want refused error", evidence)
	}
	if evidence.RouteGroup != "deployment-dep-2" || evidence.RouteName != "api" || evidence.HostHash == "" {
		t.Fatalf("validation evidence route fields = %#v, want support-safe route identity", evidence)
	}
	if !hasDiagnostic(evidence.Diagnostics, "code=route_promotion_internal_port_required") {
		t.Fatalf("diagnostics = %#v, want internal port code", evidence.Diagnostics)
	}
	if strings.Contains(strings.Join(evidence.Diagnostics, " "), "api.example.test") || strings.Contains(strings.Join(evidence.Diagnostics, " "), "10.42") {
		t.Fatalf("diagnostics leak raw route details: %#v", evidence.Diagnostics)
	}
}

func TestTraefikRoutePromoterReturnsPublisherFailure(t *testing.T) {
	publishErr := errors.New("disk full")
	promoter := TraefikRoutePromoter{
		Publisher: &fakeRouteGroupPublisher{
			evidence: router.RouteArtifactPublicationApplyEvidence{
				Status:      router.RouteArtifactPublicationApplyStatusError,
				Errors:      1,
				Diagnostics: []string{"code=route_publication_write_failed"},
			},
			err: publishErr,
		},
		Metadata: RoutePromotionMetadata{
			RouteName:    "api",
			Host:         "api.example.test",
			InternalPort: 8080,
		},
	}

	evidence, err := promoter.PromoteWithEvidence(context.Background(), runtime.RoutingTarget{
		DeploymentID: "dep-2",
		InstanceID:   "dep-2-r0",
		ContainerIP:  "10.42.0.23",
		Ready:        true,
	}, RoutePromotionMetadata{Public: true})
	if !errors.Is(err, ErrRoutePromotionPublishFailed) || !errors.Is(err, publishErr) {
		t.Fatalf("Promote() error = %v, want wrapped publisher failure", err)
	}
	if evidence.ArtifactStatus != router.RouteArtifactPublicationApplyStatusError || evidence.Errors != 1 {
		t.Fatalf("publisher failure evidence = %#v, want error count", evidence)
	}
	if !hasDiagnostic(evidence.Diagnostics, "code=route_publication_write_failed") || !hasDiagnostic(evidence.Diagnostics, "code=route_promotion_publish_failed") {
		t.Fatalf("diagnostics = %#v, want publisher and promotion failure codes", evidence.Diagnostics)
	}
	if evidence.ServiceURLHash == "" || strings.Contains(evidence.ServiceURLHash, "10.42") {
		t.Fatalf("service URL hash is not support-safe: %#v", evidence)
	}
}

func TestDNSLabelHardensNonASCIIAndOversizedInput(t *testing.T) {
	for _, r := range dnsLabel("Ünïcödé-Sérvice") {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		t.Fatalf("dnsLabel leaked non-LDH rune %q from non-ascii input", r)
	}

	oversized := dnsLabel(strings.Repeat("service", 20))
	if len(oversized) > 63 {
		t.Fatalf("dnsLabel length = %d, want <= 63 octets", len(oversized))
	}
	if strings.HasPrefix(oversized, "-") || strings.HasSuffix(oversized, "-") {
		t.Fatalf("dnsLabel = %q, must not start or end with hyphen", oversized)
	}
}

func TestBuildRoutePromotionGeneratesHardenedLeftLabel(t *testing.T) {
	_, route, err := BuildRoutePromotion(runtime.RoutingTarget{
		DeploymentID: "dep-9",
		InstanceID:   "dep-9-r0",
		ContainerIP:  "10.0.0.9",
		InternalPort: 8080,
		Ready:        true,
	}, RoutePromotionMetadata{
		RouteName:  "api_worker",
		BaseDomain: "apps.example.test",
	})
	if err != nil {
		t.Fatalf("BuildRoutePromotion() error = %v", err)
	}

	suffix := ".apps.example.test"
	if !strings.HasSuffix(route.Host, suffix) {
		t.Fatalf("host = %q, want %q suffix", route.Host, suffix)
	}
	left := strings.TrimSuffix(route.Host, suffix)
	if len(left) == 0 || len(left) > 63 {
		t.Fatalf("generated left label length = %d, want 1..63", len(left))
	}
	if left != "api-worker" {
		t.Fatalf("generated left label = %q, want api-worker", left)
	}
	for _, r := range left {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		t.Fatalf("generated left label %q leaked non-LDH rune %q", left, r)
	}
}

func TestTraefikRoutePromoterPrivateDeploySkipsPublisher(t *testing.T) {
	publisher := &fakeRouteGroupPublisher{}
	promoter := TraefikRoutePromoter{
		Publisher: publisher,
		Metadata: RoutePromotionMetadata{
			Public:       true, // A worker default must not override the deploy intent.
			RouteGroup:   "apps",
			RouteName:    "api",
			Host:         "api.example.test",
			InternalPort: 8080,
		},
	}

	evidence, err := promoter.PromoteWithEvidence(context.Background(), runtime.RoutingTarget{
		DeploymentID: "dep-private",
		InstanceID:   "dep-private-r0",
		ContainerIP:  "10.42.0.23",
		Ready:        true,
	}, RoutePromotionMetadata{Public: false})
	if err != nil {
		t.Fatalf("PromoteWithEvidence() error = %v", err)
	}
	if publisher.calls != 0 {
		t.Fatalf("publisher calls = %d, want 0 for private deploy", publisher.calls)
	}
	if evidence.ArtifactStatus != router.RouteArtifactPublicationApplyStatusReady ||
		evidence.Skips != 1 ||
		!hasDiagnostic(evidence.Diagnostics, "code=route_promotion_private") {
		t.Fatalf("private evidence = %#v, want explicit publication skip", evidence)
	}
}

func TestTraefikRoutePromoterRejectsUnsafeMetadataBeforePublish(t *testing.T) {
	validTarget := runtime.RoutingTarget{
		DeploymentID: "dep-unsafe",
		InstanceID:   "dep-unsafe-r0",
		ContainerIP:  "10.42.0.23",
		InternalPort: 8080,
		Ready:        true,
	}
	tests := []struct {
		name     string
		metadata RoutePromotionMetadata
		target   runtime.RoutingTarget
	}{
		{
			name:     "route name YAML injection",
			metadata: RoutePromotionMetadata{RouteGroup: "apps", RouteName: "api:\n  evil", Host: "api.example.test", PathPrefix: "/", Public: true},
			target:   validTarget,
		},
		{
			name:     "route group traversal",
			metadata: RoutePromotionMetadata{RouteGroup: "../apps", RouteName: "api", Host: "api.example.test", PathPrefix: "/", Public: true},
			target:   validTarget,
		},
		{
			name:     "host rule injection",
			metadata: RoutePromotionMetadata{RouteGroup: "apps", RouteName: "api", Host: "api.example.test`) || Host(`evil.example.test", PathPrefix: "/", Public: true},
			target:   validTarget,
		},
		{
			name:     "host trailing newline",
			metadata: RoutePromotionMetadata{RouteGroup: "apps", RouteName: "api", Host: "api.example.test\n", PathPrefix: "/", Public: true},
			target:   validTarget,
		},
		{
			name:     "host wildcard",
			metadata: RoutePromotionMetadata{RouteGroup: "apps", RouteName: "api", Host: "*.example.test", PathPrefix: "/", Public: true},
			target:   validTarget,
		},
		{
			name:     "host port",
			metadata: RoutePromotionMetadata{RouteGroup: "apps", RouteName: "api", Host: "api.example.test:443", PathPrefix: "/", Public: true},
			target:   validTarget,
		},
		{
			name:     "path rule injection",
			metadata: RoutePromotionMetadata{RouteGroup: "apps", RouteName: "api", Host: "api.example.test", PathPrefix: "/v1`) || Host(`evil.example.test", Public: true},
			target:   validTarget,
		},
		{
			name:     "path encoded rule injection",
			metadata: RoutePromotionMetadata{RouteGroup: "apps", RouteName: "api", Host: "api.example.test", PathPrefix: "/v1%60)%20%7C%7C", Public: true},
			target:   validTarget,
		},
		{
			name:     "invalid target port",
			metadata: RoutePromotionMetadata{RouteGroup: "apps", RouteName: "api", Host: "api.example.test", PathPrefix: "/", Public: true},
			target: func() runtime.RoutingTarget {
				target := validTarget
				target.InternalPort = 70000
				return target
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			publisher := &fakeRouteGroupPublisher{}
			promoter := TraefikRoutePromoter{Publisher: publisher}

			evidence, err := promoter.PromoteWithEvidence(context.Background(), tt.target, tt.metadata)
			if !errors.Is(err, ErrRoutePromotionInvalidTarget) {
				t.Fatalf("PromoteWithEvidence() error = %v, want ErrRoutePromotionInvalidTarget", err)
			}
			if publisher.calls != 0 {
				t.Fatalf("publisher calls = %d, want 0", publisher.calls)
			}
			if evidence.ArtifactStatus != router.RouteArtifactPublicationApplyStatusRefused || evidence.Errors != 1 {
				t.Fatalf("validation evidence = %#v, want refused error", evidence)
			}
			if strings.ContainsAny(evidence.RouteGroup, "\r\n`/") ||
				strings.ContainsAny(evidence.RouteName, "\r\n`:") {
				t.Fatalf("validation evidence contains unsafe route identity: %#v", evidence)
			}
		})
	}
}

type fakeRouteGroupPublisher struct {
	calls        int
	routeGroup   string
	routes       []router.RouteInput
	existingHash string
	evidence     router.RouteArtifactPublicationApplyEvidence
	err          error
}

func (p *fakeRouteGroupPublisher) PublishRouteGroup(routeGroup string, routes []router.RouteInput, existingHash string) (router.RouteArtifactPublicationApplyEvidence, error) {
	p.calls++
	p.routeGroup = routeGroup
	p.routes = append([]router.RouteInput(nil), routes...)
	p.existingHash = existingHash
	return p.evidence, p.err
}

func hasDiagnostic(diagnostics []string, want string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic == want {
			return true
		}
	}
	return false
}
