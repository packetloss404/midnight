//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package identity

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresMembershipCASHasOneWinnerAndStaleDeleteIsNonMutating(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dsn := strings.TrimSpace(os.Getenv("MIDNIGHT_IDENTITY_TEST_DSN")) // not-a-failure-code
	if dsn == "" {
		t.Skip("MIDNIGHT_IDENTITY_TEST_DSN is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	adminID, targetID := "usr_cas_admin_"+suffix, "usr_cas_target_"+suffix
	workspaceID, projectID := "wrk_cas_"+suffix, "prj_cas_"+suffix
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		for _, user := range []struct{ id, email string }{
			{adminID, "cas-admin-" + suffix + "@example.com"},
			{targetID, "cas-target-" + suffix + "@example.com"},
		} {
			if _, err := tx.Exec(ctx, `INSERT INTO users(id,email,display_name,created_at,updated_at) VALUES($1,$2,$1,$3,$3)`, user.id, user.email, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO workspaces(id,slug,display_name,created_at,updated_at) VALUES($1,$2,$1,$3,$3)`, workspaceID, "cas-"+suffix, now); err != nil {
			return err
		}
		for _, member := range []struct{ id, role string }{{adminID, "admin"}, {targetID, "member"}} {
			if _, err := tx.Exec(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role,invited_at,accepted_at,created_at,updated_at) VALUES($1,$2,$3,$4,$4,$4,$4)`, workspaceID, member.id, member.role, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO projects(id,workspace_id,slug,display_name,created_at,updated_at) VALUES($1,$2,$3,$1,$4,$4)`, projectID, workspaceID, "cas-"+suffix, now); err != nil {
			return err
		}
		for _, member := range []struct{ id, role string }{{adminID, "owner"}, {targetID, "viewer"}} {
			if _, err := tx.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role,invited_at,accepted_at,created_at,updated_at) VALUES($1,$2,$3,$4,$4,$4,$4)`, projectID, member.id, member.role, now); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresStore(pool)
	workspaceErrors := make(chan error, 2)
	var workspaceWG sync.WaitGroup
	for _, role := range []WorkspaceRole{WorkspaceRoleViewer, WorkspaceRoleDeployer} {
		workspaceWG.Add(1)
		go func(role WorkspaceRole) {
			defer workspaceWG.Done()
			_, err := store.PutWorkspaceMemberCAS(ctx, workspaceID, targetID, role, adminID, 1, now.Add(time.Second))
			workspaceErrors <- err
		}(role)
	}
	workspaceWG.Wait()
	assertOneMembershipCASWinner(t, workspaceErrors)
	if err := store.RemoveWorkspaceMemberCAS(ctx, workspaceID, targetID, adminID, 1, now); !errors.Is(err, ErrMembershipVersionConflict) {
		t.Fatalf("stale workspace delete error = %v", err)
	}
	if member, err := store.GetWorkspaceMember(ctx, workspaceID, targetID); err != nil || member.Version != 2 {
		t.Fatalf("workspace member after stale delete = %#v, err=%v", member, err)
	}

	projectErrors := make(chan error, 2)
	var projectWG sync.WaitGroup
	for _, role := range []ProjectRole{ProjectRoleEditor, ProjectRoleOwner} {
		projectWG.Add(1)
		go func(role ProjectRole) {
			defer projectWG.Done()
			_, err := store.PutProjectMemberCAS(ctx, projectID, targetID, role, adminID, 1, now.Add(time.Second))
			projectErrors <- err
		}(role)
	}
	projectWG.Wait()
	assertOneMembershipCASWinner(t, projectErrors)
	if err := store.RemoveProjectMemberCAS(ctx, projectID, targetID, adminID, 1, now); !errors.Is(err, ErrMembershipVersionConflict) {
		t.Fatalf("stale project delete error = %v", err)
	}
	if member, err := store.GetProjectMember(ctx, projectID, targetID); err != nil || member.Version != 2 {
		t.Fatalf("project member after stale delete = %#v, err=%v", member, err)
	}
}

func assertOneMembershipCASWinner(t *testing.T, results <-chan error) {
	t.Helper()
	var success, stale int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			success++
		case errors.Is(err, ErrMembershipVersionConflict):
			stale++
		default:
			t.Fatalf("unexpected membership CAS error: %v", err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("membership CAS success/stale = %d/%d", success, stale)
	}
}
