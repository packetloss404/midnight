// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package mcp

import (
	"encoding/json"
	"net/http"
)

// Manifest is the JSON payload served from /.well-known/mcp. The shape
// matches the spec's well-known-document contract so external clients
// can discover the server's transports, auth schemes, and mutation
// gate without speaking the JSON-RPC protocol first.
type Manifest struct {
	Name             string               `json:"name"`
	Description      string               `json:"description"`
	Version          string               `json:"version"`
	ProtocolVersions []string             `json:"protocol_versions"`
	Transports       []ManifestTransport  `json:"transports"`
	Auth             ManifestAuth         `json:"auth"`
	Capabilities     ManifestCapabilities `json:"capabilities"`
	MutationGate     ManifestMutationGate `json:"mutation_gate"`
	RateLimits       ManifestRateLimits   `json:"rate_limits"`
	Docs             string               `json:"docs,omitempty"`
}

// ManifestTransport describes one supported transport endpoint.
type ManifestTransport struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

// ManifestAuth describes the auth surface.
type ManifestAuth struct {
	Schemes            []string `json:"schemes"`
	Header             string   `json:"header"`
	ProjectScopeHeader string   `json:"project_scope_header"`
}

// ManifestCapabilities advertises which surface methods are supported.
type ManifestCapabilities struct {
	Tools     bool `json:"tools"`
	Resources bool `json:"resources"`
	Prompts   bool `json:"prompts"`
}

// ManifestMutationGate documents how clients toggle the write surface.
type ManifestMutationGate struct {
	Default          bool   `json:"default"`
	MetadataKey      string `json:"metadata_key"`
	HeaderKey        string `json:"header_key"`
	RefusalErrorCode int    `json:"refusal_error_code"`
}

// ManifestRateLimits documents the rate-limit caps.
type ManifestRateLimits struct {
	ToolCallsPerMinute int `json:"tool_calls_per_minute"`
}

// NewManifestHandler returns the /.well-known/mcp http.Handler. The
// handler is unauthenticated — discovery has to be reachable from
// clients that don't yet hold a token. The MCP wire surface itself
// (mounted separately on /mcp/v1) enforces bearer auth.
func NewManifestHandler(opts Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		endpoint := opts.Endpoint
		if endpoint == "" {
			endpoint = "/mcp/v1"
		}
		rateLimit := opts.RateLimitPerMinute
		if rateLimit <= 0 {
			rateLimit = resolveRateLimit()
		}
		m := Manifest{
			Name:             "midnight-mcp",
			Description:      "Midnight MCP server — JSON-RPC 2.0 streamable-HTTP bridge onto the Midnight control plane tool registry.",
			Version:          serverVersion,
			ProtocolVersions: []string{protocolVersion},
			Transports: []ManifestTransport{
				{Kind: "http", URL: endpoint},
			},
			Auth: ManifestAuth{
				Schemes:            []string{"Bearer"},
				Header:             "Authorization",
				ProjectScopeHeader: "Project-Access-Token",
			},
			Capabilities: ManifestCapabilities{
				Tools:     true,
				Resources: opts.Resources != nil,
				Prompts:   true,
			},
			MutationGate: ManifestMutationGate{
				Default:          false,
				MetadataKey:      "allow_mutations",
				HeaderKey:        "X-Midnight-Allow-Mutations",
				RefusalErrorCode: rpcErrMutationDisallowed,
			},
			RateLimits: ManifestRateLimits{
				ToolCallsPerMinute: rateLimit,
			},
			Docs: opts.DocsURL,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(m)
	})
}
