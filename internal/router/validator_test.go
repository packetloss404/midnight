package router

import (
	"strings"
	"testing"
)

func TestValidateRenderedRouteGroupAcceptsRenderedArtifact(t *testing.T) {
	rendered, err := RenderTraefikDynamicYAML([]RouteInput{{
		Name:         "app",
		Host:         "app.example.test",
		ServiceURL:   "http://127.0.0.1:8080",
		TLS:          true,
		CertResolver: "letsencrypt",
	}})
	if err != nil {
		t.Fatalf("RenderTraefikDynamicYAML returned error: %v", err)
	}
	file, err := RenderTraefikFile([]RouteInput{{
		Name:         "app",
		Host:         "app.example.test",
		ServiceURL:   "http://127.0.0.1:8080",
		TLS:          true,
		CertResolver: "letsencrypt",
	}})
	if err != nil {
		t.Fatalf("RenderTraefikFile returned error: %v", err)
	}

	result := ValidateRenderedRouteGroup(file, rendered, RenderedConfigHash(rendered))
	if !result.OK() {
		t.Fatalf("expected artifact to validate, diagnostics: %#v", result.Diagnostics)
	}
}

func TestValidateRenderedRouteGroupFindsDuplicateHostPath(t *testing.T) {
	file := TraefikFile{HTTP: TraefikHTTP{
		Routers: map[string]TraefikRouter{
			"a": {Rule: "Host(`app.example.test`) && PathPrefix(`/`)", Service: "a-service"},
			"b": {Rule: "Host(`app.example.test`) && PathPrefix(`/`)", Service: "b-service"},
		},
		Services: map[string]TraefikService{
			"a-service": serviceWithURL("http://127.0.0.1:8080"),
			"b-service": serviceWithURL("http://127.0.0.1:8081"),
		},
	}}

	result := ValidateRenderedRouteGroup(file, "", "")
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != RouteArtifactDuplicateHostPath {
		t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
	}
}

func TestValidateRenderedRouteGroupFindsMissingServiceTargets(t *testing.T) {
	file := TraefikFile{HTTP: TraefikHTTP{
		Routers: map[string]TraefikRouter{
			"missing": {Rule: "Host(`missing.example.test`) && PathPrefix(`/`)", Service: "missing-service"},
			"empty":   {Rule: "Host(`empty.example.test`) && PathPrefix(`/`)", Service: "empty-service"},
			"bad-url": {Rule: "Host(`bad.example.test`) && PathPrefix(`/`)", Service: "bad-service"},
		},
		Services: map[string]TraefikService{
			"empty-service": {},
			"bad-service":   serviceWithURL("://bad"),
		},
	}}

	result := ValidateRenderedRouteGroup(file, "", "")
	if len(result.Diagnostics) != 3 {
		t.Fatalf("diagnostic count = %d, want 3: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code != RouteArtifactMissingServiceTarget {
			t.Fatalf("unexpected diagnostic: %#v", diagnostic)
		}
	}
}

func TestValidateRenderedRouteGroupFindsInvalidHTTPSCertResolver(t *testing.T) {
	file := TraefikFile{HTTP: TraefikHTTP{
		Routers: map[string]TraefikRouter{
			"app": {Rule: "Host(`app.example.test`) && PathPrefix(`/`)", Service: "app-service", EntryPoints: []string{"web"}, TLS: &TLS{CertResolver: "letsencrypt"}},
		},
		Services: map[string]TraefikService{"app-service": serviceWithURL("http://127.0.0.1:8080")},
	}}

	result := ValidateRenderedRouteGroup(file, "", "")
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != RouteArtifactInvalidHTTPSResolver {
		t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
	}
}

func TestValidateRenderedRouteGroupFindsStaleHash(t *testing.T) {
	result := ValidateRenderedRouteGroup(TraefikFile{}, "current-rendered", RenderedConfigHash("old-rendered"))
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != RouteArtifactStaleHash {
		t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
	}
}

func TestRouteArtifactDiagnosticsAreSupportSafe(t *testing.T) {
	file := TraefikFile{HTTP: TraefikHTTP{
		Routers: map[string]TraefikRouter{
			"app": {Rule: "Host(`secret.example.test`) && PathPrefix(`/private`)", Service: "app-service"},
		},
	}}

	result := ValidateRenderedRouteGroup(file, "current-rendered", RenderedConfigHash("old-rendered"))
	joined := strings.Join(result.SupportSafeDiagnostics(), "\n")
	for _, leaked := range []string{"secret.example.test", "/private", "app-service", "current-rendered"} {
		if strings.Contains(joined, leaked) {
			t.Fatalf("support diagnostics leaked %q: %s", leaked, joined)
		}
	}
	for _, want := range []string{"code=route_artifact_missing_service_target", "router_hash=", "service_hash=", "code=route_artifact_stale_hash"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("support diagnostics missing %q: %s", want, joined)
		}
	}
}

func TestRenderTraefikFileRejectsCertResolverWithoutTLS(t *testing.T) {
	_, err := RenderTraefikFile([]RouteInput{{
		Name:         "app",
		Host:         "app.example.test",
		ServiceURL:   "http://127.0.0.1:8080",
		CertResolver: "letsencrypt",
	}})
	if err == nil {
		t.Fatal("expected cert resolver without TLS error")
	}
}

func serviceWithURL(rawURL string) TraefikService {
	return TraefikService{LoadBalancer: TraefikLoadBalancer{Servers: []TraefikServer{{URL: rawURL}}}}
}
