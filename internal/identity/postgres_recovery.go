// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
)

// recoverableAdministratorPredicate is the single definition of which account
// operator recovery may reset. ResetAdministratorCredentialsInTx re-applies it
// under a row lock so the resolution probe and the write cannot disagree.
const recoverableAdministratorPredicate = `
	LOWER(email) = LOWER($1)
	  AND deleted_at IS NULL
	  AND is_disabled = FALSE
	  AND is_admin = TRUE`

func (s *PostgresStore) RecoverableAdministrator(ctx context.Context, email string) (RecoverableAdministrator, error) {
	if s == nil || s.Pool == nil {
		return RecoverableAdministrator{}, errors.New("identity postgres store: pool is required")
	}
	var target RecoverableAdministrator
	err := s.Pool.QueryRow(ctx, `
		SELECT id, email, display_name, totp_enrolled_at IS NOT NULL
		FROM users
		WHERE`+recoverableAdministratorPredicate+`
		LIMIT 1`,
		strings.ToLower(strings.TrimSpace(email)),
	).Scan(&target.UserID, &target.Email, &target.DisplayName, &target.TOTPEnrolled)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecoverableAdministrator{}, ErrAdministratorNotRecoverable
	}
	if err != nil {
		return RecoverableAdministrator{}, fmt.Errorf("identity administrator recovery lookup: %w", err)
	}
	return target, nil
}

func (s *PostgresStore) ResetAdministratorCredentials(
	ctx context.Context,
	request ResetAdministratorCredentialsRequest,
) (ResetAdministratorCredentialsResult, error) {
	if s == nil || s.Pool == nil {
		return ResetAdministratorCredentialsResult{}, errors.New("identity postgres store: pool is required")
	}
	var result ResetAdministratorCredentialsResult
	err := s.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		var err error
		result, err = s.ResetAdministratorCredentialsInTx(ctx, tx, request)
		return err
	})
	if err != nil {
		return ResetAdministratorCredentialsResult{}, err
	}
	return result, nil
}

// ResetAdministratorCredentialsInTx performs the credential reset using a
// transaction owned by the caller, so a production coordinator can commit the
// new credential beside its audit evidence and exact idempotency response.
// The caller must use SERIALIZABLE isolation and owns commit/rollback.
func (s *PostgresStore) ResetAdministratorCredentialsInTx(
	ctx context.Context,
	tx *pg.Tx,
	request ResetAdministratorCredentialsRequest,
) (ResetAdministratorCredentialsResult, error) {
	if s == nil || s.Pool == nil || tx == nil {
		return ResetAdministratorCredentialsResult{}, errors.New("identity postgres store: pool and transaction are required")
	}
	if strings.TrimSpace(request.UserID) == "" || strings.TrimSpace(request.PasswordHash) == "" {
		return ResetAdministratorCredentialsResult{}, errors.New("identity administrator recovery: user id and password hash are required")
	}
	if !request.PreserveTOTP && len(request.TOTPSecretEnvelope) == 0 {
		return ResetAdministratorCredentialsResult{}, errors.New("identity administrator recovery: a TOTP envelope is required unless enrollment is preserved")
	}

	var (
		userID       string
		email        string
		displayName  string
		totpEnrolled bool
	)
	err := tx.QueryRow(ctx, `
		SELECT id, email, display_name, totp_enrolled_at IS NOT NULL
		FROM users
		WHERE`+recoverableAdministratorPredicate+`
		FOR UPDATE`,
		strings.ToLower(strings.TrimSpace(request.Email)),
	).Scan(&userID, &email, &displayName, &totpEnrolled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResetAdministratorCredentialsResult{}, ErrAdministratorNotRecoverable
	}
	if err != nil {
		return ResetAdministratorCredentialsResult{}, fmt.Errorf("identity administrator recovery lock: %w", err)
	}
	// The address was resolved before the TOTP envelope was sealed against a
	// user ID. If the account behind it changed in between, the envelope no
	// longer belongs to this row and the reset must not proceed.
	if userID != request.UserID {
		return ResetAdministratorCredentialsResult{}, ErrAdministratorNotRecoverable
	}
	if request.PreserveTOTP && !totpEnrolled {
		return ResetAdministratorCredentialsResult{}, ErrTOTPEnrollmentMissing
	}

	now := request.Now.UTC()
	enrolledAt := request.TOTPEnrolledAt
	if enrolledAt.IsZero() {
		enrolledAt = now
	}
	tag, err := tx.Exec(ctx, `
		UPDATE users
		SET password_hash = $2,
		    must_rotate_password = FALSE,
		    totp_secret_encrypted = CASE WHEN $3 THEN totp_secret_encrypted ELSE $4 END,
		    totp_enrolled_at = CASE WHEN $3 THEN totp_enrolled_at ELSE $5 END,
		    session_generation = session_generation + 1,
		    updated_at = $6,
		    resource_version = resource_version + 1
		WHERE id = $1`,
		userID,
		request.PasswordHash,
		request.PreserveTOTP,
		request.TOTPSecretEnvelope,
		enrolledAt,
		now,
	)
	if err != nil {
		return ResetAdministratorCredentialsResult{}, fmt.Errorf("identity administrator recovery credential update: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ResetAdministratorCredentialsResult{}, ErrAdministratorNotRecoverable
	}

	// Bumping session_generation already makes every existing session
	// unauthenticatable. Revoking them as well leaves durable evidence of the
	// reset and keeps session listings honest.
	revoked, err := tx.Exec(ctx, `
		UPDATE auth_sessions
		SET revoked_at = $2, revoked_reason = $3
		WHERE user_id = $1 AND revoked_at IS NULL`,
		userID,
		now,
		administratorRecoveryRevokeReason(request.RevokedReason),
	)
	if err != nil {
		return ResetAdministratorCredentialsResult{}, fmt.Errorf("identity administrator recovery session revoke: %w", err)
	}

	return ResetAdministratorCredentialsResult{
		User: User{
			ID:            userID,
			Email:         email,
			DisplayName:   displayName,
			InstanceAdmin: true,
			MFAEnabled:    true,
		},
		TOTPReenrolled:  !request.PreserveTOTP,
		SessionsRevoked: int(revoked.RowsAffected()),
	}, nil
}

func administratorRecoveryRevokeReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "administrator-password-reset"
	}
	if len(reason) > 128 {
		reason = reason[:128]
	}
	return reason
}
