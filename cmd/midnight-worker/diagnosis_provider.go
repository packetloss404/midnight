// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"errors"
	"os"
	"strings"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/claude"
	"github.com/midnight/midnight/internal/agent/diagnosis"
	agentopenai "github.com/midnight/midnight/internal/agent/openai"
	"github.com/midnight/midnight/internal/agent/tools"
)

// The worker must select the same provider as chat. In particular, passing an
// empty MiniMax key to the Anthropic-compatible adapter would make it fall
// back to ANTHROPIC_API_KEY and send that credential to the wrong provider.
func newWorkerDiagnosisCaller(registry *tools.Registry) (agent.Caller, string, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("MIDNIGHT_AGENT_PROVIDER")))
	if provider == "" || provider == "claude" {
		provider = "anthropic"
	}
	model := strings.TrimSpace(os.Getenv(diagnosis.EnvModel))
	var key string
	switch provider {
	case "anthropic":
		key = strings.TrimSpace(os.Getenv(claude.EnvAPIKey))
		if model == "" {
			model = diagnosis.DefaultModel
		}
	case "openai":
		key = strings.TrimSpace(os.Getenv(agentopenai.EnvAPIKey))
		if model == "" {
			model = envFirst(agentopenai.DefaultChatModel, agentopenai.EnvChatModel)
		}
	case "minimax":
		key = strings.TrimSpace(os.Getenv("MINIMAX_API_KEY"))
		if model == "" {
			model = envFirst("MiniMax-M2.7", "MIDNIGHT_MINIMAX_CHAT_MODEL")
		}
	default:
		return nil, "", errors.New("diagnosis: unsupported provider (valid: anthropic, openai, minimax)")
	}
	if key == "" {
		return nil, model, errors.New("diagnosis: selected provider credential is not configured")
	}
	if provider == "openai" {
		caller, err := agentopenai.NewCaller(agentopenai.Options{
			APIKey: key, BaseURL: os.Getenv(agentopenai.EnvBaseURL), Registry: registry, DefaultModel: model,
		})
		return caller, model, err
	}
	opts := claude.Options{APIKey: key, BaseURL: os.Getenv("ANTHROPIC_BASE_URL"), Registry: registry, DefaultModel: model} // not-a-failure-code
	if provider == "minimax" {
		opts.BaseURL = envFirst("https://api.minimax.io/anthropic", "MIDNIGHT_MINIMAX_BASE_URL")
	}
	caller, err := claude.NewCaller(opts)
	return caller, model, err
}
