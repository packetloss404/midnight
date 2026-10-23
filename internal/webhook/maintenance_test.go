// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package webhook

import (
	"context"
	"errors"
	"testing"
	"time"
)

type payloadPurgerFunc func(
	context.Context,
	time.Time,
	int,
) (PayloadPurgeResult, error)

func (f payloadPurgerFunc) PurgeTerminalPayloads(
	ctx context.Context,
	before time.Time,
	limit int,
) (PayloadPurgeResult, error) {
	return f(ctx, before, limit)
}

func TestRunPayloadRetentionRunsAtBootWithBoundedBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	var cutoff time.Time
	RunPayloadRetention(
		ctx,
		payloadPurgerFunc(func(
			_ context.Context,
			before time.Time,
			limit int,
		) (PayloadPurgeResult, error) {
			calls++
			cutoff = before
			if limit != DefaultPayloadPurgeBatch {
				t.Fatalf("limit = %d", limit)
			}
			cancel()
			return PayloadPurgeResult{}, nil
		}),
		time.Hour,
		nil,
	)
	if calls != 1 {
		t.Fatalf("purge calls = %d", calls)
	}
	age := time.Since(cutoff)
	if age < time.Hour || age > time.Hour+time.Minute {
		t.Fatalf("cutoff age = %s", age)
	}
}

func TestRunPayloadRetentionReportsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	want := errors.New("purge failed")
	var reported error
	RunPayloadRetention(
		ctx,
		payloadPurgerFunc(func(
			context.Context,
			time.Time,
			int,
		) (PayloadPurgeResult, error) {
			return PayloadPurgeResult{}, want
		}),
		DefaultTerminalPayloadRetention,
		func(err error) {
			reported = err
			cancel()
		},
	)
	if !errors.Is(reported, want) {
		t.Fatalf("reported = %v", reported)
	}
}
