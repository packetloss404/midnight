//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	logrecords "github.com/midnight/midnight/internal/logs"
	"github.com/midnight/midnight/internal/observability"
	runtimedocker "github.com/midnight/midnight/internal/runtime/docker"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
	dockerclient "github.com/moby/moby/client"
	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type projectScopedDockerTelemetryClient struct {
	observability.DockerTelemetryClient
	projectID string
}

func (c projectScopedDockerTelemetryClient) ContainerList(ctx context.Context, options dockerclient.ContainerListOptions) (dockerclient.ContainerListResult, error) {
	result, err := c.DockerTelemetryClient.ContainerList(ctx, options)
	if err != nil {
		return dockerclient.ContainerListResult{}, err
	}
	items := result.Items[:0]
	for _, item := range result.Items {
		if item.Labels[runtimedocker.LabelProject] == c.projectID {
			items = append(items, item)
		}
	}
	result.Items = items
	return result, nil
}

func TestPostgresObservabilitySurvivesRestartAndSSEReconnect(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	pool := newBootPool(t, ctx, dsn)
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}

	resources := NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "observability"})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := resources.CreateEnvironment(ctx, Environment{
		ProjectID: project.ID, Slug: "production", Tier: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationID := defaultOperationID()
	if _, err := NewPostgresOperationStore(pool).Create(ctx, orailtypes.Operation{
		ID: operationID, Kind: "deploy", State: orailtypes.OperationRunning,
	}); err != nil {
		t.Fatal(err)
	}
	deploymentID := mintID(orailtypes.IDPrefixDeployment)
	if _, err := NewPostgresDeploymentStore(pool).CreateDeployment(ctx, DeploymentRecord{
		ID: deploymentID, ProjectID: string(project.ID), ServiceID: string(service.ID),
		EnvironmentID: string(environment.ID), Action: deploy.ActionDeployImage,
		OperationID: string(operationID),
	}); err != nil {
		t.Fatal(err)
	}
	scope := observability.Scope{
		ProjectID: string(project.ID), EnvironmentID: string(environment.ID),
		ServiceID: string(service.ID), DeploymentID: deploymentID,
		OperationID: string(operationID), ReplicaID: "replica-1",
	}
	now := time.Now().UTC().Add(-time.Second)
	store := observability.NewPostgresStore(pool)
	store.IngestLimit = 2
	for index, message := range []string{
		"server started token=midn_ABCDEF123456",
		"request from 192.0.2.20",
		"this minute is over the limit",
	} {
		result, err := store.IngestLog(ctx, observability.LogRecord{
			ObservedAt: now.Add(time.Duration(index) * time.Millisecond),
			Scope:      scope, Surface: "runtime", Stream: "stdout", Level: "info",
			Component: "app.runtime", Message: message,
			Attributes: map[string]any{
				"password": "do-not-store",
				"nested":   map[string]any{"api_key": "nested-do-not-store"},
				"bounded":  true,
			},
			SourceKey: "integration-runtime", SourceSequence: string(rune('a' + index)),
		})
		if err != nil {
			t.Fatal(err)
		}
		if index < 2 && (!result.Stored || result.Dropped) {
			t.Fatalf("accepted ingest %d = %#v", index, result)
		}
		if index == 2 && !result.Dropped {
			t.Fatalf("rate-limited ingest = %#v, want dropped", result)
		}
	}
	page, err := store.QueryLogs(ctx, observability.LogQuery{Scope: scope, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Truncated || len(page.Records) != 1 || page.DropCounts != 1 {
		t.Fatalf("first page = %#v", page)
	}
	encoded, _ := json.Marshal(page.Records[0])
	if strings.Contains(string(encoded), "midn_ABCDEF123456") ||
		strings.Contains(string(encoded), "do-not-store") ||
		strings.Contains(string(encoded), "nested-do-not-store") {
		t.Fatalf("persisted record contains a secret: %s", encoded)
	}
	if !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatalf("persisted record does not carry visible redaction: %s", encoded)
	}
	firstID := page.NextID

	if err := store.IngestMetric(ctx, observability.MetricSample{
		ObservedAt: now, Scope: scope, CPUPercent: 10, MemoryBytes: 100,
		NetworkRXBytes: 10, BlockReadBytes: 5,
	}); err != nil {
		t.Fatal(err)
	}
	replicaTwo := scope
	replicaTwo.ReplicaID = "replica-2"
	if err := store.IngestMetric(ctx, observability.MetricSample{
		ObservedAt: now, Scope: replicaTwo, CPUPercent: 20, MemoryBytes: 200,
		NetworkRXBytes: 20, BlockReadBytes: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDeploymentMarker(ctx, scope, "deployed", now); err != nil {
		t.Fatal(err)
	}
	if err := store.HeartbeatCollector(ctx, "worker-1", "docker", nil); err != nil {
		t.Fatal(err)
	}
	oldAt := now.Add(-31 * 24 * time.Hour)
	if _, err := store.IngestLog(ctx, observability.LogRecord{
		ObservedAt: oldAt, Scope: scope, Surface: "runtime", Stream: "stdout", Level: "info",
		Component: "app.runtime", Message: "expired record",
		SourceKey: "integration-expired", SourceSequence: "old",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.IngestMetric(ctx, observability.MetricSample{
		ObservedAt: oldAt, Scope: scope, CPUPercent: 1, MemoryBytes: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCollectorCheckpoint(ctx, "expired-source", oldAt); err != nil {
		t.Fatal(err)
	}
	store.Now = func() time.Time { return oldAt }
	if err := store.HeartbeatCollector(ctx, "expired-worker", "docker", nil); err != nil {
		t.Fatal(err)
	}
	store.Now = func() time.Time { return time.Now().UTC() }
	cleanup, err := store.Cleanup(ctx, now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Logs != 1 || cleanup.Metrics != 1 || cleanup.Windows != 1 ||
		cleanup.Checkpoints != 1 || cleanup.Collectors != 1 {
		t.Fatalf("retention cleanup = %#v", cleanup)
	}

	// Simulate API process restart by discarding the pool/store and opening a
	// fresh connection against the same database.
	pool.Close()
	restartedPool := newBootPool(t, ctx, dsn)
	defer restartedPool.Close()
	restarted := observability.NewPostgresStore(restartedPool)
	replayed, err := restarted.QueryLogs(ctx, observability.LogQuery{Scope: scope, AfterID: firstID, Limit: 10})
	if err != nil || len(replayed.Records) != 1 {
		t.Fatalf("durable replay after restart = %#v, %v", replayed, err)
	}
	series, err := restarted.QueryMetrics(ctx, observability.MetricQuery{
		Scope: scopeWithoutReplica(scope), Since: now.Add(-time.Minute),
		Until: now.Add(time.Minute), Mode: "sum", Step: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Points) != 1 || series.Points[0].CPUPercent != 30 ||
		series.Points[0].MemoryBytes != 300 || len(series.Markers) != 1 ||
		len(series.Collectors) != 1 || len(series.CollectorHealth) != 1 ||
		series.CollectorHealth[0].HeartbeatGap {
		t.Fatalf("summed durable metrics = %#v", series)
	}
	health, err := restarted.CollectorHealth(ctx, []string{
		observability.CollectorKindDocker,
		observability.CollectorKindTraefik,
	}, observability.DefaultHeartbeatAge)
	if err != nil {
		t.Fatal(err)
	}
	if len(health) != 2 || health[0].Kind != observability.CollectorKindDocker ||
		health[0].HeartbeatGap || health[1].Kind != observability.CollectorKindTraefik ||
		!health[1].HeartbeatGap || health[1].Status != "unavailable" {
		t.Fatalf("collector health = %#v", health)
	}
	restarted.Now = func() time.Time { return time.Now().UTC().Add(5 * time.Minute) }
	health, err = restarted.CollectorHealth(ctx, []string{observability.CollectorKindDocker}, observability.DefaultHeartbeatAge)
	if err != nil {
		t.Fatal(err)
	}
	if len(health) != 1 || !health[0].HeartbeatGap || health[0].Status != "stale" {
		t.Fatalf("stale collector health = %#v", health)
	}
	restarted.Now = func() time.Time { return time.Now().UTC() }

	const bearer = "observability-integration-token"
	stack, err := NewPostgresDataStack(PostgresDataStackConfig{
		Pool: restartedPool, MigrationsFS: migrations.FS, ManifestPath: "manifest.yaml",
		CursorSecret:          []byte("0123456789abcdef0123456789abcdef"),
		AutomationTokenMACKey: []byte("observability-automation-token-key"),
		InvitationMACKey:      []byte("observability-invitation-mac-key"),
		VariableIntentMACKey:  []byte("observability-variable-intent-key"),
		VariableKeyProvider:   testVariableKeyProvider(t),
		Clock:                 func() time.Time { return now.Add(time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	stack.ObservabilityStore = restarted
	stack.LogStore = restarted
	stack.APIBearerToken = bearer
	stack.ProductionAuthRequired = true
	router := NewRouterWithDataStack(stack)
	server := httptest.NewServer(router)
	defer server.Close()
	query := url.Values{
		"project_id": []string{scope.ProjectID}, "environment_id": []string{scope.EnvironmentID},
		"service_id": []string{scope.ServiceID}, "deployment_id": []string{scope.DeploymentID},
		"operation_id": []string{scope.OperationID}, "replica_id": []string{scope.ReplicaID},
		"since": []string{now.Add(-time.Minute).Format(time.RFC3339Nano)}, "limit": []string{"1"},
	}
	unauthorized, err := http.Get(server.URL + "/api/v1alpha/observability/logs?" + query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.StatusCode)
	}
	operationLogsRequest, err := http.NewRequestWithContext(
		ctx, http.MethodGet,
		server.URL+"/api/v1alpha/operations/"+string(operationID)+"/logs?limit=20",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	operationLogsRequest.Header.Set("Authorization", "Bearer "+bearer)
	operationLogsHTTPResponse, err := http.DefaultClient.Do(operationLogsRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer operationLogsHTTPResponse.Body.Close()
	if operationLogsHTTPResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(operationLogsHTTPResponse.Body)
		t.Fatalf("operation logs status=%d body=%s", operationLogsHTTPResponse.StatusCode, body)
	}
	var operationLogs operationLogsResponse
	if err := json.NewDecoder(operationLogsHTTPResponse.Body).Decode(&operationLogs); err != nil {
		t.Fatal(err)
	}
	var runtimeRecords int
	for _, surface := range operationLogs.AppLogs {
		if surface.Surface == "runtime" {
			runtimeRecords = len(surface.Entries)
		}
	}
	if runtimeRecords != 2 || len(operationLogs.CollectorHealth) != 2 ||
		operationLogs.CollectorHealth[0].HeartbeatGap ||
		!operationLogs.CollectorHealth[1].HeartbeatGap {
		t.Fatalf("operation durable log surfaces=%#v health=%#v", operationLogs.AppLogs, operationLogs.CollectorHealth)
	}
	firstCursor := readOneObservabilitySSE(t, server.URL, bearer, query, "")
	if strings.Contains(firstCursor, "postgres") || strings.Contains(firstCursor, "offset") {
		t.Fatalf("SSE cursor leaked storage details: %q", firstCursor)
	}

	// A record committed while the first client is disconnected is replayed
	// after Last-Event-ID. The first replay is the already-durable second
	// record, demonstrating there is no process-local-only tail gap.
	restarted.IngestLimit = observability.DefaultIngestPerMin
	if _, err := restarted.IngestLog(ctx, observability.LogRecord{
		// Deliberately older than the initial 15-minute replay window while
		// carrying a newer durable id. Last-Event-ID must be the reconnect
		// boundary or a long disconnect would silently lose this row.
		ObservedAt: now.Add(-30 * time.Minute), Scope: scope, Surface: "runtime",
		Stream: "stderr", Level: "error", Component: "app.runtime",
		Message: "after disconnect", SourceKey: "integration-runtime-2",
		SourceSequence: "after-disconnect",
	}); err != nil {
		t.Fatal(err)
	}
	secondCursor := readOneObservabilitySSE(t, server.URL, bearer, query, firstCursor)
	if secondCursor == "" || secondCursor == firstCursor {
		t.Fatalf("reconnect cursor = %q, first = %q", secondCursor, firstCursor)
	}
}

func TestDockerObservabilityCollectorCapturesRealLogsAndStats(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	postgresContainer, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = postgresContainer.Terminate(ctx) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	resources := NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "docker-observability"})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := resources.CreateEnvironment(ctx, Environment{
		ProjectID: project.ID, Slug: "production", Tier: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	operationID := defaultOperationID()
	if _, err := NewPostgresOperationStore(pool).Create(ctx, orailtypes.Operation{
		ID: operationID, Kind: "deploy", State: orailtypes.OperationRunning,
	}); err != nil {
		t.Fatal(err)
	}
	deploymentID := mintID(orailtypes.IDPrefixDeployment)
	if _, err := NewPostgresDeploymentStore(pool).CreateDeployment(ctx, DeploymentRecord{
		ID: deploymentID, ProjectID: string(project.ID), ServiceID: string(service.ID),
		EnvironmentID: string(environment.ID), Action: deploy.ActionDeployImage,
		OperationID: string(operationID),
	}); err != nil {
		t.Fatal(err)
	}
	const replicaID = "replica-docker-real"
	appContainer, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{
		ContainerRequest: tc.ContainerRequest{
			Image: "alpine:3.20",
			Cmd:   []string{"sh", "-c", "echo 'ready midn_DOCKERSECRET123'; echo 'runtime error' >&2; sleep 60"},
			Labels: map[string]string{
				runtimedocker.LabelManagedBy:   runtimedocker.LabelManagedByV,
				runtimedocker.LabelProject:     string(project.ID),
				runtimedocker.LabelEnvironment: string(environment.ID),
				runtimedocker.LabelService:     string(service.ID),
				runtimedocker.LabelDeployment:  deploymentID,
				runtimedocker.LabelInstance:    replicaID,
			},
			WaitingFor: wait.ForLog("ready"),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("real Docker container unavailable: %v", err)
	}
	t.Cleanup(func() { _ = appContainer.Terminate(context.Background()) })

	store := observability.NewPostgresStore(pool)
	collector, err := observability.NewDockerCollector("", store, "integration-docker")
	if err != nil {
		t.Skipf("real Docker client unavailable: %v", err)
	}
	collector.Client = projectScopedDockerTelemetryClient{
		DockerTelemetryClient: collector.Client,
		projectID:             string(project.ID),
	}
	if err := collector.CollectOnce(ctx); err != nil {
		_ = collector.Close()
		t.Fatal(err)
	}
	_ = collector.Close()
	scope := observability.Scope{
		ProjectID: string(project.ID), EnvironmentID: string(environment.ID),
		ServiceID: string(service.ID), DeploymentID: deploymentID, ReplicaID: replicaID,
	}
	logPage, err := store.QueryLogs(ctx, observability.LogQuery{Scope: scope, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(logPage.Records) < 2 {
		t.Fatalf("real Docker logs = %#v, want stdout and stderr", logPage.Records)
	}
	var stdoutSeen, stderrSeen, secretSeen bool
	for _, record := range logPage.Records {
		stdoutSeen = stdoutSeen || record.Stream == "stdout"
		stderrSeen = stderrSeen || record.Stream == "stderr"
		secretSeen = secretSeen || strings.Contains(record.Message, "midn_DOCKERSECRET123")
	}
	if !stdoutSeen || !stderrSeen || secretSeen {
		t.Fatalf("Docker streams stdout=%v stderr=%v raw-secret=%v records=%#v", stdoutSeen, stderrSeen, secretSeen, logPage.Records)
	}
	legacyPage, err := store.Query(logrecords.Filter{
		Deployments: []string{deploymentID},
		Components:  []string{"app.runtime"},
	}, logrecords.Cursor{}, 20)
	if err != nil || len(legacyPage.Entries) < 2 {
		t.Fatalf("deployment-scoped operation log adapter omitted Docker runtime logs: %#v, %v", legacyPage, err)
	}
	metrics, err := store.QueryMetrics(ctx, observability.MetricQuery{
		Scope: scope, Since: time.Now().Add(-time.Minute), Until: time.Now().Add(time.Minute),
		Mode: "replica", Step: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics.Points) == 0 || metrics.Points[0].MemoryBytes <= 0 {
		t.Fatalf("real Docker metrics = %#v", metrics)
	}

	// A replacement collector resumes from the durable per-container
	// checkpoint. Inclusive Docker timestamps are harmless because the
	// source-sequence uniqueness key rejects replayed lines.
	restarted, err := observability.NewDockerCollector("", store, "integration-docker-restart")
	if err != nil {
		t.Fatal(err)
	}
	restarted.Client = projectScopedDockerTelemetryClient{
		DockerTelemetryClient: restarted.Client,
		projectID:             string(project.ID),
	}
	defer restarted.Close()
	if err := restarted.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.QueryLogs(ctx, observability.LogQuery{Scope: scope, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Records) != len(logPage.Records) {
		t.Fatalf("restart duplicated Docker logs: before=%d after=%d", len(logPage.Records), len(replayed.Records))
	}
}

func TestTraefikFileCollectorResolvesDurableRouteAndSurvivesRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	postgresContainer, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = postgresContainer.Terminate(ctx) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	resources := NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "traefik-observability"})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := resources.CreateEnvironment(ctx, Environment{
		ProjectID: project.ID, Slug: "production", Tier: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "http"})
	if err != nil {
		t.Fatal(err)
	}
	operationID := defaultOperationID()
	if _, err := NewPostgresOperationStore(pool).Create(ctx, orailtypes.Operation{
		ID: operationID, Kind: "deploy", State: orailtypes.OperationRunning,
	}); err != nil {
		t.Fatal(err)
	}
	deploymentID := mintID(orailtypes.IDPrefixDeployment)
	if _, err := NewPostgresDeploymentStore(pool).CreateDeployment(ctx, DeploymentRecord{
		ID: deploymentID, ProjectID: string(project.ID), ServiceID: string(service.ID),
		EnvironmentID: string(environment.ID), Action: deploy.ActionDeployImage,
		OperationID: string(operationID),
	}); err != nil {
		t.Fatal(err)
	}
	const (
		domainID  = "dom_observability_test"
		routeID   = "route_observability_test"
		backendID = "backend_observability_test"
		replicaID = "replica-traefik-real"
	)
	if _, err := pool.Exec(ctx, `
INSERT INTO domains (
    id, project_id, environment_id, service_id, kind, hostname,
    normalized_hostname, tls_desired, tls_state, desired_revision
) VALUES ($1,$2,$3,$4,'custom','app.example.test','app.example.test',FALSE,'disabled',1)`,
		domainID, string(project.ID), string(environment.ID), string(service.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO http_routes (
    id, domain_id, project_id, environment_id, service_id, name,
    path_prefix, target_port, public_enabled, desired_revision
) VALUES ($1,$2,$3,$4,$5,'api','/api',8080,TRUE,1)`,
		routeID, domainID, string(project.ID), string(environment.ID), string(service.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO route_backends (
    id, route_id, project_id, environment_id, service_id, deployment_id,
    instance_id, service_url, ready, desired_revision
) VALUES ($1,$2,$3,$4,$5,$6,$7,'http://127.0.0.1:8080',TRUE,1)`,
		backendID, routeID, string(project.ID), string(environment.ID),
		string(service.ID), deploymentID, replicaID); err != nil {
		t.Fatal(err)
	}

	networkName := "midnight-observability-" + deploymentID
	testNetwork, err := tc.GenericNetwork(ctx, tc.GenericNetworkRequest{
		NetworkRequest: tc.NetworkRequest{Name: networkName, CheckDuplicate: true},
	})
	if err != nil {
		t.Skipf("real Traefik network unavailable: %v", err)
	}
	t.Cleanup(func() { _ = testNetwork.Remove(context.Background()) })
	backendContainer, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{
		ContainerRequest: tc.ContainerRequest{
			Image:    "nginx:1.27-alpine",
			Networks: []string{networkName},
			NetworkAliases: map[string][]string{
				networkName: {"observability-backend"},
			},
			WaitingFor: wait.ForListeningPort("80/tcp"),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("real Traefik backend unavailable: %v", err)
	}
	t.Cleanup(func() { _ = backendContainer.Terminate(context.Background()) })
	dynamicConfig := `
http:
  routers:
    observability:
      rule: "Host(` + "`app.example.test`" + `) && PathPrefix(` + "`/api`" + `)"
      entryPoints: [web]
      service: observability
  services:
    observability:
      loadBalancer:
        servers:
          - url: "http://observability-backend:80"
`
	traefikContainer, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{
		ContainerRequest: tc.ContainerRequest{
			Image:        "traefik:v3.1.5",
			ExposedPorts: []string{"8080/tcp"},
			Networks:     []string{networkName},
			Files: []tc.ContainerFile{{
				Reader: strings.NewReader(dynamicConfig), ContainerFilePath: "/dynamic/routes.yml", FileMode: 0o600,
			}},
			Cmd: []string{
				"--entryPoints.web.address=:8080",
				"--providers.file.directory=/dynamic",
				"--providers.file.watch=true",
				"--accesslog=true",
				"--accesslog.format=json",
				"--accesslog.filepath=/tmp/access.jsonl",
				"--accesslog.bufferingsize=0",
			},
			WaitingFor: wait.ForListeningPort("8080/tcp"),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("real Traefik container unavailable: %v", err)
	}
	t.Cleanup(func() { _ = traefikContainer.Terminate(context.Background()) })
	host, err := traefikContainer.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := traefikContainer.MappedPort(ctx, "8080/tcp")
	if err != nil {
		t.Fatal(err)
	}
	var response *http.Response
	requestDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(requestDeadline) {
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("http://%s:%s/api/health", host, port.Port()), nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Host = "app.example.test"
		response, err = http.DefaultClient.Do(request)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("request through real Traefik: %v", err)
	}
	_ = response.Body.Close()

	var accessJSON []byte
	copyDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(copyDeadline) {
		// Keep exercising the real entry point while waiting. Under a loaded
		// Docker daemon the listening socket can become ready just before the
		// access-log writer/provider goroutines finish starting.
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("http://%s:%s/api/health", host, port.Port()), nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Host = "app.example.test"
		if retried, retryErr := http.DefaultClient.Do(request); retryErr == nil {
			_ = retried.Body.Close()
			response = retried
		}
		reader, copyErr := traefikContainer.CopyFileFromContainer(ctx, "/tmp/access.jsonl")
		if copyErr == nil {
			accessJSON, copyErr = io.ReadAll(reader)
			_ = reader.Close()
		}
		if copyErr == nil && len(accessJSON) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(accessJSON) == 0 {
		t.Fatal("real Traefik did not produce a JSON access log")
	}
	path := filepath.Join(t.TempDir(), "access.jsonl")
	if err := os.WriteFile(path, accessJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	store := observability.NewPostgresStore(pool)
	collector := &observability.TraefikFileCollector{
		Path: path, ID: "integration-traefik", Store: store,
		Resolver: observability.PostgresTraefikScopeResolver{Pool: pool},
	}
	if err := collector.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	scope := observability.Scope{
		ProjectID: string(project.ID), EnvironmentID: string(environment.ID),
		ServiceID: string(service.ID), DeploymentID: deploymentID,
		OperationID: string(operationID), ReplicaID: replicaID,
	}
	page, err := store.QueryLogs(ctx, observability.LogQuery{Scope: scope, Surfaces: []string{"http"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	wantMessage := fmt.Sprintf("GET /api/health %d", response.StatusCode)
	if len(page.Records) == 0 {
		t.Fatalf("Traefik durable route record = %#v", page.Records)
	}
	for _, record := range page.Records {
		if record.Message != wantMessage {
			t.Fatalf("Traefik durable route record = %#v, want message %q", page.Records, wantMessage)
		}
	}
	beforeRestart := len(page.Records)

	restarted := &observability.TraefikFileCollector{
		Path: path, ID: "integration-traefik", Store: observability.NewPostgresStore(pool),
		Resolver: observability.PostgresTraefikScopeResolver{Pool: pool},
	}
	if err := restarted.CollectOnce(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.QueryLogs(ctx, observability.LogQuery{Scope: scope, Surfaces: []string{"http"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Records) != beforeRestart {
		t.Fatalf("Traefik restart duplicated access logs: %#v", replayed.Records)
	}
}

func scopeWithoutReplica(scope observability.Scope) observability.Scope {
	scope.ReplicaID = ""
	return scope
}

func readOneObservabilitySSE(t *testing.T, baseURL, bearer string, query url.Values, lastEventID string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/api/v1alpha/observability/logs/stream?"+query.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("SSE status = %d body=%s", response.StatusCode, body)
	}
	scanner := bufio.NewScanner(response.Body)
	var id, event string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "id:"):
			id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case line == "" && event == "log" && id != "":
			return id
		}
	}
	t.Fatalf("SSE ended without a log frame: %v", scanner.Err())
	return ""
}
