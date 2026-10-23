// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestInvitationListIsExactScopeAuthorizedAndMetadataOnly(t *testing.T) {
	_, service, _ := newInvitationHarness(t)
	viewer := mustCreateInvitation(t, service, CreateInvitationRequest{
		Email:           "viewer@example.com",
		WorkspaceID:     "wrk_a",
		ProjectID:       "prj_a",
		ProjectRole:     ProjectRoleViewer,
		InvitedByUserID: "usr_admin",
	})
	owner := mustCreateInvitation(t, service, CreateInvitationRequest{
		Email:           "owner@example.com",
		WorkspaceID:     "wrk_a",
		ProjectID:       "prj_a",
		ProjectRole:     ProjectRoleOwner,
		InvitedByUserID: "usr_admin",
	})
	workspace := mustCreateInvitation(t, service, CreateInvitationRequest{
		Email:           "workspace@example.com",
		WorkspaceID:     "wrk_a",
		WorkspaceRole:   WorkspaceRoleViewer,
		InvitedByUserID: "usr_admin",
	})

	projectInvitations, err := service.List(context.Background(), ListInvitationsRequest{
		WorkspaceID: "wrk_a",
		ProjectID:   "prj_a",
		ActorUserID: "usr_admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(projectInvitations) != 2 {
		t.Fatalf("admin project invitation count = %d, want 2", len(projectInvitations))
	}
	if projectInvitations[0].ID != viewer.Invitation.ID ||
		projectInvitations[1].ID != owner.Invitation.ID {
		t.Fatalf("stable project invitation order = %+v", projectInvitations)
	}

	editorInvitations, err := service.List(context.Background(), ListInvitationsRequest{
		WorkspaceID: "wrk_a",
		ProjectID:   "prj_a",
		ActorUserID: "usr_editor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(editorInvitations) != 1 ||
		editorInvitations[0].ID != viewer.Invitation.ID {
		t.Fatalf("editor-visible invitations = %+v", editorInvitations)
	}

	workspaceInvitations, err := service.List(context.Background(), ListInvitationsRequest{
		WorkspaceID: "wrk_a",
		ActorUserID: "usr_admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaceInvitations) != 1 ||
		workspaceInvitations[0].ID != workspace.Invitation.ID {
		t.Fatalf("workspace invitations = %+v", workspaceInvitations)
	}
	if _, err := service.List(context.Background(), ListInvitationsRequest{
		WorkspaceID: "wrk_a",
		ProjectID:   "prj_a",
		ActorUserID: "usr_member",
	}); !errors.Is(err, ErrAuthorizationDenied) {
		t.Fatalf("unauthorized list error = %v, want %v", err, ErrAuthorizationDenied)
	}

	encoded, err := json.Marshal(projectInvitations)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		viewer.InvitationToken,
		owner.InvitationToken,
		workspace.InvitationToken,
	} {
		if bytes.Contains(encoded, []byte(raw)) {
			t.Fatalf("invitation list exposed raw token %q", raw)
		}
	}
	if bytes.Contains(encoded, []byte("verifier")) ||
		bytes.Contains(encoded, []byte("token_hash")) {
		t.Fatalf("invitation list exposed verifier metadata: %s", encoded)
	}
}

func TestInvitationReissueAtomicallyRotatesSecretAndPreservesScope(t *testing.T) {
	_, service, clock := newInvitationHarnessWithClock(t)
	original := mustCreateInvitation(t, service, CreateInvitationRequest{
		Email:           "target@example.com",
		WorkspaceID:     "wrk_a",
		ProjectID:       "prj_a",
		ProjectRole:     ProjectRoleViewer,
		InvitedByUserID: "usr_admin",
	})
	clock.now = clock.now.Add(2)
	reissued, err := service.Reissue(context.Background(), ReissueInvitationRequest{
		InvitationID:     original.Invitation.ID,
		WorkspaceID:      "wrk_a",
		ProjectID:        "prj_a",
		ReissuedByUserID: "usr_editor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reissued.InvitationToken == original.InvitationToken ||
		!validRawInvitationToken(reissued.InvitationToken) {
		t.Fatalf("replacement token was not independently minted: %q", reissued.InvitationToken)
	}
	replaced := reissued.Metadata.ReplacedInvitation
	replacement := reissued.Metadata.Invitation
	if replaced.ID != original.Invitation.ID ||
		replaced.RevokedAt == nil ||
		replaced.RevokedByUserID != "usr_editor" ||
		replaced.RevokedReason != invitationReissueReason ||
		replaced.ResourceVersion != 2 {
		t.Fatalf("unexpected replaced invitation: %+v", replaced)
	}
	if replacement.ReplacesInvitationID != original.Invitation.ID ||
		replacement.Email != original.Invitation.Email ||
		replacement.WorkspaceID != original.Invitation.WorkspaceID ||
		replacement.ProjectID != original.Invitation.ProjectID ||
		replacement.ProjectRole != original.Invitation.ProjectRole ||
		replacement.InvitedByUserID != "usr_editor" ||
		replacement.ResourceVersion != 1 {
		t.Fatalf("replacement did not preserve lineage and grant: %+v", replacement)
	}

	if _, err := service.Accept(context.Background(), AcceptInvitationRequest{
		InvitationToken: original.InvitationToken,
		UserID:          "usr_target",
		Email:           "target@example.com",
	}); !errors.Is(err, ErrInvitationRevoked) {
		t.Fatalf("old token error = %v, want %v", err, ErrInvitationRevoked)
	}
	accepted, err := service.Accept(context.Background(), AcceptInvitationRequest{
		InvitationToken: reissued.InvitationToken,
		UserID:          "usr_target",
		Email:           "target@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.ID != replacement.ID || accepted.AcceptedByUserID != "usr_target" {
		t.Fatalf("accepted replacement = %+v", accepted)
	}
}

func TestInvitationReissuePreservesFirstRevocationAndRejectsBranching(t *testing.T) {
	_, service, clock := newInvitationHarnessWithClock(t)
	original := mustCreateInvitation(t, service, CreateInvitationRequest{
		Email:           "target@example.com",
		WorkspaceID:     "wrk_a",
		ProjectID:       "prj_a",
		ProjectRole:     ProjectRoleViewer,
		InvitedByUserID: "usr_admin",
	})
	clock.now = clock.now.Add(2)
	firstRevocation, err := service.Revoke(context.Background(), RevokeInvitationRequest{
		InvitationID:    original.Invitation.ID,
		WorkspaceID:     "wrk_a",
		ProjectID:       "prj_a",
		RevokedByUserID: "usr_admin",
		Reason:          "wrong recipient",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(2)
	reissued, err := service.Reissue(context.Background(), ReissueInvitationRequest{
		InvitationID:     original.Invitation.ID,
		WorkspaceID:      "wrk_a",
		ProjectID:        "prj_a",
		ReissuedByUserID: "usr_editor",
	})
	if err != nil {
		t.Fatal(err)
	}
	replaced := reissued.Metadata.ReplacedInvitation
	if replaced.RevokedAt == nil || firstRevocation.RevokedAt == nil ||
		!replaced.RevokedAt.Equal(*firstRevocation.RevokedAt) ||
		replaced.RevokedByUserID != firstRevocation.RevokedByUserID ||
		replaced.RevokedReason != firstRevocation.RevokedReason ||
		replaced.UpdatedAt != firstRevocation.UpdatedAt ||
		replaced.ResourceVersion != firstRevocation.ResourceVersion {
		t.Fatalf("first revocation attribution changed: before=%+v after=%+v", firstRevocation, replaced)
	}
	if _, err := service.Reissue(context.Background(), ReissueInvitationRequest{
		InvitationID:     original.Invitation.ID,
		WorkspaceID:      "wrk_a",
		ProjectID:        "prj_a",
		ReissuedByUserID: "usr_admin",
	}); !errors.Is(err, ErrInvitationReplaced) {
		t.Fatalf("branched reissue error = %v, want %v", err, ErrInvitationReplaced)
	}
}

func TestInvitationReissueRechecksCurrentAuthorityLineageAndMembership(t *testing.T) {
	t.Run("editor cannot reissue owner", func(t *testing.T) {
		_, service, _ := newInvitationHarness(t)
		original := mustCreateInvitation(t, service, CreateInvitationRequest{
			Email:           "owner@example.com",
			WorkspaceID:     "wrk_a",
			ProjectID:       "prj_a",
			ProjectRole:     ProjectRoleOwner,
			InvitedByUserID: "usr_admin",
		})
		if _, err := service.Reissue(context.Background(), ReissueInvitationRequest{
			InvitationID:     original.Invitation.ID,
			WorkspaceID:      "wrk_a",
			ProjectID:        "prj_a",
			ReissuedByUserID: "usr_editor",
		}); !errors.Is(err, ErrAuthorizationDenied) {
			t.Fatalf("editor owner reissue error = %v, want %v", err, ErrAuthorizationDenied)
		}
	})

	t.Run("changed project lineage leaves old invitation active", func(t *testing.T) {
		store, service, _ := newInvitationHarness(t)
		original := mustCreateInvitation(t, service, CreateInvitationRequest{
			Email:           "target@example.com",
			WorkspaceID:     "wrk_a",
			ProjectID:       "prj_a",
			ProjectRole:     ProjectRoleViewer,
			InvitedByUserID: "usr_admin",
		})
		store.mu.Lock()
		store.projects["prj_a"] = memoryProjectScope{WorkspaceID: "wrk_other"}
		store.mu.Unlock()
		if _, err := service.Reissue(context.Background(), ReissueInvitationRequest{
			InvitationID:     original.Invitation.ID,
			WorkspaceID:      "wrk_a",
			ProjectID:        "prj_a",
			ReissuedByUserID: "usr_admin",
		}); !errors.Is(err, ErrInvitationScopeNotActive) {
			t.Fatalf("lineage reissue error = %v, want %v", err, ErrInvitationScopeNotActive)
		}
		if stored := store.invitations[original.Invitation.ID]; stored.RevokedAt != nil {
			t.Fatalf("failed lineage check revoked old invitation: %+v", stored)
		}
	})

	t.Run("new membership leaves old invitation active", func(t *testing.T) {
		store, service, now := newInvitationHarness(t)
		original := mustCreateInvitation(t, service, CreateInvitationRequest{
			Email:           "target@example.com",
			WorkspaceID:     "wrk_a",
			ProjectID:       "prj_a",
			ProjectRole:     ProjectRoleViewer,
			InvitedByUserID: "usr_admin",
		})
		store.SeedProject("prj_a", "wrk_a", ProjectMember{
			ProjectID: "prj_a",
			User:      User{ID: "usr_target", Email: "target@example.com"},
			Role:      ProjectRoleViewer,
			AcceptedAt: func() *time.Time {
				value := now
				return &value
			}(),
		})
		if _, err := service.Reissue(context.Background(), ReissueInvitationRequest{
			InvitationID:     original.Invitation.ID,
			WorkspaceID:      "wrk_a",
			ProjectID:        "prj_a",
			ReissuedByUserID: "usr_admin",
		}); !errors.Is(err, ErrInvitationMemberExists) {
			t.Fatalf("member reissue error = %v, want %v", err, ErrInvitationMemberExists)
		}
		if stored := store.invitations[original.Invitation.ID]; stored.RevokedAt != nil {
			t.Fatalf("failed member check revoked old invitation: %+v", stored)
		}
	})
}

func TestInvitationReissueAndAcceptanceRacesHaveOneTerminalWinner(t *testing.T) {
	t.Run("concurrent reissues cannot branch lineage", func(t *testing.T) {
		_, service, _ := newInvitationHarness(t)
		original := mustCreateInvitation(t, service, CreateInvitationRequest{
			Email:           "target@example.com",
			WorkspaceID:     "wrk_a",
			ProjectID:       "prj_a",
			ProjectRole:     ProjectRoleViewer,
			InvitedByUserID: "usr_admin",
		})
		request := ReissueInvitationRequest{
			InvitationID:     original.Invitation.ID,
			WorkspaceID:      "wrk_a",
			ProjectID:        "prj_a",
			ReissuedByUserID: "usr_admin",
		}
		first, err := service.PrepareReissue(request)
		if err != nil {
			t.Fatal(err)
		}
		defer first.Destroy()
		second, err := service.PrepareReissue(request)
		if err != nil {
			t.Fatal(err)
		}
		defer second.Destroy()

		start := make(chan struct{})
		results := make(chan ReissueInvitationResult, 2)
		errs := make(chan error, 2)
		var wait sync.WaitGroup
		for _, plan := range []*InvitationReissuePlan{first, second} {
			wait.Add(1)
			go func(candidate *InvitationReissuePlan) {
				defer wait.Done()
				<-start
				result, commitErr := candidate.Commit(context.Background(), service.store)
				results <- result
				errs <- commitErr
			}(plan)
		}
		close(start)
		wait.Wait()
		close(results)
		close(errs)

		successes := 0
		replaced := 0
		var winningToken string
		for result := range results {
			if result.InvitationToken != "" {
				successes++
				winningToken = result.InvitationToken
			}
		}
		for commitErr := range errs {
			switch {
			case commitErr == nil:
			case errors.Is(commitErr, ErrInvitationReplaced):
				replaced++
			default:
				t.Fatalf("unexpected concurrent reissue error: %v", commitErr)
			}
		}
		if successes != 1 || replaced != 1 || winningToken == "" {
			t.Fatalf("reissue race results: successes=%d replaced=%d", successes, replaced)
		}
		if _, err := service.Accept(context.Background(), AcceptInvitationRequest{
			InvitationToken: original.InvitationToken,
			UserID:          "usr_target",
			Email:           "target@example.com",
		}); !errors.Is(err, ErrInvitationRevoked) {
			t.Fatalf("old token after race error = %v", err)
		}
		if _, err := service.Accept(context.Background(), AcceptInvitationRequest{
			InvitationToken: winningToken,
			UserID:          "usr_target",
			Email:           "target@example.com",
		}); err != nil {
			t.Fatalf("winning token acceptance: %v", err)
		}
	})

	t.Run("accept and reissue cannot both consume old invitation", func(t *testing.T) {
		_, service, _ := newInvitationHarness(t)
		original := mustCreateInvitation(t, service, CreateInvitationRequest{
			Email:           "target@example.com",
			WorkspaceID:     "wrk_a",
			ProjectID:       "prj_a",
			ProjectRole:     ProjectRoleViewer,
			InvitedByUserID: "usr_admin",
		})
		start := make(chan struct{})
		acceptErr := make(chan error, 1)
		reissueResult := make(chan ReissueInvitationResult, 1)
		reissueErr := make(chan error, 1)
		go func() {
			<-start
			_, err := service.Accept(context.Background(), AcceptInvitationRequest{
				InvitationToken: original.InvitationToken,
				UserID:          "usr_target",
				Email:           "target@example.com",
			})
			acceptErr <- err
		}()
		go func() {
			<-start
			result, err := service.Reissue(context.Background(), ReissueInvitationRequest{
				InvitationID:     original.Invitation.ID,
				WorkspaceID:      "wrk_a",
				ProjectID:        "prj_a",
				ReissuedByUserID: "usr_admin",
			})
			reissueResult <- result
			reissueErr <- err
		}()
		close(start)

		accepted := <-acceptErr
		reissued := <-reissueResult
		reissueFailure := <-reissueErr
		switch {
		case accepted == nil:
			if !errors.Is(reissueFailure, ErrInvitationConsumed) {
				t.Fatalf("accept won but reissue error = %v", reissueFailure)
			}
			if reissued.InvitationToken != "" {
				t.Fatal("losing reissue exposed a token")
			}
		case reissueFailure == nil:
			if !errors.Is(accepted, ErrInvitationRevoked) {
				t.Fatalf("reissue won but acceptance error = %v", accepted)
			}
			if reissued.InvitationToken == "" {
				t.Fatal("winning reissue did not return its token")
			}
		default:
			t.Fatalf("race had no winner: accept=%v reissue=%v", accepted, reissueFailure)
		}
	})
}

func TestInvitationPrepareAcceptanceHashesOnceAndCanBeDestroyed(t *testing.T) {
	_, service, _ := newInvitationHarness(t)
	created := mustCreateInvitation(t, service, CreateInvitationRequest{
		Email:           "target@example.com",
		WorkspaceID:     "wrk_a",
		WorkspaceRole:   WorkspaceRoleViewer,
		InvitedByUserID: "usr_admin",
	})
	acceptance, err := service.PrepareAcceptance(AcceptInvitationRequest{
		InvitationToken: created.InvitationToken,
		UserID:          " usr_target ",
		Email:           " TARGET@example.com ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(acceptance.Verifier) != 32 ||
		acceptance.UserID != "usr_target" ||
		acceptance.Email != "target@example.com" {
		t.Fatalf("prepared acceptance = %+v", acceptance)
	}
	expectedVerifier := append([]byte(nil), acceptance.Verifier...)
	second, err := service.PrepareAcceptance(AcceptInvitationRequest{
		InvitationToken: created.InvitationToken,
		UserID:          "usr_target",
		Email:           "target@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Destroy()
	if !bytes.Equal(expectedVerifier, second.Verifier) {
		t.Fatal("same raw token produced verifier drift")
	}
	acceptance.Destroy()
	if acceptance.UserID != "" || acceptance.Email != "" || acceptance.Verifier != nil {
		t.Fatalf("destroyed acceptance retained data: %+v", acceptance)
	}
	if _, err := service.PrepareAcceptance(AcceptInvitationRequest{
		InvitationToken: InvitationTokenPrefix + "not-canonical",
		UserID:          "usr_target",
		Email:           "target@example.com",
	}); !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("malformed prepared acceptance error = %v", err)
	}
}

func mustCreateInvitation(
	t *testing.T,
	service *InvitationService,
	request CreateInvitationRequest,
) CreateInvitationResult {
	t.Helper()
	result, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
