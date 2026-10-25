//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/version"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// TestPostgresBootPersistsAcrossPoolRouterRebuild is the Phase 0 Alpha
// durable control-plane boot canary. It stays out of default CI via the
// integration build tag, but proves the Postgres-backed router can boot,
// apply migrations, serve the v1alpha compatibility surface, persist API
// resources, and recover that state through a fresh pool/router rebuild.
func TestPostgresBootPersistsAcrossPoolRouterRebuild(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool1 := newBootPool(t, ctx, dsn)
	// Production applies the schema before constructing stores that load
	// persisted settings. Exercise the same fresh-database boot order here.
	if _, err := (&pg.MigrationRunner{Pool: pool1, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatalf("initial migrations: %v", err)
	}
	router1 := newPostgresBootRouter(t, pool1)
	compat := doRouterJSON[version.Compatibility](t, router1, http.MethodGet, "/api/v1alpha/compat", "", nil, http.StatusOK)
	if compat.APIVersion != version.APIVersion || compat.SchemaVersion != version.SchemaVersion {
		t.Fatalf("compat = %#v, want api=%s schema=%d", compat, version.APIVersion, version.SchemaVersion)
	}
	up := doRouterJSON[migrationsActionResponse](t, router1, http.MethodPost, "/api/v1alpha/migrations", `{"action":"up"}`, nil, http.StatusOK)
	if up.Action != "up" || len(up.Files) == 0 {
		t.Fatalf("migration up response = %#v, want applied files", up)
	}
	for _, file := range up.Files {
		if !file.Applied || !strings.HasPrefix(file.UpSHA256, "sha256:") {
			t.Fatalf("migration file = %#v, want applied with real sha", file)
		}
	}
	list1 := doRouterJSON[migrationsListResponse](t, router1, http.MethodGet, "/api/v1alpha/migrations", "", nil, http.StatusOK)
	if list1.AppliedCount != list1.LatestVersion || list1.PendingCount != 0 || len(list1.Migrations) != list1.LatestVersion {
		t.Fatalf("migration list = %#v, want fully applied", list1)
	}

	project := doRouterJSON[Project](t, router1, http.MethodPost, "/api/v1alpha/projects", `{"workspace_id":"wrk_default","slug":"boot","display_name":"Boot Project"}`, map[string]string{"Idempotency-Key": "boot-create-project"}, http.StatusCreated)
	if !strings.HasPrefix(string(project.ID), "prj_") || project.Slug != "boot" || project.DisplayName != "Boot Project" {
		t.Fatalf("project = %#v, want boot project", project)
	}
	environments := doRouterJSON[[]Environment](t, router1, http.MethodGet, "/api/v1alpha/environments?project_id="+string(project.ID), "", nil, http.StatusOK)
	if len(environments) != 1 || environments[0].ProjectID != project.ID ||
		environments[0].Slug != "production" || environments[0].Tier != "production" {
		t.Fatalf("project environments = %#v, want one production environment", environments)
	}
	serviceBody := `{"project_id":"` + string(project.ID) + `","slug":"control-plane","display_name":"Control Plane API"}`
	service := doRouterJSON[Service](t, router1, http.MethodPost, "/api/v1alpha/services", serviceBody, map[string]string{"Idempotency-Key": "boot-create-service"}, http.StatusCreated)
	if !strings.HasPrefix(string(service.ID), "svc_") || service.ProjectID != project.ID || service.Slug != "control-plane" {
		t.Fatalf("service = %#v, want control-plane service for project %s", service, project.ID)
	}
	pool1.Close()

	pool2 := newBootPool(t, ctx, dsn)
	defer pool2.Close()
	router2 := newPostgresBootRouter(t, pool2)
	list2 := doRouterJSON[migrationsListResponse](t, router2, http.MethodGet, "/api/v1alpha/migrations", "", nil, http.StatusOK)
	if list2.AppliedCount != list2.LatestVersion || list2.PendingCount != 0 {
		t.Fatalf("migration list after rebuild = %#v, want fully applied", list2)
	}
	got := doRouterJSON[Project](t, router2, http.MethodGet, "/api/v1alpha/projects/"+string(project.ID), "", nil, http.StatusOK)
	if got.ID != project.ID || got.Slug != "boot" || got.DisplayName != "Boot Project" {
		t.Fatalf("project after rebuild = %#v, want %#v", got, project)
	}
	gotService := doRouterJSON[Service](t, router2, http.MethodGet, "/api/v1alpha/services/"+string(service.ID), "", nil, http.StatusOK)
	if gotService.ID != service.ID || gotService.ProjectID != project.ID || gotService.Slug != "control-plane" {
		t.Fatalf("service after rebuild = %#v, want %#v", gotService, service)
	}
	services := doRouterJSON[[]Service](t, router2, http.MethodGet, "/api/v1alpha/services?project_id="+string(project.ID), "", nil, http.StatusOK)
	if len(services) != 1 || services[0].ID != service.ID {
		t.Fatalf("services after rebuild = %#v, want one service %s", services, service.ID)
	}
	auditPage := doRouterJSON[orailtypes.CursorPageResponse[auditRowView]](t, router2, http.MethodGet, "/api/v1alpha/audit?resource_kind=project&resource_id="+string(project.ID), "", nil, http.StatusOK)
	if len(auditPage.Data) != 1 {
		t.Fatalf("audit rows = %d, want 1; page=%#v", len(auditPage.Data), auditPage)
	}
	if auditPage.Data[0].ResourceKind != "project" || auditPage.Data[0].ResourceID != string(project.ID) || auditPage.Data[0].Action != "project.create" {
		t.Fatalf("audit row = %#v, want project.create for project", auditPage.Data[0])
	}
	serviceAuditPage := doRouterJSON[orailtypes.CursorPageResponse[auditRowView]](t, router2, http.MethodGet, "/api/v1alpha/audit?resource_kind=service&resource_id="+string(service.ID), "", nil, http.StatusOK)
	if len(serviceAuditPage.Data) != 1 {
		t.Fatalf("service audit rows = %d, want 1; page=%#v", len(serviceAuditPage.Data), serviceAuditPage)
	}
	if serviceAuditPage.Data[0].ResourceKind != "service" || serviceAuditPage.Data[0].ResourceID != string(service.ID) || serviceAuditPage.Data[0].Action != "service.create" {
		t.Fatalf("service audit row = %#v, want service.create for service", serviceAuditPage.Data[0])
	}
}

func TestPostgresProjectBootstrapRollsBackWhenDefaultEnvironmentFails(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	runner := &pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	resources := NewPostgresResourceStore(pool)
	seed, err := resources.CreateProject(ctx, Project{
		ID:          "prj_atomic_seed",
		WorkspaceID: DefaultWorkspaceID,
		Slug:        "atomic-seed",
	})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := resources.CreateEnvironment(ctx, Environment{
		ID:        "env_atomic_collision",
		ProjectID: seed.ID,
		Slug:      "production",
		Name:      "Production",
		Tier:      "production",
	}); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	resources.IDFactory = func(prefix orailtypes.IDPrefix) orailtypes.ID {
		if prefix == orailtypes.IDPrefixProject {
			return "prj_atomic_rollback"
		}
		return "env_atomic_collision"
	}

	if _, _, err := resources.CreateProjectWithDefaultEnvironment(ctx, Project{
		WorkspaceID: DefaultWorkspaceID,
		Slug:        "must-roll-back",
	}); err == nil {
		t.Fatal("bootstrap unexpectedly succeeded with colliding environment ID")
	}
	if _, err := resources.GetProject(ctx, "prj_atomic_rollback"); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("rolled-back project lookup=%v, want ErrResourceNotFound", err)
	}
}

func newPostgresBootRouter(t *testing.T, pool *pg.Pool) http.Handler {
	t.Helper()
	keyProvider, err := secrets.NewMemoryKeyProvider([]byte("postgres-boot-variable-kek-00000"), 1)
	if err != nil {
		t.Fatalf("NewMemoryKeyProvider: %v", err)
	}
	stack, err := NewPostgresDataStack(PostgresDataStackConfig{
		Pool:                  pool,
		MigrationsFS:          migrations.FS,
		ManifestPath:          "manifest.yaml",
		CursorSecret:          []byte("0123456789abcdef0123456789abcdef"),
		AgentHMACSecret:       []byte("boot-agent-secret"),
		AutomationTokenMACKey: []byte("postgres-boot-automation-token-key"),
		InvitationMACKey:      []byte("postgres-boot-invitation-mac-key"),
		VariableIntentMACKey:  []byte("postgres-boot-variable-intent-key"),
		VariableKeyProvider:   keyProvider,
		Clock:                 func() time.Time { return time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewPostgresDataStack: %v", err)
	}
	stack.ProductionAuthRequired = false
	return NewRouterWithDataStack(stack)
}

func doRouterJSON[T any](t *testing.T, router http.Handler, method, path, body string, headers map[string]string, want int) T {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("%s %s status = %d body=%s, want %d", method, path, rec.Code, rec.Body.String(), want)
	}
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s %s: %v body=%s", method, path, err, rec.Body.String())
	}
	return out
}

func startPostgres(t *testing.T, ctx context.Context) (tc.Container, string) {
	t.Helper()
	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("midnight"),
		tcpostgres.WithUsername("midnight_test"),
		tcpostgres.WithPassword("midnight_test"),
		tc.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		if isDockerUnavailable(err) {
			t.Skipf("docker not available: %v", err)
		}
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	return container, dsn
}

func isDockerUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "docker") && (strings.Contains(msg, "not running") || strings.Contains(msg, "cannot connect") || strings.Contains(msg, "no such file") || strings.Contains(msg, "permission denied"))
}

func newBootPool(t *testing.T, ctx context.Context, dsn string) *pg.Pool {
	t.Helper()
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatalf("pg.Open: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	return pool
}
