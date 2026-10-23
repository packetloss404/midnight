// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/midnight/midnight/internal/db/pg"
)

type PostgresPreviewCommentStore struct{ Pool *pg.Pool }

func (s *PostgresPreviewCommentStore) ClaimPreviewComments(ctx context.Context, request PreviewCommentClaimRequest) ([]PendingPreviewComment, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("preview comment store: pool is required")
	}
	if request.Limit <= 0 || request.Limit > 100 {
		request.Limit = 25
	}
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
	var out []PendingPreviewComment
	err = s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE github_pull_request_preview_comments SET
			delivery_state='dead_letter',lease_token=NULL,lease_expires_at=NULL,
			last_error='delivery lease expired after final attempt',terminal_at=$1,updated_at=$1
			WHERE delivery_state='delivering' AND lease_expires_at <= $1 AND attempt >= max_attempts`, request.Now); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `WITH candidates AS (
			SELECT candidate.id FROM github_pull_request_preview_comments candidate
			 WHERE candidate.attempt < candidate.max_attempts
			 AND ((candidate.delivery_state='pending' AND candidate.available_at <= $1)
			 OR (candidate.delivery_state='delivering' AND candidate.lease_expires_at <= $1))
			 AND NOT EXISTS (
				SELECT 1 FROM github_pull_request_preview_comments earlier
				 WHERE earlier.preview_id=candidate.preview_id
				   AND earlier.delivery_state IN ('pending','delivering')
				   AND (earlier.created_at,earlier.id) < (candidate.created_at,candidate.id)
			 )
			 ORDER BY candidate.available_at,candidate.created_at,candidate.id FOR UPDATE SKIP LOCKED LIMIT $2
		) UPDATE github_pull_request_preview_comments c SET delivery_state='delivering',
			attempt=c.attempt+1,lease_token=$3,lease_expires_at=$4,updated_at=$1
		  FROM candidates WHERE c.id=candidates.id
		RETURNING c.id,c.lease_token,c.preview_id,c.installation_id,c.repository_id,
			c.repository_full_name,c.pull_request_number,c.preview_state,c.head_sha,
			COALESCE(c.deployment_id,''),c.preview_url,c.attempt,c.max_attempts,
			COALESCE((SELECT p.last_comment_id FROM github_pull_request_previews p WHERE p.id=c.preview_id),0)`,
			request.Now, request.Limit, leaseToken, request.Now.Add(request.LeaseDuration))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item PendingPreviewComment
			if err := rows.Scan(&item.ID, &item.LeaseToken, &item.PreviewID, &item.InstallationID,
				&item.RepositoryID, &item.RepoFullName, &item.PullRequest, &item.State, &item.HeadSHA,
				&item.DeploymentID, &item.PreviewURL, &item.Attempt, &item.MaxAttempts, &item.CommentID); err != nil {
				return err
			}
			out = append(out, item)
		}
		return rows.Err()
	})
	return out, err
}

func (s *PostgresPreviewCommentStore) MarkPreviewCommentPosted(ctx context.Context, id, leaseToken string, commentID int64, now time.Time) error {
	if commentID <= 0 {
		return ErrPreviewCommentInvalid
	}
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var previewID string
		err := tx.QueryRow(ctx, `UPDATE github_pull_request_preview_comments SET delivery_state='posted',
			lease_token=NULL,lease_expires_at=NULL,posted_at=$3,terminal_at=$3,last_error='',updated_at=$3
			WHERE id=$1 AND delivery_state='delivering' AND lease_token=$2 RETURNING preview_id`,
			strings.TrimSpace(id), strings.TrimSpace(leaseToken), now.UTC()).Scan(&previewID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPreviewCommentLeaseLost
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE github_pull_request_previews SET last_comment_id=$1 WHERE id=$2`, commentID, previewID)
		return err
	})
}

func (s *PostgresPreviewCommentStore) MarkPreviewCommentFailed(ctx context.Context, request PreviewCommentFailureRequest) (bool, error) {
	var state string
	err := s.Pool.QueryRow(ctx, `UPDATE github_pull_request_preview_comments SET
		delivery_state=CASE WHEN $5 OR attempt >= max_attempts THEN 'dead_letter' ELSE 'pending' END,
		lease_token=NULL,lease_expires_at=NULL,available_at=CASE WHEN $5 OR attempt >= max_attempts THEN $3 ELSE $4 END,
		last_error=$6,terminal_at=CASE WHEN $5 OR attempt >= max_attempts THEN $3 ELSE NULL END,updated_at=$3
		WHERE id=$1 AND delivery_state='delivering' AND lease_token=$2 RETURNING delivery_state`,
		request.ID, request.LeaseToken, request.FailedAt.UTC(), request.AvailableAt.UTC(), request.Permanent,
		sanitizeCommitStatusError(request.LastError)).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrPreviewCommentLeaseLost
	}
	if err != nil {
		return false, fmt.Errorf("mark preview comment failed: %w", err)
	}
	return state == "dead_letter", nil
}
