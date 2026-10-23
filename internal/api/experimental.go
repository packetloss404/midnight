// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"os"
	"strings"
)

const experimentalEnv = "MIDNIGHT_EXPERIMENTAL"

// experimentalEnabled keeps contract-only and evidence-only surfaces out of
// the normal product API. These routes are useful to contributors, but they
// are not evidence that Midnight executes the capability in production.
func experimentalEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(experimentalEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
