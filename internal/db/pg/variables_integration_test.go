//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package pg_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/varrefs"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestPostgresVariableVaultRestartScopeIntegrityAndEncryption(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool1 := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool1)
	resources := api.NewPostgresResourceStore(pool1)
	project1, err := resources.CreateProject(ctx, api.Project{WorkspaceID: api.DefaultWorkspaceID, Slug: "variables-one"})
	if err != nil {
		t.Fatalf("CreateProject one: %v", err)
	}
	project2, err := resources.CreateProject(ctx, api.Project{WorkspaceID: api.DefaultWorkspaceID, Slug: "variables-two"})
	if err != nil {
		t.Fatalf("CreateProject two: %v", err)
	}
	environment1, err := resources.CreateEnvironment(ctx, api.Environment{
		ProjectID: project1.ID,
		Slug:      "production",
		Tier:      "production",
	})
	if err != nil {
		t.Fatalf("CreateEnvironment one: %v", err)
	}
	service1, err := resources.CreateService(ctx, api.Service{
		ProjectID: project1.ID,
		Slug:      "api",
	})
	if err != nil {
		t.Fatalf("CreateService one: %v", err)
	}
	service2, err := resources.CreateService(ctx, api.Service{
		ProjectID: project2.ID,
		Slug:      "other",
	})
	if err != nil {
		t.Fatalf("CreateService two: %v", err)
	}

	key := []byte("postgres-variable-store-kek-0000")
	provider1, err := secrets.NewMemoryKeyProvider(key, 1)
	if err != nil {
		t.Fatalf("NewMemoryKeyProvider: %v", err)
	}
	vault1 := secrets.NewVaultWithStore(provider1, secrets.AllowAll{}, secrets.NewPostgresEnvelopeStore(pool1))
	const plaintext = "postgres://worker:never-persist-me@db/app"
	created, err := vault1.Set(ctx, secrets.VariableInput{
		Name:        "DATABASE_URL",
		Workspace:   api.DefaultWorkspaceID,
		Project:     string(project1.ID),
		Environment: string(environment1.ID),
		Service:     string(service1.ID),
		Scope:       secrets.ScopeSealed,
		Plaintext:   []byte(plaintext),
		ValueKind:   "literal",
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	var ciphertext, wrappedDEK, references []byte
	if err := pool1.QueryRow(ctx,
		`SELECT value_ciphertext, wrapped_dek, "references"
		 FROM service_variables WHERE id = $1`,
		created.Meta().ID,
	).Scan(&ciphertext, &wrappedDEK, &references); err != nil {
		t.Fatalf("raw variable query: %v", err)
	}
	for label, raw := range map[string][]byte{
		"ciphertext":  ciphertext,
		"wrapped_dek": wrappedDEK,
		"references":  references,
	} {
		if bytes.Contains(raw, []byte(plaintext)) {
			t.Fatalf("%s leaked variable plaintext", label)
		}
	}

	_, err = vault1.Set(ctx, secrets.VariableInput{
		Name:        "cross-project",
		Project:     string(project2.ID),
		Environment: string(environment1.ID),
		Service:     string(service2.ID),
		Scope:       secrets.ScopeSecret,
		Plaintext:   []byte("must-not-write"),
	})
	if !errors.Is(err, secrets.ErrVariableScopeIntegrity) {
		t.Fatalf("cross-project environment error = %v, want ErrVariableScopeIntegrity", err)
	}
	var crossCount int
	if err := pool1.QueryRow(ctx,
		`SELECT count(*) FROM service_variables WHERE name = 'cross-project'`,
	).Scan(&crossCount); err != nil {
		t.Fatalf("cross-project count: %v", err)
	}
	if crossCount != 0 {
		t.Fatalf("cross-project variable rows = %d, want 0", crossCount)
	}
	_, err = vault1.Set(ctx, secrets.VariableInput{
		Name:        "cross_service",
		Project:     string(project1.ID),
		Environment: string(environment1.ID),
		Service:     string(service2.ID),
		Scope:       secrets.ScopeSecret,
		Plaintext:   []byte("must-not-write-either"),
	})
	if !errors.Is(err, secrets.ErrVariableScopeIntegrity) {
		t.Fatalf("cross-project service error = %v, want ErrVariableScopeIntegrity", err)
	}

	pool1.Close()
	pool2, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 2})
	if err != nil {
		t.Fatalf("reopen pool: %v", err)
	}
	defer pool2.Close()
	if err := pool2.Ping(ctx); err != nil {
		t.Fatalf("reopen ping: %v", err)
	}
	provider2, err := secrets.NewMemoryKeyProvider(key, 1)
	if err != nil {
		t.Fatalf("restart NewMemoryKeyProvider: %v", err)
	}
	envelopeStore2 := secrets.NewPostgresEnvelopeStore(pool2)
	vault2 := secrets.NewVaultWithStore(provider2, secrets.AllowAll{}, envelopeStore2)
	metas, err := vault2.List(ctx)
	if err != nil {
		t.Fatalf("List after restart: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != created.Meta().ID ||
		metas[0].Project != string(project1.ID) ||
		metas[0].Workspace != api.DefaultWorkspaceID ||
		metas[0].Environment != string(environment1.ID) ||
		metas[0].Service != string(service1.ID) {
		t.Fatalf("metadata after restart = %#v", metas)
	}
	value, _, err := vault2.Decrypt(ctx, metas[0].ID, secrets.ScopeRuntimeInject)
	if err != nil {
		t.Fatalf("Decrypt after restart: %v", err)
	}
	if string(value) != plaintext {
		t.Fatalf("decrypted restart value mismatch for %s", orailtypes.ID(metas[0].ID))
	}
	resolved, err := varrefs.NewResolver(secrets.NewRuntimeLookup(vault2, envelopeStore2)).
		Resolve(ctx, string(project1.ID), string(environment1.ID), string(service1.ID))
	if err != nil {
		t.Fatalf("Resolve runtime variables after restart: %v", err)
	}
	if resolved["DATABASE_URL"] != plaintext {
		t.Fatalf("runtime DATABASE_URL was not injected from durable envelope")
	}

	// Two independent Vault instances model concurrent API processes. Exactly
	// one compare-and-swap may replace v1.
	vault3 := secrets.NewVaultWithStore(provider2, secrets.AllowAll{}, secrets.NewPostgresEnvelopeStore(pool2))
	updateInput := func(value string) secrets.VariableInput {
		return secrets.VariableInput{
			Name:        "DATABASE_URL",
			Workspace:   api.DefaultWorkspaceID,
			Project:     string(project1.ID),
			Environment: string(environment1.ID),
			Service:     string(service1.ID),
			Scope:       secrets.ScopeSealed,
			Plaintext:   []byte(value),
			ValueKind:   "literal",
		}
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i, candidate := range []string{"postgres://candidate-a", "postgres://candidate-b"} {
		wg.Add(1)
		go func(index int, value string) {
			defer wg.Done()
			<-start
			vault := vault2
			if index == 1 {
				vault = vault3
			}
			_, err := vault.Update(ctx, created.Meta().ID, 1, updateInput(value))
			errs <- err
		}(i, candidate)
	}
	close(start)
	wg.Wait()
	close(errs)
	var successes, conflicts int
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, secrets.ErrVariableVersionConflict):
			conflicts++
		default:
			t.Fatalf("concurrent update error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent update outcomes = %d success/%d conflict", successes, conflicts)
	}

	current, err := vault2.Get(ctx, created.Meta().ID)
	if err != nil || current.Meta().Version != 2 {
		t.Fatalf("current after CAS = %#v, %v", current, err)
	}
	if err := vault2.Delete(ctx, created.Meta().ID, 2); err != nil {
		t.Fatalf("Delete v2: %v", err)
	}
	if _, err := vault2.Get(ctx, created.Meta().ID); !errors.Is(err, secrets.ErrVariableNotFound) {
		t.Fatalf("active get after delete = %v, want ErrVariableNotFound", err)
	}
	var activeCiphertext, activeWrapped []byte
	var deletedAt *time.Time
	if err := pool2.QueryRow(ctx,
		`SELECT value_ciphertext, wrapped_dek, deleted_at
		   FROM service_variables WHERE id=$1`,
		created.Meta().ID,
	).Scan(&activeCiphertext, &activeWrapped, &deletedAt); err != nil {
		t.Fatalf("tombstone query: %v", err)
	}
	if len(activeCiphertext) != 0 || len(activeWrapped) != 0 || deletedAt == nil {
		t.Fatalf("active tombstone retained envelope material")
	}

	// The immutable deployment pin still resolves v1 after both a rotation and
	// active deletion. It never falls forward to v2.
	pinnedResolver := varrefs.NewResolver(secrets.NewRuntimeLookup(vault2, envelopeStore2))
	pinned, err := pinnedResolver.ResolveAtVersions(
		ctx,
		string(project1.ID),
		string(environment1.ID),
		string(service1.ID),
		[]varrefs.VariableVersionRef{{
			ID:        string(created.Meta().ID),
			ServiceID: string(service1.ID),
			Name:      "DATABASE_URL",
			Version:   1,
		}},
	)
	if err != nil {
		t.Fatalf("ResolveAtVersions v1 after delete: %v", err)
	}
	if pinned["DATABASE_URL"] != plaintext {
		t.Fatalf("pinned v1 drifted after rotation/delete")
	}
	var historyCount int
	if err := pool2.QueryRow(ctx,
		`SELECT count(*) FROM service_variable_versions WHERE variable_id=$1`,
		created.Meta().ID,
	).Scan(&historyCount); err != nil {
		t.Fatalf("history count: %v", err)
	}
	if historyCount != 2 {
		t.Fatalf("history versions = %d, want 2", historyCount)
	}

	// Tombstones do not reserve a name forever; a new active identity may be
	// created without reviving or mutating the deleted row.
	recreated, err := vault2.Create(ctx, updateInput("postgres://replacement"))
	if err != nil {
		t.Fatalf("recreate after tombstone: %v", err)
	}
	if recreated.Meta().ID == created.Meta().ID || recreated.Meta().Version != 1 {
		t.Fatalf("recreated metadata = %#v", recreated.Meta())
	}
}
