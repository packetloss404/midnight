//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/migrations"
)

// TestEnvironmentDuplicatePersistsReplayAndRollsBack proves the config-bearing
// clone against real Postgres. It covers the two failure modes most likely to
// produce a misleading partial environment: process restart during a retry and
// envelope decryption failure after the target metadata/config rows were
// tentatively inserted.
func TestEnvironmentDuplicatePersistsReplayAndRollsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool1 := newBootPool(t, ctx, dsn)
	runner := &pg.MigrationRunner{Pool: pool1, FS: migrations.FS, ManifestPath: "manifest.yaml"}
	if _, err := runner.Up(ctx); err != nil {
		pool1.Close()
		t.Fatalf("apply migrations: %v", err)
	}

	resources := NewPostgresResourceStore(pool1)
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "duplicate-proof", DisplayName: "Duplicate proof"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "api", DisplayName: "API"})
	if err != nil {
		t.Fatal(err)
	}
	source, err := resources.CreateEnvironment(ctx, Environment{ProjectID: project.ID, Slug: "staging", Name: "Staging", Tier: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool1.Exec(ctx,
		`INSERT INTO service_environment_configs (
			project_id, service_id, environment_id, version, builder,
			start_command, target_port, health_kind, health_path
		 ) VALUES ($1,$2,$3,7,'railpack','./serve',9090,'http','/ready')`,
		project.ID, service.ID, source.ID,
	); err != nil {
		t.Fatalf("seed service config: %v", err)
	}

	keyMaterial := []byte("0123456789abcdef0123456789abcdef")
	keyProvider1, err := secrets.NewMemoryKeyProvider(keyMaterial, 1)
	if err != nil {
		t.Fatal(err)
	}
	envelopes1 := secrets.NewPostgresEnvelopeStore(pool1)
	vault := secrets.NewVaultWithStore(keyProvider1, secrets.DenyByDefault{}, envelopes1)
	first, err := vault.Set(ctx, secrets.VariableInput{
		Name: "TOKEN", Project: string(project.ID), Environment: string(source.ID),
		Service: string(service.ID), Scope: secrets.ScopeSecret,
		Plaintext: []byte("first-value"), ValueKind: "literal",
	})
	if err != nil {
		t.Fatalf("seed variable v1: %v", err)
	}
	second, err := vault.Set(ctx, secrets.VariableInput{
		Name: "TOKEN", Project: string(project.ID), Environment: string(source.ID),
		Service: string(service.ID), Scope: secrets.ScopeSecret,
		Plaintext: []byte("second-value"), ValueKind: "literal",
	})
	if err != nil {
		t.Fatalf("seed variable v2: %v", err)
	}
	if first.Meta().ID != second.Meta().ID || second.Meta().Version != 2 {
		t.Fatalf("source variable lifecycle = first %#v second %#v", first.Meta(), second.Meta())
	}

	if _, err := pool1.Exec(ctx,
		`INSERT INTO domains (
			id, project_id, environment_id, service_id, kind, hostname,
			normalized_hostname, tls_desired, tls_state, desired_revision
		 ) VALUES ('dom_duplicate_proof',$1,$2,$3,'custom','api.example.test','api.example.test',false,'disabled',1)`,
		project.ID, source.ID, service.ID,
	); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	if _, err := pool1.Exec(ctx,
		`INSERT INTO volumes (
			id, project_id, environment_id, service_id, name, status
		 ) VALUES ('vol_duplicate_proof',$1,$2,$3,'data','ready')`,
		project.ID, source.ID, service.ID,
	); err != nil {
		t.Fatalf("seed volume: %v", err)
	}
	if _, err := pool1.Exec(ctx,
		`INSERT INTO deployments (
			id, project_id, service_id, environment_id, action, source_type,
			state, active, trigger_key
		 ) VALUES ('dep_duplicate_proof',$1,$2,$3,'deploy_image','image','queued',false,'duplicate-proof-deploy')`,
		project.ID, service.ID, source.ID,
	); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}

	request := EnvironmentDuplicateRequest{
		SourceID: source.ID, Slug: "qa", Name: "QA", Tier: "staging",
		IdempotencyKey: idempotency.Key("environment-duplicate-proof"),
	}
	copier1 := NewPostgresEnvironmentDuplicator(pool1, keyProvider1)
	firstResult, err := copier1.DuplicateEnvironment(ctx, request)
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if firstResult.Replayed || !firstResult.CopyConfig ||
		firstResult.CopiedServiceConfigs != 1 || firstResult.CopiedVariables != 1 ||
		firstResult.CopiedVariableVersions != 2 {
		t.Fatalf("first result = %#v", firstResult)
	}
	for _, resource := range []string{"domains", "volumes", "deployments"} {
		if omissionCount(firstResult.OmittedResources, resource) != 1 {
			t.Fatalf("omissions = %#v, want %s count 1", firstResult.OmittedResources, resource)
		}
	}
	var copiedConfigVersion int
	if err := pool1.QueryRow(ctx,
		`SELECT version FROM service_environment_configs WHERE service_id=$1 AND environment_id=$2`,
		service.ID, firstResult.ID,
	).Scan(&copiedConfigVersion); err != nil || copiedConfigVersion != 7 {
		t.Fatalf("copied config version=%d err=%v", copiedConfigVersion, err)
	}
	targetVariable, err := envelopes1.Find(ctx, string(project.ID), string(firstResult.ID), string(service.ID), "TOKEN")
	if err != nil {
		t.Fatalf("find target variable: %v", err)
	}
	if targetVariable.Meta.ID == second.Meta().ID {
		t.Fatal("target variable reused source ID; AAD must be rebound to a fresh ID")
	}
	sourcePersisted, err := envelopes1.Get(ctx, second.Meta().ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(targetVariable.Envelope.Ciphertext, sourcePersisted.Envelope.Ciphertext) {
		t.Fatal("target ciphertext was copied instead of re-encrypted")
	}
	plaintext, err := secrets.OpenForVariable(ctx, keyProvider1, targetVariable.Meta.ID, targetVariable.Envelope)
	if err != nil {
		t.Fatalf("open target envelope: %v", err)
	}
	if string(plaintext) != "second-value" {
		t.Fatalf("target plaintext = %q", plaintext)
	}
	zeroDuplicatePlaintext(plaintext)
	targetV1, err := envelopes1.GetVersion(ctx, targetVariable.Meta.ID, 1)
	if err != nil {
		t.Fatalf("find target v1: %v", err)
	}
	v1Plaintext, err := secrets.OpenForVariable(ctx, keyProvider1, targetVariable.Meta.ID, targetV1.Envelope)
	if err != nil {
		t.Fatalf("open target v1: %v", err)
	}
	if string(v1Plaintext) != "first-value" {
		t.Fatalf("target v1 plaintext = %q", v1Plaintext)
	}
	zeroDuplicatePlaintext(v1Plaintext)

	// The target/config rows are inserted before variable decryption. A key
	// provider that cannot open the source must still roll back every row and
	// leave no replay reservation.
	badProvider, err := secrets.NewMemoryKeyProvider([]byte("abcdef0123456789abcdef0123456789"), 1)
	if err != nil {
		t.Fatal(err)
	}
	rollbackRequest := EnvironmentDuplicateRequest{
		SourceID: source.ID, Slug: "must-rollback",
		IdempotencyKey: idempotency.Key("environment-duplicate-rollback"),
	}
	if _, err := NewPostgresEnvironmentDuplicator(pool1, badProvider).DuplicateEnvironment(ctx, rollbackRequest); err == nil {
		t.Fatal("duplicate with wrong key provider succeeded")
	}
	assertDuplicateRowsAbsent(t, ctx, pool1, project.ID, "must-rollback", rollbackRequest.IdempotencyKey)

	// Close the first pool to prove replay is not process memory.
	targetID := firstResult.ID
	pool1.Close()
	keyProvider2, err := secrets.NewMemoryKeyProvider(keyMaterial, 1)
	if err != nil {
		t.Fatal(err)
	}
	pool2 := newBootPool(t, ctx, dsn)
	defer pool2.Close()
	replayed, err := NewPostgresEnvironmentDuplicator(pool2, keyProvider2).DuplicateEnvironment(ctx, request)
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if !replayed.Replayed || replayed.ID != targetID ||
		replayed.CopiedVariables != firstResult.CopiedVariables ||
		replayed.CopiedVariableVersions != firstResult.CopiedVariableVersions {
		t.Fatalf("replayed result=%#v first=%#v", replayed, firstResult)
	}
	var targetCount int
	if err := pool2.QueryRow(ctx,
		`SELECT count(*) FROM environments WHERE project_id=$1 AND slug='qa' AND deleted_at IS NULL`,
		project.ID,
	).Scan(&targetCount); err != nil || targetCount != 1 {
		t.Fatalf("target count=%d err=%v", targetCount, err)
	}
	conflicting := request
	conflicting.Slug = "other"
	if _, err := NewPostgresEnvironmentDuplicator(pool2, keyProvider2).DuplicateEnvironment(ctx, conflicting); !errors.Is(err, ErrEnvironmentDuplicateIdempotencyConflict) {
		t.Fatalf("different-body replay error=%v, want idempotency conflict", err)
	}
}

func omissionCount(omissions []EnvironmentDuplicateOmission, resource string) int {
	for _, omission := range omissions {
		if omission.Resource == resource {
			return omission.Count
		}
	}
	return -1
}

func assertDuplicateRowsAbsent(t *testing.T, ctx context.Context, pool *pg.Pool, projectID any, slug string, key idempotency.Key) {
	t.Helper()
	var environments, reservations int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM environments WHERE project_id=$1 AND slug=$2`,
		projectID, slug,
	).Scan(&environments); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM idempotency_keys WHERE key=$1`,
		string(key),
	).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if environments != 0 || reservations != 0 {
		t.Fatalf("rollback left environments=%d reservations=%d", environments, reservations)
	}
}
