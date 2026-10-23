//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestTenantSourceUpAtomicAuthorizationReplayAndRollback(t *testing.T) {
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

	now := time.Date(2026, 7, 29, 3, 0, 0, 0, time.UTC)
	password := "tenant source up correct horse battery staple"
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, display_name, password_hash, session_generation,
			created_at, updated_at
		) VALUES
			('usr_up_member','member-up@example.com','Member',$1,1,$2,$2),
			('usr_up_viewer','viewer-up@example.com','Viewer',$1,1,$2,$2),
			('usr_up_admin','admin-up@example.com','Admin',$1,1,$2,$2),
			('usr_up_race','race-up@example.com','Race',$1,1,$2,$2)`,
		passwordHash,
		now,
	); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, slug, display_name, created_at, updated_at)
		VALUES
			('wrk_up_a','up-a','Up A',$1,$1),
			('wrk_up_b','up-b','Up B',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspaces: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id, workspace_id, slug, display_name, created_at, updated_at)
		VALUES
			('prj_up_a','wrk_up_a','api','API',$1,$1),
			('prj_up_b','wrk_up_b','foreign','Foreign',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO services (id, project_id, slug, display_name, created_at, updated_at)
		VALUES
			('svc_up_a','prj_up_a','web','Web',$1,$1),
			('svc_up_b','prj_up_a','worker','Worker',$1,$1),
			('svc_up_foreign','prj_up_b','foreign','Foreign',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed services: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO environments (
			id, project_id, slug, name, tier, access_restricted, created_at, updated_at
		) VALUES
			('env_up_a','prj_up_a','production','Production','production',FALSE,$1,$1),
			('env_up_restricted','prj_up_a','restricted','Restricted','production',TRUE,$1,$1),
			('env_up_foreign','prj_up_b','production','Production','production',FALSE,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed environments: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id, user_id, role, invited_at, accepted_at, created_at, updated_at
		) VALUES
			('wrk_up_a','usr_up_member','member',$1,$1,$1,$1),
			('wrk_up_a','usr_up_viewer','viewer',$1,$1,$1,$1),
			('wrk_up_a','usr_up_admin','admin',$1,$1,$1,$1),
			('wrk_up_a','usr_up_race','member',$1,$1,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed members: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	coordinator, ok := stack.TenantDeployIntake.(*PostgresTenantDeployIntakeCoordinator)
	if !ok {
		t.Fatalf("tenant coordinator=%T", stack.TenantDeployIntake)
	}
	artifactStore, err := sourceartifact.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stack.TenantUpIntake = coordinator
	stack.SourceArtifactStore = artifactStore
	coordinator.SourceArtifactFS = artifactStore
	coordinator.SourceArtifactQuota = sourceartifact.NewQuotaStore(
		pool,
		sourceartifact.DefaultQuotaConfig,
	)
	coordinator.SourceArtifactQuota.Now = func() time.Time { return now }
	var operationSeq, deploymentSeq, outboxSeq, eventSeq atomic.Int32
	coordinator.OperationIDFactory = func() orailtypes.ID {
		return orailtypes.ID(fmt.Sprintf("op_tenant_up_%03d", operationSeq.Add(1)))
	}
	coordinator.DeploymentIDFactory = func() string {
		return fmt.Sprintf("dep_tenant_up_%03d", deploymentSeq.Add(1))
	}
	coordinator.OutboxIDFactory = func() string {
		return fmt.Sprintf("dox_tenant_up_%03d", outboxSeq.Add(1))
	}
	coordinator.EventIDFactory = func() string {
		return fmt.Sprintf("dev_tenant_up_%03d", eventSeq.Add(1))
	}

	type human struct {
		token string
	}
	login := func(email string) human {
		t.Helper()
		result, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
			Email: email, Password: password, ClientKind: "api", RemoteAddr: "192.0.2.95:443",
		})
		if err != nil {
			t.Fatalf("login %s: %v", email, err)
		}
		return human{token: result.AccessToken}
	}
	memberFirst := login("member-up@example.com")
	memberSecond := login("member-up@example.com")
	viewer := login("viewer-up@example.com")
	admin := login("admin-up@example.com")
	race := login("race-up@example.com")
	handler := NewRouterWithDataStack(stack)
	serveWithHandler := func(
		target http.Handler,
		human human,
		serviceID string,
		environmentID string,
		key string,
		message string,
		body []byte,
	) *httptest.ResponseRecorder {
		request := httptest.NewRequest(
			http.MethodPost,
			tenantUpIdempotencyPath(orailtypes.ID(serviceID)),
			bytes.NewReader(body),
		)
		request.Header = tenantUpHeaders(key, environmentID, message)
		setTenantUpArchiveContract(request.Header, body)
		request.Header.Set("Authorization", "Bearer "+human.token)
		response := httptest.NewRecorder()
		target.ServeHTTP(response, request)
		return response
	}
	serve := func(
		human human,
		serviceID string,
		environmentID string,
		key string,
		message string,
		body []byte,
	) *httptest.ResponseRecorder {
		return serveWithHandler(
			handler,
			human,
			serviceID,
			environmentID,
			key,
			message,
			body,
		)
	}

	archive := buildTenantUpIntegrationTarball(t, "primary")
	first := serve(memberFirst, "svc_up_a", "env_up_a", "up-shared", "release candidate", archive)
	replay := serve(memberSecond, "svc_up_a", "env_up_a", "up-shared", "release candidate", archive)
	if first.Code != http.StatusAccepted ||
		replay.Code != http.StatusAccepted ||
		first.Body.String() != replay.Body.String() ||
		first.Header().Get("Idempotent-Replayed") != "" ||
		replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("first=%d/%q/%s replay=%d/%q/%s",
			first.Code,
			first.Header().Get("Idempotent-Replayed"),
			first.Body.String(),
			replay.Code,
			replay.Header().Get("Idempotent-Replayed"),
			replay.Body.String(),
		)
	}
	var accepted upOperationResponse
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Operation.Kind != "build" ||
		accepted.OperationID == "" ||
		accepted.DeploymentID == "" {
		t.Fatalf("accepted=%#v", accepted)
	}

	var operationCount, deploymentCount, operationEventCount, deploymentEventCount int
	var outboxCount, artifactCount, referenceCount, auditCount, idempotencyCount int
	var workspaceChargeCount, uploadReservationCount int
	var sourceType, sourceRepo, sourceRef, queueName, persistedMessage string
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM operations WHERE id=$1 AND idempotency_key='up-shared'),
			(SELECT COUNT(*) FROM deployments WHERE id=$2 AND operation_id=$1),
			(SELECT COUNT(*) FROM operation_events WHERE operation_id=$1),
			(SELECT COUNT(*) FROM deployment_events WHERE deployment_id=$2),
			(SELECT COUNT(*) FROM deploy_intake_outbox WHERE operation_id=$1 AND deployment_id=$2),
			(SELECT COUNT(*) FROM source_artifacts a
			 JOIN source_artifact_refs r ON r.artifact_digest=a.digest
			 WHERE r.operation_id=$1 AND r.deployment_id=$2),
			(SELECT COUNT(*) FROM source_artifact_refs WHERE operation_id=$1),
			(SELECT COUNT(*) FROM audit_logs WHERE resource_id=$1 AND action='service.up.accept'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='up-shared' AND path_template=$3
			   AND state='SUCCEEDED' AND response_status=202
			   AND operation_id=$1),
			(SELECT COUNT(*) FROM source_artifact_workspace_charges
			 WHERE workspace_id='wrk_up_a'
			   AND artifact_digest=(
			       SELECT artifact_digest
			         FROM source_artifact_refs
			        WHERE operation_id=$1
			   )),
			(SELECT COUNT(*) FROM source_artifact_upload_reservations
			 WHERE idempotency_key='up-shared'),
			(SELECT source_type FROM deployments WHERE id=$2),
			(SELECT repo FROM deployments WHERE id=$2),
			(SELECT ref FROM deployments WHERE id=$2),
			(SELECT queue FROM deploy_intake_outbox WHERE operation_id=$1),
			(SELECT COALESCE(message,'') FROM source_artifact_refs WHERE operation_id=$1)`,
		accepted.OperationID,
		accepted.DeploymentID,
		tenantUpIdempotencyPath("svc_up_a"),
	).Scan(
		&operationCount,
		&deploymentCount,
		&operationEventCount,
		&deploymentEventCount,
		&outboxCount,
		&artifactCount,
		&referenceCount,
		&auditCount,
		&idempotencyCount,
		&workspaceChargeCount,
		&uploadReservationCount,
		&sourceType,
		&sourceRepo,
		&sourceRef,
		&queueName,
		&persistedMessage,
	); err != nil {
		t.Fatal(err)
	}
	if operationCount != 1 ||
		deploymentCount != 1 ||
		operationEventCount != 1 ||
		deploymentEventCount != 1 ||
		outboxCount != 1 ||
		artifactCount != 1 ||
		referenceCount != 1 ||
		auditCount != 1 ||
		idempotencyCount != 1 ||
		workspaceChargeCount != 1 ||
		uploadReservationCount != 0 ||
		sourceType != "artifact" ||
		!strings.HasPrefix(sourceRepo, "sourceartifact://sha256:") ||
		!strings.HasPrefix(sourceRef, "sha256:") ||
		queueName != "build" ||
		persistedMessage != "release candidate" {
		t.Fatalf(
			"lineage op=%d dep=%d op_event=%d dep_event=%d outbox=%d artifact=%d ref=%d audit=%d idem=%d charge=%d reservation=%d source=%q/%q/%q queue=%q message=%q",
			operationCount,
			deploymentCount,
			operationEventCount,
			deploymentEventCount,
			outboxCount,
			artifactCount,
			referenceCount,
			auditCount,
			idempotencyCount,
			workspaceChargeCount,
			uploadReservationCount,
			sourceType,
			sourceRepo,
			sourceRef,
			queueName,
			persistedMessage,
		)
	}
	var payload, auditAfter string
	if err := pool.QueryRow(ctx, `
		SELECT o.payload::text, a.after::text
		FROM deploy_intake_outbox o
		JOIN audit_logs a ON a.resource_id=o.operation_id AND a.action='service.up.accept'
		WHERE o.operation_id=$1`,
		accepted.OperationID,
	).Scan(&payload, &auditAfter); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "release candidate") ||
		strings.Contains(auditAfter, "release candidate") ||
		strings.Contains(payload, "blobs/sha256/") {
		t.Fatalf("message/storage leaked payload=%s audit=%s", payload, auditAfter)
	}

	differentMessage := serve(memberFirst, "svc_up_a", "env_up_a", "up-shared", "different", archive)
	if differentMessage.Code != http.StatusConflict {
		t.Fatalf("different message status=%d body=%s", differentMessage.Code, differentMessage.Body.String())
	}
	differentArchive := serve(
		memberFirst,
		"svc_up_a",
		"env_up_a",
		"up-shared",
		"release candidate",
		buildTenantUpIntegrationTarball(t, "different archive"),
	)
	if differentArchive.Code != http.StatusConflict {
		t.Fatalf("different archive status=%d body=%s", differentArchive.Code, differentArchive.Body.String())
	}
	crossService := serve(memberSecond, "svc_up_b", "env_up_a", "up-shared", "release candidate", archive)
	if crossService.Code != http.StatusAccepted {
		t.Fatalf("cross-service status=%d body=%s", crossService.Code, crossService.Body.String())
	}
	var crossServiceAccepted upOperationResponse
	if err := json.Unmarshal(crossService.Body.Bytes(), &crossServiceAccepted); err != nil {
		t.Fatal(err)
	}

	beforeBlobs, err := artifactStore.ListBlobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	viewerDenied := serve(
		viewer,
		"svc_up_a",
		"env_up_a",
		"up-viewer",
		"",
		buildTenantUpIntegrationTarball(t, "viewer unique"),
	)
	restrictedDenied := serve(
		memberSecond,
		"svc_up_a",
		"env_up_restricted",
		"up-restricted",
		"",
		buildTenantUpIntegrationTarball(t, "restricted unique"),
	)
	foreignDenied := serve(
		memberSecond,
		"svc_up_foreign",
		"env_up_foreign",
		"up-foreign",
		"",
		buildTenantUpIntegrationTarball(t, "foreign unique"),
	)
	if viewerDenied.Code != http.StatusForbidden ||
		restrictedDenied.Code != http.StatusNotFound ||
		foreignDenied.Code != http.StatusNotFound {
		t.Fatalf(
			"denials viewer=%d/%s restricted=%d/%s foreign=%d/%s",
			viewerDenied.Code,
			viewerDenied.Body.String(),
			restrictedDenied.Code,
			restrictedDenied.Body.String(),
			foreignDenied.Code,
			foreignDenied.Body.String(),
		)
	}
	adminRestricted := serve(
		admin,
		"svc_up_a",
		"env_up_restricted",
		"up-admin-restricted",
		"",
		buildTenantUpIntegrationTarball(t, "admin restricted"),
	)
	if adminRestricted.Code != http.StatusAccepted {
		t.Fatalf("admin restricted status=%d body=%s", adminRestricted.Code, adminRestricted.Body.String())
	}
	afterBlobs, err := artifactStore.ListBlobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterBlobs) != len(beforeBlobs)+1 {
		t.Fatalf("preflight denials staged blobs before=%d after=%d, want only admin artifact", len(beforeBlobs), len(afterBlobs))
	}

	concurrentBody := buildTenantUpIntegrationTarball(t, "concurrent")
	concurrentCoordinator := newConcurrentReplayTenantUpCoordinator(coordinator)
	concurrentStack := stack
	concurrentStack.TenantUpIntake = concurrentCoordinator
	concurrentStack.SourceArtifactStore = &concurrentReplayArtifactStore{
		FSStore:         artifactStore,
		secondPreflight: concurrentCoordinator.secondPreflight,
	}
	concurrentHandler := NewRouterWithDataStack(concurrentStack)
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			<-start
			results <- serveWithHandler(
				concurrentHandler,
				memberFirst,
				"svc_up_a",
				"env_up_a",
				"up-concurrent",
				"",
				concurrentBody,
			)
		}()
	}
	close(start)
	concurrentFirst := <-results
	concurrentSecond := <-results
	if concurrentFirst.Code != http.StatusAccepted ||
		concurrentSecond.Code != http.StatusAccepted ||
		concurrentFirst.Body.String() != concurrentSecond.Body.String() {
		t.Fatalf(
			"concurrent=%d/%s then %d/%s",
			concurrentFirst.Code,
			concurrentFirst.Body.String(),
			concurrentSecond.Code,
			concurrentSecond.Body.String(),
		)
	}
	replayHeaders := 0
	for _, result := range []*httptest.ResponseRecorder{concurrentFirst, concurrentSecond} {
		if result.Header().Get("Idempotent-Replayed") == "true" {
			replayHeaders++
		}
	}
	if replayHeaders != 1 {
		t.Fatalf("concurrent replay headers=%q/%q", concurrentFirst.Header().Get("Idempotent-Replayed"), concurrentSecond.Header().Get("Idempotent-Replayed"))
	}
	if preflights, publications, deployments, finalizations :=
		concurrentCoordinator.counts(); preflights != 2 ||
		publications != 1 ||
		deployments != 1 ||
		finalizations != 1 {
		t.Fatalf(
			"concurrent preflight/publish/deploy/finalize=%d/%d/%d/%d, want 2/1/1/1",
			preflights,
			publications,
			deployments,
			finalizations,
		)
	}
	var concurrentAccepted upOperationResponse
	if err := json.Unmarshal(concurrentFirst.Body.Bytes(), &concurrentAccepted); err != nil {
		t.Fatal(err)
	}
	var concurrentRows int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM operations
			 WHERE id=$1 AND idempotency_key='up-concurrent') +
			(SELECT COUNT(*) FROM deployments
			 WHERE id=$2 AND operation_id=$1) +
			(SELECT COUNT(*) FROM deploy_intake_outbox
			 WHERE operation_id=$1 AND deployment_id=$2) +
			(SELECT COUNT(*) FROM source_artifact_refs
			 WHERE operation_id=$1 AND deployment_id=$2) +
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='up-concurrent' AND path_template=$3
			   AND state='SUCCEEDED' AND operation_id=$1) +
			(SELECT COUNT(*) FROM source_artifact_upload_reservations
			 WHERE idempotency_key='up-concurrent')`,
		concurrentAccepted.OperationID,
		concurrentAccepted.DeploymentID,
		tenantUpIdempotencyPath("svc_up_a"),
	).Scan(&concurrentRows); err != nil {
		t.Fatal(err)
	}
	if concurrentRows != 5 {
		t.Fatalf("concurrent atomic rows=%d, want one operation/deployment/outbox/ref/idempotency", concurrentRows)
	}

	raceReplayBody := buildTenantUpIntegrationTarball(t, "replay grant revocation")
	raceAccepted := serve(race, "svc_up_a", "env_up_a", "up-race-replay", "", raceReplayBody)
	if raceAccepted.Code != http.StatusAccepted {
		t.Fatalf("race replay seed status=%d body=%s", raceAccepted.Code, raceAccepted.Body.String())
	}
	if _, err := pool.Exec(ctx, `DELETE FROM workspace_members WHERE workspace_id='wrk_up_a' AND user_id='usr_up_race'`); err != nil {
		t.Fatal(err)
	}
	raceReplayDenied := serve(race, "svc_up_a", "env_up_a", "up-race-replay", "", raceReplayBody)
	if raceReplayDenied.Code != http.StatusNotFound {
		t.Fatalf("reauthorized replay status=%d body=%s", raceReplayDenied.Code, raceReplayDenied.Body.String())
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id,user_id,role,invited_at,accepted_at,created_at,updated_at
		) VALUES ('wrk_up_a','usr_up_race','member',$1,$1,$1,$1)`,
		now,
	); err != nil {
		t.Fatal(err)
	}

	beforeRaceBlobs, err := artifactStore.ListBlobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raceInner := stack.TenantDeployIntake.(*PostgresTenantDeployIntakeCoordinator)
	raceCoordinator := &revokingTenantUpCoordinator{
		inner: raceInner,
		revoke: func() error {
			_, err := pool.Exec(ctx, `DELETE FROM workspace_members WHERE workspace_id='wrk_up_a' AND user_id='usr_up_race'`)
			return err
		},
	}
	stack.TenantUpIntake = raceCoordinator
	raceHandler := NewRouterWithDataStack(stack)
	raceBody := buildTenantUpIntegrationTarball(t, "revoked after preflight")
	raceRequest := httptest.NewRequest(
		http.MethodPost,
		tenantUpIdempotencyPath("svc_up_a"),
		bytes.NewReader(raceBody),
	)
	raceRequest.Header = tenantUpHeaders("up-race-revoked", "env_up_a", "")
	setTenantUpArchiveContract(raceRequest.Header, raceBody)
	raceRequest.Header.Set("Authorization", "Bearer "+race.token)
	raceResponse := httptest.NewRecorder()
	raceHandler.ServeHTTP(raceResponse, raceRequest)
	if raceResponse.Code != http.StatusNotFound {
		t.Fatalf("final reauth status=%d body=%s", raceResponse.Code, raceResponse.Body.String())
	}
	var raceRows int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM operations WHERE idempotency_key='up-race-revoked') +
			(SELECT COUNT(*) FROM idempotency_keys WHERE key='up-race-revoked') +
			(SELECT COUNT(*) FROM source_artifact_refs r
			 JOIN operations o ON o.id=r.operation_id
			 WHERE o.idempotency_key='up-race-revoked')`,
	).Scan(&raceRows); err != nil {
		t.Fatal(err)
	}
	if raceRows != 0 {
		t.Fatalf("final reauth committed %d product rows", raceRows)
	}
	var raceReservations int
	if err := pool.QueryRow(
		ctx,
		`SELECT COUNT(*) FROM source_artifact_upload_reservations
		  WHERE idempotency_key='up-race-revoked'
		    AND published_at IS NOT NULL`,
	).Scan(&raceReservations); err != nil {
		t.Fatal(err)
	}
	if raceReservations != 1 {
		t.Fatalf(
			"final reauth conservative proof reservations=%d, want 1",
			raceReservations,
		)
	}
	afterRaceBlobs, err := artifactStore.ListBlobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterRaceBlobs) != len(beforeRaceBlobs) {
		t.Fatalf(
			"revoked publication changed blobs before=%d after=%d",
			len(beforeRaceBlobs),
			len(afterRaceBlobs),
		)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id,user_id,role,invited_at,accepted_at,created_at,updated_at
		) VALUES ('wrk_up_a','usr_up_race','member',$1,$1,$1,$1)`,
		now,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION reject_tenant_up_audit() RETURNS trigger AS $$
		BEGIN
			IF NEW.action = 'service.up.accept' THEN
				RAISE EXCEPTION 'injected tenant up audit failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER reject_tenant_up_audit_trigger
		BEFORE INSERT ON audit_logs
		FOR EACH ROW EXECUTE FUNCTION reject_tenant_up_audit()`,
	); err != nil {
		t.Fatalf("install fault: %v", err)
	}
	stack.TenantUpIntake = coordinator
	faultHandler := NewRouterWithDataStack(stack)
	faultBody := buildTenantUpIntegrationTarball(t, "audit rollback")
	beforeFaultBlobs, err := artifactStore.ListBlobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	faultRequest := httptest.NewRequest(
		http.MethodPost,
		tenantUpIdempotencyPath("svc_up_a"),
		bytes.NewReader(faultBody),
	)
	faultRequest.Header = tenantUpHeaders("up-audit-fault", "env_up_a", "")
	setTenantUpArchiveContract(faultRequest.Header, faultBody)
	faultRequest.Header.Set("Authorization", "Bearer "+memberFirst.token)
	faultResponse := httptest.NewRecorder()
	faultHandler.ServeHTTP(faultResponse, faultRequest)
	if faultResponse.Code != http.StatusInternalServerError ||
		strings.Contains(faultResponse.Body.String(), "injected tenant up audit failure") {
		t.Fatalf("fault status=%d body=%s", faultResponse.Code, faultResponse.Body.String())
	}
	if _, err := pool.Exec(ctx, `
		DROP TRIGGER reject_tenant_up_audit_trigger ON audit_logs;
		DROP FUNCTION reject_tenant_up_audit()`,
	); err != nil {
		t.Fatalf("remove fault: %v", err)
	}
	var faultRows int
	faultDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(faultBody))
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM operations WHERE idempotency_key='up-audit-fault') +
			(SELECT COUNT(*) FROM idempotency_keys WHERE key='up-audit-fault') +
			(SELECT COUNT(*) FROM source_artifacts WHERE digest=$1) +
			(SELECT COUNT(*) FROM source_artifact_refs r
			 JOIN operations o ON o.id=r.operation_id
			 WHERE o.idempotency_key='up-audit-fault') +
			(SELECT COUNT(*) FROM audit_logs WHERE action='service.up.accept'
			 AND attrs->>'session_id' IS NOT NULL
			 AND resource_id IN (
				SELECT id FROM operations WHERE idempotency_key='up-audit-fault'
			 ))`,
		faultDigest,
	).Scan(&faultRows); err != nil {
		t.Fatal(err)
	}
	if faultRows != 0 {
		t.Fatalf("audit failure committed %d transactional rows", faultRows)
	}
	var publishedReservations, failedCharges int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM source_artifact_upload_reservations
			 WHERE idempotency_key='up-audit-fault'
			   AND published_at IS NOT NULL),
			(SELECT COUNT(*) FROM source_artifact_workspace_charges
			 WHERE artifact_digest=$1)`,
		faultDigest,
	).Scan(&publishedReservations, &failedCharges); err != nil {
		t.Fatal(err)
	}
	if publishedReservations != 1 || failedCharges != 0 {
		t.Fatalf(
			"audit rollback reservation/charge=%d/%d, want conservative published reservation only",
			publishedReservations,
			failedCharges,
		)
	}
	afterFaultBlobs, err := artifactStore.ListBlobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterFaultBlobs) != len(beforeFaultBlobs)+1 {
		t.Fatalf("late rollback blobs before=%d after=%d, want one unreferenced GC candidate", len(beforeFaultBlobs), len(afterFaultBlobs))
	}

	if _, err := pool.Exec(
		ctx,
		`DELETE FROM operations WHERE id IN ($1,$2)`,
		accepted.OperationID,
		crossServiceAccepted.OperationID,
	); err != nil {
		t.Fatalf("cascade accepted source operations: %v", err)
	}
	var cascadedReferences, survivingCharges, survivingMetadata int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM source_artifact_refs
			 WHERE artifact_digest=$1),
			(SELECT COUNT(*) FROM source_artifact_workspace_charges
			 WHERE workspace_id='wrk_up_a' AND artifact_digest=$1),
			(SELECT COUNT(*) FROM source_artifacts WHERE digest=$1)`,
		sourceRef,
	).Scan(
		&cascadedReferences,
		&survivingCharges,
		&survivingMetadata,
	); err != nil {
		t.Fatal(err)
	}
	if cascadedReferences != 0 ||
		survivingCharges != 1 ||
		survivingMetadata != 1 {
		t.Fatalf(
			"operation cascade refs/charges/metadata=%d/%d/%d, want 0/1/1",
			cascadedReferences,
			survivingCharges,
			survivingMetadata,
		)
	}
}

type concurrentReplayTenantUpCoordinator struct {
	inner           TenantUpIntakeCoordinator
	secondPreflight chan struct{}
	closeSecond     sync.Once
	preflights      atomic.Int32
	publications    atomic.Int32
	deployments     atomic.Int32
	finalizations   atomic.Int32
}

func newConcurrentReplayTenantUpCoordinator(
	inner TenantUpIntakeCoordinator,
) *concurrentReplayTenantUpCoordinator {
	return &concurrentReplayTenantUpCoordinator{
		inner:           inner,
		secondPreflight: make(chan struct{}),
	}
}

func (c *concurrentReplayTenantUpCoordinator) PreflightSourceUp(
	ctx context.Context,
	request TenantSourceUpPreflightRequest,
) (TenantSourceUpAdmission, error) {
	if c.preflights.Add(1) == 2 {
		c.closeSecond.Do(func() { close(c.secondPreflight) })
	}
	return c.inner.PreflightSourceUp(ctx, request)
}

func (c *concurrentReplayTenantUpCoordinator) FinalizeSourceUpReplay(
	ctx context.Context,
	request TenantSourceUpPreflightRequest,
) (idempotency.StoredResponse, error) {
	c.finalizations.Add(1)
	return c.inner.FinalizeSourceUpReplay(ctx, request)
}

func (c *concurrentReplayTenantUpCoordinator) PublishSourceUp(
	ctx context.Context,
	request TenantSourceUpPublishRequest,
) (TenantSourceArtifact, error) {
	c.publications.Add(1)
	return c.inner.PublishSourceUp(ctx, request)
}

func (c *concurrentReplayTenantUpCoordinator) DeploySourceArtifact(
	ctx context.Context,
	request TenantSourceUpRequest,
) (TenantDeployIntakeResult, error) {
	c.deployments.Add(1)
	return c.inner.DeploySourceArtifact(ctx, request)
}

func (c *concurrentReplayTenantUpCoordinator) FinishSourceUpAdmission(
	ctx context.Context,
	admission TenantSourceUpAdmission,
	published bool,
) error {
	return c.inner.FinishSourceUpAdmission(ctx, admission, published)
}

func (c *concurrentReplayTenantUpCoordinator) counts() (int32, int32, int32, int32) {
	return c.preflights.Load(),
		c.publications.Load(),
		c.deployments.Load(),
		c.finalizations.Load()
}

type concurrentReplayArtifactStore struct {
	*sourceartifact.FSStore
	secondPreflight <-chan struct{}
	delayFirst      sync.Once
}

func (s *concurrentReplayArtifactStore) Stage(
	ctx context.Context,
	reader io.Reader,
	limits sourceartifact.Limits,
) (sourceartifact.Staged, error) {
	var waitErr error
	s.delayFirst.Do(func() {
		select {
		case <-ctx.Done():
			waitErr = ctx.Err()
		case <-s.secondPreflight:
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				waitErr = ctx.Err()
			case <-timer.C:
			}
		}
	})
	if waitErr != nil {
		return sourceartifact.Staged{}, waitErr
	}
	return s.FSStore.Stage(ctx, reader, limits)
}

type revokingTenantUpCoordinator struct {
	inner  TenantUpIntakeCoordinator
	once   sync.Once
	revoke func() error
	err    error
}

func (c *revokingTenantUpCoordinator) PreflightSourceUp(
	ctx context.Context,
	request TenantSourceUpPreflightRequest,
) (TenantSourceUpAdmission, error) {
	admission, err := c.inner.PreflightSourceUp(ctx, request)
	if err != nil {
		return TenantSourceUpAdmission{}, err
	}
	c.once.Do(func() {
		c.err = c.revoke()
	})
	return admission, c.err
}

func (c *revokingTenantUpCoordinator) PublishSourceUp(
	ctx context.Context,
	request TenantSourceUpPublishRequest,
) (TenantSourceArtifact, error) {
	return c.inner.PublishSourceUp(ctx, request)
}

func (c *revokingTenantUpCoordinator) FinalizeSourceUpReplay(
	ctx context.Context,
	request TenantSourceUpPreflightRequest,
) (idempotency.StoredResponse, error) {
	return c.inner.FinalizeSourceUpReplay(ctx, request)
}

func (c *revokingTenantUpCoordinator) DeploySourceArtifact(
	ctx context.Context,
	request TenantSourceUpRequest,
) (TenantDeployIntakeResult, error) {
	return c.inner.DeploySourceArtifact(ctx, request)
}

func (c *revokingTenantUpCoordinator) FinishSourceUpAdmission(
	ctx context.Context,
	admission TenantSourceUpAdmission,
	published bool,
) error {
	return c.inner.FinishSourceUpAdmission(ctx, admission, published)
}

func buildTenantUpIntegrationTarball(t *testing.T, content string) []byte {
	t.Helper()
	var body bytes.Buffer
	gzipWriter := gzip.NewWriter(&body)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name:     "README.txt",
		Mode:     0o644,
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}
