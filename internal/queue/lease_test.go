// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package queue

import (
	"strings"
	"testing"
	"time"
)

func TestNewLeaseValidatesInputs(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	if _, err := NewLease("", "worker", Build, time.Minute, now); err == nil {
		t.Fatal("expected error for empty job id")
	}
	if _, err := NewLease("job_1", "worker", Build, 0, now); err == nil {
		t.Fatal("expected error for zero TTL")
	}
}

func TestLeaseClassifyTransitions(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	lease, err := NewLease("job_1", "worker", Build, 30*time.Second, now)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		offset time.Duration
		want   LeaseLifecycleState
	}{
		{name: "fresh", offset: 1 * time.Second, want: LeaseStateAcquired},
		{name: "due", offset: 11 * time.Second, want: LeaseStateHeartbeatDue},
		{name: "at risk", offset: 22 * time.Second, want: LeaseStateAtRisk},
		{name: "expired", offset: 31 * time.Second, want: LeaseStateExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := lease.Classify(now.Add(tc.offset))
			if got != tc.want {
				t.Fatalf("Classify(%s) = %s, want %s", tc.offset, got, tc.want)
			}
		})
	}
}

func TestLeaseRenewExtendsExpiry(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	lease, err := NewLease("job_1", "worker", Build, 30*time.Second, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Renew(now.Add(10 * time.Second)); err != nil {
		t.Fatalf("Renew() = %v", err)
	}
	if lease.LeaseExpiresAt != now.Add(40*time.Second) {
		t.Fatalf("expiry = %s, want %s", lease.LeaseExpiresAt, now.Add(40*time.Second))
	}
	if err := lease.Renew(now.Add(31 * time.Second)); err == nil {
		t.Fatal("expected expired-lease renewal to fail")
	}
}

func TestNewLeaseIDAddsPerAttemptToken(t *testing.T) {
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	first := NewLeaseID("worker-a", now)
	second := NewLeaseID("worker-a", now)
	if first == second {
		t.Fatalf("lease ids matched: %q", first)
	}
	if !strings.HasPrefix(first, "worker-a/") || !strings.HasPrefix(second, "worker-a/") {
		t.Fatalf("lease ids = %q/%q, want worker prefix for diagnostics", first, second)
	}
}

func TestPlanReclaimRequeuesWithExponentialBackoff(t *testing.T) {
	cases := []struct {
		name        string
		attempt     int
		maxAttempts int
		wantState   JobState
		wantBackoff time.Duration
	}{
		{name: "pre-lease helper retry", attempt: 0, maxAttempts: 3, wantState: JobQueued, wantBackoff: 30 * time.Second},
		{name: "first leased retry", attempt: 1, maxAttempts: 3, wantState: JobQueued, wantBackoff: 30 * time.Second},
		{name: "second leased retry", attempt: 2, maxAttempts: 3, wantState: JobQueued, wantBackoff: 60 * time.Second},
		{name: "later retry caps at one hour", attempt: 9, maxAttempts: 10, wantState: JobQueued, wantBackoff: time.Hour},
		{name: "dead letter", attempt: 5, maxAttempts: 5, wantState: JobFailed, wantBackoff: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := PlanReclaim(tc.attempt, tc.maxAttempts)
			if decision.NextState != tc.wantState {
				t.Fatalf("state = %q, want %q", decision.NextState, tc.wantState)
			}
			if decision.BackoffFor != tc.wantBackoff {
				t.Fatalf("backoff = %s, want %s", decision.BackoffFor, tc.wantBackoff)
			}
		})
	}
}

func TestPoolBudgetReturnsCanonicalValues(t *testing.T) {
	if pool, ttl, err := PoolBudget(Build); err != nil || pool != 4 || ttl != 5*time.Minute {
		t.Fatalf("Build = (%d, %s, %v), want (4, 5m, nil)", pool, ttl, err)
	}
	if pool, ttl, err := PoolBudget(Webhook); err != nil || pool != 8 || ttl != 30*time.Second {
		t.Fatalf("Webhook = (%d, %s, %v), want (8, 30s, nil)", pool, ttl, err)
	}
	if _, _, err := PoolBudget("bogus"); err == nil {
		t.Fatal("expected unknown queue to fail")
	}
}
