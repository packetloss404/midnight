//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/build"
	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/migrations"
)

func TestTenantSourceUpHTTPOutboxWorkerMaterializationAndRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	container, dsn := startSourcePipelinePostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool, err := dbpg.Open(ctx, dbpg.PoolConfig{DSN: dsn, MaxConns: 6})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := (&dbpg.MigrationRunner{
		Pool:         pool,
		FS:           migrations.FS,
		ManifestPath: "manifest.yaml",
	}).Up(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	const (
		userID        = "usr_source_pipeline"
		workspaceID   = "wrk_source_pipeline"
		projectID     = "prj_source_pipeline"
		serviceID     = "svc_source_pipeline"
		environmentID = "env_source_pipeline"
		password      = "source pipeline correct horse battery staple"
		proofBody     = "HTTP to durable outbox to leased worker materialization"
	)
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, display_name, password_hash, session_generation,
			created_at, updated_at
		) VALUES ($1,'pipeline@example.com','Pipeline Member',$2,1,$3,$3)`,
		userID,
		passwordHash,
		now,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (
			id, slug, display_name, created_at, updated_at
		) VALUES ($1,'source-pipeline','Source Pipeline',$2,$2)`,
		workspaceID,
		now,
	); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (
			id, workspace_id, slug, display_name, created_at, updated_at
		) VALUES ($1,$2,'source-pipeline','Source Pipeline',$3,$3)`,
		projectID,
		workspaceID,
		now,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO services (
			id, project_id, slug, display_name, created_at, updated_at
		) VALUES ($1,$2,'web','Web',$3,$3)`,
		serviceID,
		projectID,
		now,
	); err != nil {
		t.Fatalf("seed service: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO environments (
			id, project_id, slug, name, tier, access_restricted,
			created_at, updated_at
		) VALUES ($1,$2,'production','Production','production',FALSE,$3,$3)`,
		environmentID,
		projectID,
		now,
	); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id, user_id, role, invited_at, accepted_at,
			created_at, updated_at
		) VALUES ($1,$2,'member',$3,$3,$3,$3)`,
		workspaceID,
		userID,
		now,
	); err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	artifactRoot := t.TempDir()
	keyProvider, err := secrets.NewMemoryKeyProvider(
		[]byte("0123456789abcdef0123456789abcdef"),
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := api.NewPostgresDataStack(api.PostgresDataStackConfig{
		Pool:                  pool,
		MigrationsFS:          migrations.FS,
		ManifestPath:          "manifest.yaml",
		CursorSecret:          []byte("source-pipeline-cursor-secret-001"),
		AgentHMACSecret:       []byte("source-pipeline-agent-secret"),
		AutomationTokenMACKey: []byte("source-pipeline-automation-key-01"),
		InvitationMACKey:      []byte("source-pipeline-invitation-key-01"),
		VariableIntentMACKey:  []byte("source-pipeline-variable-intent-key"),
		VariableKeyProvider:   keyProvider,
		SourceArtifactRoot:    artifactRoot,
		Clock:                 func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("data stack: %v", err)
	}
	login, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
		Email:      "pipeline@example.com",
		Password:   password,
		ClientKind: "api",
		RemoteAddr: "192.0.2.80:443",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	server := httptest.NewServer(api.NewRouterWithDataStack(stack))
	defer server.Close()
	archive := sourcePipelineArchive(t, proofBody)
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		server.URL+"/api/v1alpha/services/"+serviceID+"/up",
		bytes.NewReader(archive),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+login.AccessToken)
	request.Header.Set("Content-Type", "application/gzip")
	request.Header.Set("Idempotency-Key", "source-pipeline-e2e")
	request.Header.Set("X-Midnight-Environment", environmentID)
	request.Header.Set("X-Midnight-Commit-Message", "pipeline proof")
	request.Header.Set(
		"X-Midnight-Archive-Digest",
		fmt.Sprintf("sha256:%x", sha256.Sum256(archive)),
	)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("source up HTTP request: %v", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("source up status=%d body=%s", response.StatusCode, responseBody)
	}
	var accepted struct {
		OperationID  string `json:"operation_id"`
		DeploymentID string `json:"deployment_id"`
	}
	if err := json.Unmarshal(responseBody, &accepted); err != nil {
		t.Fatalf("decode source up response: %v body=%s", err, responseBody)
	}
	if accepted.OperationID == "" || accepted.DeploymentID == "" {
		t.Fatalf("source up response=%s", responseBody)
	}

	references := sourceartifact.NewPostgresStore(pool)
	reference, found, err := references.GetReference(ctx, accepted.OperationID)
	if err != nil || !found {
		t.Fatalf("active source reference: found=%t reference=%#v err=%v", found, reference, err)
	}
	if reference.ReleasedAt != nil ||
		reference.DeploymentID != accepted.DeploymentID ||
		reference.ProjectID != projectID ||
		reference.ServiceID != serviceID ||
		reference.EnvironmentID != environmentID {
		t.Fatalf("source reference before dispatch=%#v", reference)
	}
	assertSourcePipelineCounts(t, ctx, pool, accepted.OperationID, accepted.DeploymentID, "pending", "", 0)

	outbox, ok := stack.DeployIntakeOutbox.(*api.PostgresDeployOutbox)
	if !ok {
		t.Fatalf("deploy outbox=%T", stack.DeployIntakeOutbox)
	}
	outbox.WorkerID = "source-pipeline-dispatcher"
	if delivered, err := outbox.DispatchDeployIntake(ctx, 1); err != nil || delivered != 1 {
		t.Fatalf("dispatch source outbox=%d, %v", delivered, err)
	}

	queueStore := queue.NewPostgresStore(pool)
	workerCore := &buildJobExecutor{
		cfg: workerConfig{
			CheckoutRoot: filepath.Join(t.TempDir(), "checkouts"),
			UploadRoot:   artifactRoot,
		},
		sourceArtifacts: references,
	}
	proof := &sourcePipelineMaterializationProof{
		worker:              workerCore,
		expectedOperationID: accepted.OperationID,
		expectedDeployment:  accepted.DeploymentID,
		expectedBody:        proofBody,
	}
	processed, err := processOneBuildJob(
		ctx,
		queueStore,
		"source-pipeline-worker",
		time.Minute,
		time.Minute,
		now.Add(time.Second),
		proof,
	)
	if err != nil || !processed {
		t.Fatalf("process source build job=%t, %v", processed, err)
	}
	if !proof.materialized || !proof.terminalized {
		t.Fatalf("worker proof materialized=%t terminalized=%t", proof.materialized, proof.terminalized)
	}
	if proof.checkoutPath == "" {
		t.Fatal("worker did not record a checkout path")
	}
	if _, err := os.Stat(proof.checkoutPath); !os.IsNotExist(err) {
		t.Fatalf("attempt checkout still exists after discard: path=%q err=%v", proof.checkoutPath, err)
	}

	released, found, err := references.GetReference(ctx, accepted.OperationID)
	if err != nil || !found {
		t.Fatalf("released source reference: found=%t reference=%#v err=%v", found, released, err)
	}
	if released.ReleasedAt == nil {
		t.Fatalf("source reference remained active after terminal job: %#v", released)
	}
	assertSourcePipelineCounts(t, ctx, pool, accepted.OperationID, accepted.DeploymentID, "delivered", "succeeded", 1)

	filesystem, err := sourceartifact.NewFSStore(artifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := filesystem.ListBlobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != 1 || blobs[0].Digest != reference.Digest {
		t.Fatalf("durable blobs=%v, want released artifact %q retained for GC", blobs, reference.Digest)
	}
}

// sourcePipelineMaterializationProof deliberately replaces only the external
// image solve. It invokes the worker's production payload/lineage checks,
// ownership check, content-addressed materializer, queue terminalization, and
// terminal ownership reconciler against the same PostgreSQL and filesystem
// stores production uses.
type sourcePipelineMaterializationProof struct {
	worker              *buildJobExecutor
	expectedOperationID string
	expectedDeployment  string
	expectedBody        string
	checkoutPath        string
	materialized        bool
	terminalized        bool
}

func (p *sourcePipelineMaterializationProof) Execute(ctx context.Context, job queue.Job) error {
	var payload buildJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return err
	}
	deploymentID, err := validateBuildJobLineage(job, payload)
	if err != nil {
		return err
	}
	if payload.OperationID != p.expectedOperationID || deploymentID != p.expectedDeployment {
		return fmt.Errorf(
			"unexpected worker lineage operation=%q deployment=%q",
			payload.OperationID,
			deploymentID,
		)
	}
	if payload.Request.Source == nil || payload.Request.Source.Provider != "artifact" {
		return fmt.Errorf("unexpected worker source=%#v", payload.Request.Source)
	}
	source := build.SourceRef{
		Repo: payload.Request.Source.Repo,
		Ref:  payload.Request.Source.Ref,
	}
	identity := &sourceArtifactCheckoutIdentity{
		OperationID:       payload.OperationID,
		DeploymentID:      deploymentID,
		ProjectID:         payload.ProjectID,
		ServiceID:         payload.ServiceID,
		EnvironmentID:     payload.EnvironmentID,
		Queue:             job.Queue,
		ParentOperationID: job.ParentOperationID,
	}
	checkout, err := p.worker.checkoutSource(
		ctx,
		payload.ServiceID,
		payload.Request.Source,
		source,
		identity,
	)
	if err != nil {
		return err
	}
	p.checkoutPath = checkout.RepoDir
	body, readErr := os.ReadFile(filepath.Join(checkout.WorkDir, "README.txt"))
	discardErr := checkout.Discard()
	if readErr != nil {
		return readErr
	}
	if discardErr != nil {
		return discardErr
	}
	if string(body) != p.expectedBody {
		return fmt.Errorf("materialized README=%q, want %q", body, p.expectedBody)
	}
	p.materialized = true
	return nil
}

func (p *sourcePipelineMaterializationProof) OnBuildJobTerminalized(
	ctx context.Context,
	job queue.Job,
) error {
	if err := p.worker.OnBuildJobTerminalized(ctx, job); err != nil {
		return err
	}
	p.terminalized = true
	return nil
}

func assertSourcePipelineCounts(
	t *testing.T,
	ctx context.Context,
	pool *dbpg.Pool,
	operationID string,
	deploymentID string,
	outboxState string,
	jobState string,
	wantJobs int,
) {
	t.Helper()
	var (
		operations  int
		deployments int
		outboxes    int
		jobs        int
		linkedJobs  int
		jobAttempts int
		gotOutbox   string
		gotJob      string
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM operations WHERE id=$1),
			(SELECT COUNT(*) FROM deployments WHERE id=$2 AND operation_id=$1),
			(SELECT COUNT(*) FROM deploy_intake_outbox
			 WHERE operation_id=$1 AND deployment_id=$2),
			(SELECT COUNT(*) FROM jobs WHERE parent_operation_id=$1),
			(SELECT COUNT(*)
			   FROM deploy_intake_outbox o
			   JOIN jobs j ON j.id=o.job_id AND j.outbox_id=o.id
			  WHERE o.operation_id=$1 AND o.deployment_id=$2),
			(SELECT COALESCE(MAX(attempt),0) FROM jobs
			 WHERE parent_operation_id=$1),
			(SELECT COALESCE(MAX(state),'') FROM deploy_intake_outbox
			 WHERE operation_id=$1 AND deployment_id=$2),
			(SELECT COALESCE(MAX(state),'') FROM jobs
			 WHERE parent_operation_id=$1)`,
		operationID,
		deploymentID,
	).Scan(
		&operations,
		&deployments,
		&outboxes,
		&jobs,
		&linkedJobs,
		&jobAttempts,
		&gotOutbox,
		&gotJob,
	); err != nil {
		t.Fatal(err)
	}
	if operations != 1 ||
		deployments != 1 ||
		outboxes != 1 ||
		jobs != wantJobs ||
		linkedJobs != wantJobs ||
		jobAttempts != wantJobs ||
		gotOutbox != outboxState ||
		gotJob != jobState {
		t.Fatalf(
			"pipeline rows operations=%d deployments=%d outboxes=%d jobs=%d linked=%d attempts=%d states=%q/%q",
			operations,
			deployments,
			outboxes,
			jobs,
			linkedJobs,
			jobAttempts,
			gotOutbox,
			gotJob,
		)
	}
}

func sourcePipelineArchive(t *testing.T, body string) []byte {
	t.Helper()
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name:     "README.txt",
		Mode:     0o644,
		Typeflag: tar.TypeReg,
		Size:     int64(len(body)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func startSourcePipelinePostgres(t *testing.T, ctx context.Context) (tc.Container, string) {
	t.Helper()
	container, err := tcpostgres.Run(
		ctx,
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
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "docker") &&
			(strings.Contains(message, "not running") ||
				strings.Contains(message, "cannot connect") ||
				strings.Contains(message, "no such file") ||
				strings.Contains(message, "permission denied")) {
			t.Skipf("docker not available: %v", err)
		}
		t.Fatalf("start Postgres: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(context.Background())
		t.Fatalf("Postgres DSN: %v", err)
	}
	return container, dsn
}
