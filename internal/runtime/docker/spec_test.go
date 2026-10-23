// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package docker

import (
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestHardenedHostConfigKeepsScheduledApplicationsRunning(t *testing.T) {
	hc := HardenedHostConfig()
	if hc.RestartPolicy.Name != container.RestartPolicyUnlessStopped {
		t.Fatalf("restart policy = %q, want %q", hc.RestartPolicy.Name, container.RestartPolicyUnlessStopped)
	}
	if hc.RestartPolicy.MaximumRetryCount != 0 {
		t.Fatalf("restart maximum retries = %d, want unlimited", hc.RestartPolicy.MaximumRetryCount)
	}
}
