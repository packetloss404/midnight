// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package identity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/midnight/midnight/internal/db/pg"
)

// InvitationOnboarding contains no bearer or plaintext password. The caller
// owns its transaction, allowing audit evidence to commit with the grant.
type InvitationOnboarding struct {
	Verifier                          []byte
	UserID, DisplayName, PasswordHash string
}

// OnboardInvitationInTx creates an ordinary local account and consumes exactly
// its invitation atomically. Scope locks precede the invitation row, matching
// the existing accept/revoke/reissue ordering. Any failure rolls back the user.
func (s *PostgresStore) OnboardInvitationInTx(ctx context.Context, tx *pg.Tx, input InvitationOnboarding, now time.Time) (Invitation, error) {
	if tx == nil || len(input.Verifier) != sha256.Size || !strings.HasPrefix(input.UserID, "usr_") || input.DisplayName == "" || len(input.DisplayName) > 160 || !strings.HasPrefix(input.PasswordHash, "pbkdf2-sha256$") {
		return Invitation{}, ErrInvalidInvitation
	}
	var scope Invitation
	var project sql.NullString
	err := tx.QueryRow(ctx, `SELECT workspace_id, project_id FROM identity_invitations WHERE token_hash=$1`, input.Verifier).Scan(&scope.WorkspaceID, &project)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrInvitationNotFound
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("invitation onboarding scope: %w", err)
	}
	scope.ProjectID = project.String
	if err := lockPostgresInvitationMembership(ctx, tx, scope); err != nil {
		return Invitation{}, err
	}
	invitation, err := scanInvitation(tx.QueryRow(ctx, `SELECT `+invitationColumns+` FROM identity_invitations WHERE token_hash=$1 FOR UPDATE`, input.Verifier))
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrInvitationNotFound
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("invitation onboarding lock: %w", err)
	}
	if invitation.AcceptedAt != nil {
		return Invitation{}, ErrInvitationConsumed
	}
	if invitation.RevokedAt != nil {
		return Invitation{}, ErrInvitationRevoked
	}
	if now.Before(invitation.CreatedAt) || !now.Before(invitation.ExpiresAt) {
		return Invitation{}, ErrInvitationExpired
	}
	var created string
	err = tx.QueryRow(ctx, `INSERT INTO users (id,email,display_name,password_hash,is_admin,is_disabled,must_rotate_password,session_generation,created_at,updated_at)
		VALUES ($1,$2,$3,$4,FALSE,FALSE,FALSE,1,$5,$5) ON CONFLICT DO NOTHING RETURNING id`, input.UserID, invitation.Email, input.DisplayName, input.PasswordHash, now.UTC()).Scan(&created)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrInvitationAccountExists
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("invitation account insert: %w", err)
	}
	// Reuse the exact canonical acceptance logic, including durable email and
	// membership checks. Locks are reentrant within this same transaction.
	return s.AcceptInvitationInTx(ctx, tx, InvitationAcceptance{Verifier: input.Verifier, UserID: created, Email: invitation.Email}, now)
}
