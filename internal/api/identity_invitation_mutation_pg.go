// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

// PostgresIdentityInvitationMutationCoordinator is the durable invitation
// write boundary. It uses one serializable transaction for current-session
// validation, current RBAC, invitation/membership state, idempotency, and audit.
type PostgresIdentityInvitationMutationCoordinator struct {
	Pool        *pg.Pool
	Invitations *identity.InvitationService
	Store       *identity.PostgresStore
	Idempotency *idempotency.PostgresStore
	Audit       *audit.PostgresChain
	Now         func() time.Time
}

func NewPostgresIdentityInvitationMutationCoordinator(
	pool *pg.Pool,
	invitations *identity.InvitationService,
	store *identity.PostgresStore,
	idem *idempotency.PostgresStore,
	chain *audit.PostgresChain,
	now func() time.Time,
) *PostgresIdentityInvitationMutationCoordinator {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PostgresIdentityInvitationMutationCoordinator{
		Pool: pool, Invitations: invitations, Store: store,
		Idempotency: idem, Audit: chain, Now: now,
	}
}

func (c *PostgresIdentityInvitationMutationCoordinator) CreateIdentityInvitation(
	ctx context.Context,
	request IdentityInvitationCreateMutationRequest,
) (TenantMutationResult, error) {
	if err := c.validateContext(request.identityInvitationMutationContext); err != nil {
		return TenantMutationResult{}, err
	}
	plan, err := c.Invitations.PrepareCreate(identity.CreateInvitationRequest{
		Email:           request.Email,
		WorkspaceID:     request.WorkspaceID,
		ProjectID:       request.ProjectID,
		WorkspaceRole:   request.WorkspaceRole,
		ProjectRole:     request.ProjectRole,
		InvitedByUserID: request.Principal.UserID,
		ExpiresAt:       request.ExpiresAt,
	})
	if err != nil {
		return TenantMutationResult{}, err
	}
	defer plan.Destroy()
	return c.runScopedMutation(
		ctx,
		request.identityInvitationMutationContext,
		request.WorkspaceID,
		request.ProjectID,
		func(txCtx context.Context, tx *pg.Tx, reservation idempotency.Reservation, attemptNow time.Time) (TenantMutationResult, error) {
			created, err := plan.Commit(txCtx, identity.InvitationCreatorFunc(
				func(ctx context.Context, credential identity.InvitationCredential) (identity.Invitation, error) {
					return c.Store.CreateInvitationInTx(ctx, tx, credential)
				},
			))
			if err != nil {
				return TenantMutationResult{}, err
			}
			response, err := identityInvitationCredentialResponse(
				http.StatusCreated,
				created.InvitationToken,
				created.Invitation,
			)
			if err != nil {
				return TenantMutationResult{}, err
			}
			if _, err := c.Idempotency.CompleteSensitiveSuccessInTx(
				txCtx, tx, reservation.Reference(), response,
			); err != nil {
				return TenantMutationResult{}, fmt.Errorf("identity invitation encrypted idempotency completion: %w", err)
			}
			if err := c.appendAudit(txCtx, tx, request.identityInvitationMutationContext, identityInvitationAudit{
				WorkspaceID: created.Invitation.WorkspaceID,
				ProjectID:   created.Invitation.ProjectID,
				ResourceID:  created.Invitation.ID,
				Action:      "identity.invitation.create",
				After:       invitationAuditMetadata(created.Invitation),
			}, attemptNow); err != nil {
				return TenantMutationResult{}, err
			}
			return TenantMutationResult{Response: response}, nil
		},
	)
}

func (c *PostgresIdentityInvitationMutationCoordinator) ReissueIdentityInvitation(
	ctx context.Context,
	request IdentityInvitationReissueMutationRequest,
) (TenantMutationResult, error) {
	if err := c.validateContext(request.identityInvitationMutationContext); err != nil {
		return TenantMutationResult{}, err
	}
	plan, err := c.Invitations.PrepareReissue(identity.ReissueInvitationRequest{
		InvitationID:     request.InvitationID,
		WorkspaceID:      request.WorkspaceID,
		ProjectID:        request.ProjectID,
		ReissuedByUserID: request.Principal.UserID,
		ExpiresAt:        request.ExpiresAt,
	})
	if err != nil {
		return TenantMutationResult{}, err
	}
	defer plan.Destroy()
	return c.runScopedMutation(
		ctx,
		request.identityInvitationMutationContext,
		request.WorkspaceID,
		request.ProjectID,
		func(txCtx context.Context, tx *pg.Tx, reservation idempotency.Reservation, attemptNow time.Time) (TenantMutationResult, error) {
			reissued, err := plan.Commit(txCtx, identity.InvitationReissuerFunc(
				func(ctx context.Context, replacement identity.InvitationReplacement) (identity.ReissueInvitationMetadata, error) {
					return c.Store.ReissueInvitationInTx(ctx, tx, replacement)
				},
			))
			if err != nil {
				return TenantMutationResult{}, err
			}
			response, err := identityInvitationCredentialResponse(
				http.StatusCreated,
				reissued.InvitationToken,
				reissued.Metadata.Invitation,
			)
			if err != nil {
				return TenantMutationResult{}, err
			}
			if _, err := c.Idempotency.CompleteSensitiveSuccessInTx(
				txCtx, tx, reservation.Reference(), response,
			); err != nil {
				return TenantMutationResult{}, fmt.Errorf("identity invitation reissue encrypted idempotency completion: %w", err)
			}
			if err := c.appendAudit(txCtx, tx, request.identityInvitationMutationContext, identityInvitationAudit{
				WorkspaceID: reissued.Metadata.Invitation.WorkspaceID,
				ProjectID:   reissued.Metadata.Invitation.ProjectID,
				ResourceID:  reissued.Metadata.Invitation.ID,
				Action:      "identity.invitation.reissue",
				Before:      invitationAuditMetadata(reissued.Metadata.ReplacedInvitation),
				After:       invitationAuditMetadata(reissued.Metadata.Invitation),
				Attrs: map[string]any{
					"replaces_invitation_id": reissued.Metadata.ReplacedInvitation.ID,
				},
			}, attemptNow); err != nil {
				return TenantMutationResult{}, err
			}
			return TenantMutationResult{Response: response}, nil
		},
	)
}

func (c *PostgresIdentityInvitationMutationCoordinator) RevokeIdentityInvitation(
	ctx context.Context,
	request IdentityInvitationRevokeMutationRequest,
) (TenantMutationResult, error) {
	if err := c.validateContext(request.identityInvitationMutationContext); err != nil {
		return TenantMutationResult{}, err
	}
	if strings.TrimSpace(request.InvitationID) == "" ||
		strings.TrimSpace(request.WorkspaceID) == "" {
		return TenantMutationResult{}, identity.ErrInvalidInvitation
	}
	return c.runScopedMutation(
		ctx,
		request.identityInvitationMutationContext,
		request.WorkspaceID,
		request.ProjectID,
		func(txCtx context.Context, tx *pg.Tx, reservation idempotency.Reservation, attemptNow time.Time) (TenantMutationResult, error) {
			revoked, err := c.Store.RevokeInvitationInTx(txCtx, tx, identity.RevokeInvitationRequest{
				InvitationID:    request.InvitationID,
				WorkspaceID:     request.WorkspaceID,
				ProjectID:       request.ProjectID,
				RevokedByUserID: request.Principal.UserID,
				Reason:          "user-requested",
				ExpectedVersion: &request.ExpectedVersion,
			}, attemptNow)
			if err != nil {
				return TenantMutationResult{}, err
			}
			response := idempotency.StoredResponse{
				StatusCode: http.StatusNoContent,
				Header:     http.Header{"Cache-Control": []string{"no-store"}},
			}
			if _, err := c.Idempotency.CompleteSuccessInTx(
				txCtx, tx, reservation.Reference(), response,
			); err != nil {
				return TenantMutationResult{}, err
			}
			if err := c.appendAudit(txCtx, tx, request.identityInvitationMutationContext, identityInvitationAudit{
				WorkspaceID: revoked.WorkspaceID,
				ProjectID:   revoked.ProjectID,
				ResourceID:  revoked.ID,
				Action:      "identity.invitation.revoke",
				After:       invitationAuditMetadata(revoked),
			}, attemptNow); err != nil {
				return TenantMutationResult{}, err
			}
			return TenantMutationResult{Response: response}, nil
		},
	)
}

func (c *PostgresIdentityInvitationMutationCoordinator) AcceptIdentityInvitation(
	ctx context.Context,
	request IdentityInvitationAcceptMutationRequest,
) (TenantMutationResult, error) {
	if err := c.validateContext(request.identityInvitationMutationContext); err != nil {
		return TenantMutationResult{}, err
	}
	acceptance, err := c.Invitations.PrepareAcceptance(identity.AcceptInvitationRequest{
		InvitationToken: request.InvitationToken,
		UserID:          request.Principal.UserID,
		Email:           request.Principal.Email,
	})
	if err != nil {
		return TenantMutationResult{}, err
	}
	defer acceptance.Destroy()
	mutationCtx := idempotency.WithActor(ctx, idempotency.UserActor(request.Principal.UserID))
	mutationCtx = idempotency.WithTenant(mutationCtx, idempotency.Tenant("identity:invitation-accept"))
	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		attemptNow := c.now().Truncate(time.Microsecond)
		err := c.Pool.WithTxOpts(mutationCtx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			if err := revalidateAutomationTokenSession(
				mutationCtx, tx, request.Principal, request.Session, attemptNow,
			); err != nil {
				return err
			}
			reservation, err := c.Idempotency.ReserveInTx(
				mutationCtx, tx, request.Key, request.Fingerprint, 24*time.Hour,
			)
			if err != nil {
				return err
			}
			if reservation.IsTerminal() {
				if reservation.Response == nil {
					return errors.New("identity invitation accept replay has no stored response")
				}
				result = TenantMutationResult{Response: reservation.Response.Clone(), Replayed: true}
				return nil
			}
			accepted, err := c.Store.AcceptInvitationInTx(mutationCtx, tx, acceptance, attemptNow)
			if err != nil {
				return err
			}
			response, err := identityInvitationMetadataResponse(http.StatusOK, accepted)
			if err != nil {
				return err
			}
			if _, err := c.Idempotency.CompleteSuccessInTx(
				mutationCtx, tx, reservation.Reference(), response,
			); err != nil {
				return err
			}
			if err := c.appendAudit(mutationCtx, tx, request.identityInvitationMutationContext, identityInvitationAudit{
				WorkspaceID: accepted.WorkspaceID,
				ProjectID:   accepted.ProjectID,
				ResourceID:  accepted.ID,
				Action:      "identity.invitation.accept",
				After:       invitationAuditMetadata(accepted),
			}, attemptNow); err != nil {
				return err
			}
			result = TenantMutationResult{Response: response}
			return nil
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, err
		}
	}
	return TenantMutationResult{}, errors.New("identity invitation accept serializable retries exhausted")
}

type identityInvitationMutationWork func(
	context.Context,
	*pg.Tx,
	idempotency.Reservation,
	time.Time,
) (TenantMutationResult, error)

func (c *PostgresIdentityInvitationMutationCoordinator) runScopedMutation(
	ctx context.Context,
	request identityInvitationMutationContext,
	workspaceID string,
	projectID string,
	work identityInvitationMutationWork,
) (TenantMutationResult, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	projectID = strings.TrimSpace(projectID)
	if workspaceID == "" {
		return TenantMutationResult{}, identity.ErrInvalidInvitation
	}
	mutationCtx := idempotency.WithActor(ctx, idempotency.UserActor(request.Principal.UserID))
	mutationCtx = idempotency.WithTenant(
		mutationCtx,
		identityInvitationIdempotencyTenant(workspaceID, projectID),
	)
	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		attemptNow := c.now().Truncate(time.Microsecond)
		err := c.Pool.WithTxOpts(mutationCtx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			if err := revalidateAutomationTokenSession(
				mutationCtx, tx, request.Principal, request.Session, attemptNow,
			); err != nil {
				return err
			}
			if err := c.Store.AuthorizeInvitationScopeInTx(
				mutationCtx,
				tx,
				identity.ListInvitationsRequest{
					WorkspaceID: workspaceID,
					ProjectID:   projectID,
					ActorUserID: request.Principal.UserID,
				},
			); err != nil {
				return err
			}
			reservation, err := c.Idempotency.ReserveInTx(
				mutationCtx, tx, request.Key, request.Fingerprint, 24*time.Hour,
			)
			if err != nil {
				return err
			}
			if reservation.IsTerminal() {
				if reservation.Response == nil {
					return errors.New("identity invitation replay has no stored response")
				}
				result = TenantMutationResult{Response: reservation.Response.Clone(), Replayed: true}
				return nil
			}
			result, err = work(mutationCtx, tx, reservation, attemptNow)
			return err
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, err
		}
	}
	return TenantMutationResult{}, errors.New("identity invitation mutation serializable retries exhausted")
}

func (c *PostgresIdentityInvitationMutationCoordinator) validateContext(
	request identityInvitationMutationContext,
) error {
	if c == nil || c.Pool == nil || c.Invitations == nil || c.Store == nil ||
		c.Idempotency == nil || c.Audit == nil {
		return errors.New("identity invitation mutation coordinator is not fully configured")
	}
	if err := validateAutomationTokenSessionTuple(request.Principal, request.Session); err != nil {
		return err
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	return idempotency.ValidateFingerprint(request.Fingerprint)
}

type identityInvitationCredentialResult struct {
	InvitationToken string              `json:"invitation_token"`
	Invitation      identity.Invitation `json:"invitation"`
}

func identityInvitationCredentialResponse(
	status int,
	token string,
	invitation identity.Invitation,
) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(identityInvitationCredentialResult{
		InvitationToken: token,
		Invitation:      invitation,
	})
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("identity invitation credential response: %w", err)
	}
	return idempotency.StoredResponse{
		StatusCode: status,
		Header: http.Header{
			"Content-Type":  []string{"application/json"},
			"Cache-Control": []string{"no-store"},
			"Location": []string{
				v1alphaPrefix + invitationRoute + "/" + invitation.ID,
			},
		},
		Body: append(body, '\n'),
	}, nil
}

func identityInvitationMetadataResponse(
	status int,
	invitation identity.Invitation,
) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(invitation)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("identity invitation metadata response: %w", err)
	}
	return idempotency.StoredResponse{
		StatusCode: status,
		Header: http.Header{
			"Content-Type":  []string{"application/json"},
			"Cache-Control": []string{"no-store"},
		},
		Body: append(body, '\n'),
	}, nil
}

type identityInvitationAudit struct {
	WorkspaceID string
	ProjectID   string
	ResourceID  string
	Action      string
	Before      map[string]any
	After       map[string]any
	Attrs       map[string]any
}

func (c *PostgresIdentityInvitationMutationCoordinator) appendAudit(
	ctx context.Context,
	tx *pg.Tx,
	request identityInvitationMutationContext,
	event identityInvitationAudit,
	now time.Time,
) error {
	attrs := map[string]any{
		"session_id": request.Session.ID,
	}
	for key, value := range event.Attrs {
		attrs[key] = value
	}
	if _, err := c.Audit.AppendInTx(ctx, tx, audit.Row{
		RequestID:      request.RequestID,
		OccurredAt:     now,
		RecordedAt:     now,
		ActorID:        request.Principal.UserID,
		ActorType:      "user",
		AuthMethod:     request.Principal.AuthMethod,
		SourceIP:       normalizedMutationSourceIP(request.SourceIP),
		UserAgent:      request.UserAgent,
		WorkspaceID:    event.WorkspaceID,
		ProjectID:      event.ProjectID,
		ResourceKind:   "identity_invitation",
		ResourceID:     event.ResourceID,
		Action:         event.Action,
		PolicyDecision: "allowed",
		Before:         event.Before,
		After:          event.After,
		Attrs:          attrs,
	}); err != nil {
		return fmt.Errorf("%s audit append: %w", event.Action, err)
	}
	return nil
}

func invitationAuditMetadata(invitation identity.Invitation) map[string]any {
	return map[string]any{
		"email":                  invitation.Email,
		"workspace_role":         invitation.WorkspaceRole,
		"project_role":           invitation.ProjectRole,
		"token_prefix":           invitation.TokenPrefix,
		"expires_at":             invitation.ExpiresAt,
		"revoked_at":             invitation.RevokedAt,
		"revoked_reason":         invitation.RevokedReason,
		"accepted_at":            invitation.AcceptedAt,
		"replaces_invitation_id": invitation.ReplacesInvitationID,
	}
}

func identityInvitationIdempotencyTenant(workspaceID, projectID string) idempotency.Tenant {
	if projectID != "" {
		return projectScopedIdempotencyTenant(workspaceID, projectID)
	}
	return idempotency.Tenant(fmt.Sprintf("workspace:%d:%s", len(workspaceID), workspaceID))
}

func (c *PostgresIdentityInvitationMutationCoordinator) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

var _ IdentityInvitationMutationCoordinator = (*PostgresIdentityInvitationMutationCoordinator)(nil)
