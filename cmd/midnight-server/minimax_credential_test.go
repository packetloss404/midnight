// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"testing"

	"github.com/midnight/midnight/internal/agent/tools"
)

func TestMiniMaxNeverFallsBackToAnthropicCredential(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "must-not-be-forwarded")
	t.Setenv("MINIMAX_API_KEY", "")
	if caller, err := newAgentCaller(providerMiniMax, tools.New()); err == nil || caller != nil {
		t.Fatal("MiniMax without its own credential accepted another provider's key")
	}
}
