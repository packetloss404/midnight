//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package pg_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
)

func TestTenantVariableScopeIntegrityMigrationRepairsMetadataWithoutRewritingEnvelopes(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newPool(t, ctx, dsn)

	runner50 := migrationRunnerThrough(t, pool, 50)
	applied, err := runner50.Up(ctx)
	if err != nil {
		t.Fatalf("migrations through 0050: %v", err)
	}
	if len(applied) == 0 || applied[len(applied)-1].Version != 50 {
		t.Fatalf("latest pre-repair migration = %#v, want 50", applied)
	}

	resources := api.NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, api.Project{
		WorkspaceID: api.DefaultWorkspaceID,
		Slug:        "variable-scope-upgrade",
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
		Slug:        "variable-scope-upgrade-other",
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

	activeCiphertext := []byte{0x01, 0x00, 0xfe, 0x7f, 0x23}
	activeWrappedDEK := []byte{0xa1, 0x00, 0xb2, 0xc3}
	versionCiphertext := []byte{0x02, 0x00, 0xfd, 0x6e, 0x34}
	versionWrappedDEK := []byte{0xd4, 0x00, 0xe5, 0xf6}
	const variableID = "var_scope_upgrade"
	const variableName = "API_TOKEN"

	if _, err := pool.Exec(ctx, `
		INSERT INTO service_variables (
			id, workspace_id, project_id, environment_id, service_id, name,
			scope, value_kind, value_ciphertext, kek_id, wrapped_dek,
			canonical_form, "references", length_bucket, version,
			kek_fingerprint
		) VALUES (
			$1, 'wrk_stale_parent', $2, $3, $4, $5,
			'secret', 'literal', $6, 'kek-upgrade', $7,
			'midnight-envelope-v1', '[]'::jsonb, '1-16', 1,
			'kek-fingerprint'
		)`,
		variableID,
		project.ID,
		environment.ID,
		service.ID,
		variableName,
		activeCiphertext,
		activeWrappedDEK,
	); err != nil {
		t.Fatalf("seed pre-0051 parent scope mismatch: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO service_variable_versions (
			variable_id, version, workspace_id, project_id, environment_id,
			service_id, name, scope, value_kind, value_ciphertext, kek_id,
			wrapped_dek, canonical_form, "references", length_bucket,
			kek_fingerprint, last_rotated_at
		) VALUES (
			$1, 1, 'wrk_stale_version', 'prj_stale_version',
			'env_stale_version', 'svc_stale_version', 'STALE_NAME',
			'secret', 'literal', $2, 'kek-upgrade', $3,
			'midnight-envelope-v1', '[]'::jsonb, '1-16',
			'kek-fingerprint', CURRENT_TIMESTAMP
		)`,
		variableID,
		versionCiphertext,
		versionWrappedDEK,
	); err != nil {
		t.Fatalf("seed pre-0051 version scope mismatch: %v", err)
	}

	beforeActiveCiphertext, beforeActiveWrapped := readVariableEnvelopeBytes(
		t,
		ctx,
		pool,
		`SELECT value_ciphertext, wrapped_dek
		   FROM service_variables
		  WHERE id = $1`,
		variableID,
	)
	beforeVersionCiphertext, beforeVersionWrapped := readVariableEnvelopeBytes(
		t,
		ctx,
		pool,
		`SELECT value_ciphertext, wrapped_dek
		   FROM service_variable_versions
		  WHERE variable_id = $1 AND version = 1`,
		variableID,
	)

	runner51 := migrationRunnerThrough(t, pool, 51)
	applied, err = runner51.Up(ctx)
	if err != nil {
		t.Fatalf("migration 0051 up: %v", err)
	}
	if len(applied) != 1 || applied[0].Version != 51 {
		t.Fatalf("0051 applied migrations = %#v", applied)
	}

	assertVariableOwnership(
		t,
		ctx,
		pool,
		`SELECT workspace_id, project_id, environment_id, service_id, name
		   FROM service_variables
		  WHERE id = $1`,
		variableID,
		api.DefaultWorkspaceID,
		string(project.ID),
		string(environment.ID),
		string(service.ID),
		variableName,
	)
	assertVariableOwnership(
		t,
		ctx,
		pool,
		`SELECT workspace_id, project_id, environment_id, service_id, name
		   FROM service_variable_versions
		  WHERE variable_id = $1 AND version = 1`,
		variableID,
		api.DefaultWorkspaceID,
		string(project.ID),
		string(environment.ID),
		string(service.ID),
		variableName,
	)
	assertVariableEnvelopeBytes(
		t,
		ctx,
		pool,
		variableID,
		beforeActiveCiphertext,
		beforeActiveWrapped,
		beforeVersionCiphertext,
		beforeVersionWrapped,
	)

	if _, err := pool.Exec(ctx, `
		UPDATE service_variables
		   SET workspace_id = 'wrk_direct_writer_spoof'
		 WHERE id = $1`,
		variableID,
	); err != nil {
		t.Fatalf("parent direct-writer trigger repair: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE service_variable_versions
		   SET workspace_id = 'wrk_version_spoof',
		       project_id = 'prj_version_spoof',
		       environment_id = 'env_version_spoof',
		       service_id = 'svc_version_spoof',
		       name = 'VERSION_SPOOF'
		 WHERE variable_id = $1 AND version = 1`,
		variableID,
	); err != nil {
		t.Fatalf("version direct-writer trigger repair: %v", err)
	}
	assertVariableOwnership(
		t,
		ctx,
		pool,
		`SELECT workspace_id, project_id, environment_id, service_id, name
		   FROM service_variable_versions
		  WHERE variable_id = $1 AND version = 1`,
		variableID,
		api.DefaultWorkspaceID,
		string(project.ID),
		string(environment.ID),
		string(service.ID),
		variableName,
	)

	_, err = pool.Exec(ctx, `
		INSERT INTO service_variables (
			id, workspace_id, project_id, environment_id, service_id, name,
			scope, value_kind, value_ciphertext, kek_id, wrapped_dek,
			canonical_form, "references", length_bucket, version,
			kek_fingerprint
		) VALUES (
			'var_cross_project_service', 'wrk_direct_writer_spoof', $1, $2, $3,
			'INVALID_SERVICE_SCOPE', 'secret', 'literal', $4, 'kek-upgrade', $5,
			'midnight-envelope-v1', '[]'::jsonb, '1-16', 1,
			'kek-fingerprint'
		)`,
		project.ID,
		environment.ID,
		otherService.ID,
		activeCiphertext,
		activeWrappedDEK,
	)
	requirePostgresIntegrityError(
		t,
		err,
		"23503",
		"service_variables_service_project_fk",
	)
	assertVariableOwnership(
		t,
		ctx,
		pool,
		`SELECT workspace_id, project_id, environment_id, service_id, name
		   FROM service_variables
		  WHERE id = $1`,
		variableID,
		api.DefaultWorkspaceID,
		string(project.ID),
		string(environment.ID),
		string(service.ID),
		variableName,
	)

	_, err = pool.Exec(ctx, `
		INSERT INTO service_variable_versions (
			variable_id, version, workspace_id, project_id, environment_id,
			service_id, name, scope, value_kind, value_ciphertext, kek_id,
			wrapped_dek, canonical_form, "references", length_bucket,
			kek_fingerprint, last_rotated_at
		) VALUES (
			'var_missing_parent', 1, 'wrk_spoof', 'prj_spoof', 'env_spoof',
			'svc_spoof', 'ORPHAN', 'secret', 'literal', $1, 'kek-upgrade',
			$2, 'midnight-envelope-v1', '[]'::jsonb, '1-16',
			'kek-fingerprint', CURRENT_TIMESTAMP
		)`,
		versionCiphertext,
		versionWrappedDEK,
	)
	requirePostgresIntegrityError(t, err, "23503", "")

	assertVariableEnvelopeBytes(
		t,
		ctx,
		pool,
		variableID,
		beforeActiveCiphertext,
		beforeActiveWrapped,
		beforeVersionCiphertext,
		beforeVersionWrapped,
	)

	down, err := runner51.Down(ctx)
	if err != nil || down == nil || down.Version != 51 {
		t.Fatalf("migration 0051 down = %#v, err=%v", down, err)
	}
	reapplied, err := runner51.Up(ctx)
	if err != nil || len(reapplied) != 1 || reapplied[0].Version != 51 {
		t.Fatalf("migration 0051 reapply = %#v, err=%v", reapplied, err)
	}
	assertVariableEnvelopeBytes(
		t,
		ctx,
		pool,
		variableID,
		beforeActiveCiphertext,
		beforeActiveWrapped,
		beforeVersionCiphertext,
		beforeVersionWrapped,
	)
}

func migrationRunnerThrough(
	t *testing.T,
	pool *pg.Pool,
	targetVersion int,
) *pg.MigrationRunner {
	t.Helper()
	files, _, err := pg.LoadOrderedMigrations(
		migrations.FS,
		"manifest.yaml",
	)
	if err != nil {
		t.Fatalf("load migration catalog: %v", err)
	}
	migrationFS := fstest.MapFS{}
	var manifest strings.Builder
	fmt.Fprintf(&manifest, "schema_version: %d\nmigrations:\n", targetVersion)
	latest := 0
	for _, migration := range files {
		if migration.Version > targetVersion {
			continue
		}
		for _, name := range []string{migration.Up, migration.Down} {
			data, err := fs.ReadFile(migrations.FS, name)
			if err != nil {
				t.Fatalf("read migration %s: %v", name, err)
			}
			migrationFS[name] = &fstest.MapFile{Data: data}
		}
		fmt.Fprintf(
			&manifest,
			"  - version: %d\n    name: %s\n    up: %s\n    down: %s\n",
			migration.Version,
			migration.Name,
			migration.Up,
			migration.Down,
		)
		latest = migration.Version
	}
	if latest != targetVersion {
		t.Fatalf(
			"migration catalog stops at %d, cannot build target %d",
			latest,
			targetVersion,
		)
	}
	migrationFS["manifest.yaml"] = &fstest.MapFile{
		Data: []byte(manifest.String()),
	}
	return &pg.MigrationRunner{
		Pool:         pool,
		FS:           migrationFS,
		ManifestPath: "manifest.yaml",
	}
}

func readVariableEnvelopeBytes(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	query string,
	variableID string,
) ([]byte, []byte) {
	t.Helper()
	var ciphertext, wrappedDEK []byte
	if err := pool.QueryRow(ctx, query, variableID).Scan(
		&ciphertext,
		&wrappedDEK,
	); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), ciphertext...), append([]byte(nil), wrappedDEK...)
}

func assertVariableEnvelopeBytes(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	variableID string,
	activeCiphertext []byte,
	activeWrapped []byte,
	versionCiphertext []byte,
	versionWrapped []byte,
) {
	t.Helper()
	gotActiveCiphertext, gotActiveWrapped := readVariableEnvelopeBytes(
		t,
		ctx,
		pool,
		`SELECT value_ciphertext, wrapped_dek
		   FROM service_variables
		  WHERE id = $1`,
		variableID,
	)
	gotVersionCiphertext, gotVersionWrapped := readVariableEnvelopeBytes(
		t,
		ctx,
		pool,
		`SELECT value_ciphertext, wrapped_dek
		   FROM service_variable_versions
		  WHERE variable_id = $1 AND version = 1`,
		variableID,
	)
	if !bytes.Equal(gotActiveCiphertext, activeCiphertext) ||
		!bytes.Equal(gotActiveWrapped, activeWrapped) ||
		!bytes.Equal(gotVersionCiphertext, versionCiphertext) ||
		!bytes.Equal(gotVersionWrapped, versionWrapped) {
		t.Fatal("migration changed ciphertext or wrapped-DEK bytes")
	}
}

func assertVariableOwnership(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	query string,
	variableID string,
	wantWorkspace string,
	wantProject string,
	wantEnvironment string,
	wantService string,
	wantName string,
) {
	t.Helper()
	var workspace, project, environment, service, name string
	if err := pool.QueryRow(ctx, query, variableID).Scan(
		&workspace,
		&project,
		&environment,
		&service,
		&name,
	); err != nil {
		t.Fatal(err)
	}
	if workspace != wantWorkspace ||
		project != wantProject ||
		environment != wantEnvironment ||
		service != wantService ||
		name != wantName {
		t.Fatalf(
			"variable ownership = (%q,%q,%q,%q,%q), want (%q,%q,%q,%q,%q)",
			workspace,
			project,
			environment,
			service,
			name,
			wantWorkspace,
			wantProject,
			wantEnvironment,
			wantService,
			wantName,
		)
	}
}

func requirePostgresIntegrityError(
	t *testing.T,
	err error,
	code string,
	constraint string,
) {
	t.Helper()
	if err == nil {
		t.Fatal("direct writer unexpectedly bypassed scope integrity")
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		t.Fatalf("direct-writer error = %T %v, want PostgreSQL error", err, err)
	}
	if postgresError.Code != code {
		t.Fatalf(
			"direct-writer SQLSTATE = %q, want %q: %v",
			postgresError.Code,
			code,
			err,
		)
	}
	if constraint != "" && postgresError.ConstraintName != constraint {
		t.Fatalf(
			"direct-writer constraint = %q, want %q: %v",
			postgresError.ConstraintName,
			constraint,
			err,
		)
	}
}
