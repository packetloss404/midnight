// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package observability

import (
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestDockerCPUPercentHandlesCounterResetAndOnlineCPUs(t *testing.T) {
	stats := container.StatsResponse{}
	stats.CPUStats.CPUUsage.TotalUsage = 300
	stats.PreCPUStats.CPUUsage.TotalUsage = 100
	stats.CPUStats.SystemUsage = 1000
	stats.PreCPUStats.SystemUsage = 500
	stats.CPUStats.OnlineCPUs = 2
	if got := dockerCPUPercent(stats); got != 80 {
		t.Fatalf("cpu percent = %v, want 80", got)
	}
	stats.CPUStats.CPUUsage.TotalUsage = 50
	if got := dockerCPUPercent(stats); got != 0 {
		t.Fatalf("counter-reset cpu percent = %v, want 0", got)
	}
}

func TestDockerStatsRequestIncludesPreviousSample(t *testing.T) {
	opts := dockerStatsRequestOptions()
	if opts.Stream {
		t.Fatal("Docker stats request unexpectedly enables streaming")
	}
	if !opts.IncludePreviousSample {
		t.Fatal("Docker stats request must include a previous sample for CPU delta calculation")
	}
}

func TestScopeFromDockerLabelsRequiresCanonicalScope(t *testing.T) {
	if _, ok := scopeFromDockerLabels(map[string]string{}); ok {
		t.Fatal("empty labels unexpectedly resolved")
	}
}
