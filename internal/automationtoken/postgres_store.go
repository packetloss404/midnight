// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package automationtoken

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	dbpg "github.com/midnight/midnight/internal/db/pg"
)

// PostgresStore is the durable project automation-token verifier store.
type PostgresStore struct {
	Pool *dbpg.Pool
}

func NewPostgresStore(pool *dbpg.Pool) *PostgresStore {
	return &PostgresStore{Pool: pool}
}

const tokenColumns = `
	id, service_account_id, workspace_id, project_id, name, token_prefix,
	profile, created_by_user_id, created_at, updated_at, expires_at,
	last_used_at, revoked_at, revoked_by_user_id, revoked_reason,
	resource_version`

func (s *PostgresStore) Create(ctx context.Context, credential Credential) (Token, error) {
	if err := s.ready(); err != nil {
		return Token{}, err
	}
	return createWithQuerier(ctx, s.Pool, credential)
}

// CreateInTx persists a verifier using an existing transaction. The caller
// owns commit/rollback and must have already established a serializable
// authorization boundary.
func (s *PostgresStore) CreateInTx(ctx context.Context, tx *dbpg.Tx, credential Credential) (Token, error) {
	if tx == nil {
		return Token{}, errors.New("automation token CreateInTx: transaction is required")
	}
	return createWithQuerier(ctx, tx, credential)
}

type tokenQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func createWithQuerier(ctx context.Context, queryer tokenQueryer, credential Credential) (Token, error) {
	if len(credential.Verifier) != 32 {
		return Token{}, ErrInvalidRequest
	}
	row := queryer.QueryRow(ctx, `
		INSERT INTO project_automation_tokens (
			id, service_account_id, workspace_id, project_id, name, token_hash,
			token_prefix, profile, created_by_user_id, created_at, updated_at,
			expires_at, resource_version
		)
		VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11,
			$12, $13
		)
		RETURNING `+tokenColumns,
		credential.ID,
		credential.ServiceAccountID,
		credential.WorkspaceID,
		credential.ProjectID,
		credential.Name,
		credential.Verifier,
		credential.TokenPrefix,
		credential.Profile,
		credential.CreatedByUserID,
		credential.CreatedAt,
		credential.UpdatedAt,
		credential.ExpiresAt,
		credential.ResourceVersion,
	)
	token, err := scanToken(row)
	if err != nil {
		return Token{}, fmt.Errorf("insert project automation token: %w", err)
	}
	return token, nil
}

func (s *PostgresStore) Authenticate(
	ctx context.Context,
	verifier []byte,
	now time.Time,
) (Token, error) {
	if err := s.ready(); err != nil {
		return Token{}, err
	}
	if len(verifier) != 32 {
		return Token{}, ErrTokenNotFound
	}
	row := s.Pool.QueryRow(ctx, `
		UPDATE project_automation_tokens AS token
		SET last_used_at = $2,
		    updated_at = GREATEST(token.updated_at, $2)
		FROM service_accounts AS account, projects AS project
		WHERE token.token_hash = $1
		  AND token.profile = 'ci-deploy-v1'
		  AND token.revoked_at IS NULL
		  AND token.expires_at > $2
		  AND account.id = token.service_account_id
		  AND account.workspace_id = token.workspace_id
		  AND account.deleted_at IS NULL
		  AND project.id = token.project_id
		  AND project.workspace_id = token.workspace_id
		  AND project.deleted_at IS NULL
		RETURNING `+prefixedTokenColumns("token"),
		verifier,
		now.UTC(),
	)
	token, err := scanToken(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Token{}, ErrTokenNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("authenticate project automation token: %w", err)
	}
	return token, nil
}

func (s *PostgresStore) List(
	ctx context.Context,
	workspaceID,
	projectID string,
) ([]Token, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT `+tokenColumns+`
		FROM project_automation_tokens
		WHERE workspace_id = $1 AND project_id = $2
		ORDER BY created_at DESC, id`,
		workspaceID,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list project automation tokens: %w", err)
	}
	defer rows.Close()
	out := make([]Token, 0)
	for rows.Next() {
		token, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project automation token: %w", err)
		}
		out = append(out, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list project automation tokens: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) Get(ctx context.Context, workspaceID, projectID, tokenID string) (Token, error) {
	if err := s.ready(); err != nil {
		return Token{}, err
	}
	token, err := scanToken(s.Pool.QueryRow(ctx, `
		SELECT `+tokenColumns+`
		FROM project_automation_tokens
		WHERE id=$1 AND workspace_id=$2 AND project_id=$3`,
		tokenID, workspaceID, projectID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Token{}, ErrTokenNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("get project automation token: %w", err)
	}
	return token, nil
}

func (s *PostgresStore) Revoke(
	ctx context.Context,
	request RevokeRequest,
	now time.Time,
) (Token, error) {
	if err := s.ready(); err != nil {
		return Token{}, err
	}
	return revokeWithQuerier(ctx, s.Pool, request, now)
}

// RevokeInTx applies first-attribution revocation inside an existing
// transaction.
func (s *PostgresStore) RevokeInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	request RevokeRequest,
	now time.Time,
) (Token, error) {
	if tx == nil {
		return Token{}, errors.New("automation token RevokeInTx: transaction is required")
	}
	return revokeWithQuerier(ctx, tx, request, now)
}

func revokeWithQuerier(
	ctx context.Context,
	queryer tokenQueryer,
	request RevokeRequest,
	now time.Time,
) (Token, error) {
	row := queryer.QueryRow(ctx, `
		UPDATE project_automation_tokens AS token
		SET revoked_at = CASE
		        WHEN token.revoked_at IS NULL THEN $4
		        ELSE token.revoked_at
		    END,
		    revoked_by_user_id = CASE
		        WHEN token.revoked_at IS NULL THEN $5
		        ELSE token.revoked_by_user_id
		    END,
		    revoked_reason = CASE
		        WHEN token.revoked_at IS NULL THEN $6
		        ELSE token.revoked_reason
		    END,
		    updated_at = CASE
		        WHEN token.revoked_at IS NULL THEN $4
		        ELSE token.updated_at
		    END,
		    resource_version = CASE
		        WHEN token.revoked_at IS NULL THEN token.resource_version + 1
		        ELSE token.resource_version
		    END
		WHERE token.id = $1
		  AND token.workspace_id = $2
		  AND token.project_id = $3
		  AND ($7::BIGINT IS NULL OR token.resource_version = $7)
		RETURNING `+prefixedTokenColumns("token"),
		request.TokenID,
		request.WorkspaceID,
		request.ProjectID,
		now.UTC(),
		request.RevokedByUserID,
		request.Reason,
		request.ExpectedVersion,
	)
	token, err := scanToken(row)
	if errors.Is(err, pgx.ErrNoRows) {
		if request.ExpectedVersion != nil {
			var exists bool
			lookupErr := queryer.QueryRow(ctx, `
				SELECT EXISTS(SELECT 1 FROM project_automation_tokens WHERE id=$1 AND workspace_id=$2 AND project_id=$3)`,
				request.TokenID, request.WorkspaceID, request.ProjectID,
			).Scan(&exists)
			if lookupErr != nil {
				return Token{}, fmt.Errorf("resolve project automation token version conflict: %w", lookupErr)
			}
			if exists {
				return Token{}, ErrVersionConflict
			}
		}
		return Token{}, ErrTokenNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("revoke project automation token: %w", err)
	}
	return token, nil
}

func (s *PostgresStore) ready() error {
	if s == nil || s.Pool == nil || s.Pool.Inner() == nil {
		return errors.New("automation token postgres store: pool is required")
	}
	return nil
}

type tokenScanner interface {
	Scan(...any) error
}

func scanToken(row tokenScanner) (Token, error) {
	var (
		token     Token
		profile   string
		createdBy sql.NullString
		lastUsed  sql.NullTime
		revokedAt sql.NullTime
		revokedBy sql.NullString
	)
	err := row.Scan(
		&token.ID,
		&token.ServiceAccountID,
		&token.WorkspaceID,
		&token.ProjectID,
		&token.Name,
		&token.TokenPrefix,
		&profile,
		&createdBy,
		&token.CreatedAt,
		&token.UpdatedAt,
		&token.ExpiresAt,
		&lastUsed,
		&revokedAt,
		&revokedBy,
		&token.RevokedReason,
		&token.ResourceVersion,
	)
	if err != nil {
		return Token{}, err
	}
	token.Profile = Profile(profile)
	if createdBy.Valid {
		token.CreatedByUserID = createdBy.String
	}
	if lastUsed.Valid {
		value := lastUsed.Time
		token.LastUsedAt = &value
	}
	if revokedAt.Valid {
		value := revokedAt.Time
		token.RevokedAt = &value
	}
	if revokedBy.Valid {
		token.RevokedByUserID = revokedBy.String
	}
	return token, nil
}

func prefixedTokenColumns(alias string) string {
	return alias + `.id, ` +
		alias + `.service_account_id, ` +
		alias + `.workspace_id, ` +
		alias + `.project_id, ` +
		alias + `.name, ` +
		alias + `.token_prefix, ` +
		alias + `.profile, ` +
		alias + `.created_by_user_id, ` +
		alias + `.created_at, ` +
		alias + `.updated_at, ` +
		alias + `.expires_at, ` +
		alias + `.last_used_at, ` +
		alias + `.revoked_at, ` +
		alias + `.revoked_by_user_id, ` +
		alias + `.revoked_reason, ` +
		alias + `.resource_version`
}
