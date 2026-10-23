// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"time"

	githubapp "github.com/midnight/midnight/internal/integrations/github"
)

const defaultPreviewReconcileInterval = 30 * time.Second

const (
	previewActionUpsert = "upsert"
	previewActionDelete = "delete"
)

// PreviewEnvironmentView is the redaction-safe service-scoped product view.
type PreviewEnvironmentView struct {
	ID            string
	PullRequest   int
	EnvironmentID string
	DeploymentID  string
	State         string
	URL           string
	HeadSHA       string
	FromFork      bool
	ExpiresAt     time.Time
	UpdatedAt     time.Time
}

// PreviewPreparation is the durable environment identity prepared before the
// canonical GitHub build intake transaction attaches a deployment.
type PreviewPreparation struct {
	PreviewID     string
	EnvironmentID string
	URL           string
}

// GitHubPreviewLifecycle owns PR planning, focused environment preparation,
// close/expiry cleanup, and service-scoped reads. Deployment creation remains
// in the canonical staged-work outbox.
type GitHubPreviewLifecycle interface {
	PlanPullRequest(
		context.Context,
		githubapp.PullRequestEvent,
		time.Time,
		string,
	) ([]githubapp.DeployIntent, error)
	PreparePullRequest(context.Context, githubapp.DeployIntent) (PreviewPreparation, error)
	ClosePullRequest(context.Context, githubapp.DeployIntent) (PreviewEnvironmentView, error)
	ListServicePreviews(context.Context, string) ([]PreviewEnvironmentView, error)
	Reconcile(context.Context, int) (int, error)
}

// RunPreviewLifecycleReconciler converges TTL and close cleanup until the
// owning server shuts down. Errors remain retryable and are reported without
// terminating the loop.
func RunPreviewLifecycleReconciler(
	ctx context.Context,
	lifecycle GitHubPreviewLifecycle,
	interval time.Duration,
	report func(error),
) {
	if lifecycle == nil {
		return
	}
	if interval <= 0 {
		interval = defaultPreviewReconcileInterval
	}
	reconcile := func() {
		if _, err := lifecycle.Reconcile(ctx, 25); err != nil && report != nil && ctx.Err() == nil {
			report(err)
		}
	}
	reconcile()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}
