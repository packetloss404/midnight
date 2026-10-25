//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package github

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
)

func TestSourceLinkStoreCanonicalPersistenceAndRevocation(t *testing.T) {
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
		tc.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		if sourceLinkDockerUnavailable(err) {
			t.Skipf("docker not available: %v", err)
		}
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres dsn: %v", err)
	}
	pool, err := dbpg.Open(ctx, dbpg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	runner := &dbpg.MigrationRunner{
		Pool:         pool,
		FS:           migrations.FS,
		ManifestPath: "manifest.yaml",
	}
	applied, err := runner.Up(ctx)
	if err != nil {
		t.Fatalf("migrations up: %v", err)
	}
	_, manifest, err := dbpg.LoadOrderedMigrations(migrations.FS, "manifest.yaml")
	if err != nil {
		t.Fatalf("load current migration manifest: %v", err)
	}
	if len(applied) != manifest.SchemaVersion || applied[len(applied)-1].Version != manifest.SchemaVersion {
		t.Fatalf("applied migrations = %#v, want complete schema %d", applied, manifest.SchemaVersion)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id,slug,display_name)
		VALUES ('wrk_source_other','source-other','Source Other');
		INSERT INTO projects (id,workspace_id,slug,display_name)
		VALUES
			('prj_source_one','wrk_default','source-one','Source One'),
			('prj_source_other','wrk_source_other','source-other','Source Other');
		INSERT INTO services (id,project_id,slug,display_name)
		VALUES
			('svc_source_one','prj_source_one','api','API'),
			('svc_source_other','prj_source_other','api','API')`); err != nil {
		t.Fatalf("seed source-link scope: %v", err)
	}

	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := NewSourceLinkStore(pool)
	store.Now = func() time.Time { return now }
	installation := InstallationGrant{
		WorkspaceID:    "wrk_default",
		InstallationID: 42,
		AccountLogin:   "acme",
		TargetType:     "Organization",
		GrantedByActor: "operator:root",
	}
	repositoryOne := RepositoryGrant{
		WorkspaceID:    "wrk_default",
		InstallationID: 42,
		RepositoryID:   4201,
		Repository:     "acme/api",
		CloneURL:       "https://github.com/acme/api.git",
		DefaultBranch:  "main",
		Private:        true,
		VerifiedAt:     now.Add(-time.Minute),
		GrantedByActor: "operator:root",
	}
	repositoryTwo := RepositoryGrant{
		WorkspaceID:    "wrk_default",
		InstallationID: 42,
		RepositoryID:   4202,
		Repository:     "acme/worker",
		CloneURL:       "https://github.com/acme/worker.git",
		DefaultBranch:  "main",
		Private:        true,
		VerifiedAt:     now.Add(-time.Minute),
		GrantedByActor: "operator:root",
	}
	link := DurableServiceLink{
		ServiceID:   "svc_source_one",
		ProjectID:   "prj_source_one",
		WorkspaceID: "wrk_default",
		Link: ServiceLink{
			InstallationID: 42,
			RepositoryID:   4201,
			Repo:           "acme/api",
			Branch:         "main",
		},
		AutoDeploy: true,
	}
	if err := pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *dbpg.Tx) error {
		if _, err := store.PutInstallationGrantInTx(ctx, tx, installation); err != nil {
			return err
		}
		if _, err := store.PutRepositoryGrantInTx(ctx, tx, repositoryOne); err != nil {
			return err
		}
		if _, err := store.PutRepositoryGrantInTx(ctx, tx, repositoryTwo); err != nil {
			return err
		}
		_, err := store.PutServiceLinkInTx(ctx, tx, link)
		return err
	}); err != nil {
		t.Fatalf("persist canonical github source: %v", err)
	}

	got, err := store.GetServiceLink(ctx, "wrk_default", "prj_source_one", "svc_source_one")
	if err != nil {
		t.Fatalf("get exact service link: %v", err)
	}
	if got.Link.RepositoryID != 4201 || got.Link.CloneURL != repositoryOne.CloneURL ||
		got.Link.WaitForCI || len(got.Link.RequiredChecks) != 0 ||
		!got.VerifiedAt.Equal(repositoryOne.VerifiedAt) {
		t.Fatalf("canonical service link = %#v", got)
	}
	if err := pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		installations, installationErr := store.ListInstallationGrantsInTx(
			ctx,
			tx,
			"wrk_default",
		)
		if installationErr != nil {
			return installationErr
		}
		if len(installations) != 1 || installations[0].InstallationID != 42 {
			t.Fatalf("transactional installation grants = %#v", installations)
		}
		grants, listErr := store.ListRepositoryGrantsInTx(ctx, tx, "wrk_default")
		if listErr != nil {
			return listErr
		}
		if len(grants) != 2 {
			t.Fatalf("transactional repository grants = %#v", grants)
		}
		byID, byIDErr := store.GetRepositoryGrantByIDInTx(ctx, tx, "wrk_default", 4201)
		if byIDErr != nil {
			return byIDErr
		}
		if byID.InstallationID != 42 || byID.Repository != "acme/api" {
			t.Fatalf("transactional repository grant by id = %#v", byID)
		}
		inTx, getErr := store.GetServiceLinkInTx(
			ctx,
			tx,
			"wrk_default",
			"prj_source_one",
			"svc_source_one",
		)
		if getErr != nil {
			return getErr
		}
		if inTx.Link.RepositoryID != 4201 {
			t.Fatalf("transactional service link = %#v", inTx)
		}
		return nil
	}); err != nil {
		t.Fatalf("current-authority transactional reads: %v", err)
	}
	if _, err := store.GetServiceLink(
		ctx,
		"wrk_source_other",
		"prj_source_other",
		"svc_source_one",
	); !errors.Is(err, ErrSourceLinkNotFound) {
		t.Fatalf("cross-scope GetServiceLink error = %v", err)
	}
	matches, err := store.FindServiceLinksForPush(ctx, 42, 4201, "Acme/API", "refs/heads/main")
	if err != nil || len(matches) != 1 || matches[0].ServiceID != "svc_source_one" {
		t.Fatalf("exact push matches = %#v, error = %v", matches, err)
	}
	if matches[0].InstallationGrantResourceVersion != 1 ||
		matches[0].RepositoryGrantResourceVersion != 1 ||
		matches[0].ResourceVersion != 1 ||
		!matches[0].RepositoryAccessVerifiedAt.Equal(repositoryOne.VerifiedAt) ||
		!matches[0].LinkRepositoryAccessVerifiedAt.Equal(repositoryOne.VerifiedAt) {
		t.Fatalf("exact push authority snapshot = %#v", matches[0])
	}
	matches, err = store.FindServiceLinksForPush(ctx, 42, 9999, "acme/api", "main")
	if err != nil || len(matches) != 0 {
		t.Fatalf("wrong repository id matches = %#v, error = %v", matches, err)
	}

	// Durable delivery lookup is temporal: current authority may authorize a
	// new push, but must not retroactively authorize an older retained push.
	initialAuthorityAt := now
	matches, err = store.FindServiceLinksForPushAt(
		ctx,
		42,
		4201,
		"acme/api",
		"main",
		initialAuthorityAt.Add(-time.Microsecond),
	)
	if err != nil || len(matches) != 0 {
		t.Fatalf("pre-authority temporal matches = %#v, error = %v", matches, err)
	}
	matches, err = store.FindServiceLinksForPushAt(
		ctx,
		42,
		4201,
		"acme/api",
		"main",
		initialAuthorityAt,
	)
	if err != nil || len(matches) != 1 {
		t.Fatalf("initial temporal matches = %#v, error = %v", matches, err)
	}

	now = initialAuthorityAt.Add(time.Minute)
	var refreshedInstallation InstallationGrant
	if err := pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		var putErr error
		refreshedInstallation, putErr = store.PutInstallationGrantInTx(ctx, tx, installation)
		return putErr
	}); err != nil {
		t.Fatalf("refresh installation grant: %v", err)
	}
	matches, err = store.FindServiceLinksForPushAt(ctx, 42, 4201, "acme/api", "main", initialAuthorityAt)
	if err != nil || len(matches) != 0 {
		t.Fatalf("post-installation-refresh historical matches = %#v, error = %v", matches, err)
	}
	matches, err = store.FindServiceLinksForPushAt(ctx, 42, 4201, "acme/api", "main", now)
	if err != nil || len(matches) != 1 ||
		matches[0].InstallationGrantResourceVersion != refreshedInstallation.ResourceVersion ||
		!matches[0].InstallationGrantUpdatedAt.Equal(refreshedInstallation.UpdatedAt) {
		t.Fatalf("refreshed installation temporal snapshot = %#v, error = %v", matches, err)
	}

	installationRefreshAt := now
	now = initialAuthorityAt.Add(2 * time.Minute)
	repositoryOne.VerifiedAt = now
	var refreshedRepository RepositoryGrant
	if err := pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		var putErr error
		refreshedRepository, putErr = store.PutRepositoryGrantInTx(ctx, tx, repositoryOne)
		return putErr
	}); err != nil {
		t.Fatalf("refresh repository grant: %v", err)
	}
	matches, err = store.FindServiceLinksForPushAt(ctx, 42, 4201, "acme/api", "main", installationRefreshAt)
	if err != nil || len(matches) != 0 {
		t.Fatalf("post-repository-refresh historical matches = %#v, error = %v", matches, err)
	}
	matches, err = store.FindServiceLinksForPushAt(ctx, 42, 4201, "acme/api", "main", now)
	if err != nil || len(matches) != 1 ||
		matches[0].RepositoryGrantResourceVersion != refreshedRepository.ResourceVersion ||
		!matches[0].RepositoryGrantUpdatedAt.Equal(refreshedRepository.UpdatedAt) ||
		!matches[0].RepositoryAccessVerifiedAt.Equal(refreshedRepository.VerifiedAt) ||
		matches[0].LinkRepositoryAccessVerifiedAt.Equal(refreshedRepository.VerifiedAt) {
		t.Fatalf("refreshed repository temporal snapshot = %#v, error = %v", matches, err)
	}

	repositoryRefreshAt := now
	now = initialAuthorityAt.Add(3 * time.Minute)
	var refreshedLink DurableServiceLink
	if err := pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		var putErr error
		refreshedLink, putErr = store.PutServiceLinkInTx(ctx, tx, link)
		return putErr
	}); err != nil {
		t.Fatalf("refresh service link: %v", err)
	}
	matches, err = store.FindServiceLinksForPushAt(ctx, 42, 4201, "acme/api", "main", repositoryRefreshAt)
	if err != nil || len(matches) != 0 {
		t.Fatalf("post-link-refresh historical matches = %#v, error = %v", matches, err)
	}
	matches, err = store.FindServiceLinksForPushAt(ctx, 42, 4201, "acme/api", "main", now)
	if err != nil || len(matches) != 1 ||
		matches[0].ResourceVersion != refreshedLink.ResourceVersion ||
		!matches[0].UpdatedAt.Equal(refreshedLink.UpdatedAt) ||
		!matches[0].LinkRepositoryAccessVerifiedAt.Equal(refreshedRepository.VerifiedAt) {
		t.Fatalf("refreshed link temporal snapshot = %#v, error = %v", matches, err)
	}

	crossWorkspace := installation
	crossWorkspace.WorkspaceID = "wrk_source_other"
	err = pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		_, putErr := store.PutInstallationGrantInTx(ctx, tx, crossWorkspace)
		return putErr
	})
	if !errors.Is(err, ErrSourceLinkConflict) {
		t.Fatalf("cross-workspace installation error = %v, want conflict", err)
	}

	wrongScope := link
	wrongScope.ServiceID = "svc_source_other"
	err = pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		_, putErr := store.PutServiceLinkInTx(ctx, tx, wrongScope)
		return putErr
	})
	if !errors.Is(err, ErrSourceLinkConflict) {
		t.Fatalf("cross-scope service link error = %v, want conflict", err)
	}

	var revoked []string
	if err := pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *dbpg.Tx) error {
		var revokeErr error
		revoked, revokeErr = store.RevokeRepositoryGrantInTx(ctx, tx, "wrk_default", 42, 4201)
		return revokeErr
	}); err != nil {
		t.Fatalf("revoke exact repository: %v", err)
	}
	if len(revoked) != 1 || revoked[0] != "svc_source_one" {
		t.Fatalf("revoked service ids = %#v", revoked)
	}
	if _, err := store.GetServiceLink(ctx, "wrk_default", "prj_source_one", "svc_source_one"); !errors.Is(err, ErrSourceLinkNotFound) {
		t.Fatalf("service link after repository revoke = %v", err)
	}
	if _, err := store.GetRepositoryGrant(ctx, "wrk_default", 42, 4201); !errors.Is(err, ErrRepositoryGrantNotFound) {
		t.Fatalf("repository grant after revoke = %v", err)
	}
	if _, err := store.GetRepositoryGrant(ctx, "wrk_default", 42, 4202); err != nil {
		t.Fatalf("sibling repository grant removed: %v", err)
	}
	if _, err := store.GetInstallationGrant(ctx, "wrk_default", 42); err != nil {
		t.Fatalf("installation removed by repository revoke: %v", err)
	}

	link.Link.RepositoryID = 4202
	link.Link.Repo = "acme/worker"
	if err := pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		_, putErr := store.PutServiceLinkInTx(ctx, tx, link)
		return putErr
	}); err != nil {
		t.Fatalf("link sibling repository: %v", err)
	}
	if err := pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *dbpg.Tx) error {
		var revokeErr error
		revoked, revokeErr = store.RevokeInstallationGrantInTx(ctx, tx, "wrk_default", 42)
		return revokeErr
	}); err != nil {
		t.Fatalf("revoke installation: %v", err)
	}
	if len(revoked) != 1 || revoked[0] != "svc_source_one" {
		t.Fatalf("installation revoked service ids = %#v", revoked)
	}
	if _, err := store.GetInstallationGrant(ctx, "wrk_default", 42); !errors.Is(err, ErrInstallationGrantNotFound) {
		t.Fatalf("installation after revoke = %v", err)
	}

	down, err := runner.Down(ctx)
	if err != nil || down == nil || down.Version != 50 {
		t.Fatalf("down migration 50 = %#v, error = %v", down, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 49 {
		t.Fatalf("down migration 49 = %#v, error = %v", down, err)
	}
	const legacyLink = `{"installation_id":99,"repo":"legacy/api","branch":"main","clone_url":"https://github.com/legacy/api.git"}`
	if _, err := pool.Exec(ctx, `
		UPDATE services
		   SET spec=jsonb_set(spec,'{github}',$1::jsonb,TRUE)
		 WHERE id='svc_source_one'`,
		legacyLink,
	); err != nil {
		t.Fatalf("seed legacy source link: %v", err)
	}
	applied, err = runner.Up(ctx)
	if err != nil ||
		len(applied) != 2 ||
		applied[0].Version != 49 ||
		applied[1].Version != 50 {
		t.Fatalf("reapply migrations 49-50 = %#v, error = %v", applied, err)
	}
	var quarantined string
	if err := pool.QueryRow(ctx, `
		SELECT legacy_link::TEXT
		  FROM github_source_link_quarantine
		 WHERE service_id='svc_source_one'`,
	).Scan(&quarantined); err != nil {
		t.Fatalf("read quarantined legacy link: %v", err)
	}
	if !strings.Contains(quarantined, `"repo": "legacy/api"`) {
		t.Fatalf("quarantined legacy link = %s", quarantined)
	}
	var stillInSpec bool
	if err := pool.QueryRow(ctx, `
		SELECT spec ? 'github'
		  FROM services
		 WHERE id='svc_source_one'`,
	).Scan(&stillInSpec); err != nil || stillInSpec {
		t.Fatalf("legacy source remained active: present=%v error=%v", stillInSpec, err)
	}
	var canonicalCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM service_github_links`).Scan(&canonicalCount); err != nil || canonicalCount != 0 {
		t.Fatalf("legacy source activated canonical links: count=%d error=%v", canonicalCount, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 50 {
		t.Fatalf("final down migration 50 = %#v, error = %v", down, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 49 {
		t.Fatalf("final down migration 49 = %#v, error = %v", down, err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT spec ? 'github'
		  FROM services
		 WHERE id='svc_source_one'`,
	).Scan(&stillInSpec); err != nil || !stillInSpec {
		t.Fatalf("down did not restore legacy source: present=%v error=%v", stillInSpec, err)
	}
}

func sourceLinkDockerUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "docker") &&
		(strings.Contains(message, "not running") ||
			strings.Contains(message, "cannot connect") ||
			strings.Contains(message, "no such file") ||
			strings.Contains(message, "permission denied"))
}
