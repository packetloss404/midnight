//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package pg_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/secrets"
)

func TestPostgresVariableTransactionPrimitivesScopeRollbackCASAndTombstone(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	resources := api.NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, api.Project{
		WorkspaceID: api.DefaultWorkspaceID,
		Slug:        "variable-transaction",
	})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := resources.CreateEnvironment(ctx, api.Environment{
		ProjectID: project.ID,
		Slug:      "production",
		Tier:      "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, api.Service{
		ProjectID: project.ID,
		Slug:      "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherProject, err := resources.CreateProject(ctx, api.Project{
		WorkspaceID: api.DefaultWorkspaceID,
		Slug:        "variable-transaction-other",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherService, err := resources.CreateService(ctx, api.Service{
		ProjectID: otherProject.ID,
		Slug:      "other",
	})
	if err != nil {
		t.Fatal(err)
	}

	keyProvider, err := secrets.NewMemoryKeyProvider(
		[]byte("transaction-variable-kek-0000000"),
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	store := secrets.NewPostgresEnvelopeStore(pool)
	scope := secrets.VariableOwnershipScope{
		Workspace:   api.DefaultWorkspaceID,
		Project:     string(project.ID),
		Environment: string(environment.ID),
		Service:     string(service.ID),
	}
	prepare := func(id secrets.VariableID, name string, version int, value string, ownership secrets.VariableOwnershipScope) secrets.PersistedVariable {
		t.Helper()
		envelope, sealErr := secrets.SealForVariable(ctx, keyProvider, id, []byte(value))
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		fingerprint, fingerprintErr := keyProvider.Fingerprint(ctx)
		if fingerprintErr != nil {
			t.Fatal(fingerprintErr)
		}
		return secrets.PersistedVariable{
			Meta: secrets.VariableMeta{
				ID:             id,
				Workspace:      ownership.Workspace,
				Project:        ownership.Project,
				Environment:    ownership.Environment,
				Service:        ownership.Service,
				Name:           name,
				Scope:          secrets.ScopeSealed,
				ValueKind:      "literal",
				Version:        version,
				LengthBucket:   secrets.LengthBucket(len(value)),
				KEKFingerprint: fingerprint,
				LastRotatedAt:  time.Now().UTC().Truncate(time.Microsecond),
			},
			Envelope: envelope,
		}
	}

	const firstPlaintext = "transaction-first-plaintext"
	first := prepare("var_transaction_token", "TOKEN", 1, firstPlaintext, scope)
	rollback := errors.New("force variable insert rollback")
	err = pool.WithTx(ctx, func(tx *pg.Tx) error {
		if insertErr := store.InsertPreparedInTx(ctx, tx, scope, first); insertErr != nil {
			return insertErr
		}
		metas, listErr := store.ListMetadataInTx(ctx, tx, scope)
		if listErr != nil {
			return listErr
		}
		if len(metas) != 1 || metas[0].ID != first.Meta.ID || metas[0].Version != 1 {
			t.Fatalf("metadata inside rolled-back insert = %#v", metas)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("insert rollback error = %v", err)
	}
	assertVariableRowCounts(t, ctx, pool, first.Meta.ID, 0, 0)

	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		if insertErr := store.InsertPreparedInTx(ctx, tx, scope, first); insertErr != nil {
			return insertErr
		}
		got, getErr := store.GetActiveInTx(ctx, tx, scope, first.Meta.ID)
		if getErr != nil {
			return getErr
		}
		if got.Meta.ID != first.Meta.ID ||
			got.Meta.Workspace != scope.Workspace ||
			got.Meta.Version != 1 ||
			bytes.Equal(got.Envelope.Ciphertext, []byte(firstPlaintext)) {
			t.Fatalf("inserted encrypted row = %#v", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("insert commit: %v", err)
	}
	assertVariableRowCounts(t, ctx, pool, first.Meta.ID, 1, 1)

	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		duplicate := prepare("var_transaction_duplicate", "TOKEN", 1, "duplicate", scope)
		insertErr := store.InsertPreparedInTx(ctx, tx, scope, duplicate)
		var conflict *secrets.VariableVersionConflict
		if !errors.As(insertErr, &conflict) || conflict.Expected != 0 || conflict.Actual != 1 {
			t.Fatalf("duplicate insert error = %#v", insertErr)
		}
		var one int
		if queryErr := tx.QueryRow(ctx, `SELECT 1`).Scan(&one); queryErr != nil || one != 1 {
			t.Fatalf("transaction unusable after duplicate conflict: one=%d err=%v", one, queryErr)
		}
		return nil
	}); err != nil {
		t.Fatalf("duplicate conflict transaction: %v", err)
	}

	wrongServiceScope := scope
	wrongServiceScope.Service = string(otherService.ID)
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		_, getErr := store.GetActiveInTx(ctx, tx, wrongServiceScope, first.Meta.ID)
		if !errors.Is(getErr, secrets.ErrVariableScopeIntegrity) {
			t.Fatalf("cross-project service get error = %v", getErr)
		}
		var one int
		if queryErr := tx.QueryRow(ctx, `SELECT 1`).Scan(&one); queryErr != nil || one != 1 {
			t.Fatalf("transaction unusable after scope rejection: one=%d err=%v", one, queryErr)
		}
		return nil
	}); err != nil {
		t.Fatalf("scope rejection transaction: %v", err)
	}

	const secondPlaintext = "transaction-second-plaintext"
	second := prepare(first.Meta.ID, first.Meta.Name, 2, secondPlaintext, scope)
	err = pool.WithTx(ctx, func(tx *pg.Tx) error {
		if updateErr := store.UpdatePreparedCASInTx(ctx, tx, scope, second, 1); updateErr != nil {
			return updateErr
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("update rollback error = %v", err)
	}
	assertActiveVariableVersion(t, ctx, pool, first.Meta.ID, 1)
	assertVariableRowCounts(t, ctx, pool, first.Meta.ID, 1, 1)

	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		return store.UpdatePreparedCASInTx(ctx, tx, scope, second, 1)
	}); err != nil {
		t.Fatalf("update commit: %v", err)
	}
	assertActiveVariableVersion(t, ctx, pool, first.Meta.ID, 2)
	assertVariableRowCounts(t, ctx, pool, first.Meta.ID, 1, 2)

	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		staleErr := store.UpdatePreparedCASInTx(ctx, tx, scope, second, 1)
		var conflict *secrets.VariableVersionConflict
		if !errors.As(staleErr, &conflict) || conflict.Expected != 1 || conflict.Actual != 2 {
			t.Fatalf("stale update error = %#v", staleErr)
		}
		var one int
		if queryErr := tx.QueryRow(ctx, `SELECT 1`).Scan(&one); queryErr != nil || one != 1 {
			t.Fatalf("transaction unusable after CAS conflict: one=%d err=%v", one, queryErr)
		}
		return nil
	}); err != nil {
		t.Fatalf("stale CAS transaction: %v", err)
	}

	err = pool.WithTx(ctx, func(tx *pg.Tx) error {
		if deleteErr := store.TombstoneCASInTx(ctx, tx, scope, first.Meta.ID, 2); deleteErr != nil {
			return deleteErr
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("tombstone rollback error = %v", err)
	}
	assertActiveVariableVersion(t, ctx, pool, first.Meta.ID, 2)

	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		return store.TombstoneCASInTx(ctx, tx, scope, first.Meta.ID, 2)
	}); err != nil {
		t.Fatalf("tombstone commit: %v", err)
	}
	assertVariableRowCounts(t, ctx, pool, first.Meta.ID, 1, 2)
	var activeCiphertext, activeWrapped []byte
	var deletedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT value_ciphertext, wrapped_dek, deleted_at
		  FROM service_variables
		 WHERE id = $1`,
		first.Meta.ID,
	).Scan(&activeCiphertext, &activeWrapped, &deletedAt); err != nil {
		t.Fatal(err)
	}
	if len(activeCiphertext) != 0 || len(activeWrapped) != 0 || deletedAt == nil {
		t.Fatal("committed tombstone retained active encrypted material")
	}
	historical, err := store.GetVersion(ctx, first.Meta.ID, 1)
	if err != nil {
		t.Fatalf("historical version after tombstone: %v", err)
	}
	if historical.Meta.Version != 1 || len(historical.Envelope.Ciphertext) == 0 {
		t.Fatalf("historical version after tombstone = %#v", historical)
	}

	sharedScope := scope
	sharedScope.Service = ""
	shared := prepare("var_transaction_shared", "SHARED_TOKEN", 1, "shared-value", sharedScope) // not-a-failure-code
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		if insertErr := store.InsertPreparedInTx(ctx, tx, sharedScope, shared); insertErr != nil {
			return insertErr
		}
		serviceMetas, listErr := store.ListMetadataInTx(ctx, tx, scope)
		if listErr != nil {
			return listErr
		}
		if len(serviceMetas) != 0 {
			t.Fatalf("service metadata included shared rows: %#v", serviceMetas)
		}
		sharedMetas, listErr := store.ListMetadataInTx(ctx, tx, sharedScope)
		if listErr != nil {
			return listErr
		}
		if len(sharedMetas) != 1 ||
			sharedMetas[0].ID != shared.Meta.ID ||
			sharedMetas[0].Service != "" {
			t.Fatalf("shared metadata = %#v", sharedMetas)
		}
		return nil
	}); err != nil {
		t.Fatalf("shared sentinel commit: %v", err)
	}
}

func assertVariableRowCounts(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	id secrets.VariableID,
	wantActiveRows int,
	wantVersions int,
) {
	t.Helper()
	var activeRows, versions int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM service_variables
		 WHERE id = $1`,
		id,
	).Scan(&activeRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM service_variable_versions
		 WHERE variable_id = $1`,
		id,
	).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if activeRows != wantActiveRows || versions != wantVersions {
		t.Fatalf(
			"variable %s rows = active:%d versions:%d, want active:%d versions:%d",
			id,
			activeRows,
			versions,
			wantActiveRows,
			wantVersions,
		)
	}
}

func assertActiveVariableVersion(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	id secrets.VariableID,
	want int,
) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, `
		SELECT version
		  FROM service_variables
		 WHERE id = $1
		   AND deleted_at IS NULL`,
		id,
	).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("variable %s active version = %d, want %d", id, got, want)
	}
}
