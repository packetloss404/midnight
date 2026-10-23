// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/queue"
)

const defaultWorkerDeployConcurrency = 4

// deployJobPool bounds how many leased deploy jobs execute at once and
// serializes jobs that target the same service. Before it existed the worker
// loop executed one job inline, so a single rollout stuck in its readiness
// wait (up to the service health timeout, 5 minutes by default) blocked every
// other service's deploy, lease reclamation, and monitor evaluation.
//
// Ordering guarantee: jobs sharing a serialization key run one at a time in
// lease order. Jobs for different services run concurrently up to the slot
// count. The lease heartbeat keeps running while a job waits for its key so
// the queue never reclaims a job that is merely queued behind a sibling.
type deployJobPool struct {
	slots chan struct{}
	wg    sync.WaitGroup
	mu    sync.Mutex
	keys  map[string]*keyQueue
}

// keyQueue is the FIFO of jobs waiting on one serialization key.
type keyQueue struct {
	held    bool
	waiters []*keyTicket
}

// keyTicket is one job's place in a keyQueue. ready is closed when the job
// may run; release hands the key to the next waiter.
type keyTicket struct {
	pool      *deployJobPool
	key       string
	ready     chan struct{}
	granted   bool
	abandoned bool
}

func newDeployJobPool(concurrency int) *deployJobPool {
	if concurrency < 1 {
		concurrency = 1
	}
	return &deployJobPool{
		slots: make(chan struct{}, concurrency),
		keys:  make(map[string]*keyQueue),
	}
}

func (p *deployJobPool) tryAcquireSlot() bool {
	select {
	case p.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (p *deployJobPool) releaseSlot() { <-p.slots }

// enqueueKey registers a job in the FIFO for key and returns its ticket. The
// ticket is granted immediately when nobody holds the key. An empty key means
// the job carries no service identity and is not serialized (nil ticket).
func (p *deployJobPool) enqueueKey(key string) *keyTicket {
	if key == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	q, ok := p.keys[key]
	if !ok {
		q = &keyQueue{}
		p.keys[key] = q
	}
	ticket := &keyTicket{pool: p, key: key, ready: make(chan struct{})}
	if !q.held {
		q.held = true
		ticket.granted = true
		close(ticket.ready)
		return ticket
	}
	q.waiters = append(q.waiters, ticket)
	return ticket
}

// release passes the key to the next waiter, or frees it. Must be called with
// p.mu held.
func (p *deployJobPool) releaseLocked(key string) {
	q, ok := p.keys[key]
	if !ok {
		return
	}
	for len(q.waiters) > 0 {
		next := q.waiters[0]
		q.waiters = q.waiters[1:]
		if next.abandoned {
			continue
		}
		next.granted = true
		close(next.ready)
		return
	}
	delete(p.keys, key)
}

// release is called once by a granted ticket when its job finishes.
func (t *keyTicket) release() {
	if t == nil {
		return
	}
	t.pool.mu.Lock()
	defer t.pool.mu.Unlock()
	if !t.granted {
		return
	}
	t.granted = false
	t.pool.releaseLocked(t.key)
}

// abandon withdraws a ticket whose job gave up waiting. A ticket granted
// concurrently with the abandonment is released so the key is never leaked.
func (t *keyTicket) abandon() {
	if t == nil {
		return
	}
	t.pool.mu.Lock()
	defer t.pool.mu.Unlock()
	t.abandoned = true
	if t.granted {
		t.granted = false
		t.pool.releaseLocked(t.key)
	}
}

// wait blocks until every in-flight job has returned.
func (p *deployJobPool) wait() { p.wg.Wait() }

// deployJobSerializationKey derives the per-service ordering key for a leased
// deploy job. Deploy payloads carry the service id; runtime-control action
// payloads and unknown schemas fall back to the queue partition key.
func deployJobSerializationKey(job queue.Job) string {
	if payload, _, err := deploy.DecodeDeployJobPayload(job.Payload); err == nil && payload.ServiceID != "" {
		return "service:" + payload.ServiceID
	}
	if key := strings.TrimSpace(job.PartitionKey); key != "" {
		return "partition:" + key
	}
	return ""
}

// pumpDeployJobs leases deploy jobs while pool slots are free and executes
// each on its own goroutine. It returns once the queue is empty or the pool is
// full; the caller invokes it on every poll tick. report receives per-job
// errors asynchronously and must be safe for concurrent use.
func pumpDeployJobs(ctx context.Context, pool *deployJobPool, store queue.Store, workerID string, leaseTTL time.Duration, now time.Time, executor deployJobExecutor, deploymentStore api.DeploymentStore, report func(error)) (int, error) {
	if pool == nil {
		return 0, errors.New("worker deploy job: pool is required")
	}
	if store == nil {
		return 0, errors.New("worker deploy job: queue store is required")
	}
	started := 0
	for pool.tryAcquireSlot() {
		job, err := store.Lease(ctx, queue.LeaseRequest{
			Queue:     queue.Deploy,
			WorkerID:  workerID,
			LeaseTime: leaseTTL,
			Now:       now,
		})
		if errors.Is(err, queue.ErrNoWork) {
			pool.releaseSlot()
			return started, nil
		}
		if err != nil {
			pool.releaseSlot()
			return started, err
		}
		started++
		ticket := pool.enqueueKey(deployJobSerializationKey(job))
		pool.wg.Add(1)
		go func(job queue.Job) {
			defer pool.wg.Done()
			defer pool.releaseSlot()
			_, err := processLeasedDeployJobSerialized(ctx, store, job, leaseTTL, executor, deploymentStore, ticket)
			if err != nil && report != nil {
				report(err)
			}
		}(job)
	}
	return started, nil
}
