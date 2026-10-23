package router

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type fakeDurableRouteStore struct {
	groups      []DurableRouteGroup
	loads       int
	recorded    []RouteApplyResult
	rejectFirst bool
}

func (s *fakeDurableRouteStore) ListRouteGroups(context.Context) ([]DurableRouteGroup, error) {
	return append([]DurableRouteGroup(nil), s.groups...), nil
}

func (s *fakeDurableRouteStore) LoadRouteGroup(_ context.Context, _, _ string) (DurableRouteGroup, error) {
	if len(s.groups) == 0 {
		return DurableRouteGroup{}, ErrDurableRouteNotFound
	}
	index := s.loads
	if index >= len(s.groups) {
		index = len(s.groups) - 1
	}
	s.loads++
	return s.groups[index], nil
}

func (s *fakeDurableRouteStore) RecordRouteApply(_ context.Context, _, _ string, _ int64, result RouteApplyResult) (bool, error) {
	s.recorded = append(s.recorded, result)
	if s.rejectFirst && len(s.recorded) == 1 {
		return false, nil
	}
	return true, nil
}

type fakeRouteSetPublisher struct {
	routes   [][]RouteInput
	err      error
	evidence *RouteArtifactPublicationApplyEvidence
	called   chan struct{}
	lock     sync.Mutex
}

type fakePublicationVerifier struct {
	result RoutePublicationVerification
	err    error
}

func (v fakePublicationVerifier) VerifyRoutePublication(context.Context, DurableRouteGroup, []RouteInput, string) (RoutePublicationVerification, error) {
	return v.result, v.err
}

type blockingPublicationVerifier struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (v *blockingPublicationVerifier) VerifyRoutePublication(
	ctx context.Context,
	_ DurableRouteGroup,
	_ []RouteInput,
	_ string,
) (RoutePublicationVerification, error) {
	v.once.Do(func() { close(v.entered) })
	select {
	case <-ctx.Done():
		return RoutePublicationVerification{}, ctx.Err()
	case <-v.release:
		return RoutePublicationVerification{
			TraefikAcknowledged:   true,
			IngressProbeSucceeded: true,
		}, nil
	}
}

type trackingPublicationDirectory struct {
	directory *TraefikDynamicPublicationDirectory
	attempted chan struct{}
	once      sync.Once
}

func (p *trackingPublicationDirectory) PublishRouteSet(
	routeGroup string,
	routes []RouteInput,
) (RouteArtifactPublicationApplyEvidence, string, error) {
	return p.directory.PublishRouteSet(routeGroup, routes)
}

func (p *trackingPublicationDirectory) WithRouteGroupPublicationLock(
	ctx context.Context,
	projectID string,
	environmentID string,
	fn func() error,
) error {
	p.once.Do(func() { close(p.attempted) })
	return p.directory.WithRouteGroupPublicationLock(
		ctx,
		projectID,
		environmentID,
		fn,
	)
}

func (p *fakeRouteSetPublisher) PublishRouteSet(_ string, routes []RouteInput) (RouteArtifactPublicationApplyEvidence, string, error) {
	copyRoutes := append([]RouteInput(nil), routes...)
	p.routes = append(p.routes, copyRoutes)
	if p.called != nil {
		select {
		case p.called <- struct{}{}:
		default:
		}
	}
	if p.err != nil {
		return RouteArtifactPublicationApplyEvidence{Status: RouteArtifactPublicationApplyStatusError, Errors: 1}, "", p.err
	}
	if p.evidence != nil {
		return *p.evidence, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
	}
	return RouteArtifactPublicationApplyEvidence{Status: RouteArtifactPublicationApplyStatusReady, Writes: 1}, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
}

func (p *fakeRouteSetPublisher) WithRouteGroupPublicationLock(
	_ context.Context,
	_ string,
	_ string,
	fn func() error,
) error {
	p.lock.Lock()
	defer p.lock.Unlock()
	return fn()
}

func TestDurableRouteReconcilerKeepsPrivateIntentOutOfArtifact(t *testing.T) {
	store := &fakeDurableRouteStore{groups: []DurableRouteGroup{durableTestGroup(false, false, DomainTLSDisabled)}}
	publisher := &fakeRouteSetPublisher{}
	reconciler := DurableRouteReconciler{
		Store: store, Publisher: publisher,
		Verifier: fakePublicationVerifier{result: RoutePublicationVerification{
			TraefikAcknowledged: true, IngressProbeSucceeded: true,
		}},
	}

	if err := reconciler.ReconcileGroup(context.Background(), "prj_one", "env_one"); err != nil {
		t.Fatalf("ReconcileGroup: %v", err)
	}
	if len(publisher.routes) != 1 || len(publisher.routes[0]) != 0 {
		t.Fatalf("published routes = %#v, want an empty private route set", publisher.routes)
	}
	if len(store.recorded) != 1 || store.recorded[0].State != RouteApplyPrivate || !store.recorded[0].FilePublished || !store.recorded[0].ArtifactChanged {
		t.Fatalf("recorded = %#v", store.recorded)
	}
}

func TestDurableRouteReconcilerDistinguishesUnchangedPrivateArtifact(t *testing.T) {
	store := &fakeDurableRouteStore{groups: []DurableRouteGroup{durableTestGroup(false, false, DomainTLSDisabled)}}
	publisher := &fakeRouteSetPublisher{evidence: &RouteArtifactPublicationApplyEvidence{
		Status: RouteArtifactPublicationApplyStatusReady,
		Skips:  1,
	}}
	reconciler := DurableRouteReconciler{Store: store, Publisher: publisher}

	if err := reconciler.ReconcileGroup(context.Background(), "prj_one", "env_one"); err != nil {
		t.Fatalf("ReconcileGroup: %v", err)
	}
	if len(store.recorded) != 1 ||
		store.recorded[0].State != RouteApplyPrivate ||
		!store.recorded[0].FilePublished ||
		store.recorded[0].ArtifactChanged {
		t.Fatalf("recorded = %#v, want published but unchanged private result", store.recorded)
	}
}

func TestDurableRouteReconcilerBlocksTLSWithoutCertificateTruth(t *testing.T) {
	store := &fakeDurableRouteStore{groups: []DurableRouteGroup{durableTestGroup(true, true, DomainTLSUnavailable)}}
	publisher := &fakeRouteSetPublisher{}
	reconciler := DurableRouteReconciler{Store: store, Publisher: publisher}

	if err := reconciler.ReconcileGroup(context.Background(), "prj_one", "env_one"); !errors.Is(err, ErrDurableRouteBlocked) {
		t.Fatalf("ReconcileGroup error = %v, want blocked publication", err)
	}
	if len(publisher.routes) != 1 || len(publisher.routes[0]) != 0 {
		t.Fatalf("blocked TLS route was not removed from published set: %#v", publisher.routes)
	}
	if len(store.recorded) != 1 ||
		store.recorded[0].State != RouteApplyBlockedTLS ||
		store.recorded[0].ErrorCode != "certificate_issuance_unavailable" ||
		!store.recorded[0].FilePublished {
		t.Fatalf("recorded = %#v", store.recorded)
	}
}

func TestDurableRouteReconcilerRendersSortedReadyBackendsAndRetriesRevisionCAS(t *testing.T) {
	first := durableTestGroup(true, true, DomainTLSReady)
	second := first
	second.DesiredRevision = 3
	second.Routes[0].Backends = append(second.Routes[0].Backends, DurableRouteBackend{
		ID:         "backend_a",
		ServiceURL: "http://10.0.0.1:8080",
		Ready:      true,
	})
	store := &fakeDurableRouteStore{
		groups:      []DurableRouteGroup{first, second},
		rejectFirst: true,
	}
	publisher := &fakeRouteSetPublisher{}
	reconciler := DurableRouteReconciler{
		Store: store, Publisher: publisher,
		Verifier: fakePublicationVerifier{result: RoutePublicationVerification{
			TraefikAcknowledged: true, IngressProbeSucceeded: true,
		}},
	}

	if err := reconciler.ReconcileGroup(context.Background(), "prj_one", "env_one"); err != nil {
		t.Fatalf("ReconcileGroup: %v", err)
	}
	if len(publisher.routes) != 2 {
		t.Fatalf("publish calls = %d, want revision retry", len(publisher.routes))
	}
	got := publisher.routes[1][0].ServiceURLs
	want := []string{"http://10.0.0.1:8080", "http://10.0.0.2:8080"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ready backends = %#v, want %#v", got, want)
	}
	if !publisher.routes[1][0].TLS {
		t.Fatal("durably ready TLS state was not rendered")
	}
}

func TestDurableRouteReconcilerCarriesOperatorCertResolver(t *testing.T) {
	store := &fakeDurableRouteStore{groups: []DurableRouteGroup{durableTestGroup(true, true, DomainTLSReady)}}
	publisher := &fakeRouteSetPublisher{}
	reconciler := DurableRouteReconciler{
		Store: store, Publisher: publisher, CertResolver: "letsencrypt-staging",
		Verifier: fakePublicationVerifier{result: RoutePublicationVerification{
			TraefikAcknowledged: true, IngressProbeSucceeded: true,
		}},
	}
	if err := reconciler.ReconcileGroup(context.Background(), "prj_one", "env_one"); err != nil {
		t.Fatalf("ReconcileGroup: %v", err)
	}
	if len(publisher.routes) != 1 || len(publisher.routes[0]) != 1 {
		t.Fatalf("published routes = %#v", publisher.routes)
	}
	if got := publisher.routes[0][0].CertResolver; got != "letsencrypt-staging" {
		t.Fatalf("cert resolver = %q, want letsencrypt-staging", got)
	}
}

func TestDurableRouteReconcilerRecordsPublicationFailureWithoutAppliedRevision(t *testing.T) {
	store := &fakeDurableRouteStore{groups: []DurableRouteGroup{durableTestGroup(true, false, DomainTLSDisabled)}}
	publisher := &fakeRouteSetPublisher{err: errors.New("write refused")}
	reconciler := DurableRouteReconciler{Store: store, Publisher: publisher}

	if err := reconciler.ReconcileGroup(context.Background(), "prj_one", "env_one"); err == nil {
		t.Fatal("publication failure returned nil")
	}
	if len(store.recorded) != 1 ||
		store.recorded[0].State != RouteApplyFailed ||
		store.recorded[0].FilePublished {
		t.Fatalf("recorded = %#v", store.recorded)
	}
}

func TestDurableRouteReconcilerRequiresVerifierForPublicRevision(t *testing.T) {
	store := &fakeDurableRouteStore{
		groups: []DurableRouteGroup{
			durableTestGroup(true, false, DomainTLSDisabled),
		},
	}
	reconciler := DurableRouteReconciler{
		Store: store, Publisher: &fakeRouteSetPublisher{},
	}
	if err := reconciler.ReconcileGroup(
		context.Background(),
		"prj_one",
		"env_one",
	); !errors.Is(err, ErrDurableRouteUnverified) {
		t.Fatalf("missing verifier error = %v, want unverified", err)
	}
	if len(store.recorded) != 1 ||
		store.recorded[0].State != RouteApplyFileApplied ||
		store.recorded[0].ErrorCode != "route_publication_unverified" {
		t.Fatalf("recorded = %#v", store.recorded)
	}
}

func TestDurableRouteReconcilerRequiresAcknowledgementAndProbeForReady(t *testing.T) {
	store := &fakeDurableRouteStore{groups: []DurableRouteGroup{durableTestGroup(true, false, DomainTLSDisabled)}}
	reconciler := DurableRouteReconciler{
		Store: store, Publisher: &fakeRouteSetPublisher{},
		Verifier: fakePublicationVerifier{result: RoutePublicationVerification{
			TraefikAcknowledged: true, IngressProbeSucceeded: false,
			ErrorDetail: "probe pending",
		}},
	}
	if err := reconciler.ReconcileGroup(context.Background(), "prj_one", "env_one"); !errors.Is(err, ErrDurableRouteUnverified) {
		t.Fatalf("unverified publication error = %v, want typed unverified", err)
	}
	if got := store.recorded[0].State; got != RouteApplyFileApplied {
		t.Fatalf("state = %s, want file_applied", got)
	}

	store.recorded = nil
	reconciler.Verifier = fakePublicationVerifier{result: RoutePublicationVerification{
		TraefikAcknowledged: true, IngressProbeSucceeded: true,
	}}
	if err := reconciler.ReconcileGroup(context.Background(), "prj_one", "env_one"); err != nil {
		t.Fatalf("verified ReconcileGroup: %v", err)
	}
	if got := store.recorded[0].State; got != RouteApplyReady {
		t.Fatalf("state = %s, want ready", got)
	}
}

func TestDurableRouteReconcilerSerializesLoadThroughReadyRecordAcrossPublishers(
	t *testing.T,
) {
	root := t.TempDir()
	firstDirectory, err := NewTraefikDynamicPublicationDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	secondDirectory, err := NewTraefikDynamicPublicationDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	firstPublisher := &trackingPublicationDirectory{
		directory: firstDirectory,
		attempted: make(chan struct{}),
	}
	secondPublisher := &trackingPublicationDirectory{
		directory: secondDirectory,
		attempted: make(chan struct{}),
	}
	store := &fakeDurableRouteStore{
		groups: []DurableRouteGroup{
			durableTestGroup(true, false, DomainTLSDisabled),
		},
	}
	verifier := &blockingPublicationVerifier{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	first := DurableRouteReconciler{
		Store: store, Publisher: firstPublisher, Verifier: verifier,
	}
	second := DurableRouteReconciler{
		Store: store, Publisher: secondPublisher,
		Verifier: fakePublicationVerifier{
			result: RoutePublicationVerification{
				TraefikAcknowledged:   true,
				IngressProbeSucceeded: true,
			},
		},
	}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.ReconcileGroup(
			context.Background(),
			"prj_one",
			"env_one",
		)
	}()
	select {
	case <-verifier.entered:
	case <-time.After(time.Second):
		t.Fatal("first reconciler did not enter its verification window")
	}

	secondCtx, cancelSecond := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- second.ReconcileGroup(
			secondCtx,
			"prj_one",
			"env_one",
		)
	}()
	select {
	case <-secondPublisher.attempted:
	case <-time.After(time.Second):
		t.Fatal("second reconciler did not attempt the shared publication lock")
	}
	cancelSecond()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("second reconciliation error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second reconciler was not cancelable while waiting for the lock")
	}
	if store.loads != 1 {
		t.Fatalf(
			"route-group loads while first verifier is active = %d, want 1",
			store.loads,
		)
	}

	close(verifier.release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first reconciliation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first reconciler did not finish after verification")
	}
	if len(store.recorded) != 1 ||
		store.recorded[0].State != RouteApplyReady {
		t.Fatalf("recorded = %#v, want one ready result", store.recorded)
	}
}

func TestRunDurableRouteControllerReconcilesImmediatelyAndStops(t *testing.T) {
	store := &fakeDurableRouteStore{groups: []DurableRouteGroup{durableTestGroup(false, false, DomainTLSDisabled)}}
	publisher := &fakeRouteSetPublisher{called: make(chan struct{}, 1)}
	reconciler := &DurableRouteReconciler{Store: store, Publisher: publisher}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunDurableRouteController(ctx, reconciler, time.Hour, nil)
	}()
	select {
	case <-publisher.called:
	case <-time.After(time.Second):
		t.Fatal("startup reconciliation did not run")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("controller did not stop after cancellation")
	}
}

func durableTestGroup(public, tlsDesired bool, tlsState DomainTLSState) DurableRouteGroup {
	return DurableRouteGroup{
		RouteGroup:      "durable-test",
		ProjectID:       "prj_one",
		EnvironmentID:   "env_one",
		DesiredRevision: 2,
		Routes: []DurableDesiredRoute{{
			Domain: DurableDomain{
				ID: "dom_one",
				RouteScope: RouteScope{
					ProjectID:     "prj_one",
					EnvironmentID: "env_one",
					ServiceID:     "svc_one",
				},
				NormalizedHostname: "app.example.test",
				TLSDesired:         tlsDesired,
				TLSState:           tlsState,
			},
			Route: DurableHTTPRoute{
				ID:       "rte_one",
				DomainID: "dom_one",
				RouteScope: RouteScope{
					ProjectID:     "prj_one",
					EnvironmentID: "env_one",
					ServiceID:     "svc_one",
				},
				Name:       "api",
				PathPrefix: "/",
				TargetPort: 8080,
				Public:     public,
			},
			Backends: []DurableRouteBackend{{
				ID:         "backend_b",
				ServiceURL: "http://10.0.0.2:8080",
				Ready:      true,
			}},
		}},
	}
}

// Route groups are scoped to an environment, so one service's public route
// with no ready backend must not fail the deploys of every other service in
// that environment. Before this, any blocked route overwrote the group state,
// skipped verification for the routes that had published perfectly well, and
// returned a group-wide error that rolled back the promoting deployment.
func TestDurableRouteReconcilerPublishesHealthyRoutesWhenOneServiceIsBlocked(t *testing.T) {
	group := durableTestGroup(true, false, DomainTLSDisabled)
	// A second service in the same environment has a public route that has
	// never had a ready backend.
	group.Routes = append(group.Routes, DurableDesiredRoute{
		Domain: DurableDomain{
			ID:                 "dom_two",
			RouteScope:         RouteScope{ProjectID: "prj_one", EnvironmentID: "env_one", ServiceID: "svc_two"},
			NormalizedHostname: "other.example.test",
			TLSState:           DomainTLSDisabled,
		},
		Route: DurableHTTPRoute{
			ID:         "rte_two",
			DomainID:   "dom_two",
			RouteScope: RouteScope{ProjectID: "prj_one", EnvironmentID: "env_one", ServiceID: "svc_two"},
			Name:       "other",
			PathPrefix: "/",
			TargetPort: 8080,
			Public:     true,
		},
	})
	store := &fakeDurableRouteStore{groups: []DurableRouteGroup{group}}
	publisher := &fakeRouteSetPublisher{evidence: &RouteArtifactPublicationApplyEvidence{
		Status: RouteArtifactPublicationApplyStatusReady, Writes: 1,
	}}
	reconciler := &DurableRouteReconciler{
		Store: store, Publisher: publisher,
		Verifier: fakePublicationVerifier{result: RoutePublicationVerification{
			TraefikAcknowledged: true, IngressProbeSucceeded: true,
		}},
	}

	if err := reconciler.ReconcileGroup(context.Background(), "prj_one", "env_one"); err != nil {
		t.Fatalf("one blocked service must not fail the group: %v", err)
	}
	if len(publisher.routes) != 1 || len(publisher.routes[0]) != 1 {
		t.Fatalf("expected exactly the healthy route to publish, got %#v", publisher.routes)
	}
	if got := publisher.routes[0][0].Host; got != "app.example.test" {
		t.Fatalf("published the wrong route: %q", got)
	}
	if len(store.recorded) != 1 {
		t.Fatalf("recorded = %#v", store.recorded)
	}
	// The healthy route is verified and applied, and the block is still
	// reported rather than silently dropped.
	if store.recorded[0].State != RouteApplyReady {
		t.Fatalf("healthy route must reach ready, got %q", store.recorded[0].State)
	}
	if store.recorded[0].ErrorCode != "ready_route_backend_unavailable" {
		t.Fatalf("the blocked route must still be reported, got %q", store.recorded[0].ErrorCode)
	}
}

// When every public route is blocked the group genuinely has nothing to serve,
// so the previous group-wide behaviour is preserved.
func TestDurableRouteReconcilerStillFailsWhenEveryPublicRouteIsBlocked(t *testing.T) {
	group := durableTestGroup(true, false, DomainTLSDisabled)
	group.Routes[0].Backends = nil
	store := &fakeDurableRouteStore{groups: []DurableRouteGroup{group}}
	publisher := &fakeRouteSetPublisher{evidence: &RouteArtifactPublicationApplyEvidence{
		Status: RouteArtifactPublicationApplyStatusReady, Writes: 1,
	}}
	reconciler := &DurableRouteReconciler{Store: store, Publisher: publisher}

	err := reconciler.ReconcileGroup(context.Background(), "prj_one", "env_one")
	var blockedErr *DurableRouteBlockedError
	if !errors.As(err, &blockedErr) {
		t.Fatalf("want DurableRouteBlockedError, got %v", err)
	}
	if blockedErr.State != RouteApplyBlockedBackend {
		t.Fatalf("blocked state = %q", blockedErr.State)
	}
	if len(store.recorded) != 1 || store.recorded[0].State != RouteApplyBlockedBackend {
		t.Fatalf("recorded = %#v", store.recorded)
	}
}
