// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package webhook

import (
	"context"
	"time"
)

const (
	defaultPayloadPurgeInterval = 24 * time.Hour
	maxPayloadPurgeBatches      = 100
)

// RunPayloadRetention applies the terminal payload-retention boundary at boot
// and daily thereafter. Metadata, hashes, outcomes, and audit evidence remain.
func RunPayloadRetention(
	ctx context.Context,
	purger TerminalPayloadPurger,
	retention time.Duration,
	report func(error),
) {
	if purger == nil {
		return
	}
	if retention <= 0 {
		retention = DefaultTerminalPayloadRetention
	}
	if report == nil {
		report = func(error) {}
	}
	cleanup := func() {
		cutoff := time.Now().UTC().Add(-retention)
		for batch := 0; batch < maxPayloadPurgeBatches; batch++ {
			result, err := purger.PurgeTerminalPayloads(
				ctx,
				cutoff,
				DefaultPayloadPurgeBatch,
			)
			if err != nil {
				if ctx.Err() == nil {
					report(err)
				}
				return
			}
			if result.Deliveries < DefaultPayloadPurgeBatch &&
				result.Targets < DefaultPayloadPurgeBatch {
				return
			}
		}
	}
	cleanup()
	ticker := time.NewTicker(defaultPayloadPurgeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}
