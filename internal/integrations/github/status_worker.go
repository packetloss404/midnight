// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrCommitStatusLeaseLost = errors.New("github commit status lease lost")

// PendingCommitStatus is one leased commit-status delivery. LeaseToken is an
// opaque compare-and-swap fence; only the owning attempt may complete or
// reschedule the row.
type PendingCommitStatus struct {
	ID             string
	LeaseToken     string
	InstallationID int64
	RepositoryID   int64
	ProviderHost   string
	RepoFullName   string
	CommitSHA      string
	State          string
	Context        string
	TargetURL      string
	Description    string
	Attempt        int
	MaxAttempts    int
}

type StatusClaimRequest struct {
	Limit         int
	Now           time.Time
	LeaseDuration time.Duration
}

type StatusFailureRequest struct {
	ID          string
	LeaseToken  string
	FailedAt    time.Time
	AvailableAt time.Time
	LastError   string
	Permanent   bool
}

// StatusStore provides leased, bounded, at-least-once delivery. GitHub may
// receive the same context twice if a process dies after the HTTP response but
// before the database completion; lease fencing prevents stale workers from
// completing a newer database attempt.
type StatusStore interface {
	ClaimCommitStatuses(context.Context, StatusClaimRequest) ([]PendingCommitStatus, error)
	MarkCommitStatusPosted(context.Context, string, string, time.Time) error
	MarkCommitStatusFailed(context.Context, StatusFailureRequest) (deadLettered bool, err error)
}

type StatusPublisher interface {
	PublishInstallationCommitStatus(context.Context, PublishInstallationCommitStatusRequest) (CommitStatusResponse, error)
}

var _ StatusPublisher = (*AppClient)(nil)

type StatusDrainResult struct {
	Fetched      int
	Posted       int
	Failed       int
	DeadLettered int
}

type StatusDrainWorker struct {
	Store         StatusStore
	Publisher     StatusPublisher
	BatchSize     int
	PollInterval  time.Duration
	MaxBackoff    time.Duration
	LeaseDuration time.Duration
	Now           func() time.Time
	Logf          func(format string, args ...any)
}

func (w StatusDrainWorker) batchSize() int {
	if w.BatchSize > 0 {
		if w.BatchSize > 100 {
			return 100
		}
		return w.BatchSize
	}
	return 25
}

func (w StatusDrainWorker) pollInterval() time.Duration {
	if w.PollInterval > 0 {
		return w.PollInterval
	}
	return 5 * time.Second
}

func (w StatusDrainWorker) maxBackoff() time.Duration {
	if w.MaxBackoff > 0 {
		return w.MaxBackoff
	}
	return 60 * time.Second
}

func (w StatusDrainWorker) leaseDuration() time.Duration {
	if w.LeaseDuration > 0 {
		return w.LeaseDuration
	}
	return 30 * time.Second
}

func (w StatusDrainWorker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w StatusDrainWorker) logf(format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
	}
}

func statusRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second
	for i := 1; i < attempt && delay < time.Minute; i++ {
		delay *= 2
	}
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}

func (w StatusDrainWorker) fail(ctx context.Context, status PendingCommitStatus, failure error, permanent bool, now time.Time) (bool, error) {
	return w.Store.MarkCommitStatusFailed(ctx, StatusFailureRequest{
		ID:          status.ID,
		LeaseToken:  status.LeaseToken,
		FailedAt:    now,
		AvailableAt: now.Add(statusRetryDelay(status.Attempt)),
		LastError:   failure.Error(),
		Permanent:   permanent,
	})
}

// RunOnce claims one batch, revalidates persisted metadata at the publication
// boundary, and either posts, reschedules, or dead-letters every owned row.
func (w StatusDrainWorker) RunOnce(ctx context.Context) (StatusDrainResult, error) {
	if w.Store == nil || w.Publisher == nil {
		return StatusDrainResult{}, fmt.Errorf("status drain worker is not configured")
	}
	now := w.now()
	statuses, err := w.Store.ClaimCommitStatuses(ctx, StatusClaimRequest{
		Limit:         w.batchSize(),
		Now:           now,
		LeaseDuration: w.leaseDuration(),
	})
	if err != nil {
		return StatusDrainResult{}, err
	}
	result := StatusDrainResult{Fetched: len(statuses)}
	for _, status := range statuses {
		metadata, validationErr := NewCommitStatusMetadata(CommitStatusInput{
			ProviderHost:   status.ProviderHost,
			InstallationID: status.InstallationID,
			RepositoryID:   status.RepositoryID,
			RepoFullName:   status.RepoFullName,
			SHA:            status.CommitSHA,
			State:          status.State,
			Context:        status.Context,
			TargetURL:      status.TargetURL,
			Description:    status.Description,
		})
		if validationErr != nil {
			dead, storeErr := w.fail(ctx, status, validationErr, true, w.now())
			if storeErr != nil {
				return result, storeErr
			}
			result.Failed++
			if dead {
				result.DeadLettered++
			}
			w.logf("github status %s rejected at publish boundary: %v", status.ID, validationErr)
			continue
		}
		_, publishErr := w.Publisher.PublishInstallationCommitStatus(ctx, PublishInstallationCommitStatusRequest{
			InstallationID: metadata.InstallationID,
			RepositoryID:   metadata.RepositoryID,
			RepoFullName:   metadata.RepoFullName,
			SHA:            metadata.SHA,
			State:          CommitStatusState(metadata.State),
			TargetURL:      metadata.TargetURL,
			Description:    metadata.Description,
			Context:        metadata.Context,
		})
		if publishErr != nil {
			dead, storeErr := w.fail(ctx, status, publishErr, false, w.now())
			if storeErr != nil {
				return result, storeErr
			}
			result.Failed++
			if dead {
				result.DeadLettered++
			}
			w.logf("publish github status %s failed: %v", status.ID, publishErr)
			continue
		}
		if err := w.Store.MarkCommitStatusPosted(ctx, status.ID, status.LeaseToken, w.now()); err != nil {
			return result, err
		}
		result.Posted++
	}
	return result, nil
}

func (w StatusDrainWorker) Run(ctx context.Context) error {
	backoff := w.pollInterval()
	for {
		result, err := w.RunOnce(ctx)
		var wait time.Duration
		switch {
		case err != nil:
			w.logf("github status drain pass failed: %v", err)
			wait = backoff
			backoff *= 2
			if backoff > w.maxBackoff() {
				backoff = w.maxBackoff()
			}
		default:
			backoff = w.pollInterval()
			if result.Fetched >= w.batchSize() && result.Failed == 0 {
				continue
			}
			wait = w.pollInterval()
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
