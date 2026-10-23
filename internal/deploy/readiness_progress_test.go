// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package deploy

import (
	"testing"
	"time"
)

func TestReadinessProgressReporterThrottles(t *testing.T) {
	now := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	var reports []string
	r := newReadinessProgressReporter(clock, func(reason string) { reports = append(reports, reason) })

	r.observe("starting")
	now = now.Add(5 * time.Second)
	r.observe("starting")
	if len(reports) != 0 {
		t.Fatalf("early observations must not report, got %v", reports)
	}
	now = now.Add(readinessProgressInterval)
	r.observe("starting")
	if len(reports) != 1 || reports[0] != "starting" {
		t.Fatalf("expected first report after interval, got %v", reports)
	}
	now = now.Add(time.Second)
	r.observe("starting")
	if len(reports) != 1 {
		t.Fatalf("same reason inside interval must not re-report, got %v", reports)
	}
	r.observe("route probe failed")
	if len(reports) != 2 || reports[1] != "route probe failed" {
		t.Fatalf("new reason should report immediately, got %v", reports)
	}
}
