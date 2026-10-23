//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestPostgresGraphQLDeployIntakeAtomicReplayScopeAndRollback(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	runner := &pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 8, 13, 18, 30, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, slug, display_name, created_at, updated_at)
		VALUES
			('wrk_graphql_a','graphql-a','GraphQL A',$1,$1),
			('wrk_graphql_b','graphql-b','GraphQL B',$1,$1)`, now); err != nil {
		t.Fatalf("seed GraphQL workspaces: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id, workspace_id, slug, display_name, created_at, updated_at)
		VALUES
			('prj_graphql_a','wrk_graphql_a','api','API',$1,$1),
			('prj_graphql_b','wrk_graphql_b','api','API',$1,$1)`, now); err != nil {
		t.Fatalf("seed GraphQL projects: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO services (id, project_id, slug, display_name, created_at, updated_at)
		VALUES
			('svc_graphql_a','prj_graphql_a','web','Web',$1,$1),
			('svc_graphql_b','prj_graphql_b','web','Web',$1,$1)`, now); err != nil {
		t.Fatalf("seed GraphQL services: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO environments (id, project_id, slug, name, tier, access_restricted, created_at, updated_at)
		VALUES
			('env_graphql_a','prj_graphql_a','production','Production','production',FALSE,$1,$1),
			('env_graphql_b','prj_graphql_b','production','Production','production',FALSE,$1,$1)`, now); err != nil {
		t.Fatalf("seed GraphQL environments: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	coordinator, ok := stack.GraphQLDeployIntake.(*PostgresTenantDeployIntakeCoordinator)
	if !ok {
		t.Fatalf("GraphQLDeployIntake=%T, want Postgres coordinator", stack.GraphQLDeployIntake)
	}
	tenantCoordinator, ok := stack.TenantDeployIntake.(*PostgresTenantDeployIntakeCoordinator)
	if !ok || coordinator != tenantCoordinator {
		t.Fatal("GraphQL and REST deploy intake do not share one Postgres authority")
	}
	var operationSequence, deploymentSequence, outboxSequence, eventSequence atomic.Int32
	coordinator.OperationIDFactory = func() orailtypes.ID {
		return orailtypes.ID(fmt.Sprintf("op_graphql_%02d", operationSequence.Add(1)))
	}
	coordinator.DeploymentIDFactory = func() string {
		return fmt.Sprintf("dep_graphql_%02d", deploymentSequence.Add(1))
	}
	coordinator.OutboxIDFactory = func() string {
		return fmt.Sprintf("dox_graphql_%02d", outboxSequence.Add(1))
	}
	coordinator.EventIDFactory = func() string {
		return fmt.Sprintf("dev_graphql_%02d", eventSequence.Add(1))
	}

	authenticated := idempotency.WithActor(ctx, idempotency.ActorRecoveryOperator)
	imageA := "registry.example.test/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	imageB := "registry.example.test/api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	fingerprint := func(body string) idempotency.RequestFingerprint {
		return idempotency.RequestFingerprint{Method: http.MethodPost, Path: GraphQLDeployFingerprintPath, BodySHA256: idempotency.BodyHashSHA256([]byte(body))}
	}
	encode := func(accepted GraphQLDeployAccepted) ([]byte, error) {
		return json.Marshal(map[string]interface{}{
			"id":          accepted.Deployment.ID,
			"operationId": accepted.Operation.ID,
			"serviceId":   accepted.Deployment.ServiceID,
			"status":      "QUEUED",
			"logs": []map[string]interface{}{{
				"message": accepted.InitialEvent.Message, "severity": accepted.InitialEvent.State,
			}},
		})
	}
	request := func(serviceID, environmentID, image, body string) GraphQLDeployRequest {
		return GraphQLDeployRequest{
			ServiceID: orailtypes.ID(serviceID), Intent: DeployIntakeRequest{EnvironmentID: environmentID, Image: image},
			Key: "graphql-atomic-key", Fingerprint: fingerprint(body), RequestID: "req_graphql_atomic",
			SourceIP: "192.0.2.70:443", UserAgent: "midnight-graphql-integration", EncodeResult: encode,
		}
	}

	firstRequest := request("svc_graphql_a", "env_graphql_a", imageA, `{"operationName":"Deploy","query":"mutation Deploy ...","variables":{"image":"a"}}`)
	first, err := coordinator.DeployGraphQL(authenticated, firstRequest)
	if err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	if first.Replayed || len(first.Body) == 0 {
		t.Fatalf("first result=%+v", first)
	}
	var firstBody map[string]interface{}
	if err := json.Unmarshal(first.Body, &firstBody); err != nil {
		t.Fatalf("decode committed GraphQL result: %v", err)
	}
	if firstBody["id"] != "dep_graphql_01" || firstBody["operationId"] != "op_graphql_01" {
		t.Fatalf("committed result=%s", first.Body)
	}

	encoderCalledOnReplay := false
	replayRequest := firstRequest
	replayRequest.EncodeResult = func(GraphQLDeployAccepted) ([]byte, error) {
		encoderCalledOnReplay = true
		return nil, errors.New("terminal replay must not re-encode")
	}
	replay, err := coordinator.DeployGraphQL(authenticated, replayRequest)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if !replay.Replayed || encoderCalledOnReplay || !bytes.Equal(replay.Body, first.Body) {
		t.Fatalf("replay=%+v encoder_called=%v first=%q", replay, encoderCalledOnReplay, first.Body)
	}

	conflictRequest := firstRequest
	conflictRequest.Intent.Image = imageB
	conflictRequest.Fingerprint = fingerprint(`{"operationName":"Deploy","query":"mutation Deploy ...","variables":{"image":"b"}}`)
	if _, err := coordinator.DeployGraphQL(authenticated, conflictRequest); !errors.Is(err, idempotency.ErrKeyReusedDifferentBody) {
		t.Fatalf("different-body replay error=%v, want idempotency conflict", err)
	}

	// The same authenticated recovery actor and key may operate independently
	// in another server-resolved workspace. The client cannot choose this
	// tenant namespace; it comes from service -> project -> workspace lineage.
	crossTenantRequest := request("svc_graphql_b", "env_graphql_b", imageB, `{"operationName":"Deploy","query":"mutation Deploy ...","variables":{"image":"b"}}`)
	crossTenant, err := coordinator.DeployGraphQL(authenticated, crossTenantRequest)
	if err != nil {
		t.Fatalf("cross-tenant deploy: %v", err)
	}
	if crossTenant.Replayed {
		t.Fatalf("cross-tenant key incorrectly replayed first tenant: %s", crossTenant.Body)
	}

	invalidActor := idempotency.WithActor(ctx, idempotency.ActorLocalDeveloper)
	invalidRequest := request("svc_graphql_a", "env_graphql_a", imageA, `{"actor":"local"}`)
	invalidRequest.Key = "graphql-invalid-actor"
	if _, err := coordinator.DeployGraphQL(invalidActor, invalidRequest); err == nil {
		t.Fatal("local developer actor crossed durable GraphQL authority boundary")
	}

	assertCount := func(query string, want int, args ...interface{}) {
		t.Helper()
		var got int
		if err := pool.QueryRow(ctx, query, args...).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("count query %q = %d, want %d", query, got, want)
		}
	}
	assertCommittedCounts := func(want int) {
		t.Helper()
		assertCount(`SELECT COUNT(*) FROM operations WHERE actor = 'operator:recovery'`, want)
		assertCount(`SELECT COUNT(*) FROM deployments WHERE operation_id LIKE 'op_graphql_%'`, want)
		assertCount(`SELECT COUNT(*) FROM deploy_intake_outbox WHERE operation_id LIKE 'op_graphql_%'`, want)
		assertCount(`SELECT COUNT(*) FROM idempotency_keys WHERE key = 'graphql-atomic-key'`, want)
		assertCount(`SELECT COUNT(*) FROM audit_logs WHERE action = 'graphql.deploy.intake.accept'`, want)
		assertCount(`SELECT COUNT(*) FROM jobs`, 0)
	}
	assertCommittedCounts(2)

	fault := errors.New("injected GraphQL crash before commit")
	coordinator.setGraphQLBeforeCommitForTest(func() error { return fault })
	faultRequest := request("svc_graphql_a", "env_graphql_a", imageB, `{"operationName":"DeployFault"}`)
	faultRequest.Key = "graphql-fault-key"
	if _, err := coordinator.DeployGraphQL(authenticated, faultRequest); !errors.Is(err, fault) {
		t.Fatalf("fault result=%v, want injected error", err)
	}
	coordinator.setGraphQLBeforeCommitForTest(nil)
	assertCommittedCounts(2)
	assertCount(`SELECT COUNT(*) FROM idempotency_keys WHERE key = 'graphql-fault-key'`, 0)
	assertCount(`SELECT COUNT(*) FROM operations WHERE id = 'op_graphql_05'`, 0)
	assertCount(`SELECT COUNT(*) FROM deployments WHERE id = 'dep_graphql_05'`, 0)
}
