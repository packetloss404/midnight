// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/queue"
)

// sequenceQueueStore hands out a fixed list of jobs, then ErrNoWork.
type sequenceQueueStore struct {
	workerQueueStore
	mu   sync.Mutex
	jobs []queue.Job
	done []string
}

func (s *sequenceQueueStore) Lease(_ context.Context, _ queue.LeaseRequest) (queue.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.jobs) == 0 {
		return queue.Job{}, queue.ErrNoWork
	}
	job := s.jobs[0]
	s.jobs = s.jobs[1:]
	return job, nil
}

func (s *sequenceQueueStore) Complete(_ context.Context, jobID string, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = append(s.done, jobID)
	return nil
}

func (s *sequenceQueueStore) completed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.done...)
}

// gateExecutor records when each job starts and blocks until released.
type gateExecutor struct {
	mu      sync.Mutex
	started []string
	release map[string]chan struct{}
	signal  chan string
}

func newGateExecutor() *gateExecutor {
	return &gateExecutor{release: map[string]chan struct{}{}, signal: make(chan string, 16)}
}

func (g *gateExecutor) Execute(ctx context.Context, job queue.Job) (deploy.ExecutionOutcome, error) {
	g.mu.Lock()
	g.started = append(g.started, job.ID)
	ch, ok := g.release[job.ID]
	if !ok {
		ch = make(chan struct{})
		g.release[job.ID] = ch
	}
	g.mu.Unlock()
	g.signal <- job.ID
	select {
	case <-ch:
		return deploy.ExecutionOutcome{Succeeded: true, OperationID: "op-" + job.ID}, nil
	case <-ctx.Done():
		return deploy.ExecutionOutcome{}, ctx.Err()
	}
}

func (g *gateExecutor) let(jobID string) {
	g.mu.Lock()
	ch, ok := g.release[jobID]
	if !ok {
		ch = make(chan struct{})
		g.release[jobID] = ch
	}
	g.mu.Unlock()
	close(ch)
}

func (g *gateExecutor) startedIDs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.started...)
}

func waitStart(t *testing.T, g *gateExecutor) string {
	t.Helper()
	select {
	case id := <-g.signal:
		return id
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a job to start")
		return ""
	}
}

func expectNoStart(t *testing.T, g *gateExecutor) {
	t.Helper()
	select {
	case id := <-g.signal:
		t.Fatalf("job %s started unexpectedly", id)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPumpDeployJobsRunsDistinctServicesConcurrently(t *testing.T) {
	store := &sequenceQueueStore{jobs: []queue.Job{
		{ID: "job-a", Queue: queue.Deploy, LeaseID: "l-a", PartitionKey: "svc-a"},
		{ID: "job-b", Queue: queue.Deploy, LeaseID: "l-b", PartitionKey: "svc-b"},
	}}
	executor := newGateExecutor()
	pool := newDeployJobPool(4)

	started, err := pumpDeployJobs(context.Background(), pool, store, "w", time.Minute, time.Now(), executor, nil, nil)
	if err != nil || started != 2 {
		t.Fatalf("pumpDeployJobs() = %d, %v; want 2, nil", started, err)
	}
	first, second := waitStart(t, executor), waitStart(t, executor)
	if first == second {
		t.Fatalf("expected two distinct jobs to start, got %q twice", first)
	}
	executor.let("job-a")
	executor.let("job-b")
	pool.wait()
	if got := store.completed(); len(got) != 2 {
		t.Fatalf("completed = %v, want both jobs", got)
	}
}

func TestPumpDeployJobsSerializesSameService(t *testing.T) {
	store := &sequenceQueueStore{jobs: []queue.Job{
		{ID: "job-1", Queue: queue.Deploy, LeaseID: "l-1", PartitionKey: "svc-a"},
		{ID: "job-2", Queue: queue.Deploy, LeaseID: "l-2", PartitionKey: "svc-a"},
	}}
	executor := newGateExecutor()
	pool := newDeployJobPool(4)

	if _, err := pumpDeployJobs(context.Background(), pool, store, "w", time.Minute, time.Now(), executor, nil, nil); err != nil {
		t.Fatalf("pumpDeployJobs() error = %v", err)
	}
	if id := waitStart(t, executor); id != "job-1" {
		t.Fatalf("first started job = %q, want job-1 (lease order)", id)
	}
	expectNoStart(t, executor)
	executor.let("job-1")
	if id := waitStart(t, executor); id != "job-2" {
		t.Fatalf("second started job = %q, want job-2", id)
	}
	executor.let("job-2")
	pool.wait()
	if got := store.completed(); len(got) != 2 || got[0] != "job-1" || got[1] != "job-2" {
		t.Fatalf("completed order = %v, want [job-1 job-2]", got)
	}
}

func TestPumpDeployJobsBoundsConcurrency(t *testing.T) {
	store := &sequenceQueueStore{jobs: []queue.Job{
		{ID: "job-a", Queue: queue.Deploy, LeaseID: "l-a", PartitionKey: "svc-a"},
		{ID: "job-b", Queue: queue.Deploy, LeaseID: "l-b", PartitionKey: "svc-b"},
	}}
	executor := newGateExecutor()
	pool := newDeployJobPool(1)

	started, err := pumpDeployJobs(context.Background(), pool, store, "w", time.Minute, time.Now(), executor, nil, nil)
	if err != nil || started != 1 {
		t.Fatalf("pumpDeployJobs() = %d, %v; want 1 (pool full), nil", started, err)
	}
	waitStart(t, executor)
	expectNoStart(t, executor)
	executor.let("job-a")
	pool.wait()
	// Next tick drains the remaining job.
	if started, err := pumpDeployJobs(context.Background(), pool, store, "w", time.Minute, time.Now(), executor, nil, nil); err != nil || started != 1 {
		t.Fatalf("second pumpDeployJobs() = %d, %v; want 1, nil", started, err)
	}
	waitStart(t, executor)
	executor.let("job-b")
	pool.wait()
}

func TestProcessLeasedDeployJobSerializedAbortsWaitOnCancel(t *testing.T) {
	store := &workerQueueStore{}
	job := queue.Job{ID: "job-1", Queue: queue.Deploy, LeaseID: "l-1"}
	pool := newDeployJobPool(2)
	holder := pool.enqueueKey("svc") // a sibling holds the key
	waiter := pool.enqueueKey("svc")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := processLeasedDeployJobSerialized(ctx, store, job, time.Minute, fakeDeployExecutor{}, nil, waiter); err == nil {
		t.Fatal("expected context error while waiting for the service lock")
	}
	if store.completedJobID != "" || store.failedJobID != "" {
		t.Fatalf("store = %#v, want no completion or failure while lock was never acquired", store)
	}
	holder.release()
	// The abandoned waiter must not keep the key held.
	if next := pool.enqueueKey("svc"); !next.granted {
		t.Fatal("key leaked after abandoned waiter")
	}
}

func TestDeployJobSerializationKey(t *testing.T) {
	if got := deployJobSerializationKey(queue.Job{PartitionKey: "prj_1"}); got != "partition:prj_1" {
		t.Fatalf("partition fallback = %q", got)
	}
	if got := deployJobSerializationKey(queue.Job{}); got != "" {
		t.Fatalf("empty job key = %q, want empty", got)
	}
	payload := []byte(`{"schema_version":"midnight.dev/deploy-job/v1alpha1","operation_id":"op_1","service_id":"svc_9","project_id":"prj_1","request":{"deployment_id":"dep_1","service_id":"svc_9","project_id":"prj_1","environment_id":"env_1","image":"redis:7.2.5@sha256:3aaec283e6e593bde528077d60280ac1589887067a39273348860837c9346d7e","idempotency_key":"op_1"}}`)
	got := deployJobSerializationKey(queue.Job{Payload: payload, PartitionKey: "prj_1"})
	if got != "service:svc_9" && got != "partition:prj_1" {
		t.Fatalf("key = %q, want service or partition key", got)
	}
}
