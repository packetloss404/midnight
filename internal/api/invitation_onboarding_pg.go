// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
)

type PostgresInvitationOnboardingCoordinator struct {
	Pool         *pg.Pool
	Identity     *identity.PostgresStore
	Invitations  *identity.InvitationService
	Audit        *audit.PostgresChain
	Clock        func() time.Time
	beforeCommit func() error
}

func NewPostgresInvitationOnboardingCoordinator(pool *pg.Pool, store *identity.PostgresStore, invitations *identity.InvitationService, chain *audit.PostgresChain) *PostgresInvitationOnboardingCoordinator {
	return &PostgresInvitationOnboardingCoordinator{Pool: pool, Identity: store, Invitations: invitations, Audit: chain, Clock: func() time.Time { return time.Now().UTC() }}
}

func (c *PostgresInvitationOnboardingCoordinator) OnboardInvitation(ctx context.Context, request InvitationOnboardingRequest) (InvitationOnboardingResult, error) {
	if c == nil || c.Pool == nil || c.Identity == nil || c.Identity.Pool != c.Pool || !c.Invitations.UsesOnboardingStore(c.Identity) || c.Audit == nil || c.Audit.Pool != c.Pool || c.Clock == nil {
		return InvitationOnboardingResult{}, errors.New("durable invitation onboarding is not configured")
	}
	request.InvitationToken = strings.TrimSpace(request.InvitationToken)
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	if request.DisplayName == "" || len(request.DisplayName) > 160 || len(request.Password) < 12 || len(request.Password) > 1024 || len(request.InvitationToken) > 128 {
		return InvitationOnboardingResult{}, identity.ErrInvalidInvitation
	}
	retry, err := c.Invitations.ConsumeOnboardingAttempt(ctx, request.RemoteAddr, request.InvitationToken)
	if err != nil {
		return InvitationOnboardingResult{}, err
	}
	if retry > 0 {
		return InvitationOnboardingResult{}, &identity.LoginRateLimitError{RetryAfter: retry}
	}
	verifier, err := c.Invitations.PrepareOnboarding(request.InvitationToken)
	if err != nil {
		return InvitationOnboardingResult{}, err
	}
	defer clear(verifier)
	hash, err := bootstrap.HashPassword(request.Password)
	if err != nil {
		return InvitationOnboardingResult{}, identity.ErrInvalidInvitation
	}
	userID, err := newIdentityID("usr", 18)
	if err != nil {
		return InvitationOnboardingResult{}, err
	}
	now := c.Clock().UTC()
	var result InvitationOnboardingResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err = c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			invitation, err := c.Identity.OnboardInvitationInTx(ctx, tx, identity.InvitationOnboarding{Verifier: verifier, UserID: userID, DisplayName: request.DisplayName, PasswordHash: hash}, now)
			if err != nil {
				return err
			}
			_, err = c.Audit.AppendInTx(ctx, tx, audit.Row{RequestID: request.RequestID, OccurredAt: now, ActorID: userID, ActorType: "user", AuthMethod: "invitation", WorkspaceID: invitation.WorkspaceID, ProjectID: invitation.ProjectID, ResourceKind: "invitation", ResourceID: invitation.ID, Action: "identity.invitation.onboard", PolicyDecision: "allowed", After: map[string]any{"user_id": userID, "workspace_role": invitation.WorkspaceRole, "project_role": invitation.ProjectRole}})
			if err != nil {
				return err
			}
			if c.beforeCommit != nil {
				if err := c.beforeCommit(); err != nil {
					return err
				}
			}
			result = InvitationOnboardingResult{UserID: userID, Email: invitation.Email, Invitation: invitation}
			return nil
		})
		if err == nil {
			return result, nil
		}
		if !identityClaimTransactionRetryable(err) {
			return InvitationOnboardingResult{}, err
		}
	}
	return InvitationOnboardingResult{}, err
}
