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

func TestInvitationServiceCreatesHashedScopedInvitationAndAcceptsOnce(t *testing.T) {
	store, service, now := newInvitationHarness(t)
	result, err := service.Create(context.Background(), CreateInvitationRequest{
		Email:           "  TARGET@Example.com ",
		WorkspaceID:     "wrk_a",
		ProjectID:       "prj_a",
		ProjectRole:     ProjectRoleViewer,
		InvitedByUserID: "usr_admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Invitation.Email != "target@example.com" {
		t.Fatalf("normalized email = %q", result.Invitation.Email)
	}
	if result.Invitation.ProjectRole != ProjectRoleViewer ||
		result.Invitation.WorkspaceRole != "" {
		t.Fatalf("unexpected role bounds: %+v", result.Invitation)
	}
	if result.Invitation.ExpiresAt != now.Add(defaultInvitationTTL) {
		t.Fatalf("default expiry = %v, want %v", result.Invitation.ExpiresAt, now.Add(defaultInvitationTTL))
	}
	if !validRawInvitationToken(result.InvitationToken) {
		t.Fatalf("invalid minted token shape %q", result.InvitationToken)
	}

	stored := store.invitations[result.Invitation.ID]
	if len(stored.Verifier) != 32 {
		t.Fatalf("stored verifier length = %d", len(stored.Verifier))
	}
	if bytes.Contains(stored.Verifier, []byte(result.InvitationToken)) ||
		string(stored.Verifier) == result.InvitationToken {
		t.Fatal("raw invitation token was persisted")
	}
	encodedMetadata, err := json.Marshal(result.Invitation)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encodedMetadata, []byte(result.InvitationToken)) ||
		bytes.Contains(encodedMetadata, stored.Verifier) {
		t.Fatal("safe invitation metadata exposed a credential or verifier")
	}

	accepted, err := service.Accept(context.Background(), AcceptInvitationRequest{
		InvitationToken: result.InvitationToken,
		UserID:          "usr_target",
		Email:           "TARGET@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.AcceptedAt == nil || accepted.AcceptedByUserID != "usr_target" ||
		accepted.ResourceVersion != 2 {
		t.Fatalf("unexpected acceptance metadata: %+v", accepted)
	}
	members, err := store.ListProjectMembers(context.Background(), "prj_a")
	if err != nil {
		t.Fatal(err)
	}
	target, ok := findProjectMember(members, "usr_target")
	if !ok {
		t.Fatal("accepted user did not become a project member")
	}
	if target.Role != ProjectRoleViewer || target.InvitedBy != "usr_admin" ||
		target.AcceptedAt == nil {
		t.Fatalf("unexpected accepted membership: %+v", target)
	}

	_, err = service.Accept(context.Background(), AcceptInvitationRequest{
		InvitationToken: result.InvitationToken,
		UserID:          "usr_target",
		Email:           "target@example.com",
	})
	if !errors.Is(err, ErrInvitationConsumed) {
		t.Fatalf("second acceptance error = %v, want %v", err, ErrInvitationConsumed)
	}
}

func TestInvitationAcceptanceIsConcurrencySafe(t *testing.T) {
	store, service, _ := newInvitationHarness(t)
	result, err := service.Create(context.Background(), CreateInvitationRequest{
		Email:           "target@example.com",
		WorkspaceID:     "wrk_a",
		WorkspaceRole:   WorkspaceRoleDeployer,
		InvitedByUserID: "usr_admin",
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, acceptErr := service.Accept(context.Background(), AcceptInvitationRequest{
				InvitationToken: result.InvitationToken,
				UserID:          "usr_target",
				Email:           "target@example.com",
			})
			errs <- acceptErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	consumed := 0
	for acceptErr := range errs {
		switch {
		case acceptErr == nil:
			successes++
		case errors.Is(acceptErr, ErrInvitationConsumed):
			consumed++
		default:
			t.Fatalf("unexpected concurrent acceptance error: %v", acceptErr)
		}
	}
	if successes != 1 || consumed != 1 {
		t.Fatalf("concurrent results: successes=%d consumed=%d", successes, consumed)
	}
	members, err := store.ListWorkspaceMembers(context.Background(), "wrk_a")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, member := range members {
		if member.User.ID == "usr_target" {
			count++
			if member.Role != WorkspaceRoleDeployer {
				t.Fatalf("accepted workspace role = %q", member.Role)
			}
		}
	}
	if count != 1 {
		t.Fatalf("target workspace membership count = %d, want 1", count)
	}
}

func TestInvitationMetadataGetAndConditionalRevokeAreConcurrencySafe(t *testing.T) {
	store, service, _ := newInvitationHarness(t)
	created, err := service.Create(context.Background(), CreateInvitationRequest{
		Email:           "target@example.com",
		WorkspaceID:     "wrk_a",
		ProjectID:       "prj_a",
		ProjectRole:     ProjectRoleViewer,
		InvitedByUserID: "usr_admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := service.Get(context.Background(), GetInvitationRequest{
		InvitationID: created.Invitation.ID,
		WorkspaceID:  "wrk_a",
		ProjectID:    "prj_a",
		ActorUserID:  "usr_editor",
	})
	if err != nil || metadata.ResourceVersion != 1 {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
	if _, err := service.Get(context.Background(), GetInvitationRequest{
		InvitationID: created.Invitation.ID,
		WorkspaceID:  "wrk_a",
		ActorUserID:  "usr_admin",
	}); !errors.Is(err, ErrInvitationNotFound) {
		t.Fatalf("cross-scope get error=%v, want %v", err, ErrInvitationNotFound)
	}

	expected := metadata.ResourceVersion
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, revokeErr := service.Revoke(context.Background(), RevokeInvitationRequest{
				InvitationID:    created.Invitation.ID,
				WorkspaceID:     "wrk_a",
				ProjectID:       "prj_a",
				RevokedByUserID: "usr_editor",
				ExpectedVersion: &expected,
			})
			errs <- revokeErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for revokeErr := range errs {
		switch {
		case revokeErr == nil:
			successes++
		case errors.Is(revokeErr, ErrInvitationVersionConflict):
			conflicts++
		default:
			t.Fatalf("revoke error=%v", revokeErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	stored := store.invitations[created.Invitation.ID]
	if stored.RevokedAt == nil || stored.ResourceVersion != 2 {
		t.Fatalf("stored invitation=%+v", stored.Invitation)
	}
}

func TestInvitationAuthorityAndRoleBounds(t *testing.T) {
	t.Run("workspace member cannot invite", func(t *testing.T) {
		_, service, _ := newInvitationHarness(t)
		_, err := service.Create(context.Background(), CreateInvitationRequest{
			Email:           "target@example.com",
			WorkspaceID:     "wrk_a",
			WorkspaceRole:   WorkspaceRoleViewer,
			InvitedByUserID: "usr_member",
		})
		if !errors.Is(err, ErrAuthorizationDenied) {
			t.Fatalf("error = %v, want %v", err, ErrAuthorizationDenied)
		}
	})

	t.Run("project editor cannot grant owner", func(t *testing.T) {
		_, service, _ := newInvitationHarness(t)
		_, err := service.Create(context.Background(), CreateInvitationRequest{
			Email:           "target@example.com",
			WorkspaceID:     "wrk_a",
			ProjectID:       "prj_a",
			ProjectRole:     ProjectRoleOwner,
			InvitedByUserID: "usr_editor",
		})
		if !errors.Is(err, ErrAuthorizationDenied) {
			t.Fatalf("error = %v, want %v", err, ErrAuthorizationDenied)
		}
	})

	t.Run("project editor may grant bounded viewer", func(t *testing.T) {
		_, service, _ := newInvitationHarness(t)
		result, err := service.Create(context.Background(), CreateInvitationRequest{
			Email:           "target@example.com",
			WorkspaceID:     "wrk_a",
			ProjectID:       "prj_a",
			ProjectRole:     ProjectRoleViewer,
			InvitedByUserID: "usr_editor",
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Invitation.ProjectRole != ProjectRoleViewer {
			t.Fatalf("role = %q", result.Invitation.ProjectRole)
		}
	})

	t.Run("project lineage is server owned", func(t *testing.T) {
		_, service, _ := newInvitationHarness(t)
		_, err := service.Create(context.Background(), CreateInvitationRequest{
			Email:           "target@example.com",
			WorkspaceID:     "wrk_other",
			ProjectID:       "prj_a",
			ProjectRole:     ProjectRoleViewer,
			InvitedByUserID: "usr_admin",
		})
		if !errors.Is(err, ErrInvitationScopeNotActive) {
			t.Fatalf("error = %v, want %v", err, ErrInvitationScopeNotActive)
		}
	})

	for name, request := range map[string]CreateInvitationRequest{
		"both roles": {
			WorkspaceID: "wrk_a", ProjectID: "prj_a",
			WorkspaceRole: WorkspaceRoleAdmin, ProjectRole: ProjectRoleOwner,
		},
		"project without project role": {
			WorkspaceID: "wrk_a", ProjectID: "prj_a",
			WorkspaceRole: WorkspaceRoleViewer,
		},
		"workspace with project role": {
			WorkspaceID: "wrk_a", ProjectRole: ProjectRoleViewer,
		},
		"unknown workspace role": {
			WorkspaceID: "wrk_a", WorkspaceRole: WorkspaceRole("superadmin"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, service, _ := newInvitationHarness(t)
			request.Email = "target@example.com"
			request.InvitedByUserID = "usr_admin"
			_, err := service.Create(context.Background(), request)
			if !errors.Is(err, ErrInvalidInvitation) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidInvitation)
			}
		})
	}
}

func TestInvitationExpiryRevocationAndEmailBinding(t *testing.T) {
	t.Run("request and durable email must both match", func(t *testing.T) {
		store, service, _ := newInvitationHarness(t)
		result, err := service.Create(context.Background(), CreateInvitationRequest{
			Email:           "target@example.com",
			WorkspaceID:     "wrk_a",
			WorkspaceRole:   WorkspaceRoleViewer,
			InvitedByUserID: "usr_admin",
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.Accept(context.Background(), AcceptInvitationRequest{
			InvitationToken: result.InvitationToken,
			UserID:          "usr_target",
			Email:           "other@example.com",
		})
		if !errors.Is(err, ErrInvitationEmailMismatch) {
			t.Fatalf("request mismatch error = %v", err)
		}
		store.users["usr_target"] = User{ID: "usr_target", Email: "changed@example.com"}
		_, err = service.Accept(context.Background(), AcceptInvitationRequest{
			InvitationToken: result.InvitationToken,
			UserID:          "usr_target",
			Email:           "target@example.com",
		})
		if !errors.Is(err, ErrInvitationEmailMismatch) {
			t.Fatalf("durable mismatch error = %v", err)
		}
	})

	t.Run("expired invitation is inert", func(t *testing.T) {
		_, service, clock := newInvitationHarnessWithClock(t)
		result, err := service.Create(context.Background(), CreateInvitationRequest{
			Email:           "target@example.com",
			WorkspaceID:     "wrk_a",
			WorkspaceRole:   WorkspaceRoleViewer,
			InvitedByUserID: "usr_admin",
			ExpiresAt:       clock.now.Add(minInvitationTTL),
		})
		if err != nil {
			t.Fatal(err)
		}
		clock.now = result.Invitation.ExpiresAt
		_, err = service.Accept(context.Background(), AcceptInvitationRequest{
			InvitationToken: result.InvitationToken,
			UserID:          "usr_target",
			Email:           "target@example.com",
		})
		if !errors.Is(err, ErrInvitationExpired) {
			t.Fatalf("error = %v, want %v", err, ErrInvitationExpired)
		}
	})

	t.Run("revocation is attributed immutable and blocks acceptance", func(t *testing.T) {
		_, service, _ := newInvitationHarness(t)
		result, err := service.Create(context.Background(), CreateInvitationRequest{
			Email:           "target@example.com",
			WorkspaceID:     "wrk_a",
			ProjectID:       "prj_a",
			ProjectRole:     ProjectRoleViewer,
			InvitedByUserID: "usr_editor",
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.Revoke(context.Background(), RevokeInvitationRequest{
			InvitationID:    result.Invitation.ID,
			WorkspaceID:     "wrk_a",
			ProjectID:       "prj_a",
			RevokedByUserID: "usr_member",
		})
		if !errors.Is(err, ErrAuthorizationDenied) {
			t.Fatalf("unauthorized revoke error = %v", err)
		}
		revoked, err := service.Revoke(context.Background(), RevokeInvitationRequest{
			InvitationID:    result.Invitation.ID,
			WorkspaceID:     "wrk_a",
			ProjectID:       "prj_a",
			RevokedByUserID: "usr_editor",
			Reason:          "sent to wrong person",
		})
		if err != nil {
			t.Fatal(err)
		}
		if revoked.RevokedAt == nil || revoked.RevokedByUserID != "usr_editor" ||
			revoked.RevokedReason != "sent to wrong person" ||
			revoked.ResourceVersion != 2 {
			t.Fatalf("unexpected revocation: %+v", revoked)
		}
		repeated, err := service.Revoke(context.Background(), RevokeInvitationRequest{
			InvitationID:    result.Invitation.ID,
			WorkspaceID:     "wrk_a",
			ProjectID:       "prj_a",
			RevokedByUserID: "usr_admin",
			Reason:          "replacement reason",
		})
		if err != nil {
			t.Fatal(err)
		}
		if repeated.RevokedByUserID != revoked.RevokedByUserID ||
			repeated.RevokedReason != revoked.RevokedReason ||
			repeated.ResourceVersion != revoked.ResourceVersion {
			t.Fatalf("idempotent revoke changed attribution: before=%+v after=%+v", revoked, repeated)
		}
		_, err = service.Accept(context.Background(), AcceptInvitationRequest{
			InvitationToken: result.InvitationToken,
			UserID:          "usr_target",
			Email:           "target@example.com",
		})
		if !errors.Is(err, ErrInvitationRevoked) {
			t.Fatalf("accept revoked error = %v", err)
		}
	})
}

func TestInvitationPendingAndExistingMembershipDoNotConsumeSecret(t *testing.T) {
	store, service, now := newInvitationHarness(t)
	first, err := service.Create(context.Background(), CreateInvitationRequest{
		Email:           "target@example.com",
		WorkspaceID:     "wrk_a",
		ProjectID:       "prj_a",
		ProjectRole:     ProjectRoleViewer,
		InvitedByUserID: "usr_admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), CreateInvitationRequest{
		Email:           "target@example.com",
		WorkspaceID:     "wrk_a",
		ProjectID:       "prj_a",
		ProjectRole:     ProjectRoleEditor,
		InvitedByUserID: "usr_admin",
	})
	if !errors.Is(err, ErrInvitationPending) {
		t.Fatalf("duplicate pending error = %v", err)
	}

	accepted := now
	store.SeedProject("prj_a", "wrk_a", ProjectMember{
		ProjectID:  "prj_a",
		User:       User{ID: "usr_target", Email: "target@example.com"},
		Role:       ProjectRoleViewer,
		AcceptedAt: &accepted,
	})
	_, err = service.Accept(context.Background(), AcceptInvitationRequest{
		InvitationToken: first.InvitationToken,
		UserID:          "usr_target",
		Email:           "target@example.com",
	})
	if !errors.Is(err, ErrInvitationMemberExists) {
		t.Fatalf("existing membership error = %v", err)
	}
	if store.invitations[first.Invitation.ID].AcceptedAt != nil {
		t.Fatal("membership conflict consumed invitation")
	}
}

func TestInvitationCreationRejectsExistingDirectMember(t *testing.T) {
	store, service, now := newInvitationHarness(t)
	accepted := now
	store.SeedProject("prj_a", "wrk_a", ProjectMember{
		ProjectID:  "prj_a",
		User:       User{ID: "usr_target", Email: "target@example.com"},
		Role:       ProjectRoleViewer,
		AcceptedAt: &accepted,
	})
	_, err := service.Create(context.Background(), CreateInvitationRequest{
		Email:           "target@example.com",
		WorkspaceID:     "wrk_a",
		ProjectID:       "prj_a",
		ProjectRole:     ProjectRoleEditor,
		InvitedByUserID: "usr_admin",
	})
	if !errors.Is(err, ErrInvitationMemberExists) {
		t.Fatalf("existing member creation error = %v", err)
	}
}

func TestInvitationServiceRejectsWeakKeyMalformedTokenAndUnboundedExpiry(t *testing.T) {
	store, _, now := newInvitationHarness(t)
	if _, err := NewInvitationService(InvitationServiceConfig{
		Store: store, MACKey: make([]byte, InvitationMinMACBytes-1),
	}); err == nil {
		t.Fatal("weak MAC key was accepted")
	}
	service, err := NewInvitationService(InvitationServiceConfig{
		Store: store, MACKey: bytes.Repeat([]byte{0x42}, InvitationMinMACBytes),
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), CreateInvitationRequest{
		Email: "target@example.com", WorkspaceID: "wrk_a",
		WorkspaceRole: WorkspaceRoleViewer, InvitedByUserID: "usr_admin",
		ExpiresAt: now.Add(maxInvitationTTL + time.Second),
	})
	if !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("unbounded expiry error = %v", err)
	}
	_, err = service.Accept(context.Background(), AcceptInvitationRequest{
		InvitationToken: InvitationTokenPrefix + "not-canonical",
		UserID:          "usr_target", Email: "target@example.com",
	})
	if !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("malformed token error = %v", err)
	}
}

type mutableInvitationClock struct {
	now time.Time
}

func newInvitationHarness(t *testing.T) (*MemoryAccessStore, *InvitationService, time.Time) {
	t.Helper()
	store, service, clock := newInvitationHarnessWithClock(t)
	return store, service, clock.now
}

func newInvitationHarnessWithClock(t *testing.T) (*MemoryAccessStore, *InvitationService, *mutableInvitationClock) {
	t.Helper()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	accepted := now.Add(-24 * time.Hour)
	store := NewMemoryAccessStore()
	store.SeedWorkspace(Workspace{
		ID: "wrk_a", Slug: "alpha", DisplayName: "Alpha",
		CreatedAt: accepted, UpdatedAt: accepted,
	},
		WorkspaceMember{
			WorkspaceID: "wrk_a",
			User:        User{ID: "usr_admin", Email: "admin@example.com"},
			Role:        WorkspaceRoleAdmin, AcceptedAt: &accepted,
		},
		WorkspaceMember{
			WorkspaceID: "wrk_a",
			User:        User{ID: "usr_member", Email: "member@example.com"},
			Role:        WorkspaceRoleMember, AcceptedAt: &accepted,
		},
	)
	store.SeedProject("prj_a", "wrk_a",
		ProjectMember{
			ProjectID: "prj_a",
			User:      User{ID: "usr_admin", Email: "admin@example.com"},
			Role:      ProjectRoleOwner, AcceptedAt: &accepted,
		},
		ProjectMember{
			ProjectID: "prj_a",
			User:      User{ID: "usr_editor", Email: "editor@example.com"},
			Role:      ProjectRoleEditor, AcceptedAt: &accepted,
		},
	)
	store.SeedUser(User{ID: "usr_target", Email: "target@example.com"})
	store.SeedWorkspace(Workspace{
		ID: "wrk_other", Slug: "other", DisplayName: "Other",
		CreatedAt: accepted, UpdatedAt: accepted,
	})

	clock := &mutableInvitationClock{now: now}
	var randomMu sync.Mutex
	randomCall := byte(0)
	service, err := NewInvitationService(InvitationServiceConfig{
		Store:  store,
		MACKey: bytes.Repeat([]byte{0x5a}, InvitationMinMACBytes),
		Clock:  func() time.Time { return clock.now },
		Random: func(dst []byte) error {
			randomMu.Lock()
			defer randomMu.Unlock()
			randomCall++
			for index := range dst {
				dst[index] = randomCall + byte(index)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, service, clock
}

func findProjectMember(members []ProjectMember, userID string) (ProjectMember, bool) {
	for _, member := range members {
		if member.User.ID == userID {
			return member, true
		}
	}
	return ProjectMember{}, false
}
