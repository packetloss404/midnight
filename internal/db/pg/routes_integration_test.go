//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package pg_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/domain"
	"github.com/midnight/midnight/internal/router"
	migrations "github.com/midnight/midnight/migrations"
)

func TestDurableHTTPRoutingRestartScopeAndHonestTLSState(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool1 := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool1)
	resources := api.NewPostgresResourceStore(pool1)
	project1, err := resources.CreateProject(ctx, api.Project{WorkspaceID: api.DefaultWorkspaceID, Slug: "routes-one"})
	if err != nil {
		t.Fatalf("CreateProject one: %v", err)
	}
	environment1, err := resources.CreateEnvironment(ctx, api.Environment{
		ProjectID: project1.ID,
		Slug:      "production",
		Tier:      "production",
	})
	if err != nil {
		t.Fatalf("CreateEnvironment one: %v", err)
	}
	service1, err := resources.CreateService(ctx, api.Service{
		ProjectID: project1.ID,
		Slug:      "api",
	})
	if err != nil {
		t.Fatalf("CreateService one: %v", err)
	}
	if _, err := pool1.Exec(ctx, `
		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action, state
		) VALUES ('dep_routes_one', $1, $2, $3, 'deploy_image', 'queued')`,
		project1.ID, service1.ID, environment1.ID,
	); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}

	hostname, err := domain.GeneratedHostname("api--production--routes-one", "a1b2c3", "apps.example.test")
	if err != nil {
		t.Fatalf("GeneratedHostname: %v", err)
	}
	scope1 := router.RouteScope{
		ProjectID:     string(project1.ID),
		EnvironmentID: string(environment1.ID),
		ServiceID:     string(service1.ID),
	}
	store1 := router.NewPostgresDurableRouteStore(pool1)
	domain1, err := store1.PutDomain(ctx, router.DurableDomain{
		ID:         "dom_routes_one",
		RouteScope: scope1,
		Kind:       router.DomainKindGenerated,
		Hostname:   hostname,
		AppsDomain: "apps.example.test",
		TLSDesired: false,
	})
	if err != nil {
		t.Fatalf("PutDomain: %v", err)
	}
	if domain1.NormalizedHostname != hostname || domain1.TLSState != router.DomainTLSDisabled {
		t.Fatalf("domain = %#v", domain1)
	}
	route1, err := store1.PutHTTPRoute(ctx, router.DurableHTTPRoute{
		ID:         "rte_routes_one",
		DomainID:   domain1.ID,
		RouteScope: scope1,
		Name:       "api",
		PathPrefix: "/",
		TargetPort: 8080,
		// Public intentionally omitted: private-by-default.
	})
	if err != nil {
		t.Fatalf("PutHTTPRoute private: %v", err)
	}
	if route1.Public {
		t.Fatal("route became public without explicit intent")
	}
	backendIntent := []router.DurableRouteBackend{{
		ID:           "rbe_routes_one",
		RouteScope:   scope1,
		DeploymentID: "dep_routes_one",
		InstanceID:   "inst_routes_one",
		ServiceURL:   "http://10.42.0.8:8080",
		Ready:        true,
	}}
	if _, err := store1.ReplaceRouteBackends(ctx, route1.ID, backendIntent); err != nil {
		t.Fatalf("ReplaceRouteBackends: %v", err)
	}
	beforeIdempotent, err := store1.LoadRouteGroup(ctx, scope1.ProjectID, scope1.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store1.PutDomain(ctx, domain1); err != nil {
		t.Fatalf("idempotent PutDomain: %v", err)
	}
	if _, err := store1.PutHTTPRoute(ctx, route1); err != nil {
		t.Fatalf("idempotent PutHTTPRoute: %v", err)
	}
	if _, err := store1.ReplaceRouteBackends(ctx, route1.ID, backendIntent); err != nil {
		t.Fatalf("idempotent ReplaceRouteBackends: %v", err)
	}
	afterIdempotent, err := store1.LoadRouteGroup(ctx, scope1.ProjectID, scope1.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if afterIdempotent.DesiredRevision != beforeIdempotent.DesiredRevision {
		t.Fatalf("idempotent writes advanced revision %d -> %d", beforeIdempotent.DesiredRevision, afterIdempotent.DesiredRevision)
	}
	if _, _, err := store1.ReplaceRouteBackendsCAS(
		ctx, route1.ID, afterIdempotent.DesiredRevision-1, backendIntent,
	); !errors.Is(err, router.ErrDurableRouteRevisionMoved) {
		t.Fatalf("stale ReplaceRouteBackendsCAS() error = %v, want revision moved", err)
	}
	afterStaleCAS, err := store1.LoadRouteGroup(ctx, scope1.ProjectID, scope1.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if afterStaleCAS.DesiredRevision != afterIdempotent.DesiredRevision ||
		!reflect.DeepEqual(afterStaleCAS.Routes[0].Backends, afterIdempotent.Routes[0].Backends) {
		t.Fatalf("stale CAS changed route winner: before=%#v after=%#v", afterIdempotent, afterStaleCAS)
	}
	stagedIntent := append([]router.DurableRouteBackend(nil), backendIntent...)
	stagedIntent = append(stagedIntent, router.DurableRouteBackend{
		ID:           "rbe_routes_staged",
		RouteScope:   scope1,
		DeploymentID: "dep_routes_one",
		InstanceID:   "inst_routes_staged",
		ServiceURL:   "http://10.42.0.9:8080",
		Ready:        true,
	})
	_, stagedRevision, err := store1.ReplaceRouteBackendsCAS(
		ctx, route1.ID, afterIdempotent.DesiredRevision, stagedIntent,
	)
	if err != nil {
		t.Fatalf("stage backends with CAS: %v", err)
	}
	domain1.TLSDesired = true
	if domain1, err = store1.PutDomain(ctx, domain1); err != nil {
		t.Fatalf("advance unrelated domain revision: %v", err)
	}
	domain1.TLSDesired = false
	if domain1, err = store1.PutDomain(ctx, domain1); err != nil {
		t.Fatalf("restore unrelated domain intent: %v", err)
	}
	if _, _, err := store1.ReplaceRouteBackendsCAS(
		ctx, route1.ID, stagedRevision, backendIntent,
	); err != nil {
		t.Fatalf("owned rollback after unrelated group revision: %v", err)
	}
	afterOwnedRollback, err := store1.LoadRouteGroup(ctx, scope1.ProjectID, scope1.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterOwnedRollback.Routes[0].Backends) != 1 ||
		afterOwnedRollback.Routes[0].Backends[0].ID != backendIntent[0].ID ||
		afterOwnedRollback.Routes[0].Backends[0].ServiceURL != backendIntent[0].ServiceURL {
		t.Fatalf("owned rollback did not restore backend intent: got=%#v want=%#v", afterOwnedRollback.Routes[0].Backends, backendIntent)
	}

	publicationRoot := t.TempDir()
	publisher1, err := router.NewTraefikDynamicPublicationDirectory(publicationRoot)
	if err != nil {
		t.Fatalf("NewTraefikDynamicPublicationDirectory: %v", err)
	}
	reconciler1 := router.DurableRouteReconciler{Store: store1, Publisher: publisher1}
	if err := reconciler1.ReconcileAll(ctx); err != nil {
		t.Fatalf("private ReconcileAll: %v", err)
	}
	group, err := store1.LoadRouteGroup(ctx, scope1.ProjectID, scope1.EnvironmentID)
	if err != nil {
		t.Fatalf("LoadRouteGroup private: %v", err)
	}
	if group.ApplyState != router.RouteApplyPrivate ||
		group.AppliedRevision != group.DesiredRevision ||
		group.ArtifactHash == "" {
		t.Fatalf("private group = %#v", group)
	}
	artifactPath := filepath.Join(publicationRoot, group.RouteGroup+"."+router.TraefikDynamicArtifactExtension)
	privateArtifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read private artifact: %v", err)
	}
	if strings.Contains(string(privateArtifact), hostname) {
		t.Fatalf("private hostname leaked into route artifact: %s", privateArtifact)
	}

	route1.Public = true
	publicRoute, err := store1.PutHTTPRoute(ctx, route1)
	if err != nil {
		t.Fatalf("PutHTTPRoute public: %v", err)
	}
	if !publicRoute.Public {
		t.Fatal("explicit public intent was not persisted")
	}
	assertEnvironmentMutationSerialized := func(
		name string,
		mutate func() error,
	) {
		t.Helper()
		lockHeld := make(chan struct{})
		releaseLock := make(chan struct{})
		holderDone := make(chan error, 1)
		go func() {
			holderDone <- pool1.WithTx(ctx, func(tx *pg.Tx) error {
				if err := router.AcquireRouteEnvironmentMutationLock(
					ctx,
					tx,
					scope1.ProjectID,
					scope1.EnvironmentID,
				); err != nil {
					return err
				}
				close(lockHeld)
				select {
				case <-releaseLock:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
		select {
		case <-lockHeld:
		case err := <-holderDone:
			t.Fatalf("%s lock holder exited early: %v", name, err)
		case <-ctx.Done():
			t.Fatalf("%s lock holder did not start: %v", name, ctx.Err())
		}

		mutationDone := make(chan error, 1)
		go func() {
			mutationDone <- mutate()
		}()
		select {
		case err := <-mutationDone:
			close(releaseLock)
			<-holderDone
			t.Fatalf(
				"%s bypassed the environment mutation lock: %v",
				name,
				err,
			)
		case <-time.After(100 * time.Millisecond):
		}
		close(releaseLock)
		if err := <-holderDone; err != nil {
			t.Fatalf("%s lock holder: %v", name, err)
		}
		if err := <-mutationDone; err != nil {
			t.Fatalf("%s after lock release: %v", name, err)
		}
	}

	serializedRoute := publicRoute
	serializedRoute.Name = "api-serialized"
	assertEnvironmentMutationSerialized("route intent convergence", func() error {
		var mutationErr error
		serializedRoute, mutationErr = store1.PutHTTPRoute(ctx, serializedRoute)
		return mutationErr
	})
	assertEnvironmentMutationSerialized("single-route backend replace", func() error {
		_, mutationErr := store1.ReplaceRouteBackends(
			ctx,
			serializedRoute.ID,
			backendIntent,
		)
		return mutationErr
	})
	beforeBatch, err := store1.LoadRouteGroup(
		ctx,
		scope1.ProjectID,
		scope1.EnvironmentID,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertEnvironmentMutationSerialized("route-group backend replace", func() error {
		_, _, mutationErr := store1.ReplaceRouteGroupBackendsCAS(
			ctx,
			scope1.ProjectID,
			scope1.EnvironmentID,
			beforeBatch.DesiredRevision,
			[]router.DurableRouteBackendSet{{
				RouteID:  serializedRoute.ID,
				Backends: backendIntent,
			}},
		)
		return mutationErr
	})
	if err := reconciler1.ReconcileAll(ctx); err != nil {
		if !errors.Is(err, router.ErrDurableRouteUnverified) {
			t.Fatalf("public ReconcileAll: %v", err)
		}
	}
	publicArtifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read public artifact: %v", err)
	}
	if !strings.Contains(string(publicArtifact), hostname) ||
		!strings.Contains(string(publicArtifact), "http://10.42.0.8:8080") {
		t.Fatalf("public artifact missing durable route/backend: %s", publicArtifact)
	}
	if err := pool1.WithTx(ctx, func(tx *pg.Tx) error {
		return store1.BindDeploymentRouteIntentInTx(
			ctx,
			tx,
			"dep_routes_one",
			domain1.ID,
			publicRoute.ID,
			scope1,
			time.Now().UTC(),
		)
	}); err != nil {
		t.Fatalf("bind deployment route intent: %v", err)
	}
	projections, err := store1.ListGeneratedRouteProjections(
		ctx,
		scope1.ProjectID,
		scope1.ServiceID,
		[]string{"dep_routes_one", "dep_missing", "dep_routes_one"},
	)
	if err != nil {
		t.Fatalf("list pending generated route projection: %v", err)
	}
	if len(projections) != 1 ||
		projections[0].DeploymentID != "dep_routes_one" ||
		projections[0].Hostname != hostname ||
		projections[0].Path != "/" ||
		projections[0].ApplyState != router.RouteApplyFileApplied ||
		projections[0].Reachable {
		t.Fatalf("file-applied projection overclaimed reachability: %#v", projections)
	}
	readyGroup, err := store1.LoadRouteGroup(ctx, scope1.ProjectID, scope1.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	readyEvidence := router.RoutePublicationVerification{
		TraefikAcknowledged:   true,
		IngressProbeSucceeded: true,
		ProbeURL:              "http://127.0.0.1:8080/",
		ProbeStatus:           http.StatusNoContent,
		ObservedAt:            time.Now().UTC(),
	}
	if err := store1.RecordRoutePublicationObservation(
		ctx,
		readyGroup,
		readyGroup.ArtifactHash,
		readyEvidence,
	); err != nil {
		t.Fatalf("record ready route observation: %v", err)
	}
	applied, err := store1.RecordRouteApply(
		ctx,
		scope1.ProjectID,
		scope1.EnvironmentID,
		readyGroup.DesiredRevision,
		router.RouteApplyResult{
			State:         router.RouteApplyReady,
			ArtifactHash:  readyGroup.ArtifactHash,
			FilePublished: true,
			AttemptedAt:   time.Now().UTC(),
		},
	)
	if err != nil || !applied {
		t.Fatalf("record ready route apply: applied=%t err=%v", applied, err)
	}
	projections, err = store1.ListGeneratedRouteProjections(
		ctx,
		scope1.ProjectID,
		scope1.ServiceID,
		[]string{"dep_routes_one"},
	)
	if err != nil {
		t.Fatalf("list ready generated route projection: %v", err)
	}
	if len(projections) != 1 ||
		projections[0].ApplyState != router.RouteApplyReady ||
		!projections[0].Reachable {
		t.Fatalf("ready projection missing current evidence: %#v", projections)
	}
	foreignProjection, err := store1.ListGeneratedRouteProjections(
		ctx,
		"prj_foreign",
		scope1.ServiceID,
		[]string{"dep_routes_one"},
	)
	if err != nil || len(foreignProjection) != 0 {
		t.Fatalf("cross-project projection=%#v err=%v, want empty", foreignProjection, err)
	}

	project2, err := resources.CreateProject(ctx, api.Project{WorkspaceID: api.DefaultWorkspaceID, Slug: "routes-two"})
	if err != nil {
		t.Fatalf("CreateProject two: %v", err)
	}
	environment2, err := resources.CreateEnvironment(ctx, api.Environment{
		ProjectID: project2.ID,
		Slug:      "production",
		Tier:      "production",
	})
	if err != nil {
		t.Fatalf("CreateEnvironment two: %v", err)
	}
	service2, err := resources.CreateService(ctx, api.Service{
		ProjectID: project2.ID,
		Slug:      "api",
	})
	if err != nil {
		t.Fatalf("CreateService two: %v", err)
	}
	scope2 := router.RouteScope{
		ProjectID:     string(project2.ID),
		EnvironmentID: string(environment2.ID),
		ServiceID:     string(service2.ID),
	}
	if _, err := store1.PutDomain(ctx, router.DurableDomain{
		ID:         "dom_routes_two",
		RouteScope: scope2,
		Kind:       router.DomainKindCustom,
		Hostname:   strings.ToUpper(hostname) + ".",
	}); !errors.Is(err, router.ErrDurableHostnameOwned) {
		t.Fatalf("duplicate normalized hostname err = %v", err)
	}
	if _, err := store1.PutHTTPRoute(ctx, router.DurableHTTPRoute{
		ID:         "rte_cross_scope",
		DomainID:   domain1.ID,
		RouteScope: scope2,
		Name:       "cross",
		TargetPort: 8080,
		Public:     true,
	}); !errors.Is(err, router.ErrDurableRouteScopeMismatch) {
		t.Fatalf("cross-scope route err = %v", err)
	}

	pool1.Close()
	pool2, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 2})
	if err != nil {
		t.Fatalf("reopen pool: %v", err)
	}
	defer pool2.Close()
	if err := pool2.Ping(ctx); err != nil {
		t.Fatalf("reopen ping: %v", err)
	}
	store2 := router.NewPostgresDurableRouteStore(pool2)
	restartedGroup, err := store2.LoadRouteGroup(ctx, scope1.ProjectID, scope1.EnvironmentID)
	if err != nil {
		t.Fatalf("LoadRouteGroup after restart: %v", err)
	}
	if len(restartedGroup.Routes) != 1 ||
		len(restartedGroup.Routes[0].Backends) != 1 ||
		!restartedGroup.Routes[0].Route.Public {
		t.Fatalf("restart group lost durable intent: %#v", restartedGroup)
	}
	publisher2, err := router.NewTraefikDynamicPublicationDirectory(publicationRoot)
	if err != nil {
		t.Fatalf("restart publisher: %v", err)
	}
	reconciler2 := router.DurableRouteReconciler{Store: store2, Publisher: publisher2}
	if err := reconciler2.ReconcileAll(ctx); !errors.Is(
		err,
		router.ErrDurableRouteUnverified,
	) {
		t.Fatalf("restart ReconcileAll: %v", err)
	}

	// Requiring TLS records typed unavailability and fail-closed removal from
	// the published set. This slice never claims issuance or reachability.
	domain1.TLSDesired = true
	tlsDomain, err := store2.PutDomain(ctx, domain1)
	if err != nil {
		t.Fatalf("PutDomain TLS desired: %v", err)
	}
	if tlsDomain.TLSState != router.DomainTLSUnavailable {
		t.Fatalf("TLS state = %q, want unavailable", tlsDomain.TLSState)
	}
	tlsPendingStates, err := store2.ListDeploymentPublicRouteStates(
		ctx,
		"dep_routes_one",
		scope1,
	)
	if err != nil {
		t.Fatalf("list TLS-pending deployment route state: %v", err)
	}
	if len(tlsPendingStates) != 1 ||
		!tlsPendingStates[0].Route.Public ||
		!tlsPendingStates[0].Domain.TLSDesired ||
		tlsPendingStates[0].Domain.TLSState != router.DomainTLSUnavailable {
		t.Fatalf("TLS-pending deployment route was hidden before fail-closed validation: %#v", tlsPendingStates)
	}
	beforeBlocked, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler2.ReconcileAll(ctx); !errors.Is(
		err,
		router.ErrDurableRouteBlocked,
	) {
		t.Fatalf("blocked TLS ReconcileAll: %v", err)
	}
	blockedGroup, err := store2.LoadRouteGroup(ctx, scope1.ProjectID, scope1.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if blockedGroup.ApplyState != router.RouteApplyBlockedTLS ||
		blockedGroup.LastErrorCode != "certificate_issuance_unavailable" ||
		blockedGroup.AppliedRevision != blockedGroup.DesiredRevision {
		t.Fatalf("blocked TLS group = %#v", blockedGroup)
	}
	afterBlocked, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterBlocked) == string(beforeBlocked) ||
		strings.Contains(string(afterBlocked), hostname) {
		t.Fatal("blocked TLS reconciliation did not remove the unsafe route")
	}

	runner := &pg.MigrationRunner{Pool: pool2, FS: migrations.FS, ManifestPath: "manifest.yaml"}
	var rolledBack *pg.MigrationFile
	for {
		rolledBack, err = runner.Down(ctx)
		if err != nil {
			t.Fatalf("migrate down toward 0022: %v", err)
		}
		if rolledBack == nil {
			t.Fatal("migration history ended before version 22")
		}
		if rolledBack.Version == 22 {
			break
		}
	}
	var domainsTable *string
	if err := pool2.QueryRow(ctx, `SELECT to_regclass('public.domains')::text`).Scan(&domainsTable); err != nil {
		t.Fatalf("check domains after down: %v", err)
	}
	if domainsTable != nil {
		t.Fatalf("domains table remains after migration down: %q", *domainsTable)
	}
}

func TestDurableRoutePromotionRetainsHistoricalPortAcrossTargetChange(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	resources := api.NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, api.Project{
		WorkspaceID: api.DefaultWorkspaceID,
		Slug:        "route-port-promotion",
	})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := resources.CreateEnvironment(ctx, api.Environment{
		ProjectID: project.ID,
		Slug:      "production",
		Tier:      "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, api.Service{
		ProjectID: project.ID,
		Slug:      "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := router.RouteScope{
		ProjectID:     string(project.ID),
		EnvironmentID: string(environment.ID),
		ServiceID:     string(service.ID),
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action, state
		) VALUES
			('dep_port_old', $1, $2, $3, 'deploy_image', 'queued'),
			('dep_port_new', $1, $2, $3, 'deploy_image', 'queued')`,
		scope.ProjectID,
		scope.ServiceID,
		scope.EnvironmentID,
	); err != nil {
		t.Fatalf("seed deployments: %v", err)
	}

	store := router.NewPostgresDurableRouteStore(pool)
	domainValue, err := store.PutDomain(ctx, router.DurableDomain{
		ID:         "dom_port_promotion",
		RouteScope: scope,
		Kind:       router.DomainKindGenerated,
		Hostname:   "route-port-promotion.apps.example.test",
		AppsDomain: "apps.example.test",
	})
	if err != nil {
		t.Fatalf("put domain: %v", err)
	}
	routeValue, err := store.PutHTTPRoute(ctx, router.DurableHTTPRoute{
		ID:         "rte_port_promotion",
		DomainID:   domainValue.ID,
		RouteScope: scope,
		Name:       "api",
		PathPrefix: "/",
		TargetPort: 8080,
		Public:     true,
	})
	if err != nil {
		t.Fatalf("put 8080 route: %v", err)
	}
	oldBackend := router.DurableRouteBackend{
		ID:           "rbe_port_old",
		RouteScope:   scope,
		DeploymentID: "dep_port_old",
		InstanceID:   "dep_port_old-r0",
		ServiceURL:   "http://10.42.0.8:8080",
		Ready:        true,
	}
	if _, err := store.ReplaceRouteBackends(
		ctx,
		routeValue.ID,
		[]router.DurableRouteBackend{oldBackend},
	); err != nil {
		t.Fatalf("persist old backend: %v", err)
	}

	routeValue.TargetPort = 9090
	if _, err := store.PutHTTPRoute(ctx, routeValue); err != nil {
		t.Fatalf("move route target to 9090: %v", err)
	}
	before, err := store.LoadRouteGroup(
		ctx,
		scope.ProjectID,
		scope.EnvironmentID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Routes) != 1 ||
		before.Routes[0].Route.TargetPort != 9090 ||
		len(before.Routes[0].Backends) != 1 {
		t.Fatalf("pre-promotion group = %#v", before)
	}
	previous := append(
		[]router.DurableRouteBackend(nil),
		before.Routes[0].Backends...,
	)
	newBackend := router.DurableRouteBackend{
		ID:           "rbe_port_new",
		RouteScope:   scope,
		DeploymentID: "dep_port_new",
		InstanceID:   "dep_port_new-r0",
		ServiceURL:   "http://10.42.0.9:8080",
		Ready:        true,
	}
	badStage := []router.DurableRouteBackendSet{{
		RouteID:  routeValue.ID,
		Backends: append(append([]router.DurableRouteBackend(nil), previous...), newBackend),
	}}
	if _, _, err := store.ReplaceEligibleRouteGroupBackendsCAS(
		ctx,
		scope.ProjectID,
		scope.EnvironmentID,
		before.DesiredRevision,
		badStage,
	); !errors.Is(err, router.ErrDurableRouteInvalid) {
		t.Fatalf("wrong-port candidate error = %v, want durable route invalid", err)
	}
	afterBadStage, err := store.LoadRouteGroup(
		ctx,
		scope.ProjectID,
		scope.EnvironmentID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if afterBadStage.DesiredRevision != before.DesiredRevision ||
		len(afterBadStage.Routes[0].Backends) != 1 ||
		afterBadStage.Routes[0].Backends[0].ServiceURL != oldBackend.ServiceURL {
		t.Fatalf("wrong-port candidate changed route group: %#v", afterBadStage)
	}

	newBackend.ServiceURL = "http://10.42.0.9:9090"
	stage := []router.DurableRouteBackendSet{{
		RouteID:  routeValue.ID,
		Backends: append(append([]router.DurableRouteBackend(nil), previous...), newBackend),
	}}
	_, stagedRevision, err := store.ReplaceEligibleRouteGroupBackendsCAS(
		ctx,
		scope.ProjectID,
		scope.EnvironmentID,
		before.DesiredRevision,
		stage,
	)
	if err != nil {
		t.Fatalf("stage 8080 and 9090 backends: %v", err)
	}
	staged, err := store.LoadRouteGroup(ctx, scope.ProjectID, scope.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	stagedURLs := make(map[string]bool, len(staged.Routes[0].Backends))
	for _, backend := range staged.Routes[0].Backends {
		stagedURLs[backend.ServiceURL] = true
	}
	if len(staged.Routes[0].Backends) != 2 ||
		!stagedURLs[oldBackend.ServiceURL] ||
		!stagedURLs[newBackend.ServiceURL] {
		t.Fatalf("staged backends = %#v, want old and candidate", staged.Routes[0].Backends)
	}

	commit := []router.DurableRouteBackendSet{{
		RouteID:  routeValue.ID,
		Backends: []router.DurableRouteBackend{newBackend},
	}}
	_, committedRevision, err := store.ReplaceEligibleRouteGroupBackendsCAS(
		ctx,
		scope.ProjectID,
		scope.EnvironmentID,
		stagedRevision,
		commit,
	)
	if err != nil {
		t.Fatalf("commit 9090 candidate: %v", err)
	}
	if _, _, err := store.RestoreEligibleRouteGroupBackendsWithOwnershipCAS(
		ctx,
		scope.ProjectID,
		scope.EnvironmentID,
		committedRevision,
		[]router.DurableRouteBackendSet{{
			RouteID:  routeValue.ID,
			Backends: previous,
		}},
	); err != nil {
		t.Fatalf("restore captured 8080 backend: %v", err)
	}
	restored, err := store.LoadRouteGroup(ctx, scope.ProjectID, scope.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Routes[0].Backends) != 1 ||
		restored.Routes[0].Backends[0].ID != oldBackend.ID ||
		restored.Routes[0].Backends[0].ServiceURL != oldBackend.ServiceURL {
		t.Fatalf("restored backends = %#v, want captured old backend", restored.Routes[0].Backends)
	}
}
