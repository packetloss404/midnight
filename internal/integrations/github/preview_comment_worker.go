// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrPreviewCommentLeaseLost = errors.New("github preview comment lease lost")

type PendingPreviewComment struct {
	ID, LeaseToken, PreviewID, RepoFullName, State, HeadSHA, DeploymentID, PreviewURL string
	InstallationID, RepositoryID, CommentID                                           int64
	PullRequest, Attempt, MaxAttempts                                                 int
}

type PreviewCommentClaimRequest struct {
	Limit         int
	Now           time.Time
	LeaseDuration time.Duration
}

type PreviewCommentFailureRequest struct {
	ID, LeaseToken, LastError string
	FailedAt, AvailableAt     time.Time
	Permanent                 bool
}

type PreviewCommentStore interface {
	ClaimPreviewComments(context.Context, PreviewCommentClaimRequest) ([]PendingPreviewComment, error)
	MarkPreviewCommentPosted(context.Context, string, string, int64, time.Time) error
	MarkPreviewCommentFailed(context.Context, PreviewCommentFailureRequest) (bool, error)
}

type PreviewCommentPublisher interface {
	PublishInstallationPreviewComment(context.Context, PublishInstallationPreviewCommentRequest) (PreviewCommentResponse, error)
}

type PreviewCommentDrainWorker struct {
	Store                       PreviewCommentStore
	Publisher                   PreviewCommentPublisher
	BatchSize                   int
	PollInterval, LeaseDuration time.Duration
	Now                         func() time.Time
	Logf                        func(string, ...any)
}

func (w PreviewCommentDrainWorker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w PreviewCommentDrainWorker) RunOnce(ctx context.Context) (int, error) {
	if w.Store == nil || w.Publisher == nil {
		return 0, errors.New("preview comment worker is not configured")
	}
	limit := w.BatchSize
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	lease := w.LeaseDuration
	if lease <= 0 {
		lease = 30 * time.Second
	}
	items, err := w.Store.ClaimPreviewComments(ctx, PreviewCommentClaimRequest{Limit: limit, Now: w.now(), LeaseDuration: lease})
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		body, validationErr := previewCommentBody(item)
		if validationErr == nil {
			var response PreviewCommentResponse
			response, validationErr = w.Publisher.PublishInstallationPreviewComment(ctx, PublishInstallationPreviewCommentRequest{
				InstallationID: item.InstallationID, RepositoryID: item.RepositoryID,
				RepoFullName: item.RepoFullName, PullRequest: item.PullRequest,
				PreviewID: item.PreviewID, CommentID: item.CommentID, Body: body,
			})
			if validationErr == nil {
				if err := w.Store.MarkPreviewCommentPosted(ctx, item.ID, item.LeaseToken, response.ID, w.now()); err != nil {
					return len(items), err
				}
				continue
			}
		}
		permanent := errors.Is(validationErr, ErrPreviewCommentInvalid)
		_, storeErr := w.Store.MarkPreviewCommentFailed(ctx, PreviewCommentFailureRequest{
			ID: item.ID, LeaseToken: item.LeaseToken, FailedAt: w.now(),
			AvailableAt: w.now().Add(statusRetryDelay(item.Attempt)), LastError: validationErr.Error(), Permanent: permanent,
		})
		if storeErr != nil {
			return len(items), storeErr
		}
		if w.Logf != nil {
			w.Logf("publish github preview comment %s failed: %v", item.ID, validationErr)
		}
	}
	return len(items), nil
}

func previewCommentBody(item PendingPreviewComment) (string, error) {
	shortSHA := strings.TrimSpace(item.HeadSHA)
	if len(shortSHA) < 7 {
		return "", ErrPreviewCommentInvalid
	}
	shortSHA = shortSHA[:7]
	switch item.State {
	case "deploying", "refreshing":
		return fmt.Sprintf("### Midnight preview\n\nBuilding `%s`.\n\n[Open preview](%s)", shortSHA, item.PreviewURL), nil
	case "ready":
		return fmt.Sprintf("### Midnight preview\n\nReady for `%s`: [open preview](%s).", shortSHA, item.PreviewURL), nil
	case "failed":
		return fmt.Sprintf("### Midnight preview\n\nDeployment failed for `%s`. Check the Midnight deployment log and retry after fixing the build.", shortSHA), nil
	case "deleting":
		return "### Midnight preview\n\nThis preview is being removed.", nil
	case "deleted":
		return "### Midnight preview\n\nThis preview has been removed.", nil
	default:
		return "", ErrPreviewCommentInvalid
	}
}

func (w PreviewCommentDrainWorker) Run(ctx context.Context) error {
	poll := w.PollInterval
	if poll <= 0 {
		poll = 5 * time.Second
	}
	for {
		fetched, err := w.RunOnce(ctx)
		if err != nil && w.Logf != nil {
			w.Logf("github preview comment drain pass failed: %v", err)
		}
		if err == nil && fetched >= w.BatchSize && w.BatchSize > 0 {
			continue
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
