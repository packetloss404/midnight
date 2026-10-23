package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"

	midnightnetwork "github.com/midnight/midnight/internal/network"
)

func TestBuildGeneratedDomainIsStableSingleLabelAndPrivateByDefault(t *testing.T) {
	input := DomainRouteIntentInput{
		Scope: RouteScope{ProjectID: "prj_one", EnvironmentID: "env_prod", ServiceID: "svc_API"},
		Kind:  DomainKindGenerated, AppsDomain: "apps.example.test",
		TargetPort: 8080,
	}
	first, route, err := BuildDomainRouteIntent(input)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := BuildDomainRouteIntent(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Hostname != second.Hostname {
		t.Fatalf("generated intent is not stable: %#v %#v", first, second)
	}
	label := strings.TrimSuffix(first.Hostname, ".apps.example.test")
	if label == first.Hostname || strings.Contains(label, ".") || len(label) > 63 {
		t.Fatalf("generated hostname is not a single safe label: %q", first.Hostname)
	}
	if route.Public {
		t.Fatal("generated route became public without explicit opt-in")
	}
}

func TestHTTPTraefikPublicationVerifierRequiresActiveRouterAndHostProbe(t *testing.T) {
	group := DurableRouteGroup{
		ProjectID:       "prj_one",
		EnvironmentID:   "env_prod",
		DesiredRevision: 7,
	}
	routes := revisionScopedRoutes(group, []RouteInput{{
		Name:       "api",
		Host:       "app.example.test",
		PathPrefix: "/",
		ServiceURL: "http://10.0.0.2:8080",
	}})
	apiServer := httptest.NewServer(activeTraefikRouteSetHandler(t, routes))
	defer apiServer.Close()
	probeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "app.example.test" {
			http.Error(w, "wrong host", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer probeServer.Close()

	verifier := HTTPTraefikPublicationVerifier{
		AcknowledgementTimeout:  50 * time.Millisecond,
		AcknowledgementInterval: 5 * time.Millisecond,
		TraefikAPIBaseURL:       apiServer.URL, IngressBaseURL: probeServer.URL,
	}
	result, err := verifier.VerifyRoutePublication(
		context.Background(),
		group,
		routes,
		renderedRouteSetHash(t, routes),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready() || result.ProbeStatus != http.StatusNoContent {
		t.Fatalf("verification = %#v", result)
	}
}

func TestHTTPTraefikPublicationVerifierRequiresEveryRouteProbe(t *testing.T) {
	group := DurableRouteGroup{
		ProjectID:       "prj_one",
		EnvironmentID:   "env_prod",
		DesiredRevision: 8,
	}
	routes := revisionScopedRoutes(group, []RouteInput{
		{Name: "first", Host: "first.example.test", PathPrefix: "/", ServiceURL: "http://10.0.0.2:8080"},
		{Name: "second", Host: "second.example.test", PathPrefix: "/", ServiceURL: "http://10.0.0.3:8080"},
	})
	apiServer := httptest.NewServer(activeTraefikRouteSetHandler(t, routes))
	defer apiServer.Close()
	probes := 0
	probeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes++
		if r.Host == "second.example.test" {
			http.Error(w, "second route did not converge", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer probeServer.Close()

	verifier := HTTPTraefikPublicationVerifier{
		AcknowledgementTimeout:  50 * time.Millisecond,
		AcknowledgementInterval: 5 * time.Millisecond,
		TraefikAPIBaseURL:       apiServer.URL,
		IngressBaseURL:          probeServer.URL,
	}
	result, err := verifier.VerifyRoutePublication(
		context.Background(),
		group,
		routes,
		renderedRouteSetHash(t, routes),
	)
	if err == nil ||
		result.Ready() ||
		result.IngressProbeSucceeded ||
		probes != 2 ||
		!strings.Contains(result.ErrorDetail, "second") {
		t.Fatalf("verification=%#v err=%v probes=%d", result, err, probes)
	}
}

func TestHTTPTraefikPublicationVerifierRejectsStaleOldRevision(t *testing.T) {
	oldGroup := DurableRouteGroup{
		ProjectID:       "prj_one",
		EnvironmentID:   "env_prod",
		DesiredRevision: 10,
	}
	newGroup := oldGroup
	newGroup.DesiredRevision = 11
	oldRoutes := revisionScopedRoutes(oldGroup, []RouteInput{{
		Name:       "api",
		Host:       "app.example.test",
		PathPrefix: "/",
		ServiceURL: "http://10.0.0.1:8080",
	}})
	newRoutes := revisionScopedRoutes(newGroup, []RouteInput{{
		Name:       "api",
		Host:       "app.example.test",
		PathPrefix: "/",
		ServiceURL: "http://10.0.0.2:8080",
	}})
	apiServer := httptest.NewServer(activeTraefikRouteSetHandler(t, oldRoutes))
	defer apiServer.Close()
	probes := 0
	probeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer probeServer.Close()

	verifier := HTTPTraefikPublicationVerifier{
		AcknowledgementTimeout:  50 * time.Millisecond,
		AcknowledgementInterval: 5 * time.Millisecond,
		TraefikAPIBaseURL:       apiServer.URL,
		IngressBaseURL:          probeServer.URL,
	}
	result, err := verifier.VerifyRoutePublication(
		context.Background(),
		newGroup,
		newRoutes,
		renderedRouteSetHash(t, newRoutes),
	)
	if err == nil ||
		result.TraefikAcknowledged ||
		result.IngressProbeSucceeded ||
		probes != 0 ||
		!strings.Contains(result.ErrorDetail, "desired route revision") {
		t.Fatalf("verification=%#v err=%v probes=%d", result, err, probes)
	}
}

func TestHTTPTraefikPublicationVerifierRejectsOverlappingOldRevision(
	t *testing.T,
) {
	oldGroup := DurableRouteGroup{
		ProjectID:       "prj_one",
		EnvironmentID:   "env_prod",
		DesiredRevision: 20,
	}
	newGroup := oldGroup
	newGroup.DesiredRevision = 21
	oldRoutes := revisionScopedRoutes(oldGroup, []RouteInput{{
		Name:       "api",
		Host:       "app.example.test",
		PathPrefix: "/",
		ServiceURL: "http://10.0.0.1:8080",
	}})
	newRoutes := revisionScopedRoutes(newGroup, []RouteInput{{
		Name:       "api",
		Host:       "app.example.test",
		PathPrefix: "/",
		ServiceURL: "http://10.0.0.2:8080",
	}})
	active := append(append([]RouteInput(nil), oldRoutes...), newRoutes...)
	apiServer := httptest.NewServer(activeTraefikRouteSetHandler(t, active))
	defer apiServer.Close()
	probes := 0
	probeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes++
		// This could be a successful response from the stale backend if the
		// verifier allowed the ambiguous same-rule overlap to reach probing.
		w.WriteHeader(http.StatusNoContent)
	}))
	defer probeServer.Close()

	verifier := HTTPTraefikPublicationVerifier{
		AcknowledgementTimeout:  50 * time.Millisecond,
		AcknowledgementInterval: 5 * time.Millisecond,
		TraefikAPIBaseURL:       apiServer.URL,
		IngressBaseURL:          probeServer.URL,
	}
	result, err := verifier.VerifyRoutePublication(
		context.Background(),
		newGroup,
		newRoutes,
		renderedRouteSetHash(t, newRoutes),
	)
	if err == nil ||
		result.TraefikAcknowledged ||
		result.IngressProbeSucceeded ||
		probes != 0 ||
		!strings.Contains(result.ErrorDetail, "conflicting stale route revision") {
		t.Fatalf("verification=%#v err=%v probes=%d", result, err, probes)
	}
}

func TestHTTPTraefikPublicationVerifierRejectsWrongCandidateBackendSet(t *testing.T) {
	group := DurableRouteGroup{
		ProjectID:       "prj_one",
		EnvironmentID:   "env_prod",
		DesiredRevision: 12,
	}
	activeRoutes := revisionScopedRoutes(group, []RouteInput{{
		Name:       "api",
		Host:       "app.example.test",
		PathPrefix: "/",
		ServiceURLs: []string{
			"http://10.0.0.1:8080",
			"http://10.0.0.2:8080",
		},
	}})
	desiredRoutes := revisionScopedRoutes(group, []RouteInput{{
		Name:       "api",
		Host:       "app.example.test",
		PathPrefix: "/",
		ServiceURLs: []string{
			"http://10.0.0.2:8080",
			"http://10.0.0.3:8080",
		},
	}})
	apiServer := httptest.NewServer(activeTraefikRouteSetHandler(t, activeRoutes))
	defer apiServer.Close()
	probes := 0
	probeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer probeServer.Close()

	verifier := HTTPTraefikPublicationVerifier{
		AcknowledgementTimeout:  50 * time.Millisecond,
		AcknowledgementInterval: 5 * time.Millisecond,
		TraefikAPIBaseURL:       apiServer.URL,
		IngressBaseURL:          probeServer.URL,
	}
	result, err := verifier.VerifyRoutePublication(
		context.Background(),
		group,
		desiredRoutes,
		renderedRouteSetHash(t, desiredRoutes),
	)
	if err == nil ||
		result.TraefikAcknowledged ||
		probes != 0 ||
		!strings.Contains(result.ErrorDetail, "backend state") {
		t.Fatalf("verification=%#v err=%v probes=%d", result, err, probes)
	}
}

func TestHTTPTraefikPublicationVerifierRejectsArtifactHashMismatch(t *testing.T) {
	group := DurableRouteGroup{
		ProjectID:       "prj_one",
		EnvironmentID:   "env_prod",
		DesiredRevision: 13,
	}
	routes := revisionScopedRoutes(group, []RouteInput{{
		Name:       "api",
		Host:       "app.example.test",
		PathPrefix: "/",
		ServiceURL: "http://10.0.0.2:8080",
	}})
	apiServer := httptest.NewServer(activeTraefikRouteSetHandler(t, routes))
	defer apiServer.Close()
	probes := 0
	probeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer probeServer.Close()

	verifier := HTTPTraefikPublicationVerifier{
		AcknowledgementTimeout:  50 * time.Millisecond,
		AcknowledgementInterval: 5 * time.Millisecond,
		TraefikAPIBaseURL:       apiServer.URL,
		IngressBaseURL:          probeServer.URL,
	}
	result, err := verifier.VerifyRoutePublication(
		context.Background(),
		group,
		routes,
		strings.Repeat("a", 64),
	)
	if err == nil ||
		result.TraefikAcknowledged ||
		probes != 0 ||
		!strings.Contains(result.ErrorDetail, "artifact hash") {
		t.Fatalf("verification=%#v err=%v probes=%d", result, err, probes)
	}
}

func revisionScopedRoutes(
	group DurableRouteGroup,
	routes []RouteInput,
) []RouteInput {
	out := append([]RouteInput(nil), routes...)
	token := durableRoutePublicationToken(group)
	for index := range out {
		out[index].PublicationToken = token
	}
	return out
}

func renderedRouteSetHash(t *testing.T, routes []RouteInput) string {
	t.Helper()
	rendered, err := RenderTraefikDynamicYAML(routes)
	if err != nil {
		t.Fatal(err)
	}
	return RenderedConfigHash(rendered)
}

func activeTraefikRouteSetHandler(
	t *testing.T,
	routes []RouteInput,
) http.Handler {
	t.Helper()
	file, err := RenderTraefikFile(routes)
	if err != nil {
		t.Fatal(err)
	}
	routerRows := make([]map[string]any, 0, len(routes))
	serviceRows := make([]map[string]any, 0, len(routes))
	for _, route := range routes {
		routerName, serviceName := routePublicationResourceNames(route)
		routerValue := file.HTTP.Routers[routerName]
		routerRow := map[string]any{
			"name":        routerName + "@file",
			"provider":    "file",
			"status":      "enabled",
			"rule":        routerValue.Rule,
			"service":     routerValue.Service + "@file",
			"entryPoints": routerValue.EntryPoints,
			"middlewares": routerValue.Middlewares,
		}
		if routerValue.TLS != nil {
			routerRow["tls"] = map[string]any{
				"certResolver": routerValue.TLS.CertResolver,
			}
		}
		routerRows = append(routerRows, routerRow)

		serviceValue := file.HTTP.Services[serviceName]
		servers := make([]map[string]string, 0, len(serviceValue.LoadBalancer.Servers))
		serverStatus := make(map[string]string, len(serviceValue.LoadBalancer.Servers))
		for _, server := range serviceValue.LoadBalancer.Servers {
			servers = append(servers, map[string]string{"url": server.URL})
			serverStatus[server.URL] = "UP"
		}
		serviceRows = append(serviceRows, map[string]any{
			"name":     serviceName + "@file",
			"provider": "file",
			"status":   "enabled",
			"loadBalancer": map[string]any{
				"servers": servers,
			},
			"serverStatus": serverStatus,
		})
	}
	routersJSON, err := json.Marshal(routerRows)
	if err != nil {
		t.Fatal(err)
	}
	servicesJSON, err := json.Marshal(serviceRows)
	if err != nil {
		t.Fatal(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/http/routers":
			_, _ = w.Write(routersJSON)
		case "/api/http/services":
			_, _ = w.Write(servicesJSON)
		default:
			http.NotFound(w, r)
		}
	})
}

type fakeIngressDocker struct {
	network    dockernetwork.Inspect
	containers map[string]container.InspectResponse
	connects   int
}

func (f *fakeIngressDocker) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	value, ok := f.containers[id]
	if !ok {
		return container.InspectResponse{}, errors.New("not found")
	}
	return value, nil
}

func (f *fakeIngressDocker) NetworkInspect(context.Context, string) (dockernetwork.Inspect, error) {
	return f.network, nil
}

func (f *fakeIngressDocker) NetworkConnect(_ context.Context, _, containerID string) error {
	f.connects++
	f.network.Containers[containerID] = dockernetwork.EndpointResource{Name: "traefik"}
	value := f.containers[containerID]
	value.NetworkSettings.Networks[f.network.Name] = &dockernetwork.EndpointSettings{}
	f.containers[containerID] = value
	return nil
}

func TestDockerTrustedIngressEnrollmentRejectsCrossEnvironmentMember(t *testing.T) {
	group := DurableRouteGroup{ProjectID: "prj_one", EnvironmentID: "env_one"}
	networkName := (midnightnetwork.ProjectNetwork{ProjectID: group.ProjectID, EnvironmentID: group.EnvironmentID}).Name()
	fake := &fakeIngressDocker{
		network: dockernetwork.Inspect{
			Network: dockernetwork.Network{
				ID: "net_one", Name: networkName,
				Labels: map[string]string{
					midnightnetwork.LabelManagedBy: midnightnetwork.LabelManagedByV,
					midnightnetwork.LabelProjectID: "prj_one", midnightnetwork.LabelEnvironmentID: "env_one",
				},
			},
			Containers: map[string]dockernetwork.EndpointResource{"workload": {Name: "wrong"}},
		},
		containers: map[string]container.InspectResponse{
			"ingress": {
				ID:              "ingress",
				Config:          &container.Config{},
				NetworkSettings: &container.NetworkSettings{Networks: map[string]*dockernetwork.EndpointSettings{}},
			},
			"workload": {
				ID: "workload",
				Config: &container.Config{Labels: map[string]string{
					midnightnetwork.LabelProjectID: "prj_one", midnightnetwork.LabelEnvironmentID: "env_other",
				}},
				NetworkSettings: &container.NetworkSettings{Networks: map[string]*dockernetwork.EndpointSettings{}},
			},
		},
	}
	enroller := DockerTrustedIngressEnroller{Client: fake, IngressContainer: "ingress"}
	if err := enroller.EnsureIngressEnrollment(context.Background(), group); err == nil || !strings.Contains(err.Error(), "cross-scope") {
		t.Fatalf("error = %v, want cross-scope rejection", err)
	}
	if fake.connects != 1 {
		t.Fatalf("connects = %d, want explicit ingress attach", fake.connects)
	}
}

type fakeLifecycleStore struct {
	values    []DomainLifecycleRecord
	challenge DomainDNSChallenge
	order     DomainCertificateOrder
	ready     bool
}

func (s *fakeLifecycleStore) ListPendingDomainLifecycles(context.Context) ([]DomainLifecycleRecord, error) {
	return s.values, nil
}
func (s *fakeLifecycleStore) RecordDNSChallengeObservation(_ context.Context, value DomainDNSChallenge) error {
	s.challenge = value
	return nil
}
func (s *fakeLifecycleStore) RecordCertificateOrder(_ context.Context, value DomainCertificateOrder) error {
	s.order = value
	return nil
}
func (s *fakeLifecycleStore) MarkDomainTLSReady(context.Context, string, string, time.Time) error {
	s.ready = true
	return nil
}

type staticTXT []string

func (s staticTXT) LookupTXT(context.Context, string) ([]string, error) { return s, nil }

type hermeticIssuer struct{ expires time.Time }

func (i hermeticIssuer) Name() string { return "hermetic-test" }
func (i hermeticIssuer) Issue(context.Context, DurableDomain) (CertificateIssueResult, error) {
	return CertificateIssueResult{OrderRef: "order-ref", CertificateRef: "secret-ref", ExpiresAt: i.expires}, nil
}

func TestDomainLifecycleNeedsExactTXTThenRecordsIssuerReferences(t *testing.T) {
	challenge := DomainDNSChallenge{
		DomainID: "dom_one", RouteScope: RouteScope{ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc"},
		RecordName: "_midnight-challenge.app.example.test", RecordValue: "expected", State: DNSChallengePending,
	}
	store := &fakeLifecycleStore{values: []DomainLifecycleRecord{{
		Domain: DurableDomain{
			ID: "dom_one", RouteScope: challenge.RouteScope, Kind: DomainKindCustom,
			Hostname: "app.example.test", NormalizedHostname: "app.example.test",
			TLSDesired: true, TLSState: DomainTLSUnavailable,
		},
		Challenge: &challenge,
	}}}
	expiry := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	controller := DomainLifecycleController{
		Store: store, Resolver: staticTXT{"expected"}, Issuer: hermeticIssuer{expires: expiry},
	}
	if err := controller.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.challenge.State != DNSChallengeVerified || store.order.State != CertificateOrderReady || !store.ready {
		t.Fatalf("lifecycle challenge=%#v order=%#v ready=%v", store.challenge, store.order, store.ready)
	}
}

func TestDomainLifecycleRenewsReadyCertificateInsideWindow(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	expires := now.Add(24 * time.Hour)
	store := &fakeLifecycleStore{values: []DomainLifecycleRecord{{
		Domain: DurableDomain{
			ID: "dom_renew", RouteScope: RouteScope{ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc"},
			Kind: DomainKindGenerated, Hostname: "app.example.test", NormalizedHostname: "app.example.test",
			TLSDesired: true, TLSState: DomainTLSReady,
		},
		Certificate: &DomainCertificateOrder{
			DomainID: "dom_renew", State: CertificateOrderReady, Attempts: 1, ExpiresAt: &expires,
		},
	}}}
	controller := DomainLifecycleController{
		Store:  store,
		Issuer: hermeticIssuer{expires: now.Add(90 * 24 * time.Hour)},
		Now:    func() time.Time { return now },
	}
	if err := controller.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.order.State != CertificateOrderReady || store.order.Attempts != 2 || !store.ready {
		t.Fatalf("renewal order=%#v ready=%v", store.order, store.ready)
	}
}

func TestRunDomainLifecycleControllerReconcilesImmediatelyAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &fakeLifecycleStore{}
	controller := &DomainLifecycleController{Store: store}
	done := make(chan struct{})
	go func() {
		RunDomainLifecycleController(ctx, controller, time.Hour, func(error) { t.Errorf("unexpected lifecycle error") })
		close(done)
	}()
	// The immediate pass has no pending values. Cancellation must stop the
	// worker without waiting for the hourly ticker.
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("domain lifecycle controller did not stop after cancellation")
	}
}

func TestCertificateRetryBackoffIsBoundedAndDurable(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	order := DomainCertificateOrder{
		State: CertificateOrderFailed, Attempts: 1, UpdatedAt: now,
	}
	if certificateRetryDue(order, now.Add(domainLifecycleRetryBackoff-time.Second)) {
		t.Fatal("failed order became retryable before the first backoff")
	}
	if !certificateRetryDue(order, now.Add(domainLifecycleRetryBackoff)) {
		t.Fatal("failed order did not become retryable at the first backoff")
	}
	order.Attempts = 99
	if certificateRetryDue(order, now.Add(domainLifecycleMaxRetryBackoff-time.Second)) {
		t.Fatal("retry backoff exceeded its bounded maximum")
	}
}

// Traefik's file provider reloads asynchronously, so the router is not visible
// the instant the artifact lands. Verifying once made publication fail purely
// on timing, and the promoter then rolled the whole route group back. The
// verifier waits for Traefik to catch up.
func TestHTTPTraefikPublicationVerifierWaitsForTraefikReload(t *testing.T) {
	group := DurableRouteGroup{ProjectID: "prj_one", EnvironmentID: "env_prod", DesiredRevision: 7}
	routes := revisionScopedRoutes(group, []RouteInput{{
		Name: "api", Host: "app.example.test", PathPrefix: "/",
		ServiceURL: "http://10.0.0.2:8080",
	}})

	var calls int32
	ready := activeTraefikRouteSetHandler(t, routes)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The first two polls model Traefik not having reloaded yet.
		if atomic.AddInt32(&calls, 1) <= 4 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		ready.ServeHTTP(w, r)
	}))
	defer apiServer.Close()
	probeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "app.example.test" {
			http.Error(w, "wrong host", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer probeServer.Close()

	verifier := HTTPTraefikPublicationVerifier{
		AcknowledgementTimeout:  5 * time.Second,
		AcknowledgementInterval: 5 * time.Millisecond,
		TraefikAPIBaseURL:       apiServer.URL, IngressBaseURL: probeServer.URL,
	}
	result, err := verifier.VerifyRoutePublication(
		context.Background(), group, routes, renderedRouteSetHash(t, routes),
	)
	if err != nil {
		t.Fatalf("verifier must wait for Traefik to reload: %v", err)
	}
	if !result.Ready() {
		t.Fatalf("verification not ready: %#v", result)
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Fatalf("expected the verifier to poll more than once, got %d", calls)
	}
}

// A genuine disagreement with the desired artifact is not a reload race and
// must fail immediately rather than burn the whole retry budget.
func TestHTTPTraefikPublicationVerifierDoesNotRetryStateMismatch(t *testing.T) {
	group := DurableRouteGroup{ProjectID: "prj_one", EnvironmentID: "env_prod", DesiredRevision: 7}
	routes := revisionScopedRoutes(group, []RouteInput{{
		Name: "api", Host: "app.example.test", PathPrefix: "/",
		ServiceURL: "http://10.0.0.2:8080",
	}})
	mismatched := revisionScopedRoutes(group, []RouteInput{{
		Name: "api", Host: "app.example.test", PathPrefix: "/",
		ServiceURL: "http://10.0.0.9:8080",
	}})
	apiServer := httptest.NewServer(activeTraefikRouteSetHandler(t, mismatched))
	defer apiServer.Close()
	probeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer probeServer.Close()

	verifier := HTTPTraefikPublicationVerifier{
		AcknowledgementTimeout:  10 * time.Second,
		AcknowledgementInterval: time.Second,
		TraefikAPIBaseURL:       apiServer.URL, IngressBaseURL: probeServer.URL,
	}
	started := time.Now()
	if _, err := verifier.VerifyRoutePublication(
		context.Background(), group, routes, renderedRouteSetHash(t, routes),
	); err == nil {
		t.Fatal("a backend mismatch must fail verification")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("a state mismatch must not be retried, took %s", elapsed)
	}
}
