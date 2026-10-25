//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package pg_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestActiveDeploymentRouteRefreshConvergesAddressesAndFencesPreparedPublication(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	resources := api.NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, api.Project{WorkspaceID: api.DefaultWorkspaceID, Slug: "route-refresh"})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := resources.CreateEnvironment(ctx, api.Environment{ProjectID: project.ID, Slug: "production", Tier: "production"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store := router.NewPostgresDurableRouteStore(pool)
	var scopes []router.RouteScope
	var routes []router.DurableHTTPRoute
	for _, slug := range []string{"web", "other"} {
		service, err := resources.CreateService(ctx, api.Service{ProjectID: project.ID, Slug: slug})
		if err != nil {
			t.Fatal(err)
		}
		scope := router.RouteScope{ProjectID: string(project.ID), EnvironmentID: string(environment.ID), ServiceID: string(service.ID)}
		seedRouteRepairActiveDeployment(t, ctx, pool, scope.ProjectID, scope.EnvironmentID, scope.ServiceID, "dep_refresh_"+slug, now)
		domain, route, err := router.BuildDomainRouteIntent(router.DomainRouteIntentInput{Scope: scope, Kind: router.DomainKindGenerated, AppsDomain: "apps.example.test", PathPrefix: "/", TargetPort: 8080, Public: true})
		if err != nil {
			t.Fatal(err)
		}
		if domain, err = store.PutDomain(ctx, domain); err != nil {
			t.Fatal(err)
		}
		if route, err = store.PutHTTPRoute(ctx, route); err != nil {
			t.Fatal(err)
		}
		if err := pool.WithTx(ctx, func(tx *pg.Tx) error { return store.BindServiceDomainInTx(ctx, tx, domain.ID, route.ID, scope, now) }); err != nil {
			t.Fatal(err)
		}
		scopes, routes = append(scopes, scope), append(routes, route)
	}
	if _, err := store.ReconcileManagedDeploymentRouteState(ctx); err != nil {
		t.Fatal(err)
	}
	instances := deploy.NewInstanceStore(pool)
	for _, slug := range []string{"web", "other"} {
		if err := instances.Observe(ctx, runtime.RoutingTarget{DeploymentID: "dep_refresh_" + slug, InstanceID: "dep_refresh_" + slug + "-r0", ContainerID: "ctr-refreshed-" + slug, ContainerIP: "10.44.0.70", Ready: true}); err != nil {
			t.Fatal(err)
		}
	}
	assertURL := func(routeID, deploymentID, instanceID, want string) {
		t.Helper()
		var got string
		if err := pool.QueryRow(ctx, `SELECT service_url FROM route_backends WHERE route_id=$1 AND deployment_id=$2 AND instance_id=$3`, routeID, deploymentID, instanceID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("backend=%q want %q", got, want)
		}
	}
	if err := store.RefreshActiveDeploymentRoutes(ctx, "dep_refresh_web", scopes[0]); err != nil {
		t.Fatal(err)
	}
	assertURL(routes[0].ID, "dep_refresh_web", "dep_refresh_web-r0", "http://10.44.0.70:8080")
	assertURL(routes[1].ID, "dep_refresh_other", "dep_refresh_other-r0", "http://10.44.0.10:8080")
	// A periodic runtime observation repairs the other service on its next pass.
	if _, err := store.ReconcileManagedDeploymentRouteState(ctx); err != nil {
		t.Fatal(err)
	}
	assertURL(routes[1].ID, "dep_refresh_other", "dep_refresh_other-r0", "http://10.44.0.70:8080")

	// Stage a newer candidate's publication while the old active pointer is
	// intentionally retained, exactly as the promotion protocol does.
	if _, err := pool.Exec(ctx, `INSERT INTO deployments (id,project_id,service_id,environment_id,action,image_ref,image_digest,state,active,trigger_key,created_at,updated_at)
		SELECT 'dep_refresh_candidate',project_id,service_id,environment_id,action,image_ref,image_digest,'queued',FALSE,'trigger:refresh-candidate',$2,$2 FROM deployments WHERE id=$1`, "dep_refresh_web", now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='deploying' WHERE id='dep_refresh_candidate'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE route_backends SET deployment_id='dep_refresh_candidate',instance_id='dep_refresh_candidate-'||instance_id,service_url='http://10.44.0.90:8080' WHERE route_id=$1`, routes[0].ID); err != nil {
		t.Fatal(err)
	}
	group, err := store.LoadRouteGroup(ctx, scopes[0].ProjectID, scopes[0].EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileManagedDeploymentRouteState(ctx); err != nil {
		t.Fatal(err)
	}
	assertURL(routes[0].ID, "dep_refresh_candidate", "dep_refresh_candidate-dep_refresh_web-r0", "http://10.44.0.90:8080")
	if err := store.RefreshActiveDeploymentRoutes(ctx, "dep_refresh_web", scopes[0]); !errors.Is(err, router.ErrDurableRouteBlocked) {
		t.Fatalf("prepared publication refresh=%v", err)
	}
	if err := store.RefreshActiveDeploymentRoutes(ctx, "dep_refresh_candidate", scopes[0]); !errors.Is(err, router.ErrDurableRouteScopeMismatch) {
		t.Fatalf("non-active candidate refresh=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET runtime_desired_state='stopped' WHERE id='dep_refresh_web'`); err != nil {
		t.Fatal(err)
	}
	if err := store.RefreshActiveDeploymentRoutes(ctx, "dep_refresh_web", scopes[0]); !errors.Is(err, router.ErrDurableRouteScopeMismatch) {
		t.Fatalf("stopped deployment refresh=%v", err)
	}
	after, err := store.LoadRouteGroup(ctx, scopes[0].ProjectID, scopes[0].EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if group.DesiredRevision != after.DesiredRevision {
		t.Fatalf("prepared/fenced publication revision changed %d -> %d", group.DesiredRevision, after.DesiredRevision)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='failed',failure_code='fixture_done' WHERE id='dep_refresh_candidate'`); err != nil {
		t.Fatal(err)
	}
	actions := api.NewPostgresDeploymentActionStore(pool)
	// A raw Docker-ready observation cannot authorize startup publication
	// while a restart is still at its durable pre-readiness checkpoint.
	restart, err := actions.CommitRuntimeAction(ctx, api.RuntimeActionRequest{ServiceID: scopes[0].ServiceID, TargetID: "dep_refresh_web", Action: deploy.RuntimeActionRestart, TriggerKey: "restart:stopped-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := actions.BeginRuntimeAction(ctx, restart.ActionID, string(restart.Operation.ID)); err != nil {
		t.Fatal(err)
	}
	if err := actions.RecordRuntimeActionStep(ctx, restart.ActionID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := instances.Observe(ctx, runtime.RoutingTarget{DeploymentID: "dep_refresh_web", InstanceID: "dep_refresh_web-r0", ContainerID: "ctr-restarting", ContainerIP: "10.44.0.71", Ready: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileManagedDeploymentRouteState(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM route_backends WHERE route_id=$1`, routes[0].ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("startup published pre-readiness restart: count=%d err=%v", count, err)
	}
	if err := actions.RecordRuntimeActionStep(ctx, restart.ActionID, "started"); err != nil {
		t.Fatal(err)
	}
	var desired, observed string
	if err := pool.QueryRow(ctx, `SELECT runtime_desired_state,runtime_observed_state FROM deployments WHERE id='dep_refresh_web'`).Scan(&desired, &observed); err != nil || desired != "running" || observed != "unknown" {
		t.Fatalf("ready checkpoint desired=%s observed=%s err=%v", desired, observed, err)
	}
	if err := actions.CompleteRuntimeAction(ctx, restart.ActionID, "running"); err != nil {
		t.Fatal(err)
	}
	publication, err := router.NewTraefikDynamicPublicationDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	withdrawer := deploy.DurableActionRouteWithdrawer{Store: store, Reconciler: &router.DurableRouteReconciler{Store: store, Publisher: publication, Verifier: routeRepairPublicationVerifier{}}}
	for index, slug := range []string{"web", "other"} {
		t.Run("stop-remove-"+slug, func(t *testing.T) {
			scope, depID := scopes[index], "dep_refresh_"+slug
			if _, err := actions.CommitRuntimeAction(ctx, api.RuntimeActionRequest{ServiceID: scope.ServiceID, TargetID: depID, Action: deploy.RuntimeActionRemove, TriggerKey: "remove-running:" + slug}); err == nil {
				t.Fatal("API allowed removal of active running deployment")
			}
			stop, err := actions.CommitRuntimeAction(ctx, api.RuntimeActionRequest{ServiceID: scope.ServiceID, TargetID: depID, Action: deploy.RuntimeActionStop, TriggerKey: "stop:" + slug})
			if err != nil {
				t.Fatal(err)
			}
			record, err := actions.BeginRuntimeAction(ctx, stop.ActionID, string(stop.Operation.ID))
			if err != nil {
				t.Fatal(err)
			}
			if err := withdrawer.WithdrawDeployment(ctx, record); err != nil {
				t.Fatal(err)
			}
			if err := actions.RecordRuntimeActionStep(ctx, stop.ActionID, "route_withdrawn"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReconcileManagedDeploymentRouteState(ctx); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM route_backends WHERE deployment_id=$1`, depID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("startup resurrected withdrawn stop: count=%d err=%v", count, err)
			}
			rt := &routeRepairActionRuntime{}
			executor := deploy.ActionExecutor{Store: actions, Runtime: rt, Instances: instances, Routes: withdrawer, Operations: routeRepairActionOperations{}}
			execute := func(result api.DeploymentActionResult, action deploy.RuntimeAction) {
				t.Helper()
				payload, _ := json.Marshal(deploy.DeploymentActionJobPayload{SchemaVersion: deploy.DeploymentActionJobPayloadSchema, ActionID: result.ActionID, OperationID: string(result.Operation.ID), DeploymentID: depID, Action: action})
				out, err := executor.Execute(ctx, queue.Job{ID: "job-lifecycle", Payload: payload})
				if err != nil || !out.Succeeded {
					t.Fatalf("%s outcome=%#v err=%v", action, out, err)
				}
			}
			execute(stop, deploy.RuntimeActionStop)
			remove, err := actions.CommitRuntimeAction(ctx, api.RuntimeActionRequest{ServiceID: scope.ServiceID, TargetID: depID, Action: deploy.RuntimeActionRemove, TriggerKey: "remove:" + slug})
			if err != nil {
				t.Fatal(err)
			}
			if index == 1 {
				rt.removeHook = func() error {
					return pool.WithTx(ctx, func(tx *pg.Tx) error {
						for _, q := range []string{
							`UPDATE deployments SET state='superseded',active=FALSE WHERE id='dep_refresh_other'`,
							`INSERT INTO deployments (id,project_id,service_id,environment_id,action,image_ref,image_digest,state,active,trigger_key) SELECT 'dep_refresh_winner',project_id,service_id,environment_id,action,image_ref,image_digest,'queued',FALSE,'trigger:refresh-winner' FROM deployments WHERE id='dep_refresh_other'`,
							`UPDATE deployments SET state='deploying' WHERE id='dep_refresh_winner'`,
							`UPDATE deployments SET state='active',active=TRUE WHERE id='dep_refresh_winner'`,
							`UPDATE service_environment_state SET active_deployment_id='dep_refresh_winner' WHERE active_deployment_id='dep_refresh_other'`,
						} {
							if _, err := tx.Exec(ctx, q); err != nil {
								return err
							}
						}
						return nil
					})
				}
			}
			execute(remove, deploy.RuntimeActionRemove)
			var state string
			var active bool
			var pointer *string
			if err := pool.QueryRow(ctx, `SELECT state,active,runtime_desired_state,runtime_observed_state FROM deployments WHERE id=$1`, depID).Scan(&state, &active, &desired, &observed); err != nil {
				t.Fatal(err)
			}
			if state != "superseded" || active || desired != "removed" || observed != "removed" {
				t.Fatalf("removed deployment state=%s active=%t runtime=%s/%s", state, active, desired, observed)
			}
			if err := pool.QueryRow(ctx, `SELECT active_deployment_id FROM service_environment_state WHERE service_id=$1 AND environment_id=$2`, scope.ServiceID, scope.EnvironmentID).Scan(&pointer); err != nil {
				t.Fatal(err)
			}
			if (index == 0 && pointer != nil) || (index == 1 && (pointer == nil || *pointer != "dep_refresh_winner")) {
				t.Fatalf("removed deployment cleared wrong active pointer: %v", pointer)
			}
		})
	}
}

type routeRepairActionRuntime struct{ removeHook func() error }

func (*routeRepairActionRuntime) Start(context.Context, string, string) error { return nil }
func (*routeRepairActionRuntime) Stop(context.Context, string, string, time.Duration) error {
	return nil
}
func (r *routeRepairActionRuntime) Remove(context.Context, string, string) error {
	if r.removeHook != nil {
		return r.removeHook()
	}
	return nil
}

type routeRepairActionOperations struct{}

func (routeRepairActionOperations) Transition(_ context.Context, id orailtypes.ID, state orailtypes.OperationState) (orailtypes.Operation, error) {
	return orailtypes.Operation{ID: id, State: state}, nil
}
func (routeRepairActionOperations) Fail(context.Context, orailtypes.ID, orailtypes.FailureCode, string) (orailtypes.Operation, error) {
	return orailtypes.Operation{}, nil
}

type routeRepairPublicationVerifier struct{}

func (routeRepairPublicationVerifier) VerifyRoutePublication(context.Context, router.DurableRouteGroup, []router.RouteInput, string) (router.RoutePublicationVerification, error) {
	return router.RoutePublicationVerification{TraefikAcknowledged: true, IngressProbeSucceeded: true, ProbeStatus: 200, ObservedAt: time.Now().UTC()}, nil
}

func TestManagedDeploymentRouteStartupRepairIsIdempotentAndOwnershipBound(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)

	resources := api.NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, api.Project{
		WorkspaceID: api.DefaultWorkspaceID,
		Slug:        "route-repair",
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
		Slug:      "web",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	seedRouteRepairActiveDeployment(
		t,
		ctx,
		pool,
		string(project.ID),
		string(environment.ID),
		string(service.ID),
		"dep_route_repair_active",
		now,
	)
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id,project_id,service_id,environment_id,action,
			image_ref,image_digest,state,active,trigger_key,
			created_at,updated_at
		) VALUES (
			'dep_route_repair_terminal',$1,$3,$2,'deploy_image',
			'registry.example/web@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'queued',FALSE,'trigger:route-repair-terminal',$4,$4
		)`, string(project.ID), string(environment.ID), string(service.ID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE deployments
		   SET state='failed',failure_code='route_repair_fixture_terminal',updated_at=$2
		 WHERE id=$1`, "dep_route_repair_terminal", now); err != nil {
		t.Fatal(err)
	}

	scope := router.RouteScope{
		ProjectID:     string(project.ID),
		EnvironmentID: string(environment.ID),
		ServiceID:     string(service.ID),
	}
	store := router.NewPostgresDurableRouteStore(pool)
	store.Now = func() time.Time { return now }
	generated, generatedRoute, err := router.BuildDomainRouteIntent(
		router.DomainRouteIntentInput{
			Scope: scope, Kind: router.DomainKindGenerated,
			AppsDomain: "apps.example.test", PathPrefix: "/",
			TargetPort: 8080, Public: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	generated, err = store.PutDomain(ctx, generated)
	if err != nil {
		t.Fatal(err)
	}
	generatedRoute, err = store.PutHTTPRoute(ctx, generatedRoute)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		return store.BindServiceDomainInTx(
			ctx,
			tx,
			generated.ID,
			generatedRoute.ID,
			scope,
			now,
		)
	}); err != nil {
		t.Fatal(err)
	}

	// A recovery-created custom row can carry matching scope and even a
	// service binding, but it remains ineligible until its exact TXT challenge
	// is verified. Startup repair must not turn scope coincidence into intent.
	custom, customRoute, err := router.BuildDomainRouteIntent(
		router.DomainRouteIntentInput{
			Scope: scope, Kind: router.DomainKindCustom,
			Hostname: "legacy.route-repair.example.test", PathPrefix: "/legacy",
			TargetPort: 8080, Public: false,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	custom, err = store.PutDomain(ctx, custom)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureDomainLifecycle(ctx, custom); err != nil {
		t.Fatal(err)
	}
	customRoute, err = store.PutHTTPRoute(ctx, customRoute)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		return store.BindServiceDomainInTx(
			ctx,
			tx,
			custom.ID,
			customRoute.ID,
			scope,
			now,
		)
	}); err != nil {
		t.Fatal(err)
	}
	customRoute.Public = true
	if _, err := store.PutHTTPRoute(ctx, customRoute); err != nil {
		t.Fatal(err)
	}

	before, err := store.LoadRouteGroup(ctx, scope.ProjectID, scope.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ReconcileManagedDeploymentRouteState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.GroupsScanned != 1 ||
		first.IntentBindingsAdded != 1 ||
		first.BackendGroupsChanged != 1 {
		t.Fatalf("first repair = %#v, want one binding and one backend group", first)
	}
	afterFirst, err := store.LoadRouteGroup(ctx, scope.ProjectID, scope.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFirst.DesiredRevision != before.DesiredRevision+1 {
		t.Fatalf(
			"repair revision = %d, want %d",
			afterFirst.DesiredRevision,
			before.DesiredRevision+1,
		)
	}

	var generatedIntents, generatedBackends, customIntents, customBackends, terminalIntents int
	var generatedBackendObservedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM deployment_route_intents
		    WHERE deployment_id='dep_route_repair_active' AND route_id=$1),
		  (SELECT count(*) FROM route_backends
		    WHERE deployment_id='dep_route_repair_active' AND route_id=$1
		      AND ready=TRUE),
		  (SELECT count(*) FROM deployment_route_intents
		    WHERE deployment_id='dep_route_repair_active' AND route_id=$2),
		  (SELECT count(*) FROM route_backends
		    WHERE deployment_id='dep_route_repair_active' AND route_id=$2),
		  (SELECT count(*) FROM deployment_route_intents
		    WHERE deployment_id='dep_route_repair_terminal'),
		  (SELECT min(observed_at) FROM route_backends WHERE route_id=$1)`,
		generatedRoute.ID,
		customRoute.ID,
	).Scan(
		&generatedIntents,
		&generatedBackends,
		&customIntents,
		&customBackends,
		&terminalIntents,
		&generatedBackendObservedAt,
	); err != nil {
		t.Fatal(err)
	}
	if generatedIntents != 1 ||
		generatedBackends != 2 ||
		customIntents != 0 ||
		customBackends != 0 ||
		terminalIntents != 0 {
		t.Fatalf(
			"repair artifacts generated=%d/%d custom=%d/%d terminal=%d",
			generatedIntents,
			generatedBackends,
			customIntents,
			customBackends,
			terminalIntents,
		)
	}

	store.Now = func() time.Time { return now.Add(time.Hour) }
	second, err := store.ReconcileManagedDeploymentRouteState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.IntentBindingsAdded != 0 || second.BackendGroupsChanged != 0 {
		t.Fatalf("second repair = %#v, want no-op", second)
	}
	afterSecond, err := store.LoadRouteGroup(ctx, scope.ProjectID, scope.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	var secondObservedAt time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT min(observed_at) FROM route_backends WHERE route_id=$1`,
		generatedRoute.ID,
	).Scan(&secondObservedAt); err != nil {
		t.Fatal(err)
	}
	if afterSecond.DesiredRevision != afterFirst.DesiredRevision ||
		!secondObservedAt.Equal(generatedBackendObservedAt) {
		t.Fatalf(
			"second repair changed revision/time: %d->%d %s->%s",
			afterFirst.DesiredRevision,
			afterSecond.DesiredRevision,
			generatedBackendObservedAt,
			secondObservedAt,
		)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE service_environment_state
		   SET active_deployment_id='dep_route_repair_terminal',updated_at=$3
		 WHERE service_id=$1 AND environment_id=$2`,
		scope.ServiceID, scope.EnvironmentID, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	third, err := store.ReconcileManagedDeploymentRouteState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if third.BackendGroupsChanged != 1 {
		t.Fatalf("inconsistent-active repair=%#v, want one fail-closed backend clear", third)
	}
	var clearedBackends int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM route_backends WHERE route_id=$1`, generatedRoute.ID).Scan(&clearedBackends); err != nil {
		t.Fatal(err)
	}
	if clearedBackends != 0 {
		t.Fatalf("inconsistent active pointer retained %d stale backends", clearedBackends)
	}
	afterThird, err := store.LoadRouteGroup(ctx, scope.ProjectID, scope.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	fourth, err := store.ReconcileManagedDeploymentRouteState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterFourth, err := store.LoadRouteGroup(ctx, scope.ProjectID, scope.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if fourth.BackendGroupsChanged != 0 || afterFourth.DesiredRevision != afterThird.DesiredRevision {
		t.Fatalf("repeated inconsistent-active repair wrote again: %#v revision %d->%d", fourth, afterThird.DesiredRevision, afterFourth.DesiredRevision)
	}

	selectedGenerated, err := store.SelectManagedServiceRoute(ctx, scope, router.ManagedRouteSelector{})
	if err != nil || selectedGenerated.Route.ID != generatedRoute.ID {
		t.Fatalf("hostless generated selector=%#v err=%v", selectedGenerated, err)
	}
	if _, err := store.SelectManagedServiceRoute(ctx, scope, router.ManagedRouteSelector{Host: custom.NormalizedHostname}); !errors.Is(err, router.ErrDurableRouteNotFound) {
		t.Fatalf("unverified custom selector error=%v, want not found", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE domain_dns_challenges SET state='verified',verified_at=$2,updated_at=$2
		 WHERE domain_id=$1`, custom.ID, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	selectedCustom, err := store.SelectManagedServiceRoute(ctx, scope, router.ManagedRouteSelector{Host: custom.NormalizedHostname, PathPrefix: "/legacy"})
	if err != nil || selectedCustom.Route.ID != customRoute.ID {
		t.Fatalf("verified custom selector=%#v err=%v", selectedCustom, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE domains SET tls_desired=TRUE,tls_state='unavailable',updated_at=$2
		 WHERE id=$1`, custom.ID, now.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SelectManagedServiceRoute(ctx, scope, router.ManagedRouteSelector{Host: custom.NormalizedHostname, PathPrefix: "/legacy", TLSDesired: true}); !errors.Is(err, router.ErrDurableRouteNotFound) {
		t.Fatalf("TLS-pending custom selector error=%v, want not found", err)
	}

	secondGeneratedRoute := generatedRoute
	secondGeneratedRoute.ID = generatedRoute.ID + "-alternate"
	secondGeneratedRoute.Name = generatedRoute.Name + "-alternate"
	secondGeneratedRoute.PathPrefix = "/alternate"
	if secondGeneratedRoute, err = store.PutHTTPRoute(ctx, secondGeneratedRoute); err != nil {
		t.Fatal(err)
	}
	bindErr := pool.WithTx(ctx, func(tx *pg.Tx) error {
		return store.BindServiceDomainInTx(ctx, tx, generated.ID, secondGeneratedRoute.ID, scope, now.Add(5*time.Hour))
	})
	if !errors.Is(bindErr, router.ErrDurableRouteInvalid) {
		t.Fatalf("second generated route binding error=%v, want singleton rejection", bindErr)
	}
	selectedGenerated, err = store.SelectManagedServiceRoute(ctx, scope, router.ManagedRouteSelector{})
	if err != nil || selectedGenerated.Route.ID != generatedRoute.ID {
		t.Fatalf("selector after rejected second binding=%#v err=%v, want original singleton", selectedGenerated, err)
	}
}

func TestManagedDeploymentRouteStartupRepairUsesOneTwoServiceSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)

	resources := api.NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, api.Project{WorkspaceID: api.DefaultWorkspaceID, Slug: "route-repair-multi"})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := resources.CreateEnvironment(ctx, api.Environment{ProjectID: project.ID, Slug: "production", Tier: "production"})
	if err != nil {
		t.Fatal(err)
	}
	firstService, err := resources.CreateService(ctx, api.Service{ProjectID: project.ID, Slug: "api"})
	if err != nil {
		t.Fatal(err)
	}
	secondService, err := resources.CreateService(ctx, api.Service{ProjectID: project.ID, Slug: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	seedRouteRepairActiveDeployment(t, ctx, pool, string(project.ID), string(environment.ID), string(firstService.ID), "dep_route_repair_api", now)
	seedRouteRepairActiveDeployment(t, ctx, pool, string(project.ID), string(environment.ID), string(secondService.ID), "dep_route_repair_worker", now)

	store := router.NewPostgresDurableRouteStore(pool)
	store.Now = func() time.Time { return now }
	var revisionMover router.DurableHTTPRoute
	for _, serviceID := range []string{string(firstService.ID), string(secondService.ID)} {
		scope := router.RouteScope{ProjectID: string(project.ID), EnvironmentID: string(environment.ID), ServiceID: serviceID}
		domainValue, routeValue, err := router.BuildDomainRouteIntent(router.DomainRouteIntentInput{
			Scope: scope, Kind: router.DomainKindGenerated, AppsDomain: "apps.example.test",
			PathPrefix: "/", TargetPort: 8080, Public: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if domainValue, err = store.PutDomain(ctx, domainValue); err != nil {
			t.Fatal(err)
		}
		routeValue.DomainID = domainValue.ID
		if routeValue, err = store.PutHTTPRoute(ctx, routeValue); err != nil {
			t.Fatal(err)
		}
		if serviceID == string(firstService.ID) {
			revisionMover = routeValue
		}
		if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
			return store.BindServiceDomainInTx(ctx, tx, domainValue.ID, routeValue.ID, scope, now)
		}); err != nil {
			t.Fatal(err)
		}
	}

	blocker, err := pool.Inner().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blockerReleased := false
	defer func() {
		if !blockerReleased {
			_ = blocker.Rollback(context.Background())
		}
	}()
	blockedKey := "service-environment-deploy:" + string(secondService.ID) + ":" + string(environment.ID)
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, blockedKey); err != nil {
		t.Fatal(err)
	}
	type repairOutcome struct {
		result router.ManagedRouteRepairResult
		err    error
	}
	repairDone := make(chan repairOutcome, 1)
	go func() {
		result, err := store.ReconcileManagedDeploymentRouteState(ctx)
		repairDone <- repairOutcome{result: result, err: err}
	}()
	waitDeadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND granted=FALSE`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatal("repair did not reach sorted second-service lock barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	revisionMover.Name += "-moved"
	moveDone := make(chan error, 1)
	go func() {
		_, err := store.PutHTTPRoute(ctx, revisionMover)
		moveDone <- err
	}()
	select {
	case err := <-moveDone:
		t.Fatalf("route revision mover escaped repair environment lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	blockerReleased = true
	firstOutcome := <-repairDone
	if firstOutcome.err != nil {
		t.Fatal(firstOutcome.err)
	}
	if err := <-moveDone; err != nil {
		t.Fatal(err)
	}
	first := firstOutcome.result
	if first.GroupsScanned != 1 || first.IntentBindingsAdded != 2 || first.BackendGroupsChanged != 1 {
		t.Fatalf("two-service repair=%#v", first)
	}
	var intents, backends, wrongServiceBackends int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM deployment_route_intents WHERE environment_id=$1),
		  (SELECT count(*) FROM route_backends WHERE environment_id=$1),
		  (SELECT count(*) FROM route_backends rb JOIN deployments d ON d.id=rb.deployment_id
		    WHERE rb.environment_id=$1 AND rb.service_id<>d.service_id)`,
		string(environment.ID)).Scan(&intents, &backends, &wrongServiceBackends); err != nil {
		t.Fatal(err)
	}
	if intents != 2 || backends != 4 || wrongServiceBackends != 0 {
		t.Fatalf("two-service artifacts intents=%d backends=%d cross-service=%d", intents, backends, wrongServiceBackends)
	}
	second, err := store.ReconcileManagedDeploymentRouteState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.IntentBindingsAdded != 0 || second.BackendGroupsChanged != 0 {
		t.Fatalf("two-service second repair=%#v, want no-op", second)
	}
}

func TestPrivateRouteReconcileSkipsUnchangedDurableAttemptButRepairsArtifact(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)

	resources := api.NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, api.Project{
		WorkspaceID: api.DefaultWorkspaceID,
		Slug:        "private-noop",
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
		Slug:      "web",
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := router.RouteScope{
		ProjectID:     string(project.ID),
		EnvironmentID: string(environment.ID),
		ServiceID:     string(service.ID),
	}
	now := time.Date(2026, 8, 29, 14, 0, 0, 0, time.UTC)
	store := router.NewPostgresDurableRouteStore(pool)
	store.Now = func() time.Time { return now }
	domain, route, err := router.BuildDomainRouteIntent(router.DomainRouteIntentInput{
		Scope: scope, Kind: router.DomainKindGenerated,
		AppsDomain: "apps.example.test", PathPrefix: "/",
		TargetPort: 8080, Public: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if domain, err = store.PutDomain(ctx, domain); err != nil {
		t.Fatal(err)
	}
	route.DomainID = domain.ID
	if route, err = store.PutHTTPRoute(ctx, route); err != nil {
		t.Fatal(err)
	}

	publicationRoot := t.TempDir()
	publisher, err := router.NewTraefikDynamicPublicationDirectory(publicationRoot)
	if err != nil {
		t.Fatal(err)
	}
	reconciler := &router.DurableRouteReconciler{
		Store: store, Publisher: publisher,
		Now: func() time.Time { return now },
	}
	now = now.Add(time.Second)
	if err := reconciler.ReconcileGroup(ctx, scope.ProjectID, scope.EnvironmentID); err != nil {
		t.Fatal(err)
	}
	group, attempts, lastAttempt, updatedAt := loadRouteApplyEvidence(
		t,
		ctx,
		pool,
		scope,
	)
	for range 3 {
		now = now.Add(time.Second)
		if err := reconciler.ReconcileGroup(ctx, scope.ProjectID, scope.EnvironmentID); err != nil {
			t.Fatal(err)
		}
	}
	unchanged, unchangedAttempts, unchangedLastAttempt, unchangedUpdatedAt := loadRouteApplyEvidence(
		t,
		ctx,
		pool,
		scope,
	)
	if unchanged.DesiredRevision != group.DesiredRevision ||
		unchanged.ObservedRevision != group.ObservedRevision ||
		unchanged.AppliedRevision != group.AppliedRevision ||
		unchanged.ArtifactHash != group.ArtifactHash ||
		unchangedAttempts != attempts ||
		!unchangedLastAttempt.Equal(lastAttempt) ||
		!unchangedUpdatedAt.Equal(updatedAt) {
		t.Fatalf(
			"unchanged private reconcile mutated durable evidence: before=%#v/%d/%s/%s after=%#v/%d/%s/%s",
			group,
			attempts,
			lastAttempt,
			updatedAt,
			unchanged,
			unchangedAttempts,
			unchangedLastAttempt,
			unchangedUpdatedAt,
		)
	}

	artifactPath := filepath.Join(
		publicationRoot,
		group.RouteGroup+"."+router.TraefikDynamicArtifactExtension,
	)
	if err := os.WriteFile(artifactPath, []byte("corrupted\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := reconciler.ReconcileGroup(ctx, scope.ProjectID, scope.EnvironmentID); err != nil {
		t.Fatal(err)
	}
	repaired, repairedAttempts, repairedLastAttempt, repairedUpdatedAt := loadRouteApplyEvidence(
		t,
		ctx,
		pool,
		scope,
	)
	if repairedAttempts != attempts+1 ||
		!repairedLastAttempt.After(lastAttempt) ||
		!repairedUpdatedAt.After(updatedAt) ||
		repaired.DesiredRevision != group.DesiredRevision ||
		repaired.ArtifactHash != group.ArtifactHash {
		t.Fatalf(
			"artifact repair evidence=%#v/%d/%s/%s, want one attempt at unchanged revision",
			repaired,
			repairedAttempts,
			repairedLastAttempt,
			repairedUpdatedAt,
		)
	}

	route.Public = true
	now = now.Add(time.Second)
	if _, err := store.PutHTTPRoute(ctx, route); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := reconciler.ReconcileGroup(ctx, scope.ProjectID, scope.EnvironmentID); !errors.Is(err, router.ErrDurableRouteBlocked) {
		t.Fatalf("public backend-less transition error=%v, want blocked route", err)
	}
	transitioned, transitionedAttempts, _, _ := loadRouteApplyEvidence(
		t,
		ctx,
		pool,
		scope,
	)
	if transitioned.ApplyState != router.RouteApplyBlockedBackend ||
		transitioned.DesiredRevision != group.DesiredRevision+1 ||
		transitionedAttempts != repairedAttempts+1 {
		t.Fatalf(
			"real state transition=%#v attempts=%d, want one durable write",
			transitioned,
			transitionedAttempts,
		)
	}
}

func seedRouteRepairActiveDeployment(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	projectID string,
	environmentID string,
	serviceID string,
	deploymentID string,
	now time.Time,
) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{query: `INSERT INTO deployments (
			id,project_id,service_id,environment_id,action,
			image_ref,image_digest,state,active,trigger_key,
			created_at,updated_at
		) VALUES (
			$5,$1,$3,$2,'deploy_image',
			'registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'queued',FALSE,'trigger:route-repair:'||$5,$4,$4
		)`, args: []any{projectID, environmentID, serviceID, now, deploymentID}},
		{query: `UPDATE deployments SET state='deploying',updated_at=$2
		 WHERE id=$1`, args: []any{deploymentID, now}},
		{query: `UPDATE deployments SET state='active',active=TRUE,updated_at=$2
		 WHERE id=$1`, args: []any{deploymentID, now}},
		{query: `INSERT INTO service_environment_state (
			service_id,environment_id,active_deployment_id,
			last_deployment_seq,version,updated_at
		) VALUES ($1,$2,$3,1,1,$4)`, args: []any{serviceID, environmentID, deploymentID, now}},
		{query: `INSERT INTO deployment_instances (
			deployment_id,instance_id,replica_ordinal,desired_state,
			observed_state,container_id,container_ip,ready,created_at,updated_at
		) VALUES
			($1,$1||'-r0',0,
			 'running','running','ctr-route-repair-0','10.44.0.10',TRUE,$2,$2),
			($1,$1||'-r1',1,
			 'running','running','ctr-route-repair-1','10.44.0.11',TRUE,$2,$2)`, args: []any{deploymentID, now}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed active route repair deployment: %v", err)
		}
	}
}

func loadRouteApplyEvidence(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	scope router.RouteScope,
) (router.DurableRouteGroup, int64, time.Time, time.Time) {
	t.Helper()
	store := router.NewPostgresDurableRouteStore(pool)
	group, err := store.LoadRouteGroup(ctx, scope.ProjectID, scope.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	var attempts int64
	var lastAttempt, updatedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT apply_attempts,last_attempt_at,updated_at
		  FROM route_controller_groups
		 WHERE project_id=$1 AND environment_id=$2`,
		scope.ProjectID,
		scope.EnvironmentID,
	).Scan(&attempts, &lastAttempt, &updatedAt); err != nil {
		t.Fatal(err)
	}
	return group, attempts, lastAttempt.UTC(), updatedAt.UTC()
}
