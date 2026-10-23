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

const invitationColumns = `
	id, email, workspace_id, project_id, workspace_role, project_role,
	token_prefix, invited_by_user_id, created_at, updated_at, expires_at,
	revoked_at, revoked_by_user_id, revoked_reason, accepted_at,
	accepted_by_user_id, replaces_invitation_id, resource_version`

func (s *PostgresStore) CreateInvitation(ctx context.Context, credential InvitationCredential) (Invitation, error) {
	if err := s.readyInvitationStore(); err != nil {
		return Invitation{}, err
	}
	if err := validateInvitationCredential(credential); err != nil {
		return Invitation{}, err
	}
	if credential.ReplacesInvitationID != "" {
		return Invitation{}, ErrInvalidInvitation
	}

	var result Invitation
	err := s.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		var txErr error
		result, txErr = s.CreateInvitationInTx(ctx, tx, credential)
		return txErr
	})
	if err != nil {
		return Invitation{}, err
	}
	return result, nil
}

// CreateInvitationInTx commits a prepared invitation through the caller's
// transaction. It takes the canonical authority and membership locks but does
// not commit or retry the transaction.
func (s *PostgresStore) CreateInvitationInTx(
	ctx context.Context,
	tx *pg.Tx,
	credential InvitationCredential,
) (Invitation, error) {
	if tx == nil {
		return Invitation{}, errors.New("identity invitation CreateInvitationInTx: transaction is required")
	}
	if err := validateInvitationCredential(credential); err != nil {
		return Invitation{}, err
	}
	if credential.ReplacesInvitationID != "" {
		return Invitation{}, ErrInvalidInvitation
	}
	if err := authorizePostgresInvitationMutation(
		ctx,
		tx,
		credential.Invitation,
		credential.InvitedByUserID,
	); err != nil {
		return Invitation{}, err
	}
	targetAlreadyMember, err := postgresInvitationTargetAlreadyMember(
		ctx,
		tx,
		credential.Invitation,
	)
	if err != nil {
		return Invitation{}, err
	}
	if targetAlreadyMember {
		return Invitation{}, ErrInvitationMemberExists
	}
	pending, err := postgresPendingInvitationExists(
		ctx,
		tx,
		credential.Invitation,
		"",
		credential.CreatedAt,
	)
	if err != nil {
		return Invitation{}, err
	}
	if pending {
		return Invitation{}, ErrInvitationPending
	}

	result, err := scanInvitation(tx.QueryRow(ctx, `
		INSERT INTO identity_invitations (
			id, email, workspace_id, project_id, workspace_role,
			project_role, token_hash, token_prefix, invited_by_user_id,
			created_at, updated_at, expires_at, replaces_invitation_id,
			resource_version
		)
		VALUES (
			$1,$2,$3,$4,$5,
			$6,$7,$8,$9,
			$10,$11,$12,$13,
			$14
		)
		RETURNING `+invitationColumns,
		credential.ID,
		credential.Email,
		credential.WorkspaceID,
		nullableInvitationID(credential.ProjectID),
		nullableInvitationRole(string(credential.WorkspaceRole)),
		nullableInvitationRole(string(credential.ProjectRole)),
		credential.Verifier,
		credential.TokenPrefix,
		credential.InvitedByUserID,
		credential.CreatedAt,
		credential.UpdatedAt,
		credential.ExpiresAt,
		nullableInvitationID(credential.ReplacesInvitationID),
		credential.ResourceVersion,
	))
	if err != nil {
		if isIdentityUniqueViolation(err) {
			return Invitation{}, ErrInvitationPending
		}
		return Invitation{}, fmt.Errorf("identity invitation insert: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) ListInvitations(
	ctx context.Context,
	request ListInvitationsRequest,
) ([]Invitation, error) {
	if err := s.readyInvitationStore(); err != nil {
		return nil, err
	}
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ActorUserID = strings.TrimSpace(request.ActorUserID)
	if request.WorkspaceID == "" || request.ActorUserID == "" {
		return nil, ErrInvalidInvitation
	}
	if request.Limit == 0 {
		request.Limit = InvitationDefaultPageLimit
	}
	if request.Limit < 1 || request.Limit > InvitationMaxPageLimit+1 ||
		request.Offset < 0 {
		return nil, ErrInvalidInvitation
	}
	var result []Invitation
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var txErr error
		result, txErr = s.ListInvitationsInTx(ctx, tx, request)
		return txErr
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *PostgresStore) GetInvitation(
	ctx context.Context,
	request GetInvitationRequest,
) (Invitation, error) {
	if err := s.readyInvitationStore(); err != nil {
		return Invitation{}, err
	}
	request.InvitationID = strings.TrimSpace(request.InvitationID)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ActorUserID = strings.TrimSpace(request.ActorUserID)
	if request.InvitationID == "" || request.WorkspaceID == "" || request.ActorUserID == "" {
		return Invitation{}, ErrInvalidInvitation
	}
	var result Invitation
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var txErr error
		result, txErr = s.GetInvitationInTx(ctx, tx, request)
		return txErr
	})
	return result, err
}

func (s *PostgresStore) GetInvitationInTx(
	ctx context.Context,
	tx *pg.Tx,
	request GetInvitationRequest,
) (Invitation, error) {
	if tx == nil {
		return Invitation{}, errors.New("identity invitation GetInvitationInTx: transaction is required")
	}
	editorOnly, err := authorizePostgresInvitationScope(ctx, tx, request.WorkspaceID, request.ProjectID, request.ActorUserID)
	if err != nil {
		return Invitation{}, err
	}
	record, err := scanInvitation(tx.QueryRow(ctx, `
		SELECT `+invitationColumns+`
		FROM identity_invitations
		WHERE id=$1
		  AND workspace_id=$2
		  AND project_id IS NOT DISTINCT FROM $3`,
		request.InvitationID,
		request.WorkspaceID,
		nullableInvitationID(request.ProjectID),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrInvitationNotFound
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("identity invitation get: %w", err)
	}
	if editorOnly && record.ProjectRole == ProjectRoleOwner {
		return Invitation{}, ErrAuthorizationDenied
	}
	return record, nil
}

func (s *PostgresStore) ListInvitationsInTx(
	ctx context.Context,
	tx *pg.Tx,
	request ListInvitationsRequest,
) ([]Invitation, error) {
	if tx == nil {
		return nil, errors.New("identity invitation ListInvitationsInTx: transaction is required")
	}
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ActorUserID = strings.TrimSpace(request.ActorUserID)
	if request.WorkspaceID == "" || request.ActorUserID == "" {
		return nil, ErrInvalidInvitation
	}
	if request.Limit == 0 {
		request.Limit = InvitationDefaultPageLimit
	}
	if request.Limit < 1 || request.Limit > InvitationMaxPageLimit+1 ||
		request.Offset < 0 {
		return nil, ErrInvalidInvitation
	}
	editorOnly, err := authorizePostgresInvitationScope(
		ctx,
		tx,
		request.WorkspaceID,
		request.ProjectID,
		request.ActorUserID,
	)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT `+invitationColumns+`
		FROM identity_invitations
		WHERE workspace_id=$1
		  AND project_id IS NOT DISTINCT FROM $2
		  AND ($3::BOOLEAN=FALSE OR project_role<>'owner')
		ORDER BY created_at DESC, id
		LIMIT $4 OFFSET $5`,
		request.WorkspaceID,
		nullableInvitationID(request.ProjectID),
		editorOnly,
		request.Limit,
		request.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("identity invitation list: %w", err)
	}
	defer rows.Close()
	out := make([]Invitation, 0)
	for rows.Next() {
		invitation, scanErr := scanInvitation(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("identity invitation list: %w", scanErr)
		}
		out = append(out, invitation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("identity invitation list: %w", err)
	}
	return out, nil
}

// AuthorizeInvitationScopeInTx checks current exact-scope membership authority
// without materializing invitation history. Mutation coordinators use this
// before reserving or replaying an idempotent response.
func (s *PostgresStore) AuthorizeInvitationScopeInTx(
	ctx context.Context,
	tx *pg.Tx,
	request ListInvitationsRequest,
) error {
	if tx == nil {
		return errors.New("identity invitation AuthorizeInvitationScopeInTx: transaction is required")
	}
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ActorUserID = strings.TrimSpace(request.ActorUserID)
	if request.WorkspaceID == "" || request.ActorUserID == "" {
		return ErrInvalidInvitation
	}
	_, err := authorizePostgresInvitationScope(
		ctx,
		tx,
		request.WorkspaceID,
		request.ProjectID,
		request.ActorUserID,
	)
	return err
}

func (s *PostgresStore) ReissueInvitation(
	ctx context.Context,
	replacement InvitationReplacement,
) (ReissueInvitationMetadata, error) {
	if err := s.readyInvitationStore(); err != nil {
		return ReissueInvitationMetadata{}, err
	}
	if err := validateInvitationReplacement(replacement); err != nil {
		return ReissueInvitationMetadata{}, err
	}
	var result ReissueInvitationMetadata
	err := s.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		var txErr error
		result, txErr = s.ReissueInvitationInTx(ctx, tx, replacement)
		return txErr
	})
	if err != nil {
		return ReissueInvitationMetadata{}, err
	}
	return result, nil
}

// ReissueInvitationInTx atomically attributes the old invitation's first
// revocation and inserts one uniquely-linked replacement. The caller owns
// commit and serializable retries.
func (s *PostgresStore) ReissueInvitationInTx(
	ctx context.Context,
	tx *pg.Tx,
	replacement InvitationReplacement,
) (ReissueInvitationMetadata, error) {
	if tx == nil {
		return ReissueInvitationMetadata{}, errors.New("identity invitation ReissueInvitationInTx: transaction is required")
	}
	if err := validateInvitationReplacement(replacement); err != nil {
		return ReissueInvitationMetadata{}, err
	}
	editorOnly, err := authorizePostgresInvitationScope(
		ctx,
		tx,
		replacement.WorkspaceID,
		replacement.ProjectID,
		replacement.ReissuedByUserID,
	)
	if err != nil {
		return ReissueInvitationMetadata{}, err
	}
	record, err := scanInvitation(tx.QueryRow(ctx, `
		SELECT `+invitationColumns+`
		FROM identity_invitations
		WHERE id=$1
		  AND workspace_id=$2
		  AND project_id IS NOT DISTINCT FROM $3
		FOR UPDATE`,
		replacement.InvitationID,
		replacement.WorkspaceID,
		nullableInvitationID(replacement.ProjectID),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return ReissueInvitationMetadata{}, ErrInvitationNotFound
	}
	if err != nil {
		return ReissueInvitationMetadata{}, fmt.Errorf("identity invitation reissue lock: %w", err)
	}
	if replacement.CreatedAt.Before(record.CreatedAt) {
		return ReissueInvitationMetadata{}, ErrInvalidInvitation
	}
	if editorOnly && record.ProjectRole == ProjectRoleOwner {
		return ReissueInvitationMetadata{}, ErrAuthorizationDenied
	}
	if record.AcceptedAt != nil {
		return ReissueInvitationMetadata{}, ErrInvitationConsumed
	}
	targetAlreadyMember, err := postgresInvitationTargetAlreadyMember(ctx, tx, record)
	if err != nil {
		return ReissueInvitationMetadata{}, err
	}
	if targetAlreadyMember {
		return ReissueInvitationMetadata{}, ErrInvitationMemberExists
	}
	var alreadyReplaced bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM identity_invitations
			WHERE replaces_invitation_id=$1
		)`,
		record.ID,
	).Scan(&alreadyReplaced); err != nil {
		return ReissueInvitationMetadata{}, fmt.Errorf("identity invitation replacement lineage: %w", err)
	}
	if alreadyReplaced {
		return ReissueInvitationMetadata{}, ErrInvitationReplaced
	}
	pending, err := postgresPendingInvitationExists(
		ctx,
		tx,
		record,
		record.ID,
		replacement.CreatedAt,
	)
	if err != nil {
		return ReissueInvitationMetadata{}, err
	}
	if pending {
		return ReissueInvitationMetadata{}, ErrInvitationPending
	}

	newCredential := InvitationCredential{
		Invitation: Invitation{
			ID:                   replacement.ReplacementID,
			Email:                record.Email,
			WorkspaceID:          record.WorkspaceID,
			ProjectID:            record.ProjectID,
			WorkspaceRole:        record.WorkspaceRole,
			ProjectRole:          record.ProjectRole,
			TokenPrefix:          replacement.ReplacementTokenPrefix,
			InvitedByUserID:      replacement.ReissuedByUserID,
			CreatedAt:            replacement.CreatedAt,
			UpdatedAt:            replacement.CreatedAt,
			ExpiresAt:            replacement.ExpiresAt,
			ReplacesInvitationID: record.ID,
			ResourceVersion:      1,
		},
		Verifier: append([]byte(nil), replacement.ReplacementVerifier...),
	}
	if err := validateInvitationCredential(newCredential); err != nil {
		return ReissueInvitationMetadata{}, err
	}

	replaced, err := scanInvitation(tx.QueryRow(ctx, `
		UPDATE identity_invitations
		SET revoked_at=CASE WHEN revoked_at IS NULL THEN $2 ELSE revoked_at END,
		    revoked_by_user_id=CASE WHEN revoked_at IS NULL THEN $3 ELSE revoked_by_user_id END,
		    revoked_reason=CASE WHEN revoked_at IS NULL THEN $4 ELSE revoked_reason END,
		    updated_at=CASE WHEN revoked_at IS NULL THEN $2 ELSE updated_at END,
		    resource_version=CASE
		        WHEN revoked_at IS NULL THEN resource_version+1
		        ELSE resource_version
		    END
		WHERE id=$1
		RETURNING `+invitationColumns,
		record.ID,
		replacement.CreatedAt.UTC(),
		replacement.ReissuedByUserID,
		invitationReissueReason,
	))
	if err != nil {
		return ReissueInvitationMetadata{}, fmt.Errorf("identity invitation reissue revocation: %w", err)
	}
	created, err := scanInvitation(tx.QueryRow(ctx, `
		INSERT INTO identity_invitations (
			id, email, workspace_id, project_id, workspace_role,
			project_role, token_hash, token_prefix, invited_by_user_id,
			created_at, updated_at, expires_at, replaces_invitation_id,
			resource_version
		)
		VALUES (
			$1,$2,$3,$4,$5,
			$6,$7,$8,$9,
			$10,$11,$12,$13,
			$14
		)
		RETURNING `+invitationColumns,
		newCredential.ID,
		newCredential.Email,
		newCredential.WorkspaceID,
		nullableInvitationID(newCredential.ProjectID),
		nullableInvitationRole(string(newCredential.WorkspaceRole)),
		nullableInvitationRole(string(newCredential.ProjectRole)),
		newCredential.Verifier,
		newCredential.TokenPrefix,
		newCredential.InvitedByUserID,
		newCredential.CreatedAt,
		newCredential.UpdatedAt,
		newCredential.ExpiresAt,
		newCredential.ReplacesInvitationID,
		newCredential.ResourceVersion,
	))
	if err != nil {
		if isIdentityUniqueViolation(err) {
			return ReissueInvitationMetadata{}, ErrInvitationReplaced
		}
		return ReissueInvitationMetadata{}, fmt.Errorf("identity invitation replacement insert: %w", err)
	}
	return ReissueInvitationMetadata{
		ReplacedInvitation: replaced,
		Invitation:         created,
	}, nil
}

func (s *PostgresStore) AcceptInvitation(
	ctx context.Context,
	acceptance InvitationAcceptance,
	now time.Time,
) (Invitation, error) {
	if err := s.readyInvitationStore(); err != nil {
		return Invitation{}, err
	}

	var result Invitation
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var txErr error
		result, txErr = s.AcceptInvitationInTx(ctx, tx, acceptance, now)
		return txErr
	})
	if err != nil {
		return Invitation{}, err
	}
	return result, nil
}

// AcceptInvitationInTx atomically exchanges one prepared invitation verifier
// for membership inside the caller's transaction. It does not commit or retry.
func (s *PostgresStore) AcceptInvitationInTx(
	ctx context.Context,
	tx *pg.Tx,
	acceptance InvitationAcceptance,
	now time.Time,
) (Invitation, error) {
	if tx == nil {
		return Invitation{}, errors.New("identity invitation AcceptInvitationInTx: transaction is required")
	}
	acceptance.UserID = strings.TrimSpace(acceptance.UserID)
	acceptance.Email = normalizeInvitationEmail(acceptance.Email)
	if len(acceptance.Verifier) != sha256.Size || acceptance.UserID == "" ||
		!validInvitationEmail(acceptance.Email) {
		return Invitation{}, ErrInvalidInvitation
	}

	var scope Invitation
	var nullableProject sql.NullString
	err := tx.QueryRow(ctx, `
		SELECT workspace_id, project_id
		FROM identity_invitations
		WHERE token_hash=$1`,
		acceptance.Verifier,
	).Scan(&scope.WorkspaceID, &nullableProject)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrInvitationNotFound
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("identity invitation acceptance scope: %w", err)
	}
	if nullableProject.Valid {
		scope.ProjectID = nullableProject.String
	}
	// All invitation lifecycle paths acquire exact-scope locks before the
	// invitation row, avoiding accept/reissue/revoke lock inversion.
	if err := lockPostgresInvitationMembership(ctx, tx, scope); err != nil {
		return Invitation{}, err
	}

	record, err := scanInvitation(tx.QueryRow(ctx, `
		SELECT `+invitationColumns+`
		FROM identity_invitations
		WHERE token_hash=$1
		FOR UPDATE`,
		acceptance.Verifier,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrInvitationNotFound
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("identity invitation acceptance lock: %w", err)
	}
	if now.Before(record.CreatedAt) {
		return Invitation{}, ErrInvalidInvitation
	}
	if record.AcceptedAt != nil {
		return Invitation{}, ErrInvitationConsumed
	}
	if record.RevokedAt != nil {
		return Invitation{}, ErrInvitationRevoked
	}
	if !now.Before(record.ExpiresAt) {
		return Invitation{}, ErrInvitationExpired
	}

	var durableEmail string
	err = tx.QueryRow(ctx, `
		SELECT email
		FROM users
		WHERE id=$1
		  AND deleted_at IS NULL
		  AND is_disabled=FALSE
		FOR SHARE`,
		acceptance.UserID,
	).Scan(&durableEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrIdentityNotFound
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("identity invitation accepting user: %w", err)
	}
	if normalizeInvitationEmail(durableEmail) != record.Email ||
		acceptance.Email != record.Email {
		return Invitation{}, ErrInvitationEmailMismatch
	}

	if record.ProjectID == "" {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM workspace_members
				WHERE workspace_id=$1 AND user_id=$2
			)`,
			record.WorkspaceID,
			acceptance.UserID,
		).Scan(&exists); err != nil {
			return Invitation{}, fmt.Errorf("identity invitation workspace membership check: %w", err)
		}
		if exists {
			return Invitation{}, ErrInvitationMemberExists
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO workspace_members (
				workspace_id, user_id, role, invited_by, invited_at,
				accepted_at, created_at, updated_at, resource_version
			)
			VALUES ($1,$2,$3,$4,$5,$6,$6,$6,1)`,
			record.WorkspaceID,
			acceptance.UserID,
			record.WorkspaceRole,
			record.InvitedByUserID,
			record.CreatedAt,
			now,
		)
	} else {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM project_members
				WHERE project_id=$1 AND user_id=$2
			)`,
			record.ProjectID,
			acceptance.UserID,
		).Scan(&exists); err != nil {
			return Invitation{}, fmt.Errorf("identity invitation project membership check: %w", err)
		}
		if exists {
			return Invitation{}, ErrInvitationMemberExists
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO project_members (
				project_id, user_id, role, invited_by, invited_at,
				accepted_at, created_at, updated_at, resource_version
			)
			VALUES ($1,$2,$3,$4,$5,$6,$6,$6,1)`,
			record.ProjectID,
			acceptance.UserID,
			record.ProjectRole,
			record.InvitedByUserID,
			record.CreatedAt,
			now,
		)
	}
	if err != nil {
		if isIdentityUniqueViolation(err) {
			return Invitation{}, ErrInvitationMemberExists
		}
		return Invitation{}, fmt.Errorf("identity invitation membership insert: %w", err)
	}

	result, err := scanInvitation(tx.QueryRow(ctx, `
		UPDATE identity_invitations
		SET accepted_at=$2,
		    accepted_by_user_id=$3,
		    updated_at=$2,
		    resource_version=resource_version+1
		WHERE id=$1
		RETURNING `+invitationColumns,
		record.ID,
		now.UTC(),
		acceptance.UserID,
	))
	if err != nil {
		return Invitation{}, fmt.Errorf("identity invitation acceptance update: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) RevokeInvitation(
	ctx context.Context,
	request RevokeInvitationRequest,
	now time.Time,
) (Invitation, error) {
	if err := s.readyInvitationStore(); err != nil {
		return Invitation{}, err
	}
	request.InvitationID = strings.TrimSpace(request.InvitationID)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.RevokedByUserID = strings.TrimSpace(request.RevokedByUserID)
	request.Reason = cleanInvitationReason(request.Reason)
	if request.InvitationID == "" || request.WorkspaceID == "" ||
		request.RevokedByUserID == "" {
		return Invitation{}, ErrInvalidInvitation
	}

	var result Invitation
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var txErr error
		result, txErr = s.RevokeInvitationInTx(ctx, tx, request, now)
		return txErr
	})
	if err != nil {
		return Invitation{}, err
	}
	return result, nil
}

func (s *PostgresStore) RevokeInvitationInTx(
	ctx context.Context,
	tx *pg.Tx,
	request RevokeInvitationRequest,
	now time.Time,
) (Invitation, error) {
	if tx == nil {
		return Invitation{}, errors.New("identity invitation RevokeInvitationInTx: transaction is required")
	}
	request.InvitationID = strings.TrimSpace(request.InvitationID)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.RevokedByUserID = strings.TrimSpace(request.RevokedByUserID)
	request.Reason = cleanInvitationReason(request.Reason)
	if request.InvitationID == "" || request.WorkspaceID == "" ||
		request.RevokedByUserID == "" {
		return Invitation{}, ErrInvalidInvitation
	}
	editorOnly, err := authorizePostgresInvitationScope(
		ctx,
		tx,
		request.WorkspaceID,
		request.ProjectID,
		request.RevokedByUserID,
	)
	if err != nil {
		return Invitation{}, err
	}
	record, err := scanInvitation(tx.QueryRow(ctx, `
		SELECT `+invitationColumns+`
		FROM identity_invitations
		WHERE id=$1
		  AND workspace_id=$2
		  AND project_id IS NOT DISTINCT FROM $3
		FOR UPDATE`,
		request.InvitationID,
		request.WorkspaceID,
		nullableInvitationID(request.ProjectID),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrInvitationNotFound
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("identity invitation revocation lock: %w", err)
	}
	if now.Before(record.CreatedAt) {
		return Invitation{}, ErrInvalidInvitation
	}
	if editorOnly && record.ProjectRole == ProjectRoleOwner {
		return Invitation{}, ErrAuthorizationDenied
	}
	if record.AcceptedAt != nil {
		return Invitation{}, ErrInvitationConsumed
	}
	if request.ExpectedVersion != nil && record.ResourceVersion != *request.ExpectedVersion {
		return Invitation{}, ErrInvitationVersionConflict
	}
	if record.RevokedAt != nil {
		return record, nil
	}
	result, err := scanInvitation(tx.QueryRow(ctx, `
		UPDATE identity_invitations
		SET revoked_at=$2,
		    revoked_by_user_id=$3,
		    revoked_reason=$4,
		    updated_at=$2,
		    resource_version=resource_version+1
		WHERE id=$1
		  AND ($5::BIGINT IS NULL OR resource_version=$5)
		RETURNING `+invitationColumns,
		record.ID,
		now.UTC(),
		request.RevokedByUserID,
		request.Reason,
		request.ExpectedVersion,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrInvitationVersionConflict
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("identity invitation revocation update: %w", err)
	}
	return result, nil
}

func authorizePostgresInvitationMutation(
	ctx context.Context,
	tx *pg.Tx,
	invitation Invitation,
	actorID string,
) error {
	editorOnly, err := authorizePostgresInvitationScope(
		ctx,
		tx,
		invitation.WorkspaceID,
		invitation.ProjectID,
		actorID,
	)
	if err != nil {
		return err
	}
	if editorOnly && invitation.ProjectRole == ProjectRoleOwner {
		return ErrAuthorizationDenied
	}
	return nil
}

func authorizePostgresInvitationScope(
	ctx context.Context,
	tx *pg.Tx,
	workspaceID,
	projectID,
	actorID string,
) (bool, error) {
	if projectID == "" {
		if _, err := tx.Exec(
			ctx,
			`SELECT pg_advisory_xact_lock(hashtext($1))`,
			"workspace-members:"+workspaceID,
		); err != nil {
			return false, fmt.Errorf("identity invitation workspace lock: %w", err)
		}
		return false, authorizeWorkspaceMemberMutation(ctx, tx, workspaceID, actorID)
	}
	canonicalWorkspaceID, err := projectWorkspaceForMembershipMutation(ctx, tx, projectID)
	if err != nil {
		if errors.Is(err, ErrAccessNotFound) {
			return false, ErrInvitationScopeNotActive
		}
		return false, fmt.Errorf("identity invitation project scope: %w", err)
	}
	if canonicalWorkspaceID != workspaceID {
		return false, ErrInvitationScopeNotActive
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`,
		"workspace-members:"+canonicalWorkspaceID,
	); err != nil {
		return false, fmt.Errorf("identity invitation workspace lock: %w", err)
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`,
		"project-members:"+projectID,
	); err != nil {
		return false, fmt.Errorf("identity invitation project lock: %w", err)
	}
	editorOnly, err := authorizeProjectMemberMutation(
		ctx,
		tx,
		projectID,
		canonicalWorkspaceID,
		actorID,
	)
	if err != nil {
		return false, err
	}
	return editorOnly, nil
}

func lockPostgresInvitationMembership(ctx context.Context, tx *pg.Tx, invitation Invitation) error {
	if invitation.ProjectID == "" {
		if _, err := tx.Exec(
			ctx,
			`SELECT pg_advisory_xact_lock(hashtext($1))`,
			"workspace-members:"+invitation.WorkspaceID,
		); err != nil {
			return fmt.Errorf("identity invitation workspace lock: %w", err)
		}
		var workspaceID string
		err := tx.QueryRow(ctx, `
			SELECT id
			FROM workspaces
			WHERE id=$1 AND deleted_at IS NULL
			FOR SHARE`,
			invitation.WorkspaceID,
		).Scan(&workspaceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvitationScopeNotActive
		}
		if err != nil {
			return fmt.Errorf("identity invitation workspace scope: %w", err)
		}
		return nil
	}
	workspaceID, err := projectWorkspaceForMembershipMutation(ctx, tx, invitation.ProjectID)
	if err != nil {
		if errors.Is(err, ErrAccessNotFound) {
			return ErrInvitationScopeNotActive
		}
		return fmt.Errorf("identity invitation project scope: %w", err)
	}
	if workspaceID != invitation.WorkspaceID {
		return ErrInvitationScopeNotActive
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`,
		"workspace-members:"+workspaceID,
	); err != nil {
		return fmt.Errorf("identity invitation workspace lock: %w", err)
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`,
		"project-members:"+invitation.ProjectID,
	); err != nil {
		return fmt.Errorf("identity invitation project lock: %w", err)
	}
	return nil
}

func postgresInvitationTargetAlreadyMember(
	ctx context.Context,
	tx *pg.Tx,
	invitation Invitation,
) (bool, error) {
	var exists bool
	if invitation.ProjectID == "" {
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM users u
				JOIN workspace_members member ON member.user_id=u.id
				WHERE member.workspace_id=$1
				  AND LOWER(u.email)=$2
				  AND u.deleted_at IS NULL
			)`,
			invitation.WorkspaceID,
			invitation.Email,
		).Scan(&exists); err != nil {
			return false, fmt.Errorf("identity invitation existing workspace member: %w", err)
		}
		return exists, nil
	}
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM users u
			JOIN project_members member ON member.user_id=u.id
			WHERE member.project_id=$1
			  AND LOWER(u.email)=$2
			  AND u.deleted_at IS NULL
		)`,
		invitation.ProjectID,
		invitation.Email,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("identity invitation existing project member: %w", err)
	}
	return exists, nil
}

func postgresPendingInvitationExists(
	ctx context.Context,
	tx *pg.Tx,
	invitation Invitation,
	excludedInvitationID string,
	now time.Time,
) (bool, error) {
	var pending bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM identity_invitations
			WHERE workspace_id=$1
			  AND project_id IS NOT DISTINCT FROM $2
			  AND email=$3
			  AND id<>$4
			  AND accepted_at IS NULL
			  AND revoked_at IS NULL
			  AND expires_at>$5
		)`,
		invitation.WorkspaceID,
		nullableInvitationID(invitation.ProjectID),
		invitation.Email,
		excludedInvitationID,
		now,
	).Scan(&pending); err != nil {
		return false, fmt.Errorf("identity invitation pending check: %w", err)
	}
	return pending, nil
}

func (s *PostgresStore) readyInvitationStore() error {
	if s == nil || s.Pool == nil || s.Pool.Inner() == nil {
		return errors.New("identity invitation postgres store: pool is required")
	}
	return nil
}

type invitationScanner interface {
	Scan(...any) error
}

func scanInvitation(row invitationScanner) (Invitation, error) {
	var (
		invitation    Invitation
		projectID     sql.NullString
		workspaceRole sql.NullString
		projectRole   sql.NullString
		revokedAt     sql.NullTime
		revokedBy     sql.NullString
		acceptedAt    sql.NullTime
		acceptedBy    sql.NullString
		replacesID    sql.NullString
	)
	err := row.Scan(
		&invitation.ID,
		&invitation.Email,
		&invitation.WorkspaceID,
		&projectID,
		&workspaceRole,
		&projectRole,
		&invitation.TokenPrefix,
		&invitation.InvitedByUserID,
		&invitation.CreatedAt,
		&invitation.UpdatedAt,
		&invitation.ExpiresAt,
		&revokedAt,
		&revokedBy,
		&invitation.RevokedReason,
		&acceptedAt,
		&acceptedBy,
		&replacesID,
		&invitation.ResourceVersion,
	)
	if err != nil {
		return Invitation{}, err
	}
	if projectID.Valid {
		invitation.ProjectID = projectID.String
	}
	if workspaceRole.Valid {
		role, ok := ParseWorkspaceRole(workspaceRole.String)
		if !ok {
			return Invitation{}, fmt.Errorf("invalid stored workspace invitation role %q", workspaceRole.String)
		}
		invitation.WorkspaceRole = role
	}
	if projectRole.Valid {
		role, ok := ParseProjectRole(projectRole.String)
		if !ok {
			return Invitation{}, fmt.Errorf("invalid stored project invitation role %q", projectRole.String)
		}
		invitation.ProjectRole = role
	}
	if revokedAt.Valid {
		value := revokedAt.Time.UTC()
		invitation.RevokedAt = &value
	}
	if revokedBy.Valid {
		invitation.RevokedByUserID = revokedBy.String
	}
	if acceptedAt.Valid {
		value := acceptedAt.Time.UTC()
		invitation.AcceptedAt = &value
	}
	if acceptedBy.Valid {
		invitation.AcceptedByUserID = acceptedBy.String
	}
	if replacesID.Valid {
		invitation.ReplacesInvitationID = replacesID.String
	}
	invitation.CreatedAt = invitation.CreatedAt.UTC()
	invitation.UpdatedAt = invitation.UpdatedAt.UTC()
	invitation.ExpiresAt = invitation.ExpiresAt.UTC()
	return invitation, nil
}

func nullableInvitationID(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableInvitationRole(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
