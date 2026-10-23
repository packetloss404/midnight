// Package queue defines the Postgres-backed job queue contract consumed by workers.
package queue

import (
	"context"
	"errors"
	"time"
)

type Name string

const (
	Build     Name = "build"
	Deploy    Name = "deploy"
	Reconcile Name = "reconcile"
	Webhook   Name = "webhook"
	Cleanup   Name = "cleanup"
)

type JobState string

const (
	JobQueued    JobState = "QUEUED"
	JobLeased    JobState = "LEASED"
	JobSucceeded JobState = "SUCCEEDED"
	JobFailed    JobState = "FAILED"
	JobCanceled  JobState = "CANCELED"
)

const (
	ReasonLeaseExpired              = "LEASE_EXPIRED"
	ReasonLeaseExpiredMaxAttempts   = "LEASE_EXPIRED_MAX_ATTEMPTS"
	ReasonDeployCanceledBeforeLease = "DEPLOY_CANCELED_BEFORE_LEASE"
)

type Job struct {
	ID                string
	Queue             Name
	State             JobState
	Priority          int
	Attempt           int
	PartitionKey      string
	ParentOperationID string
	AvailableAt       time.Time
	LeaseID           string
	LeasedUntil       time.Time
	Payload           []byte
}

type EnqueueRequest struct {
	Queue             Name
	Priority          int
	PartitionKey      string
	ParentOperationID string
	AvailableAt       time.Time
	Payload           []byte
}

type LeaseRequest struct {
	Queue     Name
	WorkerID  string
	LeaseTime time.Duration
	Now       time.Time
}

type CancelQueuedRequest struct {
	Queue             Name
	ParentOperationID string
	Reason            string
	Now               time.Time
}

type CancelQueuedResult struct {
	Job             Job
	Canceled        bool
	AlreadyLeased   bool
	AlreadyTerminal bool
}

var ErrInvalidLeaseHeartbeat = errors.New("invalid lease heartbeat")

func HeartbeatInterval(leaseTTL time.Duration) (time.Duration, error) {
	if leaseTTL <= 0 {
		return 0, ErrInvalidLeaseHeartbeat
	}
	interval := leaseTTL / 3
	if interval <= 0 {
		interval = time.Nanosecond
	}
	return interval, nil
}

func LeaseHeartbeatUntil(now time.Time, leaseTTL time.Duration) (time.Time, error) {
	if now.IsZero() || leaseTTL <= 0 {
		return time.Time{}, ErrInvalidLeaseHeartbeat
	}
	return now.Add(leaseTTL), nil
}

func HeartbeatLease(ctx context.Context, store Store, job Job, leaseTTL time.Duration, now time.Time) (time.Time, error) {
	if store == nil || job.ID == "" || job.LeaseID == "" {
		return time.Time{}, ErrInvalidLeaseHeartbeat
	}
	until, err := LeaseHeartbeatUntil(now, leaseTTL)
	if err != nil {
		return time.Time{}, err
	}
	if err := store.Heartbeat(ctx, job.ID, job.LeaseID, until); err != nil {
		return time.Time{}, err
	}
	return until, nil
}

type Store interface {
	Enqueue(ctx context.Context, request EnqueueRequest) (Job, error)
	Lease(ctx context.Context, request LeaseRequest) (Job, error)
	Heartbeat(ctx context.Context, jobID string, leaseID string, until time.Time) error
	Complete(ctx context.Context, jobID string, leaseID string) error
	Fail(ctx context.Context, jobID string, leaseID string, reason string) error
}

type QueuedCancelStore interface {
	CancelQueued(ctx context.Context, request CancelQueuedRequest) (CancelQueuedResult, error)
}
