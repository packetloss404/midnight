// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package auth (agent) implements the authorization, project-scope,
// mutation-gate, budget, audit, and kill-switch primitives that the
// agent chat backend and the MCP server consume.
//
// Track boundary (see midnight-platform-plan.md "agent authorization,
// project scoping, and cost/safety guardrails"):
//
//   - This package owns the *middleware* and *state*. It does NOT own
//     the chat REST/SSE surface (internal/api/agent.go) or the MCP
//     wire format (internal/agent/mcp/). Both of those tracks consume
//     the interfaces declared here.
//   - v1 is single-tenant: any caller holding the operator bearer can
//     see every project. The interfaces are structured so that
//     per-project ACLs slot in without a wire change when teams land.
//   - Mutating tool calls are gated twice: once by an operator-level
//     environment switch (MIDNIGHT_AGENT_ALLOW_MUTATIONS) and once by
//     the per-message allow_mutations flag the user toggles. Both
//     must say yes.
//   - Every tool call (in-product or via MCP) writes an audit row
//     through the existing tamper-evident audit.AuditChain seam.
//     Plaintext secret values are redacted via a secrets.Vault-driven
//     redactor before any text enters the audit row.
//   - Operators can revoke any active conversation. The chat-backend
//     stream loop polls IsRevoked between agent turns and closes with
//     a "revoked" reason if the flag flips.
package auth
