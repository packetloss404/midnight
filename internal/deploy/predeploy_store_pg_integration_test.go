//go:build integration

package deploy

import (
	"context"
	"errors"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresPredeployRunSurvivesStoreRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("midnight"),
		tcpostgres.WithUsername("midnight_test"),
		tcpostgres.WithPassword("midnight_test"),
		tc.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		t.Skipf("Docker/Postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := dbpg.Open(ctx, dbpg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := (&dbpg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id,workspace_id,slug,display_name) VALUES ('prj_pre','wrk_default','pre','Predeploy');
		INSERT INTO environments (id,project_id,slug,name) VALUES ('env_pre','prj_pre','production','Production');
		INSERT INTO services (id,project_id,slug,display_name) VALUES ('svc_pre','prj_pre','api','API');
		INSERT INTO deployments (
			id,project_id,service_id,environment_id,action,state,image_ref,image_digest
		) VALUES (
			'dep_pre','prj_pre','svc_pre','env_pre','deploy_image','queued',
			'registry.example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
		)`,
	); err != nil {
		t.Fatal(err)
	}

	const spec = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := NewPostgresPredeployStore(pool)
	run, err := store.Ensure(ctx, PredeployRun{
		DeploymentID: "dep_pre", ProjectID: "prj_pre", EnvironmentID: "env_pre",
		ServiceID: "svc_pre", SpecSHA256: spec,
	})
	if err != nil || run.State != PredeployStatePending {
		t.Fatalf("Ensure = %+v, %v", run, err)
	}
	if _, err := store.MarkRunning(ctx, run.DeploymentID, spec); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	// A fresh repository models the worker process restart after the Docker
	// command has exited. It repairs the durable terminal state rather than
	// requiring another command execution.
	restarted := NewPostgresPredeployStore(pool)
	terminal, err := restarted.MarkTerminal(ctx, run.DeploymentID, spec, runtime.OneOffResult{
		ContainerName: "or-predeploy-bbbbbbbbbbbbbbbbbbbbbbbb",
		ContainerID:   "container-1",
		ExitCode:      0,
	}, "", "")
	if err != nil || terminal.State != PredeployStateSucceeded {
		t.Fatalf("MarkTerminal = %+v, %v", terminal, err)
	}
	replayed, err := NewPostgresPredeployStore(pool).Ensure(ctx, PredeployRun{
		DeploymentID: "dep_pre", ProjectID: "prj_pre", EnvironmentID: "env_pre",
		ServiceID: "svc_pre", SpecSHA256: spec,
	})
	if err != nil || replayed.State != PredeployStateSucceeded ||
		replayed.ExitCode == nil || *replayed.ExitCode != 0 {
		t.Fatalf("replayed = %+v, %v", replayed, err)
	}
	conflict := replayed
	conflict.SpecSHA256 = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err := restarted.Ensure(ctx, conflict); !errors.Is(err, ErrPredeploySpecConflict) {
		t.Fatalf("spec drift error = %v, want ErrPredeploySpecConflict", err)
	}

	var sensitiveColumns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM information_schema.columns
		 WHERE table_name='deployment_predeploy_runs'
		   AND column_name IN ('command','environment','env')`,
	).Scan(&sensitiveColumns); err != nil {
		t.Fatal(err)
	}
	if sensitiveColumns != 0 {
		t.Fatalf("deployment_predeploy_runs exposes %d sensitive payload columns", sensitiveColumns)
	}
}
