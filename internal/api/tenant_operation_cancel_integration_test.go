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

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type cancelIntegrationHuman struct {
	principal identity.Principal
	session   identity.Session
	token     string
}

func TestTenantOperationCancelAtomicAuthorizationAndWorkerFence(
	t *testing.T,
) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	runner := &pg.MigrationRunner{
		Pool:         pool,
		FS:           migrations.FS,
		ManifestPath: "manifest.yaml",
	}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC)
	password := "tenant cancel correct horse battery staple"
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id,email,display_name,password_hash,session_generation,
			created_at,updated_at
		) VALUES
			('usr_cancel_member','member-cancel@example.com','Member',$1,1,$2,$2),
			('usr_cancel_worker','worker-cancel@example.com','Worker',$1,1,$2,$2),
			('usr_cancel_viewer','viewer-cancel@example.com','Viewer',$1,1,$2,$2),
			('usr_cancel_fault','fault-cancel@example.com','Fault',$1,1,$2,$2)`,
		passwordHash,
		now,
	); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (
			id,slug,display_name,created_at,updated_at
		) VALUES
			('wrk_cancel_a','cancel-a','Cancel A',$1,$1),
			('wrk_cancel_b','cancel-b','Cancel B',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspaces: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (
			id,workspace_id,slug,display_name,created_at,updated_at
		) VALUES
			('prj_cancel_a','wrk_cancel_a','api','API',$1,$1),
			('prj_cancel_b','wrk_cancel_b','api','API',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO services (
			id,project_id,slug,display_name,created_at,updated_at
		) VALUES
			('svc_cancel_a','prj_cancel_a','web','Web',$1,$1),
			('svc_cancel_b','prj_cancel_b','foreign','Foreign',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed services: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO environments (
			id,project_id,slug,name,tier,access_restricted,created_at,updated_at
		) VALUES
			('env_cancel_a','prj_cancel_a','production','Production','production',FALSE,$1,$1),
			('env_cancel_b','prj_cancel_b','production','Production','production',FALSE,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed environments: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id,user_id,role,invited_at,accepted_at,created_at,updated_at
		) VALUES
			('wrk_cancel_a','usr_cancel_member','member',$1,$1,$1,$1),
			('wrk_cancel_a','usr_cancel_worker','member',$1,$1,$1,$1),
			('wrk_cancel_a','usr_cancel_viewer','viewer',$1,$1,$1,$1),
			('wrk_cancel_a','usr_cancel_fault','member',$1,$1,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed tenant graph: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO operations (
			id,kind,state,actor,expires_at,created_at,updated_at
		) VALUES
			('op_cancel_pending','deploy','QUEUED','system',$2,$1,$1),
			('op_cancel_leased','deploy','RUNNING','system',$2,$1,$1),
			('op_cancel_viewer','deploy','QUEUED','system',$2,$1,$1),
			('op_cancel_foreign','deploy','QUEUED','system',$2,$1,$1),
			('op_cancel_dispatch_race','deploy','QUEUED','system',$2,$1,$1),
			('op_cancel_fault','deploy','QUEUED','system',$2,$1,$1)`,
		now,
		now.Add(24*time.Hour),
	); err != nil {
		t.Fatalf("seed operations: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id,project_id,service_id,environment_id,action,state,active,
			operation_id,trigger_key,created_at,updated_at,started_at
		) VALUES
			('dep_cancel_pending','prj_cancel_a','svc_cancel_a','env_cancel_a',
			 'deploy_source','queued',FALSE,'op_cancel_pending','cancel:pending',$1,$1,NULL),
			('dep_cancel_leased','prj_cancel_a','svc_cancel_a','env_cancel_a',
			 'deploy_source','queued',FALSE,'op_cancel_leased','cancel:leased',$1,$1,NULL),
			('dep_cancel_viewer','prj_cancel_a','svc_cancel_a','env_cancel_a',
			 'deploy_source','queued',FALSE,'op_cancel_viewer','cancel:viewer',$1,$1,NULL),
			('dep_cancel_foreign','prj_cancel_b','svc_cancel_b','env_cancel_b',
			 'deploy_source','queued',FALSE,'op_cancel_foreign','cancel:foreign',$1,$1,NULL),
			('dep_cancel_dispatch_race','prj_cancel_a','svc_cancel_a','env_cancel_a',
			 'deploy_source','queued',FALSE,'op_cancel_dispatch_race','cancel:dispatch-race',$1,$1,NULL),
			('dep_cancel_fault','prj_cancel_a','svc_cancel_a','env_cancel_a',
			 'deploy_source','queued',FALSE,'op_cancel_fault','cancel:fault',$1,$1,NULL)`,
		now,
	); err != nil {
		t.Fatalf("seed operation lineage: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE deployments
		   SET state='building',started_at=$1,updated_at=$1
		 WHERE id='dep_cancel_leased'`,
		now,
	); err != nil {
		t.Fatalf("advance leased deployment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deploy_intake_outbox (
			id,trigger_key,operation_id,deployment_id,queue,partition_key,
			payload,state,available_at,created_at,updated_at
		) VALUES
			('dox_cancel_pending','cancel:pending','op_cancel_pending',
			 'dep_cancel_pending','build','svc_cancel_a','{}'::jsonb,'pending',$1,$1,$1),
			('dox_cancel_viewer','cancel:viewer','op_cancel_viewer',
			 'dep_cancel_viewer','build','svc_cancel_a','{}'::jsonb,'pending',$2,$1,$1),
			('dox_cancel_foreign','cancel:foreign','op_cancel_foreign',
			 'dep_cancel_foreign','build','svc_cancel_b','{}'::jsonb,'pending',$2,$1,$1),
			('dox_cancel_dispatch_race','cancel:dispatch-race',
			 'op_cancel_dispatch_race','dep_cancel_dispatch_race','build',
			 'svc_cancel_a','{}'::jsonb,'pending',$1,$1,$1)`,
		now,
		now.Add(24*time.Hour),
	); err != nil {
		t.Fatalf("seed deploy outboxes: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO staged_work_outbox (
			id,dedupe_key,stage,operation_id,deployment_id,queue,partition_key,
			payload,state,created_at,updated_at
		) VALUES (
			'sox_cancel_fault','cancel:fault','github_build_intake',
			'op_cancel_fault','dep_cancel_fault','build','svc_cancel_a',
			'{}'::jsonb,'pending',$1,$1
		)`,
		now,
	); err != nil {
		t.Fatalf("seed staged outbox: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs (
			id,queue,state,parent_operation_id,partition_key,payload,
			lease_owner,lease_expires_at,heartbeat_at,attempt,created_at,updated_at
		) VALUES (
			'job_cancel_leased','build','leased','op_cancel_leased',
			'svc_cancel_a','{}'::jsonb,'build-worker',$2,$1,1,$1,$1
		)`,
		now,
		now.Add(time.Minute),
	); err != nil {
		t.Fatalf("seed durable work: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	coordinator, ok := stack.TenantOperationCancel.(*PostgresTenantOperationCancelCoordinator)
	if !ok {
		t.Fatalf(
			"tenant cancel coordinator = %T, want postgres",
			stack.TenantOperationCancel,
		)
	}
	login := func(email string) cancelIntegrationHuman {
		t.Helper()
		result, err := stack.IdentitySessions.Login(
			ctx,
			identity.LoginRequest{
				Email:      email,
				Password:   password,
				ClientKind: "api",
				RemoteAddr: "192.0.2.70:443",
			},
		)
		if err != nil {
			t.Fatalf("login %s: %v", email, err)
		}
		principal, session, err := stack.IdentitySessions.Authenticate(
			ctx,
			"Bearer "+result.AccessToken,
		)
		if err != nil {
			t.Fatalf("authenticate %s: %v", email, err)
		}
		return cancelIntegrationHuman{
			principal: principal,
			session:   session,
			token:     result.AccessToken,
		}
	}
	memberFirst := login("member-cancel@example.com")
	memberSecond := login("member-cancel@example.com")
	worker := login("worker-cancel@example.com")
	viewer := login("viewer-cancel@example.com")
	fault := login("fault-cancel@example.com")

	handler := NewRouterWithDataStack(stack)
	serveCancel := func(
		human cancelIntegrationHuman,
		operationID,
		key string,
	) *httptest.ResponseRecorder {
		request := httptest.NewRequest(
			http.MethodPost,
			tenantOperationCancelIdempotencyPath(orailtypes.ID(operationID)),
			nil,
		)
		request.Header.Set("Authorization", "Bearer "+human.token)
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	for _, human := range []cancelIntegrationHuman{
		memberFirst,
		memberSecond,
	} {
		human := human
		go func() {
			<-start
			results <- serveCancel(
				human,
				"op_cancel_pending",
				"cancel-concurrent",
			)
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if first.Code != http.StatusAccepted ||
		second.Code != http.StatusAccepted ||
		first.Body.String() != second.Body.String() {
		t.Fatalf(
			"concurrent cancellation = %d/%s then %d/%s",
			first.Code,
			first.Body.String(),
			second.Code,
			second.Body.String(),
		)
	}
	replays := 0
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		if response.Header().Get("Idempotent-Replayed") == "true" {
			replays++
		}
	}
	if replays != 1 {
		t.Fatalf(
			"concurrent replay headers = %q/%q, want one replay",
			first.Header().Get("Idempotent-Replayed"),
			second.Header().Get("Idempotent-Replayed"),
		)
	}
	var accepted orailtypes.Operation
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.ID != "op_cancel_pending" ||
		accepted.State != orailtypes.OperationCanceled {
		t.Fatalf("accepted operation = %#v", accepted)
	}

	var (
		pendingOperation, pendingDeployment, pendingOutbox string
		pendingKeys, pendingAudits, pendingEvents          int
		pendingCapability                                  string
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT state FROM operations WHERE id='op_cancel_pending'),
			(SELECT state FROM deployments WHERE id='dep_cancel_pending'),
			(SELECT state FROM deploy_intake_outbox
			 WHERE id='dox_cancel_pending'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='cancel-concurrent'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='operation.cancel'
			   AND resource_id='op_cancel_pending'),
			(SELECT COUNT(*) FROM operation_events
			 WHERE operation_id='op_cancel_pending'
			   AND message LIKE 'canceled:%'),
			(SELECT attrs->>'capability' FROM audit_logs
			 WHERE action='operation.cancel'
			   AND resource_id='op_cancel_pending'
			 LIMIT 1)`,
	).Scan(
		&pendingOperation,
		&pendingDeployment,
		&pendingOutbox,
		&pendingKeys,
		&pendingAudits,
		&pendingEvents,
		&pendingCapability,
	); err != nil {
		t.Fatal(err)
	}
	if pendingOperation != "CANCELED" ||
		pendingDeployment != "canceled" ||
		pendingOutbox != "canceled" ||
		pendingKeys != 1 ||
		pendingAudits != 1 ||
		pendingEvents != 1 ||
		pendingCapability !=
			string(identity.CapabilityDeploymentTriggerCLI) {
		t.Fatalf(
			"pending convergence op=%q dep=%q outbox=%q keys=%d audits=%d events=%d capability=%q",
			pendingOperation,
			pendingDeployment,
			pendingOutbox,
			pendingKeys,
			pendingAudits,
			pendingEvents,
			pendingCapability,
		)
	}

	viewerResponse := serveCancel(
		viewer,
		"op_cancel_viewer",
		"cancel-viewer-denied",
	)
	foreignResponse := serveCancel(
		memberFirst,
		"op_cancel_foreign",
		"cancel-foreign-concealed",
	)
	if viewerResponse.Code != http.StatusForbidden ||
		foreignResponse.Code != http.StatusNotFound {
		t.Fatalf(
			"denial viewer=%d/%s foreign=%d/%s",
			viewerResponse.Code,
			viewerResponse.Body.String(),
			foreignResponse.Code,
			foreignResponse.Body.String(),
		)
	}
	var deniedKeys int
	var viewerOutbox, foreignOutbox string
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key IN (
				'cancel-viewer-denied',
				'cancel-foreign-concealed'
			 )),
			(SELECT state FROM deploy_intake_outbox
			 WHERE id='dox_cancel_viewer'),
			(SELECT state FROM deploy_intake_outbox
			 WHERE id='dox_cancel_foreign')`,
	).Scan(&deniedKeys, &viewerOutbox, &foreignOutbox); err != nil {
		t.Fatal(err)
	}
	if deniedKeys != 0 ||
		viewerOutbox != "pending" ||
		foreignOutbox != "pending" {
		t.Fatalf(
			"denied artifacts keys=%d viewer=%q foreign=%q",
			deniedKeys,
			viewerOutbox,
			foreignOutbox,
		)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE workspace_members
		   SET role='viewer',
		       updated_at=$1,
		       resource_version=resource_version+1
		 WHERE workspace_id='wrk_cancel_a'
		   AND user_id='usr_cancel_member'`,
		now.Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	demotedReplay := serveCancel(
		memberSecond,
		"op_cancel_pending",
		"cancel-concurrent",
	)
	if demotedReplay.Code != http.StatusForbidden {
		t.Fatalf(
			"demoted replay status=%d body=%s",
			demotedReplay.Code,
			demotedReplay.Body.String(),
		)
	}

	raceStart := make(chan struct{})
	raceCancelResult := make(chan *httptest.ResponseRecorder, 1)
	raceDispatchResult := make(chan error, 1)
	go func() {
		<-raceStart
		raceCancelResult <- serveCancel(
			worker,
			"op_cancel_dispatch_race",
			"cancel-dispatch-race",
		)
	}()
	go func() {
		<-raceStart
		_, err := stack.DeployIntakeOutbox.DispatchDeployIntake(ctx, 1)
		raceDispatchResult <- err
	}()
	close(raceStart)
	raceResponse := <-raceCancelResult
	dispatchErr := <-raceDispatchResult
	if raceResponse.Code != http.StatusAccepted {
		t.Fatalf(
			"dispatch race cancel status=%d body=%s",
			raceResponse.Code,
			raceResponse.Body.String(),
		)
	}
	if dispatchErr != nil &&
		!errors.Is(dispatchErr, ErrDeployOutboxLeaseLost) {
		t.Fatalf("dispatch race error = %v", dispatchErr)
	}
	var (
		raceOperation, raceDeployment, raceOutbox string
		raceRunnableJobs                          int
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT state FROM operations
			 WHERE id='op_cancel_dispatch_race'),
			(SELECT state FROM deployments
			 WHERE id='dep_cancel_dispatch_race'),
			(SELECT state FROM deploy_intake_outbox
			 WHERE id='dox_cancel_dispatch_race'),
			(SELECT COUNT(*) FROM jobs
			 WHERE outbox_id='dox_cancel_dispatch_race'
			   AND state IN ('pending','leased'))`,
	).Scan(
		&raceOperation,
		&raceDeployment,
		&raceOutbox,
		&raceRunnableJobs,
	); err != nil {
		t.Fatal(err)
	}
	if raceOperation != "CANCELED" ||
		raceDeployment != "canceled" ||
		(raceOutbox != "canceled" && raceOutbox != "delivered") ||
		raceRunnableJobs != 0 {
		t.Fatalf(
			"dispatch race convergence op=%q dep=%q outbox=%q runnable_jobs=%d",
			raceOperation,
			raceDeployment,
			raceOutbox,
			raceRunnableJobs,
		)
	}

	leasedResponse := serveCancel(
		worker,
		"op_cancel_leased",
		"cancel-leased-safe-point",
	)
	if leasedResponse.Code != http.StatusAccepted {
		t.Fatalf(
			"leased cancellation status=%d body=%s",
			leasedResponse.Code,
			leasedResponse.Body.String(),
		)
	}
	var leasedAccepted orailtypes.Operation
	if err := json.Unmarshal(
		leasedResponse.Body.Bytes(),
		&leasedAccepted,
	); err != nil {
		t.Fatal(err)
	}
	if leasedAccepted.State != orailtypes.OperationRunning {
		t.Fatalf(
			"leased accepted state=%q, want RUNNING until safe point",
			leasedAccepted.State,
		)
	}
	var (
		leasedOperation, leasedDeployment, leasedJob string
		cancelRequestEvents, safePointAudits         int
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT state FROM operations WHERE id='op_cancel_leased'),
			(SELECT state FROM deployments WHERE id='dep_cancel_leased'),
			(SELECT state FROM jobs WHERE id='job_cancel_leased'),
			(SELECT COUNT(*) FROM operation_events
			 WHERE operation_id='op_cancel_leased'
			   AND message LIKE 'cancel requested:%before traffic promotion%'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='operation.cancel.request'
			   AND resource_id='op_cancel_leased'
			   AND after->>'worker_safe_point'='true')`,
	).Scan(
		&leasedOperation,
		&leasedDeployment,
		&leasedJob,
		&cancelRequestEvents,
		&safePointAudits,
	); err != nil {
		t.Fatal(err)
	}
	if leasedOperation != "RUNNING" ||
		leasedDeployment != "building" ||
		leasedJob != "leased" ||
		cancelRequestEvents != 1 ||
		safePointAudits != 1 {
		t.Fatalf(
			"leased fence op=%q dep=%q job=%q events=%d audits=%d",
			leasedOperation,
			leasedDeployment,
			leasedJob,
			cancelRequestEvents,
			safePointAudits,
		)
	}
	deploymentStore, ok := stack.DeploymentStore.(*PostgresDeploymentStore)
	if !ok {
		t.Fatalf(
			"deployment store = %T, want postgres",
			stack.DeploymentStore,
		)
	}
	if operation, err := deploymentStore.CancelDeploymentOperation(
		ctx,
		"op_cancel_leased",
	); err != nil {
		t.Fatalf("worker safe-point commit: %v", err)
	} else if operation.State != orailtypes.OperationCanceled {
		t.Fatalf("worker safe-point operation = %#v", operation)
	}
	var safeOperation, safeDeployment, safeMessage string
	var safeBuildStarted, safeBuildCompleted *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT state FROM operations WHERE id='op_cancel_leased'),
			(SELECT state FROM deployments WHERE id='dep_cancel_leased'),
			(SELECT message FROM deployment_events
			 WHERE deployment_id='dep_cancel_leased'
			 ORDER BY seq DESC LIMIT 1),
			(SELECT build_started_at FROM deployments WHERE id='dep_cancel_leased'),
			(SELECT build_completed_at FROM deployments WHERE id='dep_cancel_leased')`,
	).Scan(&safeOperation, &safeDeployment, &safeMessage, &safeBuildStarted, &safeBuildCompleted); err != nil {
		t.Fatal(err)
	}
	if safeBuildStarted != nil || safeBuildCompleted != nil {
		t.Fatalf("cancellation fabricated missing build-window evidence: start=%v complete=%v", safeBuildStarted, safeBuildCompleted)
	}
	if safeOperation != "CANCELED" ||
		safeDeployment != "canceled" ||
		!strings.Contains(safeMessage, "worker safe point") {
		t.Fatalf(
			"safe-point convergence op=%q dep=%q message=%q",
			safeOperation,
			safeDeployment,
			safeMessage,
		)
	}

	if _, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION midnight_test_fail_cancel_audit()
		RETURNS TRIGGER LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'injected operation cancellation audit failure';
		END
		$$`,
	); err != nil {
		t.Fatalf("install audit fault function: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TRIGGER midnight_test_fail_cancel_audit
		BEFORE INSERT ON audit_logs
		FOR EACH ROW
		WHEN (NEW.request_id = 'req_cancel_fault')
		EXECUTE FUNCTION midnight_test_fail_cancel_audit()`,
	); err != nil {
		t.Fatalf("install audit fault: %v", err)
	}
	faultPath := tenantOperationCancelIdempotencyPath("op_cancel_fault")
	_, faultErr := coordinator.CancelTenantOperation(
		ctx,
		TenantOperationCancelRequest{
			Principal:   fault.principal,
			Session:     fault.session,
			OperationID: "op_cancel_fault",
			Key:         "cancel-audit-fault",
			Fingerprint: idempotency.RequestFingerprint{
				Method:     http.MethodPost,
				Path:       faultPath,
				BodySHA256: idempotency.BodyHashSHA256(nil),
			},
			RequestID: "req_cancel_fault",
			SourceIP:  "192.0.2.70:443",
			UserAgent: "midnight-cancel-integration",
		},
	)
	if faultErr == nil ||
		errors.Is(faultErr, identity.ErrAuthorizationDenied) {
		t.Fatalf("audit fault error = %v", faultErr)
	}
	var (
		faultOperation, faultDeployment, faultOutbox string
		faultKeys, faultAudits, faultEvents          int
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT state FROM operations WHERE id='op_cancel_fault'),
			(SELECT state FROM deployments WHERE id='dep_cancel_fault'),
			(SELECT state FROM staged_work_outbox
			 WHERE id='sox_cancel_fault'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='cancel-audit-fault'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE request_id='req_cancel_fault'),
			(SELECT COUNT(*) FROM operation_events
			 WHERE operation_id='op_cancel_fault')`,
	).Scan(
		&faultOperation,
		&faultDeployment,
		&faultOutbox,
		&faultKeys,
		&faultAudits,
		&faultEvents,
	); err != nil {
		t.Fatal(err)
	}
	if faultOperation != "QUEUED" ||
		faultDeployment != "queued" ||
		faultOutbox != "pending" ||
		faultKeys != 0 ||
		faultAudits != 0 ||
		faultEvents != 0 {
		t.Fatalf(
			"audit rollback op=%q dep=%q outbox=%q keys=%d audits=%d events=%d",
			faultOperation,
			faultDeployment,
			faultOutbox,
			faultKeys,
			faultAudits,
			faultEvents,
		)
	}

	if _, err := pool.Exec(
		ctx,
		`DROP TRIGGER midnight_test_fail_cancel_audit ON audit_logs`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`DROP FUNCTION midnight_test_fail_cancel_audit()`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := deploymentStore.GetDeploymentByOperation(
		ctx,
		"op_cancel_pending",
	); err != nil {
		t.Fatalf("canonical canceled deployment became unreadable: %v", err)
	}
	if state := deploy.DeploymentState(pendingDeployment); state != deploy.DeploymentCanceled {
		t.Fatalf("typed pending deployment state = %q", state)
	}
}
