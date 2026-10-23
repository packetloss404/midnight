// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package webhook

import (
	"context"
	"errors"
	"time"
)

const defaultWorkerPoll = 250 * time.Millisecond

// Worker durably plans accepted deliveries and executes immutable targets.
// Store lease tokens fence every state mutation; downstream staged intake
// remains independently idempotent if a process dies after committing work.
type Worker struct {
	Store      DurableInbox
	Planner    Planner
	Dispatcher TargetDispatcher
	Poll       time.Duration
	OnError    func(error)
}

func (w *Worker) Run(ctx context.Context) {
	poll := w.Poll
	if poll <= 0 {
		poll = defaultWorkerPoll
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		err := w.ProcessOne(ctx)
		switch {
		case err == nil:
			timer.Reset(0)
		case errors.Is(err, ErrInboxNoWork):
			timer.Reset(poll)
		default:
			if w.OnError != nil {
				w.OnError(err)
			}
			timer.Reset(poll)
		}
	}
}

func (w *Worker) ProcessOne(ctx context.Context) error {
	if w == nil || w.Store == nil || w.Planner == nil || w.Dispatcher == nil {
		return errors.New("webhook worker: store, planner, and dispatcher are required")
	}
	delivery, err := w.Store.ClaimDelivery(ctx)
	if err != nil {
		return err
	}
	renewer, _ := w.Store.(InboxLeaseRenewer)
	if renewer == nil {
		return w.processClaimedDelivery(ctx, delivery, nil)
	}
	return runWithLeaseHeartbeat(
		ctx,
		renewer.LeaseHeartbeatInterval(),
		func(renewCtx context.Context) error {
			return renewer.RenewDeliveryLease(renewCtx, delivery)
		},
		func(workCtx context.Context) error {
			return w.processClaimedDelivery(workCtx, delivery, renewer)
		},
	)
}

func (w *Worker) processClaimedDelivery(
	ctx context.Context,
	delivery ClaimedDelivery,
	renewer InboxLeaseRenewer,
) error {
	if delivery.RoutingCompletedAt == nil {
		plan, planErr := w.Planner.Plan(ctx, delivery.ReceivedDelivery())
		if planErr != nil {
			if errors.Is(planErr, ErrInboxLeaseLost) {
				return planErr
			}
			var permanent *PermanentError
			if errors.As(planErr, &permanent) {
				code := permanent.Code
				if code == "" {
					code = "WEBHOOK_PAYLOAD_INVALID"
				}
				return w.Store.DeadLetterDelivery(ctx, delivery, code, permanent)
			}
			return w.Store.RetryDelivery(ctx, delivery, "WEBHOOK_ROUTE_FAILED", planErr)
		}
		if plan.IgnoredReason != "" || len(plan.Targets) == 0 {
			reason := plan.IgnoredReason
			if reason == "" {
				reason = "no_targets"
			}
			return w.Store.IgnoreDelivery(ctx, delivery, reason)
		}
		if err := w.Store.StorePlan(ctx, delivery, plan); err != nil {
			if errors.Is(err, ErrInboxLeaseLost) {
				return err
			}
			return w.Store.RetryDelivery(ctx, delivery, "WEBHOOK_PLAN_STORE_FAILED", err)
		}
		now := time.Now().UTC()
		delivery.RoutingCompletedAt = &now
	}

	for {
		target, err := w.Store.ClaimTarget(ctx, delivery)
		if errors.Is(err, ErrInboxNoWork) {
			return w.Store.AggregateDelivery(ctx, delivery)
		}
		if errors.Is(err, ErrInboxPayloadCorrupt) {
			return w.Store.AggregateDelivery(ctx, delivery)
		}
		if err != nil {
			return err
		}
		var result TargetResult
		dispatch := func(dispatchCtx context.Context) error {
			var dispatchErr error
			result, dispatchErr = w.Dispatcher.DispatchTarget(
				dispatchCtx,
				delivery.ReceivedDelivery(),
				target.DispatchSnapshot(),
			)
			return dispatchErr
		}
		var dispatchErr error
		if renewer == nil {
			dispatchErr = dispatch(ctx)
		} else {
			dispatchErr = runWithLeaseHeartbeat(
				ctx,
				renewer.LeaseHeartbeatInterval(),
				func(renewCtx context.Context) error {
					return renewer.RenewTargetLease(renewCtx, delivery, target)
				},
				dispatch,
			)
		}
		if dispatchErr == nil {
			if err := w.Store.CompleteTarget(ctx, delivery, target, result); err != nil {
				return err
			}
			continue
		}
		if errors.Is(dispatchErr, ErrInboxLeaseLost) {
			return dispatchErr
		}
		var ignored *IgnoredTargetError
		if errors.As(dispatchErr, &ignored) {
			code := ignored.Code
			if code == "" {
				code = "WEBHOOK_TARGET_AUTHORITY_CHANGED"
			}
			if err := w.Store.IgnoreTarget(ctx, delivery, target, code); err != nil {
				return err
			}
			continue
		}
		var permanent *PermanentError
		if errors.As(dispatchErr, &permanent) {
			code := permanent.Code
			if code == "" {
				code = "WEBHOOK_TARGET_INVALID"
			}
			if err := w.Store.DeadLetterTarget(ctx, delivery, target, code, permanent); err != nil {
				return err
			}
			continue
		}
		if err := w.Store.RetryTarget(ctx, delivery, target, "WEBHOOK_TARGET_DISPATCH_FAILED", dispatchErr); err != nil {
			return err
		}
	}
}

func runWithLeaseHeartbeat(
	ctx context.Context,
	interval time.Duration,
	renew func(context.Context) error,
	work func(context.Context) error,
) error {
	if interval <= 0 {
		interval = DefaultInboxLease / 3
	}
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	stop := make(chan struct{})
	renewalDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				renewalDone <- nil
				return
			case <-workCtx.Done():
				renewalDone <- nil
				return
			case <-ticker.C:
				if err := renew(workCtx); err != nil {
					select {
					case <-stop:
						renewalDone <- nil
					default:
						cancelWork()
						renewalDone <- err
					}
					return
				}
			}
		}
	}()
	workErr := work(workCtx)
	close(stop)
	cancelWork()
	if renewalErr := <-renewalDone; renewalErr != nil {
		return renewalErr
	}
	return workErr
}
