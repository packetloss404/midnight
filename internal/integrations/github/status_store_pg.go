// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
)

type PostgresStatusStore struct {
	Pool *pg.Pool
}

func NewPostgresStatusStore(pool *pg.Pool) *PostgresStatusStore {
	return &PostgresStatusStore{Pool: pool}
}

var _ StatusStore = (*PostgresStatusStore)(nil)

func newCommitStatusLeaseToken() (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("github status lease entropy: %w", err)
	}
	return "ghsl_" + hex.EncodeToString(entropy[:]), nil
}

func (s *PostgresStatusStore) ClaimCommitStatuses(ctx context.Context, request StatusClaimRequest) ([]PendingCommitStatus, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("github status store: pool not initialized")
	}
	if request.Limit <= 0 {
		request.Limit = 25
	}
	if request.Limit > 100 {
		request.Limit = 100
	}
	request.Now = request.Now.UTC()
	if request.Now.IsZero() {
		request.Now = time.Now().UTC()
	}
	if request.LeaseDuration <= 0 {
		request.LeaseDuration = 30 * time.Second
	}
	leaseToken, err := newCommitStatusLeaseToken()
	if err != nil {
		return nil, err
	}
	leaseExpiresAt := request.Now.Add(request.LeaseDuration)
	claimed := make([]PendingCommitStatus, 0, request.Limit)
	err = s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE github_commit_statuses
			   SET delivery_state='dead_letter', lease_token=NULL, lease_expires_at=NULL,
			       last_error='delivery lease expired after final attempt',
			       terminal_at=$1, updated_at=$1
			 WHERE delivery_state='delivering'
			   AND lease_expires_at <= $1
			   AND attempt >= max_attempts`, request.Now); err != nil {
			return fmt.Errorf("dead-letter exhausted github status leases: %w", err)
		}
		rows, err := tx.Query(ctx, `
			WITH candidates AS (
			    SELECT id
			      FROM github_commit_statuses
			     WHERE attempt < max_attempts
			       AND (
			           (delivery_state='pending' AND available_at <= $1)
			           OR (delivery_state='delivering' AND lease_expires_at <= $1)
			       )
			     ORDER BY available_at, created_at, id
			     FOR UPDATE SKIP LOCKED
			     LIMIT $2
			)
			UPDATE github_commit_statuses status
			   SET delivery_state='delivering', attempt=status.attempt+1,
			       lease_token=$3, lease_expires_at=$4, updated_at=$1
			  FROM candidates
			 WHERE status.id=candidates.id
			RETURNING status.id, status.lease_token, status.installation_id,
			          status.repository_id, status.provider_host, status.repo_full_name,
			          status.commit_sha, status.state, status.context, status.target_url,
			          status.description, status.attempt, status.max_attempts`,
			request.Now, request.Limit, leaseToken, leaseExpiresAt)
		if err != nil {
			return fmt.Errorf("claim github commit statuses: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var status PendingCommitStatus
			if err := rows.Scan(
				&status.ID, &status.LeaseToken, &status.InstallationID,
				&status.RepositoryID, &status.ProviderHost, &status.RepoFullName,
				&status.CommitSHA, &status.State, &status.Context, &status.TargetURL,
				&status.Description, &status.Attempt, &status.MaxAttempts,
			); err != nil {
				return fmt.Errorf("scan github commit status claim: %w", err)
			}
			claimed = append(claimed, status)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

func (s *PostgresStatusStore) MarkCommitStatusPosted(ctx context.Context, id, leaseToken string, postedAt time.Time) error {
	if s == nil || s.Pool == nil {
		return errors.New("github status store: pool not initialized")
	}
	postedAt = postedAt.UTC()
	tag, err := s.Pool.Exec(ctx, `
		UPDATE github_commit_statuses
		   SET delivery_state='posted', lease_token=NULL, lease_expires_at=NULL,
		       posted_at=$3, terminal_at=$3, last_error='', updated_at=$3
		 WHERE id=$1 AND delivery_state='delivering' AND lease_token=$2`,
		strings.TrimSpace(id), strings.TrimSpace(leaseToken), postedAt)
	if err != nil {
		return fmt.Errorf("mark github commit status posted: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrCommitStatusLeaseLost
	}
	return nil
}

func (s *PostgresStatusStore) MarkCommitStatusFailed(ctx context.Context, request StatusFailureRequest) (bool, error) {
	if s == nil || s.Pool == nil {
		return false, errors.New("github status store: pool not initialized")
	}
	request.FailedAt = request.FailedAt.UTC()
	request.AvailableAt = request.AvailableAt.UTC()
	if request.AvailableAt.Before(request.FailedAt) {
		request.AvailableAt = request.FailedAt
	}
	var state string
	err := s.Pool.QueryRow(ctx, `
		UPDATE github_commit_statuses
		   SET delivery_state=CASE WHEN $5 OR attempt >= max_attempts
		                           THEN 'dead_letter' ELSE 'pending' END,
		       lease_token=NULL,
		       lease_expires_at=NULL,
		       available_at=CASE WHEN $5 OR attempt >= max_attempts
		                         THEN $3::timestamptz ELSE $4::timestamptz END,
		       last_error=$6,
		       terminal_at=CASE WHEN $5 OR attempt >= max_attempts
		                        THEN $3::timestamptz ELSE NULL END,
		       updated_at=$3
		 WHERE id=$1 AND delivery_state='delivering' AND lease_token=$2
		RETURNING delivery_state`,
		strings.TrimSpace(request.ID), strings.TrimSpace(request.LeaseToken),
		request.FailedAt, request.AvailableAt, request.Permanent,
		sanitizeCommitStatusError(request.LastError),
	).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrCommitStatusLeaseLost
	}
	if err != nil {
		return false, fmt.Errorf("mark github commit status failed: %w", err)
	}
	return state == "dead_letter", nil
}

func sanitizeCommitStatusError(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(value))
	runes := []rune(value)
	if len(runes) > 512 {
		value = string(runes[:512])
	}
	return value
}
