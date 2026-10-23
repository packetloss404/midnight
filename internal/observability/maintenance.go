// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package observability

import (
	"context"
	"time"
)

// RunRetention applies the retention boundary at boot and daily thereafter.
func RunRetention(ctx context.Context, store Store, retention time.Duration, report func(error)) {
	if store == nil {
		return
	}
	if retention <= 0 {
		retention = DefaultRetention
	}
	if report == nil {
		report = func(error) {}
	}
	cleanup := func() {
		if _, err := store.Cleanup(ctx, time.Now().UTC().Add(-retention)); err != nil && ctx.Err() == nil {
			report(err)
		}
	}
	cleanup()
	ticker := time.NewTicker(24 * time.Hour)
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
