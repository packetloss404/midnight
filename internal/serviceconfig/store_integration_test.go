//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package serviceconfig

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	migrationfs "github.com/midnight/midnight/migrations"
)

func TestPostgresConfigCASAndImmutableSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	containerIDBytes, err := exec.CommandContext(
		ctx,
		"docker", "run", "--detach", "--rm",
		"--env", "POSTGRES_DB=midnight",
		"--env", "POSTGRES_USER=midnight_test",
		"--env", "POSTGRES_PASSWORD=midnight_test",
		"--publish", "127.0.0.1::5432",
		"postgres:16-alpine",
	).CombinedOutput()
	if err != nil {
		t.Skipf("Docker/Postgres unavailable: %v: %s", err, containerIDBytes)
	}
	containerID := strings.TrimSpace(string(containerIDBytes))
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "--force", containerID).Run()
	})
	dsn := waitForPostgresDSN(t, ctx, containerID)
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 4, ConnectTimeout: time.Second})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrationfs.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedConfigScope(t, ctx, pool)

	store := NewStore(pool)
	cfg := Default("prj_1", "svc_1", "env_1")
	cfg.RootDirectory = "apps/api"
	cfg.Builder = BuilderDockerfile
	cfg.DockerfilePath = "deploy/App.Dockerfile"
	cfg.StartCommand = "bin/api serve"
	cfg.TargetPort = 9090
	cfg.HealthKind = HealthHTTP
	cfg.HealthPath = "/ready"
	cfg.RestartPolicy = RestartOnFailure
	cfg.RestartRetries = 4
	created, err := store.Put(ctx, cfg, 0)
	if err != nil {
		t.Fatalf("Put create: %v", err)
	}
	if created.Version != 1 {
		t.Fatalf("created version = %d", created.Version)
	}
	if _, err := store.Put(ctx, cfg, 0); err != ErrConflict {
		t.Fatalf("duplicate create error = %v, want ErrConflict", err)
	}

	var retiredVariableCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM service_variables variable
		  JOIN services service ON service.id=variable.service_id
		 WHERE variable.id='var_deleted'
		   AND variable.deleted_at IS NULL
		   AND service.deleted_at IS NOT NULL`,
	).Scan(&retiredVariableCount); err != nil {
		t.Fatalf("retired service variable fixture: %v", err)
	}
	if retiredVariableCount != 1 {
		t.Fatalf("retired service active variables=%d, want 1", retiredVariableCount)
	}

	first, err := store.ResolveDeployment(ctx, "dep_1", "prj_1", "env_1", "svc_1")
	if err != nil {
		t.Fatalf("ResolveDeployment first: %v", err)
	}
	if first.ConfigVersion != 1 || first.Config.TargetPort != 9090 ||
		first.ConfigurationRevision != 3 ||
		len(first.VariableVersions) != 1 || first.VariableVersions[0].Version != 7 ||
		first.VariableVersions[0].ID != "var_1" ||
		first.VariableVersions[0].ServiceSlug != "api" ||
		first.VariableVersions[0].ServiceDisplayName != "API" ||
		first.VariableSchema != VariableRefsSchema {
		t.Fatalf("first snapshot = %+v", first)
	}
	var boundRevision int64
	if err := pool.QueryRow(ctx, `SELECT configuration_revision FROM deployments WHERE id='dep_1'`).Scan(&boundRevision); err != nil || boundRevision != 3 {
		t.Fatalf("fresh deployment revision=%d err=%v", boundRevision, err)
	}

	cfg.TargetPort = 9191
	if _, err := store.Put(ctx, cfg, 99); err != ErrConflict {
		t.Fatalf("stale update error = %v, want ErrConflict", err)
	}
	updated, err := store.Put(ctx, cfg, 1)
	if err != nil || updated.Version != 2 {
		t.Fatalf("Put update = %+v, %v", updated, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE service_variables SET version=8 WHERE id='var_1'`); err != nil {
		t.Fatalf("rotate variable: %v", err)
	}

	// Construct a fresh repository to model a worker process restart. The
	// deployment must resolve to the already persisted snapshot.
	if _, err := pool.Exec(ctx, `UPDATE deployments SET configuration_revision=0 WHERE id='dep_1'`); err != nil {
		t.Fatalf("simulate pre-revision deployment drift: %v", err)
	}
	restartedStore := NewStore(pool)
	retry, err := restartedStore.ResolveDeployment(ctx, "dep_1", "prj_1", "env_1", "svc_1")
	if err != nil {
		t.Fatalf("ResolveDeployment retry: %v", err)
	}
	if retry.SHA256 != first.SHA256 || retry.ConfigurationRevision != 3 || retry.Config.TargetPort != 9090 ||
		retry.VariableVersions[0].Version != 7 {
		t.Fatalf("retry snapshot drifted: first=%+v retry=%+v", first, retry)
	}
	if err := pool.QueryRow(ctx, `SELECT configuration_revision FROM deployments WHERE id='dep_1'`).Scan(&boundRevision); err != nil || boundRevision != 3 {
		t.Fatalf("snapshot retry revision repair=%d err=%v", boundRevision, err)
	}
	if _, err := store.ResolveDeployment(ctx, "dep_1", "prj_other", "env_1", "svc_1"); err != ErrScope {
		t.Fatalf("cross-scope retry error = %v, want ErrScope", err)
	}

	var committed Config
	updated.TargetPort = 9292
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		var putErr error
		committed, putErr = store.PutInTx(ctx, tx, updated, 2)
		if putErr != nil {
			return fmt.Errorf("PutInTx commit: %w", putErr)
		}
		inside, getErr := store.GetInTx(ctx, tx, "prj_1", "env_1", "svc_1")
		if getErr != nil {
			return fmt.Errorf("GetInTx committed write: %w", getErr)
		}
		if inside.Version != 3 || inside.TargetPort != 9292 {
			return fmt.Errorf("GetInTx committed write = %+v", inside)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	afterCommit, err := store.Get(ctx, "prj_1", "env_1", "svc_1")
	if err != nil {
		t.Fatalf("Get after caller commit: %v", err)
	}
	if committed.Version != 3 ||
		afterCommit.Version != committed.Version ||
		afterCommit.TargetPort != committed.TargetPort {
		t.Fatalf("committed=%+v after=%+v", committed, afterCommit)
	}

	rollback := errors.New("force service config rollback")
	rollbackConfig := committed
	rollbackConfig.TargetPort = 9393
	err = pool.WithTx(ctx, func(tx *pg.Tx) error {
		saved, putErr := store.PutInTx(ctx, tx, rollbackConfig, 3)
		if putErr != nil {
			return fmt.Errorf("PutInTx rollback: %w", putErr)
		}
		inside, getErr := store.GetInTx(ctx, tx, "prj_1", "env_1", "svc_1")
		if getErr != nil {
			return fmt.Errorf("GetInTx rolled-back write: %w", getErr)
		}
		if saved.Version != 4 || inside.Version != 4 || inside.TargetPort != 9393 {
			return fmt.Errorf("rolled-back write saved=%+v inside=%+v", saved, inside)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("rollback error = %v, want sentinel", err)
	}
	afterRollback, err := store.Get(ctx, "prj_1", "env_1", "svc_1")
	if err != nil {
		t.Fatalf("Get after caller rollback: %v", err)
	}
	if afterRollback.Version != 3 || afterRollback.TargetPort != 9292 {
		t.Fatalf("rolled-back config persisted: %+v", afterRollback)
	}

	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		if _, putErr := store.PutInTx(ctx, tx, rollbackConfig, 2); !errors.Is(putErr, ErrConflict) {
			return fmt.Errorf("PutInTx stale CAS error = %v, want ErrConflict", putErr)
		}
		current, getErr := store.GetInTx(ctx, tx, "prj_1", "env_1", "svc_1")
		if getErr != nil {
			return fmt.Errorf("GetInTx after stale CAS: %w", getErr)
		}
		if current.Version != 3 || current.TargetPort != 9292 {
			return fmt.Errorf("config changed after stale CAS: %+v", current)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func waitForPostgresDSN(t *testing.T, ctx context.Context, containerID string) string {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		output, err := exec.CommandContext(ctx, "docker", "port", containerID, "5432/tcp").CombinedOutput()
		if err == nil {
			binding := strings.TrimSpace(string(output))
			index := strings.LastIndex(binding, ":")
			if index > 0 {
				port, parseErr := strconv.Atoi(binding[index+1:])
				if parseErr == nil && port > 0 {
					dsn := fmt.Sprintf("postgres://midnight_test:midnight_test@127.0.0.1:%d/midnight?sslmode=disable", port)
					probe, openErr := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 1, ConnectTimeout: time.Second})
					if openErr == nil {
						pingErr := probe.Ping(ctx)
						probe.Close()
						if pingErr == nil {
							return dsn
						}
						lastErr = pingErr
					} else {
						lastErr = openErr
					}
				}
			}
		} else {
			lastErr = fmt.Errorf("%v: %s", err, output)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for Postgres: %v", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Fatalf("Postgres did not become ready: %v", lastErr)
	return ""
}

func seedConfigScope(t *testing.T, ctx context.Context, pool *pg.Pool) {
	t.Helper()
	statements := []string{
		`INSERT INTO projects (id,workspace_id,slug,display_name) VALUES ('prj_1','wrk_default','one','One')`,
		`INSERT INTO environments (id,project_id,slug,name) VALUES ('env_1','prj_1','prod','Production')`,
		`INSERT INTO services (id,project_id,slug,display_name) VALUES ('svc_1','prj_1','api','API')`,
		`INSERT INTO services (id,project_id,slug,display_name) VALUES ('svc_deleted','prj_1','retired','Retired')`,
		`INSERT INTO operations (id,kind,state) VALUES ('op_1','deploy','QUEUED')`,
		`INSERT INTO deployments (
			id,project_id,service_id,environment_id,action,state,operation_id
		) VALUES ('dep_1','prj_1','svc_1','env_1','deploy_image','queued','op_1')`,
		`INSERT INTO environment_configuration_revisions (
			environment_id,project_id,workspace_id,applied_revision
		) VALUES ('env_1','prj_1','wrk_default',3)`,
		`INSERT INTO service_configuration_revisions (
			project_id,environment_id,service_id,applied_revision,deployed_revision
		) VALUES ('prj_1','env_1','svc_1',3,0)`,
		`INSERT INTO service_variables (
			id,project_id,environment_id,service_id,name,value_ciphertext,
			wrapped_dek,version
		) VALUES (
				'var_1','prj_1','env_1','svc_1','TOKEN',
				decode('00','hex'),decode('00','hex'),7
			)`,
		`INSERT INTO service_variables (
			id,project_id,environment_id,service_id,name,value_ciphertext,
			wrapped_dek,version
		) VALUES (
			'var_deleted','prj_1','env_1','svc_deleted','STALE_TOKEN',
			decode('00','hex'),decode('00','hex'),3
		)`,
		`UPDATE services SET deleted_at=CURRENT_TIMESTAMP WHERE id='svc_deleted'`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("seed %q: %v", statement, err)
		}
	}
}
