// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package claude

import "strings"

// This file encodes the per-model request-surface differences that would
// otherwise show up as opaque 400s in production. The Anthropic API has
// moved twice on these knobs:
//
//   - `thinking: {type: "enabled", budget_tokens: N}` was replaced by
//     `{type: "adaptive"}` on the 4.6+ family, and budget_tokens is now
//     rejected outright on Opus 4.7 and later.
//   - `output_config.effort` arrived on the 4.6 family, and `xhigh` was
//     added one generation later (Opus 4.7).
//
// Midnight lets an operator point the agent at any model via
// MIDNIGHT_AGENT_CHAT_MODEL, so the client has to degrade a request to
// what the target model actually accepts rather than failing the call.

// supportsAdaptiveThinking reports whether model accepts
// `thinking: {type: "adaptive"}`. Models that do not (Haiku 4.5, the 4.5
// generation and older) still support the deprecated budget_tokens form,
// which Midnight deliberately does not emit — on those models the agent
// simply runs without extended thinking.
func supportsAdaptiveThinking(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(m, "claude-fable-5"),
		strings.HasPrefix(m, "claude-mythos-5"),
		strings.HasPrefix(m, "claude-opus-5"),
		strings.HasPrefix(m, "claude-opus-4-8"),
		strings.HasPrefix(m, "claude-opus-4-7"),
		strings.HasPrefix(m, "claude-opus-4-6"),
		strings.HasPrefix(m, "claude-sonnet-5"),
		strings.HasPrefix(m, "claude-sonnet-4-6"):
		return true
	default:
		return false
	}
}

// supportsEffort reports whether model accepts `output_config.effort`.
func supportsEffort(model string) bool {
	// The effort knob shipped alongside adaptive thinking, on the same
	// model set.
	return supportsAdaptiveThinking(model)
}

// supportsXHighEffort reports whether model accepts the `xhigh` level,
// which was introduced one generation after `effort` itself. On models
// that predate it the caller clamps to `high`.
func supportsXHighEffort(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(m, "claude-opus-4-6") || strings.HasPrefix(m, "claude-sonnet-4-6") {
		return false
	}
	return supportsEffort(model)
}

// thinkingOffRejectsHighEffort reports whether explicitly disabling
// thinking on model is rejected at the top two effort levels. On Opus 5
// `{type: "disabled"}` is accepted only at effort `high` or below.
func thinkingOffRejectsHighEffort(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "claude-opus-5")
}

// maxOutputTokens is the per-model output ceiling. Requests above it are
// clamped rather than rejected.
func maxOutputTokens(model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(m, "claude-haiku-4-5"):
		return 64_000
	case supportsAdaptiveThinking(m):
		return 128_000
	default:
		// Conservative floor for models this table does not know about.
		return 64_000
	}
}
