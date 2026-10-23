package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/queue"
)

type buildJobExecFunc func(context.Context, queue.Job) error

func (f buildJobExecFunc) Execute(ctx context.Context, job queue.Job) error {
	return f(ctx, job)
}

type buildLeaseStore struct {
	mu          sync.Mutex
	job         queue.Job
	leased      bool
	heartbeats  int
	completed   bool
	failed      bool
	completeErr error
	failErr     error
}

func (s *buildLeaseStore) Enqueue(context.Context, queue.EnqueueRequest) (queue.Job, error) {
	return queue.Job{}, errors.New("not implemented")
}

func (s *buildLeaseStore) Lease(context.Context, queue.LeaseRequest) (queue.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leased {
		return queue.Job{}, queue.ErrNoWork
	}
	s.leased = true
	return s.job, nil
}

func (s *buildLeaseStore) Heartbeat(context.Context, string, string, time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeats++
	return nil
}

func (s *buildLeaseStore) Complete(context.Context, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completeErr != nil {
		return s.completeErr
	}
	s.completed = true
	return nil
}

func (s *buildLeaseStore) Fail(context.Context, string, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failErr != nil {
		return s.failErr
	}
	s.failed = true
	return nil
}

type terminalObservingBuildExecutor struct {
	executeErr error
	store      *buildLeaseStore
	notified   int
	afterQueue bool
}

func (e *terminalObservingBuildExecutor) Execute(context.Context, queue.Job) error {
	return e.executeErr
}

func (e *terminalObservingBuildExecutor) OnBuildJobTerminalized(context.Context, queue.Job) error {
	e.notified++
	e.store.mu.Lock()
	defer e.store.mu.Unlock()
	e.afterQueue = e.store.completed || e.store.failed
	return nil
}

func TestProcessOneBuildJobHeartbeatsLongBuild(t *testing.T) {
	store := &buildLeaseStore{job: queue.Job{ID: "job-1", LeaseID: "lease-1", Queue: queue.Build}}
	executor := buildJobExecFunc(func(context.Context, queue.Job) error {
		time.Sleep(80 * time.Millisecond)
		return nil
	})

	processed, err := processOneBuildJob(context.Background(), store, "worker-1", 30*time.Millisecond, time.Second, time.Now().UTC(), executor)
	if err != nil {
		t.Fatalf("processOneBuildJob: %v", err)
	}
	if !processed {
		t.Fatal("job was not processed")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.heartbeats == 0 {
		t.Fatal("long build did not extend its queue lease")
	}
	if !store.completed || store.failed {
		t.Fatalf("queue outcome completed=%t failed=%t", store.completed, store.failed)
	}
}

func TestProcessOneBuildJobEnforcesExecutionTimeout(t *testing.T) {
	store := &buildLeaseStore{job: queue.Job{ID: "job-2", LeaseID: "lease-2", Queue: queue.Build}}
	executor := buildJobExecFunc(func(ctx context.Context, _ queue.Job) error {
		<-ctx.Done()
		return ctx.Err()
	})

	processed, err := processOneBuildJob(context.Background(), store, "worker-1", time.Second, 20*time.Millisecond, time.Now().UTC(), executor)
	if !processed {
		t.Fatal("job was not processed")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.failed || store.completed {
		t.Fatalf("queue outcome completed=%t failed=%t", store.completed, store.failed)
	}
}

func TestProcessOneBuildJobNotifiesArtifactLifecycleOnlyAfterComplete(t *testing.T) {
	store := &buildLeaseStore{job: queue.Job{ID: "job-terminal", LeaseID: "lease-terminal", Queue: queue.Build}}
	executor := &terminalObservingBuildExecutor{store: store}
	if _, err := processOneBuildJob(context.Background(), store, "worker-1", time.Second, time.Second, time.Now().UTC(), executor); err != nil {
		t.Fatalf("processOneBuildJob: %v", err)
	}
	if executor.notified != 1 || !executor.afterQueue {
		t.Fatalf("terminal notification count=%d afterQueue=%t", executor.notified, executor.afterQueue)
	}
}

func TestProcessOneBuildJobKeepsArtifactActiveWhenQueueTerminalizationFails(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		store := &buildLeaseStore{
			job:         queue.Job{ID: "job-complete-error", LeaseID: "lease-complete-error", Queue: queue.Build},
			completeErr: errors.New("complete unavailable"),
		}
		executor := &terminalObservingBuildExecutor{store: store}
		if _, err := processOneBuildJob(context.Background(), store, "worker-1", time.Second, time.Second, time.Now().UTC(), executor); err == nil {
			t.Fatal("processOneBuildJob succeeded")
		}
		if executor.notified != 0 {
			t.Fatalf("terminal notification count = %d, want zero", executor.notified)
		}
	})
	t.Run("fail", func(t *testing.T) {
		store := &buildLeaseStore{
			job:     queue.Job{ID: "job-fail-error", LeaseID: "lease-fail-error", Queue: queue.Build},
			failErr: errors.New("fail unavailable"),
		}
		executor := &terminalObservingBuildExecutor{store: store, executeErr: errors.New("build failed")}
		if _, err := processOneBuildJob(context.Background(), store, "worker-1", time.Second, time.Second, time.Now().UTC(), executor); err == nil {
			t.Fatal("processOneBuildJob succeeded")
		}
		if executor.notified != 0 {
			t.Fatalf("terminal notification count = %d, want zero", executor.notified)
		}
	})
}

func TestProcessOneBuildJobNotifiesArtifactLifecycleAfterFail(t *testing.T) {
	store := &buildLeaseStore{job: queue.Job{ID: "job-failed", LeaseID: "lease-failed", Queue: queue.Build}}
	executor := &terminalObservingBuildExecutor{
		store:      store,
		executeErr: errors.New("build failed"),
	}
	if _, err := processOneBuildJob(context.Background(), store, "worker-1", time.Second, time.Second, time.Now().UTC(), executor); err == nil {
		t.Fatal("processOneBuildJob succeeded")
	}
	if executor.notified != 1 || !executor.afterQueue {
		t.Fatalf("terminal notification count=%d afterQueue=%t", executor.notified, executor.afterQueue)
	}
}
