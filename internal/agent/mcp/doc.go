// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package mcp implements Midnight's Model Context Protocol server.
//
// The server is a streamable-HTTP JSON-RPC 2.0 endpoint that bridges
// the canonical [tools.Registry] (and its 11 platform tools) onto the
// MCP wire so external clients (Claude Code, Claude Desktop, third-
// party agents) can drive the Midnight control plane through the same
// surface the in-product chat backend consumes.
//
// Track boundary:
//
//   - The package owns the MCP wire format, session state, the
//     `/mcp/v1` endpoint, the `/.well-known/mcp` manifest, the
//     per-session rate limiter, and the mutation gate header. It does
//     NOT own the tool implementations (see internal/agent/tools) or
//     the audit chain (see internal/audit).
//   - The mutation gate has two doors:
//     1. The connection-level "AllowMutations" flag (from the
//     X-Midnight-Allow-Mutations request header or the
//     `_meta.allow_mutations` field on `initialize`).
//     2. The registry's own Mutates check inside [Registry.Execute].
//     Both must say yes before a mutating tool runs.
//   - tools/list filters out tools where t.Mutates == true when the
//     session has AllowMutations=false, so the connecting model never
//     even sees them.
//
// Transport: streamable-HTTP per MCP spec. Each HTTP POST carries
// one or more JSON-RPC 2.0 frames; the response is either
// application/json (single frame) or text/event-stream (streamed
// progress, used here for tool-call notifications during long calls).
// A future stdio mode for local clients is left to a follow-up.
package mcp
