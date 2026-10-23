// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package deploy

import (
	"testing"
	"time"

	"github.com/midnight/midnight/internal/scheduler"
)

func TestCronDueWindowsWithRecoverySkipsExpiredOutageWindow(t *testing.T) {
	now := time.Date(2026, 7, 27, 18, 30, 0, 0, time.UTC)
	cursor := cronCursor{
		Schedule:      "*/5 * * * *",
		Timeout:       time.Minute,
		MaxAttempts:   1,
		LastEvaluated: now.Add(-scheduler.MaxCronDueWindow - time.Minute),
	}

	due, skipped, err := cronDueWindowsWithRecovery(cursor, now)
	if err != nil {
		t.Fatalf("cronDueWindowsWithRecovery() error = %v", err)
	}
	if !skipped || len(due) != 0 {
		t.Fatalf("cronDueWindowsWithRecovery() = (%v, skipped=%v), want durable skip", due, skipped)
	}

	cursor.LastEvaluated = now.Add(-10 * time.Minute)
	due, skipped, err = cronDueWindowsWithRecovery(cursor, now)
	if err != nil {
		t.Fatalf("bounded cronDueWindowsWithRecovery() error = %v", err)
	}
	if skipped || len(due) != 2 {
		t.Fatalf("bounded cronDueWindowsWithRecovery() = (%v, skipped=%v), want two due windows", due, skipped)
	}
}

func TestCronDueWindowsWithRecoveryDoesNotHideInvalidSchedule(t *testing.T) {
	now := time.Date(2026, 7, 27, 18, 30, 0, 0, time.UTC)
	_, skipped, err := cronDueWindowsWithRecovery(cronCursor{
		Schedule:      "not a cron schedule",
		Timeout:       time.Minute,
		MaxAttempts:   1,
		LastEvaluated: now.Add(-time.Minute),
	}, now)
	if err == nil || skipped {
		t.Fatalf("cronDueWindowsWithRecovery() = (skipped=%v, err=%v), want validation error", skipped, err)
	}
}
