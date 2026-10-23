// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package webhook

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type plannerFunc func(context.Context, ReceivedDelivery) (DispatchPlan, error)

func (f plannerFunc) Plan(ctx context.Context, delivery ReceivedDelivery) (DispatchPlan, error) {
	return f(ctx, delivery)
}

type targetDispatcherFunc func(context.Context, ReceivedDelivery, DispatchTarget) (TargetResult, error)

func (f targetDispatcherFunc) DispatchTarget(
	ctx context.Context,
	delivery ReceivedDelivery,
	target DispatchTarget,
) (TargetResult, error) {
	return f(ctx, delivery, target)
}

type heartbeatWorkerStore struct {
	DurableInbox

	mu                    sync.Mutex
	delivery              ClaimedDelivery
	target                ClaimedTarget
	targetClaimed         bool
	deliveryRenewals      int
	targetRenewals        int
	deliveryRenewed       chan struct{}
	targetRenewed         chan struct{}
	deliveryRenewedOnce   sync.Once
	targetRenewedOnce     sync.Once
	ignored               bool
	completed             bool
	aggregated            bool
	completedTargetResult TargetResult
}

func (s *heartbeatWorkerStore) ClaimDelivery(context.Context) (ClaimedDelivery, error) {
	return s.delivery, nil
}

func (s *heartbeatWorkerStore) ClaimTarget(context.Context, ClaimedDelivery) (ClaimedTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.targetClaimed {
		return ClaimedTarget{}, ErrInboxNoWork
	}
	s.targetClaimed = true
	return s.target, nil
}

func (s *heartbeatWorkerStore) IgnoreDelivery(
	context.Context,
	ClaimedDelivery,
	string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ignored = true
	return nil
}

func (s *heartbeatWorkerStore) CompleteTarget(
	_ context.Context,
	_ ClaimedDelivery,
	_ ClaimedTarget,
	result TargetResult,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = true
	s.completedTargetResult = result
	return nil
}

func (s *heartbeatWorkerStore) AggregateDelivery(context.Context, ClaimedDelivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aggregated = true
	return nil
}

func (s *heartbeatWorkerStore) LeaseHeartbeatInterval() time.Duration {
	return 2 * time.Millisecond
}

func (s *heartbeatWorkerStore) RenewDeliveryLease(context.Context, ClaimedDelivery) error {
	s.mu.Lock()
	s.deliveryRenewals++
	renewals := s.deliveryRenewals
	s.mu.Unlock()
	if renewals >= 2 && s.deliveryRenewed != nil {
		s.deliveryRenewedOnce.Do(func() { close(s.deliveryRenewed) })
	}
	return nil
}

func (s *heartbeatWorkerStore) RenewTargetLease(
	context.Context,
	ClaimedDelivery,
	ClaimedTarget,
) error {
	s.mu.Lock()
	s.targetRenewals++
	s.mu.Unlock()
	if s.targetRenewed != nil {
		s.targetRenewedOnce.Do(func() { close(s.targetRenewed) })
	}
	return nil
}

func TestWorkerRenewsDeliveryLeaseDuringPlanning(t *testing.T) {
	store := &heartbeatWorkerStore{
		delivery: ClaimedDelivery{
			DeliverySummary: DeliverySummary{
				ID:         "delivery-plan",
				Provider:   ProviderGitHub,
				ReceivedAt: time.Now().UTC(),
			},
			LeaseToken: "delivery-lease",
		},
		deliveryRenewed: make(chan struct{}),
	}
	worker := Worker{
		Store: store,
		Planner: plannerFunc(func(ctx context.Context, _ ReceivedDelivery) (DispatchPlan, error) {
			select {
			case <-store.deliveryRenewed:
				return DispatchPlan{IgnoredReason: "no_targets"}, nil
			case <-ctx.Done():
				return DispatchPlan{}, ctx.Err()
			}
		}),
		Dispatcher: targetDispatcherFunc(func(context.Context, ReceivedDelivery, DispatchTarget) (TargetResult, error) {
			t.Fatal("dispatcher called for ignored delivery")
			return TargetResult{}, nil
		}),
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatalf("process delivery: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.deliveryRenewals < 2 || !store.ignored {
		t.Fatalf("renewals=%d ignored=%v", store.deliveryRenewals, store.ignored)
	}
}

func TestWorkerRenewsTargetLeaseAndPreservesCompleteSnapshot(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	routingComplete := now
	target := ClaimedTarget{
		TargetSummary: TargetSummary{
			ID:                               "target-1",
			TargetKey:                        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			WorkspaceID:                      "wrk_1",
			ProjectID:                        "prj_1",
			ServiceID:                        "svc_1",
			InstallationID:                   41,
			InstallationGrantResourceVersion: 2,
			RepositoryID:                     42,
			Repository:                       "acme/api",
			RepositoryGrantResourceVersion:   3,
			LinkResourceVersion:              4,
			Branch:                           "main",
			CommitSHA:                        "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		},
		LeaseToken:                 "target-lease",
		Payload:                    []byte(`{"intent":"deploy"}`),
		InstallationGrantCreatedAt: now.Add(-8 * time.Hour),
		InstallationGrantUpdatedAt: now.Add(-7 * time.Hour),
		RepositoryGrantCreatedAt:   now.Add(-6 * time.Hour),
		RepositoryGrantUpdatedAt:   now.Add(-5 * time.Hour),
		RepositoryVerifiedAt:       now.Add(-5 * time.Hour),
		LinkCreatedAt:              now.Add(-4 * time.Hour),
		LinkUpdatedAt:              now.Add(-3 * time.Hour),
		LinkRepositoryVerifiedAt:   now.Add(-4 * time.Hour),
		Ref:                        "refs/heads/main",
		CloneURL:                   "https://github.com/acme/api.git",
		RootDirectory:              "services/api",
		ConfigPath:                 "midnight.json",
		TriggerReason:              "matched_watch_path",
	}
	store := &heartbeatWorkerStore{
		delivery: ClaimedDelivery{
			DeliverySummary: DeliverySummary{
				ID:                 "delivery-target",
				Provider:           ProviderGitHub,
				ReceivedAt:         now,
				RoutingCompletedAt: &routingComplete,
			},
			LeaseToken: "delivery-lease",
		},
		target:        target,
		targetRenewed: make(chan struct{}),
	}
	wantSnapshot := target.DispatchSnapshot()
	worker := Worker{
		Store: store,
		Planner: plannerFunc(func(context.Context, ReceivedDelivery) (DispatchPlan, error) {
			t.Fatal("planner called for persisted plan")
			return DispatchPlan{}, nil
		}),
		Dispatcher: targetDispatcherFunc(func(ctx context.Context, _ ReceivedDelivery, got DispatchTarget) (TargetResult, error) {
			select {
			case <-store.targetRenewed:
			case <-ctx.Done():
				return TargetResult{}, ctx.Err()
			}
			if !reflect.DeepEqual(got, wantSnapshot) {
				t.Fatalf("dispatch snapshot = %#v, want %#v", got, wantSnapshot)
			}
			return TargetResult{
				OperationID:    "op_1",
				DeploymentID:   "dep_1",
				StagedOutboxID: "wox_1",
			}, nil
		}),
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatalf("process target: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.targetRenewals == 0 || !store.completed || !store.aggregated {
		t.Fatalf(
			"target renewals=%d completed=%v aggregated=%v",
			store.targetRenewals,
			store.completed,
			store.aggregated,
		)
	}
	if store.completedTargetResult.DeploymentID != "dep_1" {
		t.Fatalf("completed result = %#v", store.completedTargetResult)
	}
}

func TestLeaseHeartbeatReturnsFencingFailure(t *testing.T) {
	err := runWithLeaseHeartbeat(
		context.Background(),
		time.Millisecond,
		func(context.Context) error {
			return ErrInboxLeaseLost
		},
		func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	)
	if !errors.Is(err, ErrInboxLeaseLost) {
		t.Fatalf("heartbeat error = %v", err)
	}
}
