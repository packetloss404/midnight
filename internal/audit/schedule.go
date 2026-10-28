// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package audit

import (
	"context"
	"errors"
	"time"
)

const DefaultCheckpointTimeout = 15 * time.Second
const DefaultCheckpointRetry = time.Minute

type CheckpointSchedule struct {
	Checkpointer  *Checkpointer
	Timeout       time.Duration
	RetryInterval time.Duration
	Now           func() time.Time
	Wait          func(context.Context, time.Duration) error
	Report        func(Checkpoint, bool, error)
}

// Run catches up immediately, then waits for UTC midnight. Empty history and
// transient failures retry on a bounded interval. No missed signatures are
// fabricated and every database/lock wait has its own cancellation deadline.
func (s CheckpointSchedule) Run(ctx context.Context) error {
	if s.Checkpointer == nil {
		return errors.New("audit: checkpointer is required")
	}
	if s.Timeout <= 0 {
		s.Timeout = DefaultCheckpointTimeout
	}
	if s.RetryInterval <= 0 {
		s.RetryInterval = DefaultCheckpointRetry
	}
	if s.Now == nil {
		s.Now = func() time.Time { return time.Now().UTC() }
	}
	if s.Wait == nil {
		s.Wait = waitCheckpointSchedule
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		passCtx, cancel := context.WithTimeout(ctx, s.Timeout)
		cp, created, err := s.Checkpointer.SignDue(passCtx)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if s.Report != nil {
			s.Report(cp, created, err)
		}
		delay := s.RetryInterval
		if err == nil && cp.ID > 0 {
			now := s.Now().UTC()
			y, m, d := now.Date()
			if !cp.EndedAt.Before(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)) {
				delay = time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC).Sub(now)
			}
		}
		if err := s.Wait(ctx, delay); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

func waitCheckpointSchedule(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
