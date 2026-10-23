package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/runtime"
)

type generatedRouteIntentStoreFake struct {
	domains  []router.DurableDomain
	routes   []router.DurableHTTPRoute
	binds    []router.ManagedRouteSelector
	selected router.DurableDesiredRoute
	bindErr  error
}

func (s *generatedRouteIntentStoreFake) PutDomain(_ context.Context, value router.DurableDomain) (router.DurableDomain, error) {
	s.domains = append(s.domains, value)
	return value, nil
}
func (s *generatedRouteIntentStoreFake) PutHTTPRoute(_ context.Context, value router.DurableHTTPRoute) (router.DurableHTTPRoute, error) {
	s.routes = append(s.routes, value)
	return value, nil
}
func (s *generatedRouteIntentStoreFake) GetDomain(context.Context, string) (router.DurableDomain, error) {
	return router.DurableDomain{}, errors.New("unused")
}
func (s *generatedRouteIntentStoreFake) DeleteDomain(context.Context, string) error {
	return errors.New("unused")
}
func (s *generatedRouteIntentStoreFake) ListDomainRouteStates(context.Context, router.RouteScope) ([]router.DomainRouteState, error) {
	return nil, errors.New("unused")
}
func (s *generatedRouteIntentStoreFake) EnsureDomainLifecycle(context.Context, router.DurableDomain) error {
	return nil
}

func (s *generatedRouteIntentStoreFake) SelectManagedServiceRoute(context.Context, router.RouteScope, router.ManagedRouteSelector) (router.DurableDesiredRoute, error) {
	return s.selected, s.bindErr
}

func (s *generatedRouteIntentStoreFake) BindManagedServiceRouteToDeployment(_ context.Context, _ string, _ router.RouteScope, selector router.ManagedRouteSelector, _ time.Time) (router.DurableDesiredRoute, error) {
	s.binds = append(s.binds, selector)
	return s.selected, s.bindErr
}

func TestDurableRouteIntentCoordinatorBindsExistingGeneratedRouteFromSelector(t *testing.T) {
	store := &generatedRouteIntentStoreFake{}
	coordinator := DurableRouteIntentCoordinator{
		Store: store,
	}
	payload := DeployJobPayload{
		Route: &DeployJobRoute{
			Name:   "route-generated",
			Path:   "/",
			Public: true,
		},
	}
	candidate := runtime.ScheduleRequest{
		DeploymentID: "dep_generated",
		ProjectID:    "prj_generated", EnvironmentID: "env_prod", ServiceID: "svc_web",
		TargetPort: 8080,
	}
	if err := coordinator.EnsureDeployRoute(context.Background(), payload, candidate); err != nil {
		t.Fatalf("EnsureDeployRoute() error = %v", err)
	}
	if len(store.domains) != 0 || len(store.routes) != 0 {
		t.Fatalf("generated payload recreated domains=%#v routes=%#v", store.domains, store.routes)
	}
	if len(store.binds) != 1 || store.binds[0] != (router.ManagedRouteSelector{Name: "route-generated", PathPrefix: "/"}) {
		t.Fatalf("generated selector binds=%#v", store.binds)
	}
}

func TestDurableRouteIntentCoordinatorBindsExistingCustomRouteWithoutCreatingAuthority(t *testing.T) {
	store := &generatedRouteIntentStoreFake{}
	coordinator := DurableRouteIntentCoordinator{Store: store}
	payload := DeployJobPayload{
		Route: &DeployJobRoute{
			Name: "route-custom", Host: "app.example.test", Path: "/", Public: true, TLS: true,
		},
	}
	candidate := runtime.ScheduleRequest{
		DeploymentID: "dep_custom",
		ProjectID:    "prj_custom", EnvironmentID: "env_prod", ServiceID: "svc_web",
		TargetPort: 8080,
	}
	for range 2 {
		if err := coordinator.EnsureDeployRoute(context.Background(), payload, candidate); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.domains) != 0 || len(store.routes) != 0 || len(store.binds) != 2 {
		t.Fatalf("custom selector created authority domains=%#v routes=%#v binds=%#v", store.domains, store.routes, store.binds)
	}
	want := router.ManagedRouteSelector{Host: "app.example.test", Name: "route-custom", PathPrefix: "/", TLSDesired: true}
	if store.binds[0] != want || store.binds[1] != want {
		t.Fatalf("custom selectors=%#v want %#v", store.binds, want)
	}
}

func TestDurableRoutePromoterFinalReplaceFailureDoesNotCommitCanonical(t *testing.T) {
	previous := router.DurableRouteBackend{ID: "old", RouteID: "route-1", RouteScope: router.RouteScope{ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc"}, DeploymentID: "dep-old", InstanceID: "dep-old-r0", ServiceURL: "http://10.0.0.1:8080", Ready: true}
	store := &promotionStoreFake{group: promotionGroup(previous), failReplace: 2}
	promoter := DurableRoutePromoter{
		Store: store,
		Reconciler: promotionReadyReconciler(
			store,
			&promotionPublisherFake{},
		),
	}
	committed := false
	err := promoter.PromoteWithCommit(context.Background(), runtime.RoutingTarget{DeploymentID: "dep-new", InstanceID: "dep-new-r0", ProjectID: "prj", EnvironmentID: "env", ContainerIP: "10.0.0.2", InternalPort: 8080, Ready: true}, RoutePromotionMetadata{Public: true, ServiceID: "svc", Host: "app.example.test", InternalPort: 8080}, func(context.Context) error {
		committed = true
		return nil
	})
	if err == nil {
		t.Fatal("PromoteWithCommit() error = nil, want final replace failure")
	}
	if committed {
		t.Fatal("canonical commit ran after final route replacement failure")
	}
	if got := store.group.Routes[0].Backends; len(got) != 1 ||
		got[0].ID != previous.ID ||
		got[0].DeploymentID != previous.DeploymentID ||
		got[0].ServiceURL != previous.ServiceURL {
		t.Fatalf("backends after rollback = %#v, want previous %#v", got, previous)
	}
}

func TestDurableRoutePromoterRollbackDoesNotOverwriteConcurrentWinner(t *testing.T) {
	previous := router.DurableRouteBackend{ID: "old", RouteID: "route-1", RouteScope: router.RouteScope{ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc"}, DeploymentID: "dep-old", InstanceID: "dep-old-r0", ServiceURL: "http://10.0.0.1:8080", Ready: true}
	winner := router.DurableRouteBackend{ID: "winner", RouteID: "route-1", RouteScope: previous.RouteScope, DeploymentID: "dep-winner", InstanceID: "dep-winner-r0", ServiceURL: "http://10.0.0.9:8080", Ready: true}
	store := &promotionStoreFake{group: promotionGroup(previous), failReplace: 2, winnerOnFail: []router.DurableRouteBackend{winner}}
	promoter := DurableRoutePromoter{
		Store: store,
		Reconciler: promotionReadyReconciler(
			store,
			&promotionPublisherFake{},
		),
	}
	err := promoter.PromoteWithCommit(context.Background(), runtime.RoutingTarget{DeploymentID: "dep-new", InstanceID: "dep-new-r0", ProjectID: "prj", EnvironmentID: "env", ContainerIP: "10.0.0.2", InternalPort: 8080, Ready: true}, RoutePromotionMetadata{Public: true, ServiceID: "svc", Host: "app.example.test", InternalPort: 8080}, func(context.Context) error { return nil })
	if err == nil {
		t.Fatal("PromoteWithCommit() error = nil, want concurrent replacement failure")
	}
	if !errors.Is(err, router.ErrDurableRouteRevisionMoved) {
		t.Fatalf("PromoteWithCommit() error = %v, want joined restore revision error", err)
	}
	if got := store.group.Routes[0].Backends; len(got) != 1 ||
		got[0].ID != winner.ID ||
		got[0].DeploymentID != winner.DeploymentID ||
		got[0].ServiceURL != winner.ServiceURL {
		t.Fatalf("rollback overwrote concurrent winner: got %#v, want %#v", got, winner)
	}
}

func TestDurableRoutePromoterStagesAndCommitsEveryRouteAndReplica(t *testing.T) {
	scope := router.RouteScope{ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc"}
	oldOne := router.DurableRouteBackend{
		ID: "old-one", RouteID: "route-1", RouteScope: scope,
		DeploymentID: "dep-old", InstanceID: "dep-old-r0",
		ServiceURL: "http://10.0.0.1:8080", Ready: true,
	}
	oldTwo := router.DurableRouteBackend{
		ID: "old-two", RouteID: "route-2", RouteScope: scope,
		DeploymentID: "dep-old", InstanceID: "dep-old-r0",
		ServiceURL: "http://10.0.0.1:8080", Ready: true,
	}
	group := promotionGroup(oldOne)
	group.Routes = append(group.Routes, router.DurableDesiredRoute{
		Domain: router.DurableDomain{
			ID: "domain-2", RouteScope: scope, Kind: router.DomainKindCustom,
			NormalizedHostname: "app-two.example.test",
			TLSDesired:         true, TLSState: router.DomainTLSReady,
		},
		Route: router.DurableHTTPRoute{
			ID: "route-2", DomainID: "domain-2", RouteScope: scope,
			Name: "app-two", PathPrefix: "/", TargetPort: 8080, Public: true,
		},
		Backends: []router.DurableRouteBackend{oldTwo},
	})
	store := &promotionStoreFake{group: group}
	publisher := &promotionPublisherFake{}
	promoter := DurableRoutePromoter{
		Store:      store,
		Reconciler: promotionReadyReconciler(store, publisher),
	}
	committed := false
	err := promoter.PromoteReplicasWithCommit(
		context.Background(),
		[]runtime.RoutingTarget{
			{
				DeploymentID: "dep-new", InstanceID: "dep-new-r0",
				ProjectID: "prj", EnvironmentID: "env",
				ContainerIP: "10.0.0.2", InternalPort: 8080, Ready: true,
			},
			{
				DeploymentID: "dep-new", InstanceID: "dep-new-r1",
				ProjectID: "prj", EnvironmentID: "env",
				ContainerIP: "10.0.0.3", InternalPort: 8080, Ready: true,
			},
		},
		RoutePromotionMetadata{Public: true, ServiceID: "svc"},
		func(context.Context) error {
			if publisher.calls != 2 {
				t.Fatalf("canonical callback after %d reconciles, want 2", publisher.calls)
			}
			committed = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("PromoteReplicasWithCommit(): %v", err)
	}
	if !committed {
		t.Fatal("canonical callback did not run")
	}
	if len(store.history) != 2 {
		t.Fatalf("batch replacements = %d, want stage and commit", len(store.history))
	}
	for routeIndex := range 2 {
		if got := len(store.history[0][routeIndex].Backends); got != 3 {
			t.Fatalf("staged route %d backends = %d, want old + two replicas", routeIndex, got)
		}
		if got := len(store.history[1][routeIndex].Backends); got != 2 {
			t.Fatalf("committed route %d backends = %d, want two replicas", routeIndex, got)
		}
		for _, backend := range store.history[1][routeIndex].Backends {
			if backend.DeploymentID != "dep-new" {
				t.Fatalf("committed backend = %#v, want candidate only", backend)
			}
		}
	}
}

func TestDurableRoutePromoterPublicDeploymentWithNoBindingFailsClosed(t *testing.T) {
	store := &promotionStoreFake{
		group:      promotionGroup(router.DurableRouteBackend{}),
		noBindings: true,
	}
	called := false
	promoter := DurableRoutePromoter{
		Store: store,
		Reconciler: promotionReadyReconciler(
			store,
			&promotionPublisherFake{},
		),
	}
	err := promoter.PromoteWithCommit(
		context.Background(),
		runtime.RoutingTarget{
			DeploymentID: "dep-new", InstanceID: "dep-new-r0",
			ProjectID: "prj", EnvironmentID: "env",
			ContainerIP: "10.0.0.2", InternalPort: 8080, Ready: true,
		},
		RoutePromotionMetadata{Public: true, ServiceID: "svc"},
		func(context.Context) error {
			called = true
			return nil
		},
	)
	if !errors.Is(err, ErrRoutePromotionPublishFailed) ||
		!strings.Contains(err.Error(), "no exact public route binding") {
		t.Fatalf("zero-binding promotion error = %v", err)
	}
	if called || store.replaces != 0 {
		t.Fatalf("zero binding mutated routes or canonical state: called=%t replaces=%d", called, store.replaces)
	}
}

func TestDurableRoutePromoterRejectsRouteIntentFlipBeforeStage(t *testing.T) {
	tests := []struct {
		name string
		flip func(*promotionStoreFake)
	}{
		{
			name: "public disabled",
			flip: func(store *promotionStoreFake) {
				store.flipPublic = true
			},
		},
		{
			name: "TLS loses ready evidence",
			flip: func(store *promotionStoreFake) {
				store.flipTLS = true
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := router.DurableRouteBackend{
				ID: "old", RouteID: "route-1",
				RouteScope: router.RouteScope{
					ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
				},
				DeploymentID: "dep-old", InstanceID: "dep-old-r0",
				ServiceURL: "http://10.0.0.1:8080", Ready: true,
			}
			store := &promotionStoreFake{group: promotionGroup(previous)}
			test.flip(store)
			committed := false
			promoter := DurableRoutePromoter{
				Store: store,
				Reconciler: promotionReadyReconciler(
					store,
					&promotionPublisherFake{},
				),
			}
			err := promoter.PromoteWithCommit(
				context.Background(),
				runtime.RoutingTarget{
					DeploymentID: "dep-new", InstanceID: "dep-new-r0",
					ProjectID: "prj", EnvironmentID: "env",
					ContainerIP: "10.0.0.2", InternalPort: 8080, Ready: true,
				},
				RoutePromotionMetadata{Public: true, ServiceID: "svc"},
				func(context.Context) error {
					committed = true
					return nil
				},
			)
			if !errors.Is(err, ErrRoutePromotionPublishFailed) ||
				!strings.Contains(err.Error(), router.ErrDurableRouteRevisionMoved.Error()) {
				t.Fatalf("intent flip error = %v, want strict revision failure", err)
			}
			if committed || store.replaces != 0 {
				t.Fatalf(
					"intent flip advanced activation: committed=%t replacements=%d",
					committed,
					store.replaces,
				)
			}
		})
	}
}

func TestDurableRoutePromoterBlockedSiblingDoesNotBlockThisDeployment(t *testing.T) {
	scope := router.RouteScope{
		ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
	}
	previous := router.DurableRouteBackend{
		ID: "old", RouteID: "route-1", RouteScope: scope,
		DeploymentID: "dep-old", InstanceID: "dep-old-r0",
		ServiceURL: "http://10.0.0.1:8080", Ready: true,
	}
	group := promotionGroup(previous)
	siblingScope := router.RouteScope{
		ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc-idle",
	}
	group.Routes = append(group.Routes, router.DurableDesiredRoute{
		Domain: router.DurableDomain{
			ID: "domain-idle", RouteScope: siblingScope,
			Kind:               router.DomainKindGenerated,
			NormalizedHostname: "idle.example.test",
			TLSState:           router.DomainTLSDisabled,
		},
		Route: router.DurableHTTPRoute{
			ID: "route-idle", DomainID: "domain-idle", RouteScope: siblingScope,
			Name: "idle", PathPrefix: "/", TargetPort: 8080, Public: true,
		},
	})
	store := &promotionStoreFake{group: group}
	committed := false
	promoter := DurableRoutePromoter{
		Store: store,
		Reconciler: promotionReadyReconciler(
			store,
			&promotionPublisherFake{},
		),
	}
	err := promoter.PromoteWithCommit(
		context.Background(),
		runtime.RoutingTarget{
			DeploymentID: "dep-new", InstanceID: "dep-new-r0",
			ProjectID: "prj", EnvironmentID: "env",
			ContainerIP: "10.0.0.2", InternalPort: 8080, Ready: true,
		},
		RoutePromotionMetadata{Public: true, ServiceID: "svc"},
		func(context.Context) error {
			committed = true
			return nil
		},
	)
	// A sibling service whose public route has no ready backend must not stop
	// this deployment from promoting. Route groups are per environment, so the
	// previous group-wide rule meant one service with a domain it never
	// deployed to permanently blocked every other service's deploys in that
	// environment — observed on a real host, where an idle service's route
	// failed every subsequent deploy. The promoting deployment's own route is
	// still required to publish with a ready backend, which is what the
	// promoter now verifies directly.
	if err != nil {
		t.Fatalf("a blocked sibling must not fail this promotion: %v", err)
	}
	if !committed {
		t.Fatal("canonical callback must run when this deployment's own route published")
	}
	// The promotion succeeded, so the route now carries the new deployment's
	// backend rather than being rolled back to the previous one.
	if got := store.group.Routes[0].Backends; len(got) != 1 ||
		got[0].DeploymentID != "dep-new" {
		t.Fatalf("target route did not take the promoted backend: %#v", got)
	}
}

func TestDurableRoutePromoterEligibilityRefreshIncludesLegacyTLSRoute(t *testing.T) {
	scope := router.RouteScope{
		ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
	}
	group := promotionGroup(router.DurableRouteBackend{})
	group.Routes[0].Domain.Kind = router.DomainKindCustom
	group.Routes[0].Domain.TLSDesired = true
	group.Routes[0].Domain.TLSState = router.DomainTLSReady
	store := &promotionStoreFake{group: group}
	promoter := DurableRoutePromoter{Store: store}

	eligible, err := promoter.HasEligibleDeploymentPublicRouteBindings(
		context.Background(),
		"dep-new",
		scope,
	)
	if err != nil || !eligible {
		t.Fatalf("legacy TLS eligibility = %t, %v", eligible, err)
	}

	store.group.Routes[0].Domain.TLSState = router.DomainTLSUnavailable
	eligible, err = promoter.HasEligibleDeploymentPublicRouteBindings(
		context.Background(),
		"dep-new",
		scope,
	)
	if err == nil || eligible {
		t.Fatalf("unready TLS eligibility = %t, %v, want fail closed", eligible, err)
	}
}

func TestDurableRoutePromoterJoinsCallbackAndRestoreFailures(t *testing.T) {
	previous := router.DurableRouteBackend{
		ID: "old", RouteID: "route-1",
		RouteScope: router.RouteScope{
			ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
		},
		DeploymentID: "dep-old", InstanceID: "dep-old-r0",
		ServiceURL: "http://10.0.0.1:8080", Ready: true,
	}
	store := &promotionStoreFake{
		group: promotionGroup(previous), failReplace: 3,
	}
	promoter := DurableRoutePromoter{
		Store: store,
		Reconciler: promotionReadyReconciler(
			store,
			&promotionPublisherFake{},
		),
	}
	err := promoter.PromoteWithCommit(
		context.Background(),
		runtime.RoutingTarget{
			DeploymentID: "dep-new", InstanceID: "dep-new-r0",
			ProjectID: "prj", EnvironmentID: "env",
			ContainerIP: "10.0.0.2", InternalPort: 8080, Ready: true,
		},
		RoutePromotionMetadata{Public: true, ServiceID: "svc"},
		func(context.Context) error { return errors.New("canonical CAS lost") },
	)
	if err == nil ||
		!strings.Contains(err.Error(), "canonical CAS lost") ||
		!strings.Contains(err.Error(), "restore exact durable route-group backend set") {
		t.Fatalf("joined callback/restore error = %v", err)
	}
}

func TestDurableRoutePromoterBoundsCompensationContext(t *testing.T) {
	previous := router.DurableRouteBackend{
		ID: "old", RouteID: "route-1",
		RouteScope: router.RouteScope{
			ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
		},
		DeploymentID: "dep-old", InstanceID: "dep-old-r0",
		ServiceURL: "http://10.0.0.1:8080", Ready: true,
	}
	store := &promotionStoreFake{
		group:        promotionGroup(previous),
		blockRestore: true,
	}
	promoter := DurableRoutePromoter{
		Store:               store,
		CompensationTimeout: 5 * time.Millisecond,
		Reconciler: promotionReadyReconciler(
			store,
			&promotionPublisherFake{},
		),
	}
	startedAt := time.Now()
	err := promoter.PromoteWithCommit(
		context.Background(),
		runtime.RoutingTarget{
			DeploymentID: "dep-new", InstanceID: "dep-new-r0",
			ProjectID: "prj", EnvironmentID: "env",
			ContainerIP: "10.0.0.2", InternalPort: 8080, Ready: true,
		},
		RoutePromotionMetadata{Public: true, ServiceID: "svc"},
		func(context.Context) error { return errors.New("canonical CAS lost") },
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded restore error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("bounded restore took %s", elapsed)
	}
}

type promotionStoreFake struct {
	group        router.DurableRouteGroup
	failReplace  int
	replaces     int
	winnerOnFail []router.DurableRouteBackend
	noBindings   bool
	history      [][]router.DurableRouteBackendSet
	flipPublic   bool
	flipTLS      bool
	blockRestore bool
	// suppressBackendWrites models a reconcile that reports success without
	// leaving the promoting deployment's backend ready in the group.
	suppressBackendWrites bool
}

func (s *promotionStoreFake) PutDomain(context.Context, router.DurableDomain) (router.DurableDomain, error) {
	return router.DurableDomain{}, errors.New("unused")
}
func (s *promotionStoreFake) PutHTTPRoute(context.Context, router.DurableHTTPRoute) (router.DurableHTTPRoute, error) {
	return router.DurableHTTPRoute{}, errors.New("unused")
}
func (s *promotionStoreFake) GetDomain(context.Context, string) (router.DurableDomain, error) {
	return router.DurableDomain{}, errors.New("unused")
}
func (s *promotionStoreFake) DeleteDomain(context.Context, string) error { return errors.New("unused") }
func (s *promotionStoreFake) EnsureDomainLifecycle(context.Context, router.DurableDomain) error {
	return errors.New("unused")
}
func (s *promotionStoreFake) ListDomainRouteStates(context.Context, router.RouteScope) ([]router.DomainRouteState, error) {
	route := s.group.Routes[0]
	return []router.DomainRouteState{{Domain: route.Domain, Routes: []router.DurableHTTPRoute{route.Route}}}, nil
}
func (s *promotionStoreFake) ListDeploymentPublicRouteStates(
	_ context.Context,
	_ string,
	scope router.RouteScope,
) ([]router.DurableDesiredRoute, error) {
	if s.noBindings {
		return []router.DurableDesiredRoute{}, nil
	}
	out := make([]router.DurableDesiredRoute, 0, len(s.group.Routes))
	for _, desired := range s.group.Routes {
		if desired.Route.RouteScope != scope || !desired.Route.Public {
			continue
		}
		desired.Backends = append(
			[]router.DurableRouteBackend(nil),
			desired.Backends...,
		)
		out = append(out, desired)
	}
	return out, nil
}
func (s *promotionStoreFake) ListRouteGroups(context.Context) ([]router.DurableRouteGroup, error) {
	return []router.DurableRouteGroup{s.group}, nil
}
func (s *promotionStoreFake) LoadRouteGroup(context.Context, string, string) (router.DurableRouteGroup, error) {
	return s.group, nil
}
func (s *promotionStoreFake) RecordRouteApply(context.Context, string, string, int64, router.RouteApplyResult) (bool, error) {
	return true, nil
}
func (s *promotionStoreFake) ReplaceRouteBackends(_ context.Context, routeID string, backends []router.DurableRouteBackend) ([]router.DurableRouteBackend, error) {
	out, _, err := s.ReplaceRouteBackendsCAS(context.Background(), routeID, s.group.DesiredRevision, backends)
	return out, err
}
func (s *promotionStoreFake) ReplaceRouteBackendsCAS(_ context.Context, routeID string, expectedRevision int64, backends []router.DurableRouteBackend) ([]router.DurableRouteBackend, int64, error) {
	if expectedRevision != s.group.DesiredRevision {
		return nil, s.group.DesiredRevision, router.ErrDurableRouteRevisionMoved
	}
	s.replaces++
	if s.replaces == s.failReplace {
		if len(s.winnerOnFail) > 0 {
			s.group.DesiredRevision++
			s.group.Routes[0].Backends = append([]router.DurableRouteBackend(nil), s.winnerOnFail...)
		}
		return nil, s.group.DesiredRevision, errors.New("final durable backend replacement refused")
	}
	for i := range s.group.Routes {
		if s.group.Routes[i].Route.ID == routeID {
			s.group.Routes[i].Backends = append([]router.DurableRouteBackend(nil), backends...)
			s.group.DesiredRevision++
			return s.group.Routes[i].Backends, s.group.DesiredRevision, nil
		}
	}
	return nil, s.group.DesiredRevision, errors.New("route not found")
}
func (s *promotionStoreFake) ReplaceRouteGroupBackendsCAS(
	_ context.Context,
	projectID string,
	environmentID string,
	expectedRevision int64,
	sets []router.DurableRouteBackendSet,
) ([]router.DurableRouteBackendSet, int64, error) {
	return s.replaceRouteGroupBackendsCAS(
		projectID,
		environmentID,
		expectedRevision,
		sets,
		false,
		false,
	)
}

func (s *promotionStoreFake) ReplaceEligibleRouteGroupBackendsCAS(
	_ context.Context,
	projectID string,
	environmentID string,
	expectedRevision int64,
	sets []router.DurableRouteBackendSet,
) ([]router.DurableRouteBackendSet, int64, error) {
	if s.suppressBackendWrites {
		s.group.DesiredRevision++
		return sets, s.group.DesiredRevision, nil
	}
	if s.flipPublic {
		s.flipPublic = false
		s.group.Routes[0].Route.Public = false
		s.group.DesiredRevision++
	}
	if s.flipTLS {
		s.flipTLS = false
		s.group.Routes[0].Domain.TLSDesired = true
		s.group.Routes[0].Domain.TLSState = router.DomainTLSUnavailable
		s.group.DesiredRevision++
	}
	return s.replaceRouteGroupBackendsCAS(
		projectID,
		environmentID,
		expectedRevision,
		sets,
		false,
		true,
	)
}

func (s *promotionStoreFake) RestoreEligibleRouteGroupBackendsWithOwnershipCAS(
	ctx context.Context,
	projectID string,
	environmentID string,
	expectedRevision int64,
	sets []router.DurableRouteBackendSet,
) ([]router.DurableRouteBackendSet, int64, error) {
	if s.blockRestore {
		<-ctx.Done()
		return nil, s.group.DesiredRevision, ctx.Err()
	}
	return s.replaceRouteGroupBackendsCAS(
		projectID,
		environmentID,
		expectedRevision,
		sets,
		true,
		true,
	)
}

func (s *promotionStoreFake) replaceRouteGroupBackendsCAS(
	projectID string,
	environmentID string,
	expectedRevision int64,
	sets []router.DurableRouteBackendSet,
	allowOwnedMovedRevision bool,
	requireEligible bool,
) ([]router.DurableRouteBackendSet, int64, error) {
	if projectID != s.group.ProjectID || environmentID != s.group.EnvironmentID {
		return nil, s.group.DesiredRevision, router.ErrDurableRouteScopeMismatch
	}
	if expectedRevision != s.group.DesiredRevision {
		if !allowOwnedMovedRevision ||
			!fakeBackendSetsOwnRevision(s.group, sets, expectedRevision) {
			return nil, s.group.DesiredRevision, router.ErrDurableRouteRevisionMoved
		}
	}
	if requireEligible {
		seenRoutes := make(map[string]struct{}, len(sets))
		for _, set := range sets {
			found := false
			for _, desired := range s.group.Routes {
				if desired.Route.ID != set.RouteID {
					continue
				}
				if err := validateDeploymentPublicRouteState(
					desired,
					desired.Route.RouteScope,
					seenRoutes,
				); err != nil {
					return nil, s.group.DesiredRevision, err
				}
				found = true
				break
			}
			if !found {
				return nil, s.group.DesiredRevision, errors.New("route not found")
			}
		}
	}
	s.replaces++
	if s.replaces == s.failReplace {
		if len(s.winnerOnFail) > 0 {
			s.group.DesiredRevision++
			winner := append([]router.DurableRouteBackend(nil), s.winnerOnFail...)
			for index := range winner {
				winner[index].DesiredRevision = s.group.DesiredRevision
			}
			s.group.Routes[0].Backends = winner
		}
		return nil, s.group.DesiredRevision, errors.New("durable backend-set replacement refused")
	}
	s.group.DesiredRevision++
	out := cloneBackendSets(sets)
	for setIndex := range out {
		for backendIndex := range out[setIndex].Backends {
			out[setIndex].Backends[backendIndex].DesiredRevision = s.group.DesiredRevision
		}
		found := false
		for routeIndex := range s.group.Routes {
			if s.group.Routes[routeIndex].Route.ID == out[setIndex].RouteID {
				s.group.Routes[routeIndex].Backends = append(
					[]router.DurableRouteBackend(nil),
					out[setIndex].Backends...,
				)
				found = true
				break
			}
		}
		if !found {
			return nil, s.group.DesiredRevision, errors.New("route not found")
		}
	}
	s.history = append(s.history, cloneBackendSets(out))
	return out, s.group.DesiredRevision, nil
}

func fakeBackendSetsOwnRevision(
	group router.DurableRouteGroup,
	sets []router.DurableRouteBackendSet,
	expectedRevision int64,
) bool {
	for _, set := range sets {
		found := false
		for _, desired := range group.Routes {
			if desired.Route.ID != set.RouteID {
				continue
			}
			if len(desired.Backends) == 0 {
				return false
			}
			for _, backend := range desired.Backends {
				if backend.DesiredRevision != expectedRevision {
					return false
				}
			}
			found = true
			break
		}
		if !found {
			return false
		}
	}
	return len(sets) > 0
}

func cloneBackendSets(
	values []router.DurableRouteBackendSet,
) []router.DurableRouteBackendSet {
	out := make([]router.DurableRouteBackendSet, len(values))
	for index := range values {
		out[index] = router.DurableRouteBackendSet{
			RouteID: values[index].RouteID,
			Backends: append(
				[]router.DurableRouteBackend(nil),
				values[index].Backends...,
			),
		}
	}
	return out
}

type promotionPublisherFake struct {
	calls int
}

func (p *promotionPublisherFake) PublishRouteSet(string, []router.RouteInput) (router.RouteArtifactPublicationApplyEvidence, string, error) {
	p.calls++
	return router.RouteArtifactPublicationApplyEvidence{Status: router.RouteArtifactPublicationApplyStatusReady, Writes: 1}, "hash", nil
}

func (p *promotionPublisherFake) WithRouteGroupPublicationLock(
	_ context.Context,
	_ string,
	_ string,
	fn func() error,
) error {
	return fn()
}

type promotionVerifierFake struct{}

func (promotionVerifierFake) VerifyRoutePublication(
	context.Context,
	router.DurableRouteGroup,
	[]router.RouteInput,
	string,
) (router.RoutePublicationVerification, error) {
	return router.RoutePublicationVerification{
		TraefikAcknowledged:   true,
		IngressProbeSucceeded: true,
	}, nil
}

func promotionReadyReconciler(
	store router.DurableRouteStore,
	publisher router.RouteSetPublisher,
) *router.DurableRouteReconciler {
	return &router.DurableRouteReconciler{
		Store:     store,
		Publisher: publisher,
		Verifier:  promotionVerifierFake{},
	}
}

func promotionGroup(previous router.DurableRouteBackend) router.DurableRouteGroup {
	scope := router.RouteScope{ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc"}
	return router.DurableRouteGroup{RouteGroup: "group", ProjectID: "prj", EnvironmentID: "env", DesiredRevision: 1, Routes: []router.DurableDesiredRoute{{
		Domain:   router.DurableDomain{ID: "domain-1", RouteScope: scope, Kind: router.DomainKindGenerated, NormalizedHostname: "app.example.test", TLSState: router.DomainTLSDisabled},
		Route:    router.DurableHTTPRoute{ID: "route-1", DomainID: "domain-1", RouteScope: scope, Name: "app", PathPrefix: "/", TargetPort: 8080, Public: true},
		Backends: []router.DurableRouteBackend{previous},
	}}}
}

// Relaxing the group-wide block means a successful reconcile no longer proves
// this deployment's own route was published, so the promoter checks that
// directly. A reconcile that leaves this deployment's route without a ready
// backend must not promote it.
func TestDurableRoutePromoterFailsWhenItsOwnRouteHasNoReadyBackend(t *testing.T) {
	group := promotionGroup(router.DurableRouteBackend{
		ID: "bck-old", RouteID: "route-1",
		RouteScope:   router.RouteScope{ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc"},
		DeploymentID: "dep-old", InstanceID: "dep-old-r0",
		ServiceURL: "http://10.0.0.1:8080", Ready: true,
	})
	store := &promotionStoreFake{group: group}
	// A reconciler that reports success without leaving this deployment's
	// backend ready in the group.
	store.suppressBackendWrites = true
	committed := false
	promoter := DurableRoutePromoter{
		Store:      store,
		Reconciler: promotionReadyReconciler(store, &promotionPublisherFake{}),
	}
	err := promoter.PromoteWithCommit(
		context.Background(),
		runtime.RoutingTarget{
			DeploymentID: "dep-new", InstanceID: "dep-new-r0",
			ProjectID: "prj", EnvironmentID: "env",
			ContainerIP: "10.0.0.2", InternalPort: 8080, Ready: true,
		},
		RoutePromotionMetadata{Public: true, ServiceID: "svc"},
		func(context.Context) error { committed = true; return nil },
	)
	if !errors.Is(err, router.ErrDurableRouteBlocked) {
		t.Fatalf("want a typed route block when this deployment's route is absent, got %v", err)
	}
	if committed {
		t.Fatal("canonical callback ran without this deployment's route being published")
	}
}
