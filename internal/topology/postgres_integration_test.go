//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package topology

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresReplaceRejectsStaleServiceInCASWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("midnight"),
		tcpostgres.WithUsername("midnight_test"),
		tcpostgres.WithPassword("midnight_test"),
		tc.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "docker") {
			t.Skipf("docker unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	for _, statement := range []string{
		`INSERT INTO workspaces(id,slug,display_name,created_at,updated_at) VALUES ('wrk_topology','topology','Topology',$1,$1)`,
		`INSERT INTO projects(id,workspace_id,slug,display_name,created_at,updated_at) VALUES ('prj_topology','wrk_topology','app','App',$1,$1)`,
		`INSERT INTO environments(id,project_id,slug,name,tier,created_at,updated_at) VALUES ('env_topology','prj_topology','main','Main','development',$1,$1)`,
		`INSERT INTO services(id,project_id,slug,display_name,created_at,updated_at) VALUES ('svc_topology','prj_topology','api','API',$1,$1)`,
	} {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatal(err)
		}
	}
	store := NewPostgresStore(pool)
	layout := Layout{WorkspaceID: "wrk_topology", ProjectID: "prj_topology", EnvironmentID: "env_topology", Nodes: []Node{{ServiceID: "svc_topology", X: 10, Y: 20}}}
	created, err := store.Replace(ctx, layout, 0)
	if err != nil || created.Version != 1 {
		t.Fatalf("create=(%+v,%v)", created, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE services SET deleted_at=$1 WHERE id='svc_topology'`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Replace(ctx, layout, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("stale service replacement err=%v", err)
	}
}
