//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package pg_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/router"
)

func TestMultiRouteDomainResourcesUpgradePreservesAndRefusesLossyDown(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newPool(t, ctx, dsn)

	runner51 := migrationRunnerThrough(t, pool, 51)
	applied, err := runner51.Up(ctx)
	if err != nil || len(applied) == 0 || applied[len(applied)-1].Version != 51 {
		t.Fatalf("migrate through 51: applied=%#v err=%v", applied, err)
	}
	resources := api.NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, api.Project{
		WorkspaceID: api.DefaultWorkspaceID,
		Slug:        "multi-route-upgrade",
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
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id,project_id,service_id,environment_id,action,state
		) VALUES ('dep_multi_route_upgrade',$1,$2,$3,'deploy_image','queued')`,
		project.ID,
		service.ID,
		environment.ID,
	); err != nil {
		t.Fatal(err)
	}
	scope := router.RouteScope{
		ProjectID:     string(project.ID),
		EnvironmentID: string(environment.ID),
		ServiceID:     string(service.ID),
	}
	store := router.NewPostgresDurableRouteStore(pool)
	generated, generatedRoute, err := router.BuildDomainRouteIntent(
		router.DomainRouteIntentInput{
			Scope: scope, Kind: router.DomainKindGenerated,
			AppsDomain: "apps.midnight.test", PathPrefix: "/",
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
	custom, customRoute, err := router.BuildDomainRouteIntent(
		router.DomainRouteIntentInput{
			Scope: scope, Kind: router.DomainKindCustom,
			Hostname: "custom.multi-route.example.test", PathPrefix: "/custom",
			TargetPort: 8080, Public: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	custom, err = store.PutDomain(ctx, custom)
	if err != nil {
		t.Fatal(err)
	}
	customRoute, err = store.PutHTTPRoute(ctx, customRoute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployment_route_intents (
			deployment_id,route_id,domain_id,project_id,
			environment_id,service_id
		) VALUES ('dep_multi_route_upgrade',$1,$2,$3,$4,$5)`,
		customRoute.ID,
		custom.ID,
		scope.ProjectID,
		scope.EnvironmentID,
		scope.ServiceID,
	); err != nil {
		t.Fatal(err)
	}

	runner52 := migrationRunnerThrough(t, pool, 52)
	applied, err = runner52.Up(ctx)
	if err != nil || len(applied) != 1 || applied[0].Version != 52 {
		t.Fatalf("apply 52: applied=%#v err=%v", applied, err)
	}
	var resourceVersion int64
	if err := pool.QueryRow(
		ctx,
		`SELECT resource_version FROM domains WHERE id=$1`,
		generated.ID,
	).Scan(&resourceVersion); err != nil || resourceVersion != 1 {
		t.Fatalf("resource version=%d err=%v", resourceVersion, err)
	}
	var upgradedServiceBinding int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM service_domain_bindings
		 WHERE domain_id=$1
		   AND route_id=$2`,
		generated.ID,
		generatedRoute.ID,
	).Scan(&upgradedServiceBinding); err != nil || upgradedServiceBinding != 1 {
		t.Fatalf(
			"upgraded generated service binding=%d err=%v",
			upgradedServiceBinding,
			err,
		)
	}
	var customServiceBinding int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM service_domain_bindings
		 WHERE domain_id=$1`,
		custom.ID,
	).Scan(&customServiceBinding); err != nil || customServiceBinding != 0 {
		t.Fatalf(
			"upgraded custom service binding=%d err=%v",
			customServiceBinding,
			err,
		)
	}
	var localBinding, inheritedBinding int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (
		           WHERE route_id=$2 AND domain_id=$3
		       ) AS local_binding,
		       count(*) FILTER (
		           WHERE route_id=$4 AND domain_id=$5
		       ) AS inherited_binding
		  FROM deployment_route_intents
		 WHERE deployment_id=$1
		   AND project_id=$6
		   AND environment_id=$7
		   AND service_id=$8`,
		"dep_multi_route_upgrade",
		customRoute.ID,
		custom.ID,
		generatedRoute.ID,
		generated.ID,
		scope.ProjectID,
		scope.EnvironmentID,
		scope.ServiceID,
	).Scan(&localBinding, &inheritedBinding); err != nil {
		t.Fatal(err)
	}
	if localBinding != 1 || inheritedBinding != 1 {
		t.Fatalf(
			"open deployment bindings local=%d inherited=%d, want both preserved",
			localBinding,
			inheritedBinding,
		)
	}
	if down, err := runner52.Down(ctx); err == nil ||
		down != nil ||
		!strings.Contains(err.Error(), "multiple route bindings exist") {
		t.Fatalf("lossy down file=%#v err=%v", down, err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM deployment_route_intents
		 WHERE deployment_id='dep_multi_route_upgrade'
		   AND route_id=$1`,
		customRoute.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE domains SET resource_version=7 WHERE id=$1`,
		generated.ID,
	); err != nil {
		t.Fatal(err)
	}
	if down, err := runner52.Down(ctx); err == nil ||
		down != nil ||
		!strings.Contains(err.Error(), "service-managed domain bindings exist") {
		t.Fatalf("product-domain down file=%#v err=%v", down, err)
	}
	if _, err := pool.Exec(
		ctx,
		`DELETE FROM domains WHERE id=$1`,
		generated.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO retired_product_domain_ids (
			domain_id,project_id,environment_id,service_id,retired_at
		) VALUES ($1,$2,$3,$4,CURRENT_TIMESTAMP)`,
		generated.ID,
		scope.ProjectID,
		scope.EnvironmentID,
		scope.ServiceID,
	); err != nil {
		t.Fatal(err)
	}
	if down, err := runner52.Down(ctx); err == nil ||
		down != nil ||
		!strings.Contains(err.Error(), "retired product domain identity history exists") ||
		!strings.Contains(err.Error(), "preserve schema 0052") {
		t.Fatalf("retired-identity down file=%#v err=%v", down, err)
	}
	if _, err := pool.Exec(
		ctx,
		`DELETE FROM retired_product_domain_ids WHERE domain_id=$1`,
		generated.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployment_route_intents (
			deployment_id,route_id,domain_id,project_id,
			environment_id,service_id
		) VALUES ('dep_multi_route_upgrade',$1,$2,$3,$4,$5)`,
		customRoute.ID,
		custom.ID,
		scope.ProjectID,
		scope.EnvironmentID,
		scope.ServiceID,
	); err != nil {
		t.Fatal(err)
	}
	down, err := runner52.Down(ctx)
	if err != nil || down == nil || down.Version != 52 {
		t.Fatalf("compatible down file=%#v err=%v", down, err)
	}
	reapplied, err := runner52.Up(ctx)
	if err != nil || len(reapplied) != 1 || reapplied[0].Version != 52 {
		t.Fatalf("reapply 52: applied=%#v err=%v", reapplied, err)
	}
	var preserved int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM deployment_route_intents
		 WHERE deployment_id='dep_multi_route_upgrade'
		   AND route_id=$1`,
		customRoute.ID,
	).Scan(&preserved); err != nil || preserved != 1 {
		t.Fatalf("preserved binding=%d err=%v", preserved, err)
	}
}

func TestMultiRouteDomainResourcesUpgradeRefusesAmbiguousGeneratedSingletonAtomically(
	t *testing.T,
) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newPool(t, ctx, dsn)

	runner51 := migrationRunnerThrough(t, pool, 51)
	if applied, err := runner51.Up(ctx); err != nil ||
		len(applied) == 0 ||
		applied[len(applied)-1].Version != 51 {
		t.Fatalf("migrate through 51: applied=%#v err=%v", applied, err)
	}
	resources := api.NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, api.Project{
		WorkspaceID: api.DefaultWorkspaceID,
		Slug:        "ambiguous-generated-upgrade",
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
	if _, err := pool.Exec(ctx, `
		INSERT INTO domains (
			id,project_id,environment_id,service_id,kind,hostname,
			normalized_hostname,apps_domain,tls_desired,tls_state,
			desired_revision
		) VALUES
			('dom_generated_first',$1,$2,$3,'generated',
			 'first.apps.midnight.test','first.apps.midnight.test',
			 'apps.midnight.test',FALSE,'disabled',1),
			('dom_generated_second',$1,$2,$3,'generated',
			 'second.apps.midnight.test','second.apps.midnight.test',
			 'apps.midnight.test',FALSE,'disabled',1)`,
		project.ID,
		environment.ID,
		service.ID,
	); err != nil {
		t.Fatal(err)
	}

	runner52 := migrationRunnerThrough(t, pool, 52)
	applied, err := runner52.Up(ctx)
	if err == nil ||
		len(applied) != 0 ||
		!strings.Contains(err.Error(), "cannot enable generated-domain singleton") ||
		!strings.Contains(err.Error(), string(project.ID)) ||
		!strings.Contains(err.Error(), string(environment.ID)) ||
		!strings.Contains(err.Error(), string(service.ID)) {
		t.Fatalf("ambiguous singleton applied=%#v err=%v", applied, err)
	}
	var resourceVersionColumn, serviceBindingTable, retiredIdentityTable, migrationRecord int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM information_schema.columns
		 WHERE table_schema=current_schema()
		   AND table_name='domains'
		   AND column_name='resource_version'`,
	).Scan(&resourceVersionColumn); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM information_schema.tables
		 WHERE table_schema=current_schema()
		   AND table_name='service_domain_bindings'`,
	).Scan(&serviceBindingTable); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM information_schema.tables
		 WHERE table_schema=current_schema()
		   AND table_name='retired_product_domain_ids'`,
	).Scan(&retiredIdentityTable); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM schema_migrations
		 WHERE version=52
		   AND name='multi_route_domain_resources'`,
	).Scan(&migrationRecord); err != nil {
		t.Fatal(err)
	}
	if resourceVersionColumn != 0 ||
		serviceBindingTable != 0 ||
		retiredIdentityTable != 0 ||
		migrationRecord != 0 {
		t.Fatalf(
			"failed migration residue: resource_version=%d bindings=%d retired=%d record=%d",
			resourceVersionColumn,
			serviceBindingTable,
			retiredIdentityTable,
			migrationRecord,
		)
	}

	if _, err := pool.Exec(
		ctx,
		`DELETE FROM domains WHERE id='dom_generated_second'`,
	); err != nil {
		t.Fatal(err)
	}
	retried, err := runner52.Up(ctx)
	if err != nil || len(retried) != 1 || retried[0].Version != 52 {
		t.Fatalf("retry migration 52: applied=%#v err=%v", retried, err)
	}
}
