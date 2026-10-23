//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package dns

import (
	"context"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresOwnershipPersistsAcrossStoreRestartAndRestrictsScopeDeletion(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("midnight"),
		tcpostgres.WithUsername("midnight_test"),
		tcpostgres.WithPassword("midnight_test"),
		tc.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		t.Fatal(err)
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
	defer pool.Close()
	if _, err := (&dbpg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO projects(id,workspace_id,slug,display_name)
VALUES ('prj_dns','wrk_default','dns','DNS');
INSERT INTO environments(id,project_id,slug,name)
VALUES ('env_dns','prj_dns','production','Production')`); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	store := NewPostgresOwnershipStore(pool)
	store.Now = func() time.Time { return now }
	ownership := Ownership{
		Provider: "cloudflare", ZoneID: "zone-1", OwnerKind: "domain", OwnerID: "dom-1",
		Scope:            Scope{WorkspaceID: "wrk_default", ProjectID: "prj_dns", EnvironmentID: "env_dns"},
		ProviderRecordID: "record-1", Type: TypeTXT, Name: "_midnight.api.example.com",
		ContentSHA256: contentDigest("midnight-verification=value"), TTL: 300,
	}
	if err := store.PutOwnership(ctx, ownership); err != nil {
		t.Fatal(err)
	}

	restarted := NewPostgresOwnershipStore(pool)
	rows, err := restarted.ListOwnerships(ctx, "cloudflare", "zone-1", "domain", "dom-1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
	if rows[0].ContentSHA256 != ownership.ContentSHA256 || rows[0].CreatedAt != now || rows[0].UpdatedAt != now {
		t.Fatalf("persisted ownership=%#v", rows[0])
	}
	if _, err := pool.Exec(ctx, `DELETE FROM projects WHERE id='prj_dns'`); err == nil {
		t.Fatal("project deletion discarded live provider ownership evidence")
	}

	updated := rows[0]
	updated.ProviderRecordID = "record-2"
	updated.ContentSHA256 = contentDigest("midnight-verification=updated")
	updated.UpdatedAt = now.Add(time.Minute)
	if err := restarted.PutOwnership(ctx, updated); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ListOwnerships(ctx, "cloudflare", "zone-1", "domain", "dom-1")
	if err != nil || len(rows) != 1 || rows[0].ProviderRecordID != "record-2" || rows[0].CreatedAt != now || rows[0].UpdatedAt != now.Add(time.Minute) {
		t.Fatalf("updated ownership=%#v err=%v", rows, err)
	}
	if err := store.DeleteOwnership(ctx, "cloudflare", "zone-1", "domain", "dom-1", TypeTXT, "_midnight.api.example.com"); err != nil {
		t.Fatal(err)
	}
	rows, err = restarted.ListOwnerships(ctx, "cloudflare", "zone-1", "domain", "dom-1")
	if err != nil || len(rows) != 0 {
		t.Fatalf("deleted rows=%#v err=%v", rows, err)
	}
}
