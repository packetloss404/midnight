//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	routing "github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/migrations"
)

func TestOperatorSourceUpInheritsOnlyEligibleManagedRoutesAtomically(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIDNIGHT_UP_EXTRACTION_ROOT", t.TempDir())
	handler := newPostgresBootRouter(t, pool)
	project := doRouterJSON[Project](t, handler, http.MethodPost, "/api/v1alpha/projects",
		`{"workspace_id":"wrk_default","slug":"operator-source-routes","display_name":"Operator source routes"}`,
		map[string]string{"Idempotency-Key": "operator-source-project"}, http.StatusCreated)
	environments := doRouterJSON[[]Environment](t, handler, http.MethodGet, "/api/v1alpha/environments?project_id="+string(project.ID), "", nil, http.StatusOK)
	if len(environments) != 1 {
		t.Fatalf("environments = %#v", environments)
	}
	routes := routing.NewPostgresDurableRouteStore(pool)

	for _, test := range []struct {
		name     string
		kind     routing.DomainKind
		bound    bool
		public   bool
		tls      bool
		verified bool
		wantBind bool
	}{
		{name: "managed", kind: routing.DomainKindGenerated, bound: true, public: true, wantBind: true},
		{name: "private"},
		{name: "unmanaged", kind: routing.DomainKindGenerated, public: true},
		{name: "disabled", kind: routing.DomainKindGenerated, bound: true},
		{name: "verified-custom", kind: routing.DomainKindCustom, bound: true, public: true, verified: true, wantBind: true},
		{name: "unverified-custom", kind: routing.DomainKindCustom, bound: true, public: true},
		{name: "pending-tls", kind: routing.DomainKindGenerated, bound: true, public: true, tls: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := doRouterJSON[Service](t, handler, http.MethodPost, "/api/v1alpha/services",
				`{"project_id":"`+string(project.ID)+`","slug":"`+test.name+`","display_name":"`+test.name+`"}`,
				map[string]string{"Idempotency-Key": "operator-source-service-" + test.name}, http.StatusCreated)
			scope := routing.RouteScope{ProjectID: string(project.ID), EnvironmentID: string(environments[0].ID), ServiceID: string(service.ID)}
			if test.kind != "" {
				hostname := ""
				if test.kind == routing.DomainKindCustom {
					hostname = test.name + ".example.test"
				}
				domain, route, err := routing.BuildDomainRouteIntent(routing.DomainRouteIntentInput{
					Scope: scope, Kind: test.kind, AppsDomain: "apps.example.test", Hostname: hostname,
					PathPrefix: "/", TargetPort: 8080, Public: true, TLSDesired: false,
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := routes.PutDomain(ctx, domain); err != nil {
					t.Fatal(err)
				}
				if _, err := routes.PutHTTPRoute(ctx, route); err != nil {
					t.Fatal(err)
				}
				if test.kind == routing.DomainKindCustom {
					if err := routes.EnsureDomainLifecycle(ctx, domain); err != nil {
						t.Fatal(err)
					}
					if _, err := pool.Exec(ctx, `UPDATE domain_dns_challenges SET state='verified' WHERE domain_id=$1`, domain.ID); err != nil {
						t.Fatal(err)
					}
				}
				if test.bound {
					if err := routes.BindServiceDomain(ctx, domain.ID, route.ID, scope, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
				// Start with a valid binding, then simulate durable lifecycle
				// evidence becoming stale before a later source deployment.
				if !test.public {
					if _, err := pool.Exec(ctx, `UPDATE http_routes SET public_enabled=FALSE WHERE id=$1`, route.ID); err != nil {
						t.Fatal(err)
					}
				}
				if test.kind == routing.DomainKindCustom && !test.verified {
					if _, err := pool.Exec(ctx, `UPDATE domain_dns_challenges SET state='pending' WHERE domain_id=$1`, domain.ID); err != nil {
						t.Fatal(err)
					}
				}
				if test.tls {
					if _, err := pool.Exec(ctx, `UPDATE domains SET tls_desired=TRUE,tls_state='unavailable' WHERE id=$1`, domain.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			archive := buildSampleTarball(t)
			upload := func(key string) *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/up", bytes.NewReader(archive))
				request.Header.Set("Content-Type", "application/gzip")
				request.Header.Set("X-Midnight-Environment", scope.EnvironmentID)
				request.Header.Set("Idempotency-Key", key)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				return response
			}
			response := upload("operator-source-up-" + test.name)
			if response.Code != http.StatusAccepted {
				t.Fatalf("operator up = %d %s", response.Code, response.Body.String())
			}
			var accepted upOperationResponse
			if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
				t.Fatal(err)
			}
			var sourceType string
			if err := pool.QueryRow(ctx, `SELECT source_type FROM deployments WHERE id=$1`, accepted.DeploymentID).Scan(&sourceType); err != nil || sourceType != DeploySourceProviderLegacyLocal {
				t.Fatalf("source type=%q err=%v; want retained operator intake", sourceType, err)
			}
			public, err := (deploy.DurableRoutePromoter{Store: routes}).HasEligibleDeploymentPublicRouteBindings(ctx, accepted.DeploymentID, scope)
			if err != nil || public != test.wantBind {
				t.Fatalf("worker public binding=%t err=%v, want %t", public, err, test.wantBind)
			}
			var bindings int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM deployment_route_intents WHERE deployment_id=$1`, accepted.DeploymentID).Scan(&bindings); err != nil {
				t.Fatal(err)
			}
			wantBindings := 0
			if test.wantBind {
				wantBindings = 1
			}
			if bindings != wantBindings {
				t.Fatalf("deployment bindings=%d, want %d", bindings, wantBindings)
			}
			replay := upload("operator-source-up-" + test.name)
			if replay.Code != response.Code || replay.Body.String() != response.Body.String() || replay.Header().Get("Idempotent-Replayed") != "true" {
				t.Fatalf("replay=%d %s, want exact acceptance", replay.Code, replay.Body.String())
			}
			if test.wantBind {
				err := pool.WithTx(ctx, func(tx *pg.Tx) error {
					return requireServiceManagedRoutesPublishedForPromotionInTx(ctx, tx, accepted.DeploymentID, scope.ProjectID, scope.EnvironmentID, scope.ServiceID)
				})
				if !errors.Is(err, ErrPromotionConflict) {
					t.Fatalf("unpublished candidate promotion fence=%v, want conflict", err)
				}
				if _, err := pool.Exec(ctx, `
					CREATE FUNCTION reject_operator_route_binding() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN RAISE EXCEPTION 'injected binding failure'; END $$;
					CREATE TRIGGER reject_operator_route_binding BEFORE INSERT ON deployment_route_intents
					FOR EACH ROW EXECUTE FUNCTION reject_operator_route_binding()`); err != nil {
					t.Fatal(err)
				}
				failed := upload("operator-source-up-fault-" + test.name)
				if failed.Code != http.StatusInternalServerError {
					t.Fatalf("faulted intake=%d %s", failed.Code, failed.Body.String())
				}
				if _, err := pool.Exec(ctx, `DROP TRIGGER reject_operator_route_binding ON deployment_route_intents; DROP FUNCTION reject_operator_route_binding()`); err != nil {
					t.Fatal(err)
				}
				var deployments, outbox int
				if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM deployments WHERE service_id=$1),(SELECT count(*) FROM deploy_intake_outbox o JOIN deployments d ON d.id=o.deployment_id WHERE d.service_id=$1)`, scope.ServiceID).Scan(&deployments, &outbox); err != nil || deployments != 1 || outbox != 1 {
					t.Fatalf("fault rollback deployments=%d outbox=%d err=%v", deployments, outbox, err)
				}
			}
		})
	}
}
