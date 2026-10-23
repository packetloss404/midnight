// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package backup

import (
	"testing"
	"time"
)

func TestNextPolicyTimeUsesUTCCalendarBoundaries(t *testing.T) {
	at := time.Date(2026, 7, 27, 14, 30, 0, 0, time.FixedZone("local", -5*60*60))
	tests := map[string]time.Time{
		"daily":   time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC),
		"weekly":  time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC),
		"monthly": time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}
	for schedule, want := range tests {
		if got := nextPolicyTime(schedule, at); !got.Equal(want) {
			t.Errorf("%s next = %s, want %s", schedule, got, want)
		}
	}
}
