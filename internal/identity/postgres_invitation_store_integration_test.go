//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
)

// TestPostgresInvitationReplacementLifecycle runs against an explicitly
// supplied disposable database so it does not depend on a Docker client
// library. Example:
//
//	MIDNIGHT_IDENTITY_TEST_DSN=postgres://... \
//	  go test -tags=integration ./internal/identity \
//	  -run TestPostgresInvitationReplacementLifecycle
func TestPostgresInvitationReplacementLifecycle(t *testing.T) {
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
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	runner := &pg.MigrationRunner{
		Pool:         pool,
		FS:           migrations.FS,
		ManifestPath: "manifest.yaml",
	}
	applied, err := runner.Up(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = applied

	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	adminID := "usr_inv_admin_" + suffix
	editorID := "usr_inv_editor_" + suffix
	targetID := "usr_inv_target_" + suffix
	workspaceID := "wrk_inv_" + suffix
	projectID := "prj_inv_" + suffix
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	acceptedAt := now.Add(-time.Hour)
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		for _, user := range []struct {
			id, email, name string
		}{
			{adminID, "admin+" + suffix + "@example.com", "Invitation Admin"},
			{editorID, "editor+" + suffix + "@example.com", "Invitation Editor"},
			{targetID, "target+" + suffix + "@example.com", "Invitation Target"},
		} {
			if _, execErr := tx.Exec(ctx, `
				INSERT INTO users (id, email, display_name, created_at, updated_at)
				VALUES ($1,$2,$3,$4,$4)`,
				user.id,
				user.email,
				user.name,
				acceptedAt,
			); execErr != nil {
				return execErr
			}
		}
		if _, execErr := tx.Exec(ctx, `
			INSERT INTO workspaces (id, slug, display_name, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$4)`,
			workspaceID,
			"inv-"+suffix,
			"Invitation Integration",
			acceptedAt,
		); execErr != nil {
			return execErr
		}
		if _, execErr := tx.Exec(ctx, `
			INSERT INTO workspace_members (
				workspace_id, user_id, role, invited_at, accepted_at,
				created_at, updated_at
			)
			VALUES ($1,$2,'admin',$3,$3,$3,$3)`,
			workspaceID,
			adminID,
			acceptedAt,
		); execErr != nil {
			return execErr
		}
		if _, execErr := tx.Exec(ctx, `
			INSERT INTO projects (
				id, workspace_id, slug, display_name, created_at, updated_at
			)
			VALUES ($1,$2,$3,$4,$5,$5)`,
			projectID,
			workspaceID,
			"inv-"+suffix,
			"Invitation Integration",
			acceptedAt,
		); execErr != nil {
			return execErr
		}
		for _, membership := range []struct {
			userID, role string
		}{
			{adminID, string(ProjectRoleOwner)},
			{editorID, string(ProjectRoleEditor)},
		} {
			if _, execErr := tx.Exec(ctx, `
				INSERT INTO project_members (
					project_id, user_id, role, invited_at, accepted_at,
					created_at, updated_at
				)
				VALUES ($1,$2,$3,$4,$4,$4,$4)`,
				projectID,
				membership.userID,
				membership.role,
				acceptedAt,
			); execErr != nil {
				return execErr
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresStore(pool)
	service, err := NewInvitationService(InvitationServiceConfig{
		Store:  store,
		MACKey: bytes.Repeat([]byte{0x6b}, InvitationMinMACBytes),
		Clock:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	targetEmail := "target+" + suffix + "@example.com"
	original, err := service.Create(ctx, CreateInvitationRequest{
		Email:           targetEmail,
		WorkspaceID:     workspaceID,
		ProjectID:       projectID,
		ProjectRole:     ProjectRoleViewer,
		InvitedByUserID: adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	reissued, err := service.Reissue(ctx, ReissueInvitationRequest{
		InvitationID:     original.Invitation.ID,
		WorkspaceID:      workspaceID,
		ProjectID:        projectID,
		ReissuedByUserID: editorID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reissued.Metadata.Invitation.ReplacesInvitationID != original.Invitation.ID ||
		reissued.Metadata.ReplacedInvitation.RevokedReason != invitationReissueReason ||
		reissued.Metadata.ReplacedInvitation.RevokedByUserID != editorID {
		t.Fatalf("unexpected replacement metadata: %+v", reissued.Metadata)
	}

	listed, err := service.List(ctx, ListInvitationsRequest{
		WorkspaceID: workspaceID,
		ProjectID:   projectID,
		ActorUserID: editorID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("replacement history count = %d, want 2", len(listed))
	}
	encodedList := mustMarshalInvitations(t, listed)
	if bytes.Contains(encodedList, []byte(original.InvitationToken)) ||
		bytes.Contains(encodedList, []byte(reissued.InvitationToken)) {
		t.Fatal("Postgres invitation list exposed a raw credential")
	}

	if _, err := service.Accept(ctx, AcceptInvitationRequest{
		InvitationToken: original.InvitationToken,
		UserID:          targetID,
		Email:           targetEmail,
	}); !errors.Is(err, ErrInvitationRevoked) {
		t.Fatalf("old token error = %v, want %v", err, ErrInvitationRevoked)
	}
	acceptance, err := service.PrepareAcceptance(AcceptInvitationRequest{
		InvitationToken: reissued.InvitationToken,
		UserID:          targetID,
		Email:           targetEmail,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer acceptance.Destroy()
	var accepted Invitation
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		var txErr error
		accepted, txErr = store.AcceptInvitationInTx(ctx, tx, acceptance, now)
		return txErr
	}); err != nil {
		t.Fatal(err)
	}
	if accepted.ID != reissued.Metadata.Invitation.ID ||
		accepted.AcceptedByUserID != targetID {
		t.Fatalf("accepted replacement = %+v", accepted)
	}
	var durableVerifier []byte
	var replacesID string
	if err := pool.QueryRow(ctx, `
		SELECT token_hash, replaces_invitation_id
		FROM identity_invitations
		WHERE id=$1`,
		accepted.ID,
	).Scan(&durableVerifier, &replacesID); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(durableVerifier, acceptance.Verifier) ||
		bytes.Contains(durableVerifier, []byte(reissued.InvitationToken)) ||
		replacesID != original.Invitation.ID {
		t.Fatalf("unexpected durable replacement verifier or lineage")
	}

	raceOriginal, err := service.Create(ctx, CreateInvitationRequest{
		Email:           "race+" + suffix + "@example.com",
		WorkspaceID:     workspaceID,
		ProjectID:       projectID,
		ProjectRole:     ProjectRoleViewer,
		InvitedByUserID: adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	raceRequest := ReissueInvitationRequest{
		InvitationID:     raceOriginal.Invitation.ID,
		WorkspaceID:      workspaceID,
		ProjectID:        projectID,
		ReissuedByUserID: adminID,
	}
	first, err := service.PrepareReissue(raceRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Destroy()
	second, err := service.PrepareReissue(raceRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Destroy()

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, plan := range []*InvitationReissuePlan{first, second} {
		wait.Add(1)
		go func(candidate *InvitationReissuePlan) {
			defer wait.Done()
			<-start
			errs <- pool.WithTx(ctx, func(tx *pg.Tx) error {
				_, commitErr := candidate.Commit(ctx, InvitationReissuerFunc(
					func(inner context.Context, replacement InvitationReplacement) (ReissueInvitationMetadata, error) {
						return store.ReissueInvitationInTx(inner, tx, replacement)
					},
				))
				return commitErr
			})
		}(plan)
	}
	close(start)
	wait.Wait()
	close(errs)
	successes := 0
	alreadyReplaced := 0
	for raceErr := range errs {
		switch {
		case raceErr == nil:
			successes++
		case errors.Is(raceErr, ErrInvitationReplaced):
			alreadyReplaced++
		default:
			t.Fatalf("unexpected concurrent replacement error: %v", raceErr)
		}
	}
	if successes != 1 || alreadyReplaced != 1 {
		t.Fatalf("replacement race: successes=%d already_replaced=%d", successes, alreadyReplaced)
	}
	var replacementCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM identity_invitations
		WHERE replaces_invitation_id=$1`,
		raceOriginal.Invitation.ID,
	).Scan(&replacementCount); err != nil {
		t.Fatal(err)
	}
	if replacementCount != 1 {
		t.Fatalf("durable replacement branches = %d, want 1", replacementCount)
	}

	conditional, err := service.Create(ctx, CreateInvitationRequest{
		Email:           "conditional+" + suffix + "@example.com",
		WorkspaceID:     workspaceID,
		ProjectID:       projectID,
		ProjectRole:     ProjectRoleViewer,
		InvitedByUserID: adminID,
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := conditional.Invitation.ResourceVersion
	revokeErrors := make(chan error, 2)
	var revokeWait sync.WaitGroup
	for range 2 {
		revokeWait.Add(1)
		go func() {
			defer revokeWait.Done()
			_, revokeErr := service.Revoke(ctx, RevokeInvitationRequest{
				InvitationID:    conditional.Invitation.ID,
				WorkspaceID:     workspaceID,
				ProjectID:       projectID,
				RevokedByUserID: adminID,
				ExpectedVersion: &expected,
			})
			revokeErrors <- revokeErr
		}()
	}
	revokeWait.Wait()
	close(revokeErrors)
	revokeSuccesses, revokeConflicts := 0, 0
	for revokeErr := range revokeErrors {
		switch {
		case revokeErr == nil:
			revokeSuccesses++
		case errors.Is(revokeErr, ErrInvitationVersionConflict):
			revokeConflicts++
		default:
			t.Fatalf("conditional revoke error: %v", revokeErr)
		}
	}
	if revokeSuccesses != 1 || revokeConflicts != 1 {
		t.Fatalf("conditional revoke success/conflict = %d/%d", revokeSuccesses, revokeConflicts)
	}
	durableConditional, err := service.Get(ctx, GetInvitationRequest{
		InvitationID: conditional.Invitation.ID,
		WorkspaceID:  workspaceID,
		ProjectID:    projectID,
		ActorUserID:  adminID,
	})
	if err != nil || durableConditional.RevokedAt == nil || durableConditional.ResourceVersion != expected+1 {
		t.Fatalf("conditional invitation after race=%+v err=%v", durableConditional, err)
	}

	// The replacement-history refusal belongs to migration 43, not to the
	// latest migration in the evolving repository manifest.
	migrationHistory, err := pg.ListApplied(ctx, pool)
	if err != nil || len(migrationHistory) == 0 {
		t.Fatalf("list applied migrations: %v", err)
	}
	for index := len(migrationHistory) - 1; index >= 0 && migrationHistory[index].Version > 43; index-- {
		down, err := runner.Down(ctx)
		if err != nil || down == nil || down.Version != migrationHistory[index].Version {
			t.Fatalf("rollback later migration %d: result=%#v err=%v", migrationHistory[index].Version, down, err)
		}
	}
	if _, err := runner.Down(ctx); err == nil ||
		!strings.Contains(err.Error(), "replacement history exists") {
		t.Fatalf("history-preserving migration down error = %v", err)
	}
}

func mustMarshalInvitations(t *testing.T, invitations []Invitation) []byte {
	t.Helper()
	encoded, err := json.Marshal(invitations)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
