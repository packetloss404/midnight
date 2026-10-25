//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestPostgresOperationListPageUsesCreatedAtIDKeyset(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}

	createdAt := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	store := NewPostgresOperationStore(pool)
	store.Now = func() time.Time { return createdAt }
	for _, operation := range []orailtypes.Operation{
		{ID: "op_b", Kind: "deploy", State: orailtypes.OperationQueued},
		{ID: "op_a", Kind: "deploy", State: orailtypes.OperationQueued},
		{ID: "op_c", Kind: "backup", State: orailtypes.OperationQueued},
	} {
		created, err := store.Create(ctx, operation)
		if err != nil {
			t.Fatal(err)
		}
		if !created.CreatedAt.Equal(createdAt) || !created.UpdatedAt.Equal(createdAt) {
			t.Fatalf("operation timestamps = %s/%s, want %s", created.CreatedAt, created.UpdatedAt, createdAt)
		}
	}

	first, err := store.ListPage(ctx, OperationListFilter{Kind: "deploy"}, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !first.HasMore || len(first.Records) != 1 || first.Records[0].Operation.ID != "op_a" {
		t.Fatalf("first page = %#v, want op_a with more", first)
	}
	after := OperationListCursor{CreatedAt: first.Records[0].CreatedAt, ID: first.Records[0].Operation.ID}
	second, err := store.ListPage(ctx, OperationListFilter{Kind: "deploy"}, &after, 1)
	if err != nil {
		t.Fatal(err)
	}
	if second.HasMore || len(second.Records) != 1 || second.Records[0].Operation.ID != "op_b" {
		t.Fatalf("second page = %#v, want op_b", second)
	}
}
