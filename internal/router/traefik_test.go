package router

import (
	"strings"
	"testing"
)

func TestRenderTraefikFile(t *testing.T) {
	file, err := RenderTraefikFile([]RouteInput{{
		Name:        "app",
		Host:        "app.example.test",
		PathPrefix:  "/",
		ServiceURL:  "http://127.0.0.1:8080",
		TLS:         true,
		Middlewares: []string{"secure-headers"},
	}})
	if err != nil {
		t.Fatalf("RenderTraefikFile returned error: %v", err)
	}

	router := file.HTTP.Routers["app"]
	if router.Rule != "Host(`app.example.test`) && PathPrefix(`/`)" {
		t.Fatalf("unexpected rule: %s", router.Rule)
	}
	if router.TLS == nil {
		t.Fatal("expected TLS marker")
	}
	if got := file.HTTP.Services["app-service"].LoadBalancer.Servers[0].URL; got != "http://127.0.0.1:8080" {
		t.Fatalf("unexpected service URL: %s", got)
	}

	acme := file.HTTP.Routers[ACMEChallengeRouterName]
	if acme.Service != ACMEChallengeServiceName || acme.EntryPoints[0] != ACMEChallengeEntryPoint {
		t.Fatalf("unexpected ACME router: %#v", acme)
	}
	if acme.Priority != ACMEChallengePriority {
		t.Fatalf("ACME priority = %d, want %d", acme.Priority, ACMEChallengePriority)
	}
}

func TestRenderTraefikFileDefaultsPathPrefix(t *testing.T) {
	file, err := RenderTraefikFile([]RouteInput{{
		Name:       "app",
		Host:       "app.example.test",
		ServiceURL: "http://127.0.0.1:8080",
	}})
	if err != nil {
		t.Fatalf("RenderTraefikFile returned error: %v", err)
	}

	if got := file.HTTP.Routers["app"].Rule; got != "Host(`app.example.test`) && PathPrefix(`/`)" {
		t.Fatalf("unexpected rule: %s", got)
	}
}

func TestRenderTraefikFileRejectsReservedACMEPath(t *testing.T) {
	_, err := RenderTraefikFile([]RouteInput{{
		Name:       "bad",
		Host:       "app.example.test",
		PathPrefix: "/.well-known/acme-challenge/token",
		ServiceURL: "http://127.0.0.1:8080",
	}})
	if err == nil {
		t.Fatal("expected reserved ACME path error")
	}
}

func TestRenderTraefikFileRejectsUnsafeRuleAndYAMLInputs(t *testing.T) {
	valid := RouteInput{
		Name:       "app",
		Host:       "app.example.test",
		PathPrefix: "/v1",
		ServiceURL: "http://127.0.0.1:8080",
	}
	tests := []struct {
		name   string
		mutate func(*RouteInput)
	}{
		{
			name: "route name newline YAML injection",
			mutate: func(route *RouteInput) {
				route.Name = "app:\n    injected"
			},
		},
		{
			name: "route name backtick",
			mutate: func(route *RouteInput) {
				route.Name = "app`evil"
			},
		},
		{
			name: "host backtick rule injection",
			mutate: func(route *RouteInput) {
				route.Host = "app.example.test`) || Host(`evil.example.test"
			},
		},
		{
			name: "host newline rule injection",
			mutate: func(route *RouteInput) {
				route.Host = "app.example.test\nHost(`evil.example.test`)"
			},
		},
		{
			name: "host wildcard",
			mutate: func(route *RouteInput) {
				route.Host = "*.example.test"
			},
		},
		{
			name: "host with port",
			mutate: func(route *RouteInput) {
				route.Host = "app.example.test:443"
			},
		},
		{
			name: "host empty label",
			mutate: func(route *RouteInput) {
				route.Host = "app..example.test"
			},
		},
		{
			name: "host leading hyphen label",
			mutate: func(route *RouteInput) {
				route.Host = "-app.example.test"
			},
		},
		{
			name: "path backtick rule injection",
			mutate: func(route *RouteInput) {
				route.PathPrefix = "/v1`) || Host(`evil.example.test"
			},
		},
		{
			name: "path newline rule injection",
			mutate: func(route *RouteInput) {
				route.PathPrefix = "/v1\nHost(`evil.example.test`)"
			},
		},
		{
			name: "path encoded backtick rule injection",
			mutate: func(route *RouteInput) {
				route.PathPrefix = "/v1%60%29"
			},
		},
		{
			name: "path query",
			mutate: func(route *RouteInput) {
				route.PathPrefix = "/v1?admin=true"
			},
		},
		{
			name: "path dot segment",
			mutate: func(route *RouteInput) {
				route.PathPrefix = "/v1/../admin"
			},
		},
		{
			name: "backend port zero",
			mutate: func(route *RouteInput) {
				route.ServiceURL = "http://127.0.0.1:0"
			},
		},
		{
			name: "backend port too large",
			mutate: func(route *RouteInput) {
				route.ServiceURL = "http://127.0.0.1:70000"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := valid
			tt.mutate(&route)
			if _, err := RenderTraefikFile([]RouteInput{route}); err == nil {
				t.Fatalf("RenderTraefikFile(%#v) error = nil, want rejection", route)
			}
		})
	}
}

func TestIsReservedACMEPath(t *testing.T) {
	tests := map[string]bool{
		"/.well-known/acme-challenge":       true,
		"/.well-known/acme-challenge/token": true,
		"/.well-known/acme":                 false,
		"/":                                 false,
	}

	for path, want := range tests {
		if got := IsReservedACMEPath(path); got != want {
			t.Fatalf("IsReservedACMEPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestRenderTraefikDynamicYAMLIsDeterministic(t *testing.T) {
	routes := []RouteInput{
		{Name: "z-api", Host: "z.example.test", ServiceURL: "http://127.0.0.1:9000"},
		{Name: "app", Host: "app.example.test", ServiceURL: "http://127.0.0.1:8080", TLS: true, Middlewares: []string{"secure-headers"}},
	}

	first, err := RenderTraefikDynamicYAML(routes)
	if err != nil {
		t.Fatalf("RenderTraefikDynamicYAML returned error: %v", err)
	}
	second, err := RenderTraefikDynamicYAML(routes)
	if err != nil {
		t.Fatalf("RenderTraefikDynamicYAML returned error: %v", err)
	}
	if first != second {
		t.Fatalf("renderer is not deterministic:\nfirst:\n%s\nsecond:\n%s", first, second)
	}

	for _, want := range []string{
		"http:\n",
		"    acme-http01-challenge:\n",
		"      rule: 'PathPrefix(`/.well-known/acme-challenge/`)'\n",
		"      service: 'acme-http@internal'\n",
		"      priority: 2147483647\n",
		"    app:\n",
		"      tls: {}\n",
		"      middlewares:\n        - 'secure-headers'\n",
		"    app-service:\n",
		"          - url: 'http://127.0.0.1:8080'\n",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("YAML missing %q:\n%s", want, first)
		}
	}
	if strings.Contains(first, "docker") || strings.Contains(first, "providers") {
		t.Fatalf("dynamic YAML must not render provider configuration:\n%s", first)
	}
}

func TestIngressDefaultConstantsArePositive(t *testing.T) {
	if DefaultIngressReadTimeout <= 0 || DefaultIngressWriteTimeout <= 0 || DefaultIngressIdleTimeout <= 0 {
		t.Fatal("ingress timeout defaults must be positive")
	}
	if DefaultIngressMaxBodyBytes <= 0 {
		t.Fatal("ingress max body default must be positive")
	}
}
