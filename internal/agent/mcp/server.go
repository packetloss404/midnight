// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/tools"
)

// serverVersion is the advertised version on the well-known manifest
// and the JSON-RPC initialize response.
const serverVersion = "0.1.0"

// protocolVersion is the MCP protocol revision this server speaks.
// Clients that send a different protocolVersion on initialize get the
// server's preferred version echoed back so they can decide whether
// to downgrade.
const protocolVersion = "2025-06-18"

// JSON-RPC error codes per JSON-RPC 2.0 spec, extended with the
// Midnight-specific MUTATION_DISALLOWED code mandated by the MCP
// server spec on this branch.
const (
	rpcErrParse              = -32700
	rpcErrInvalidRequest     = -32600
	rpcErrMethodNotFound     = -32601
	rpcErrInvalidParams      = -32602
	rpcErrInternal           = -32603
	rpcErrMutationDisallowed = -32030 // MIDNIGHT_MUTATION_DISALLOWED
	rpcErrRateLimited        = -32029
	rpcErrUnauthorized       = -32001
)

// Options configures a [Server].
type Options struct {
	// Registry is the canonical tool registry to bridge onto MCP.
	// Required.
	Registry *tools.Registry
	// Env is the agent.Env tools see when executed. The server
	// shallow-copies it per request and overlays the per-session
	// AllowMutations flag; everything else is forwarded verbatim.
	// Required.
	Env agent.Env
	// Audit, when non-nil, replaces Env.Audit before tool execution.
	// Allows the operator to route MCP-driven audit rows through a
	// dedicated sink without disturbing the chat backend's wiring.
	Audit agent.AuditSink
	// APIBearerToken, when non-empty, is the bearer token clients
	// must present. An empty value disables auth (matches the rest
	// of the API surface during local dev).
	APIBearerToken string
	// RateLimitPerMinute is the per-session tools/call cap. <=0
	// falls back to MIDNIGHT_MCP_RATE_LIMIT_PER_MINUTE or
	// DefaultRateLimitPerMinute.
	RateLimitPerMinute int
	// Endpoint is the URL path the server is mounted at; used by
	// the manifest. Defaults to "/mcp/v1".
	Endpoint string
	// DocsURL is the operator-documentation URL surfaced on the
	// manifest's `docs` field.
	DocsURL string
	// Resources is the resources/list and resources/read seam.
	// nil disables the resources capability.
	Resources ResourceProvider
	// Now overrides the wall clock (rate limiter, audit rows).
	// nil falls back to time.Now.
	Now func() time.Time
}

// Server is the MCP wire endpoint. Use [NewServer] to construct one;
// the returned http.Handler is the streamable-HTTP transport.
type Server struct {
	opts      Options
	registry  registryView
	limiter   *rateLimiter
	sessions  *sessionStore
	prompts   *promptCatalog
	resources ResourceProvider
	now       func() time.Time
}

// NewServer constructs the MCP server. The returned handler is safe
// for concurrent use and may be mounted directly on an http.ServeMux.
func NewServer(opts Options) http.Handler {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now() }
	}
	srv := &Server{
		opts:      opts,
		registry:  opts.Registry,
		limiter:   newRateLimiter(opts.RateLimitPerMinute, now),
		sessions:  newSessionStore(),
		prompts:   newPromptCatalog(),
		resources: opts.Resources,
		now:       now,
	}
	return srv
}

// ServeHTTP implements http.Handler. Each POST is either a single
// JSON-RPC 2.0 request or a batch; the response is application/json
// (single frame) so MCP clients that don't support streaming still
// work. The per-call SSE path is not exercised by the current 11-tool
// surface — none of them progress-report — so we keep the response
// simple. A future progress-reporting tool can flip to text/event-
// stream by inspecting the request's Accept header.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Bearer auth runs before any JSON-RPC parse so a hostile caller
	// can't tickle the JSON parser at all.
	auth := extractAuth(r)
	if !authorize(s.opts.APIBearerToken, auth.Token) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "missing or invalid bearer token",
		})
		return
	}
	// Method gate. GET requests are reserved for SSE resumption
	// per the streamable-HTTP spec; v1 doesn't implement it.
	if r.Method == http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Resolve / create session.
	sid := r.Header.Get("Mcp-Session-Id")
	var sess *session
	if sid != "" {
		sess = s.sessions.get(sid)
	}
	if sess == nil {
		sess = s.sessions.create()
		sess.AllowMutations = auth.AllowMutations
	}
	sess.ProjectAccessToken = auth.ProjectAccessToken
	w.Header().Set("Mcp-Session-Id", sess.ID)

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		s.writeRPCError(w, nil, rpcErrParse, "failed to read body: "+err.Error(), nil)
		return
	}

	// JSON-RPC 2.0 supports a single object or a batch array. We
	// detect by sniffing the first non-whitespace byte.
	first := byte(0)
	for _, b := range body {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			first = b
			break
		}
	}
	w.Header().Set("Content-Type", "application/json")

	if first == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(body, &batch); err != nil {
			s.writeRPCError(w, nil, rpcErrParse, "invalid JSON-RPC batch: "+err.Error(), nil)
			return
		}
		responses := make([]json.RawMessage, 0, len(batch))
		for _, raw := range batch {
			if resp := s.dispatchOne(r.Context(), sess, raw); resp != nil {
				responses = append(responses, resp)
			}
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(responses)
		return
	}

	resp := s.dispatchOne(r.Context(), sess, body)
	if resp == nil {
		// Notification — no response per JSON-RPC spec.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp)
}

// rpcRequest is the inbound JSON-RPC envelope.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is the outbound JSON-RPC envelope.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the JSON-RPC error envelope.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// dispatchOne parses one JSON-RPC frame and returns the encoded
// response (or nil for a notification).
func (s *Server) dispatchOne(ctx context.Context, sess *session, raw json.RawMessage) json.RawMessage {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return s.encodeError(nil, rpcErrParse, "invalid JSON-RPC frame: "+err.Error(), nil)
	}
	if req.JSONRPC != "2.0" {
		return s.encodeError(req.ID, rpcErrInvalidRequest, "jsonrpc must be '2.0'", nil)
	}
	isNotification := len(req.ID) == 0

	result, rpcErr := s.dispatchMethod(ctx, sess, req.Method, req.Params)
	if isNotification {
		return nil
	}
	if rpcErr != nil {
		return s.encodeError(req.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data)
	}
	return s.encodeResult(req.ID, result)
}

// dispatchMethod is the JSON-RPC method router. Returning (nil,
// rpcErr) emits the error envelope; (result, nil) emits the success
// envelope.
func (s *Server) dispatchMethod(ctx context.Context, sess *session, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return s.handleInitialize(sess, params)
	case "notifications/initialized":
		// Client-side notification — nothing to do.
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		if !sess.Initialized {
			return nil, &rpcError{Code: rpcErrInvalidRequest, Message: "session not initialized"}
		}
		return map[string]any{"tools": listTools(s.registry, sess.AllowMutations)}, nil
	case "tools/call":
		if !sess.Initialized {
			return nil, &rpcError{Code: rpcErrInvalidRequest, Message: "session not initialized"}
		}
		return s.handleToolsCall(ctx, sess, params)
	case "resources/list":
		if s.resources == nil {
			return nil, &rpcError{Code: rpcErrMethodNotFound, Message: "resources capability is disabled on this server"}
		}
		list, err := s.resources.List(ctx)
		if err != nil {
			return nil, &rpcError{Code: rpcErrInternal, Message: "resources list failed: " + err.Error()}
		}
		return map[string]any{"resources": list}, nil
	case "resources/read":
		if s.resources == nil {
			return nil, &rpcError{Code: rpcErrMethodNotFound, Message: "resources capability is disabled on this server"}
		}
		return s.handleResourcesRead(ctx, params)
	case "prompts/list":
		return map[string]any{"prompts": s.prompts.list()}, nil
	case "prompts/get":
		return s.handlePromptsGet(params)
	}
	return nil, &rpcError{Code: rpcErrMethodNotFound, Message: "unknown method: " + method}
}

// handleInitialize captures session-level state and reports
// capabilities back to the client.
func (s *Server) handleInitialize(sess *session, params json.RawMessage) (any, *rpcError) {
	type initParams struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
		Capabilities map[string]any `json:"capabilities"`
		Meta         struct {
			AllowMutations bool `json:"allow_mutations"`
		} `json:"_meta"`
	}
	var p initParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: "invalid initialize params: " + err.Error()}
		}
	}
	// _meta.allow_mutations overrides the header path when the
	// caller chooses to use it. The header path is the documented
	// default.
	if p.Meta.AllowMutations {
		sess.AllowMutations = true
	}
	sess.Initialized = true

	caps := map[string]any{
		"tools":   map[string]any{"listChanged": false},
		"prompts": map[string]any{"listChanged": false},
	}
	if s.resources != nil {
		caps["resources"] = map[string]any{"listChanged": false, "subscribe": false}
	}
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    caps,
		"serverInfo": map[string]any{
			"name":    "midnight-mcp",
			"version": serverVersion,
		},
		"instructions": "Midnight MCP server. Call tools/list to discover the platform tool surface. Mutating tools require allow_mutations=true on the session (set via the X-Midnight-Allow-Mutations header on initialize, or _meta.allow_mutations).",
	}, nil
}

// handleToolsCall runs one tool through the registry, honoring the
// per-session mutation gate and the rate limiter.
func (s *Server) handleToolsCall(ctx context.Context, sess *session, params json.RawMessage) (any, *rpcError) {
	type callParams struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	var p callParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "invalid tools/call params: " + err.Error()}
	}
	if p.Name == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "tools/call: name is required"}
	}
	if ok, _ := s.limiter.allow(sess.ID); !ok {
		return nil, &rpcError{
			Code:    rpcErrRateLimited,
			Message: "rate limit exceeded for this session",
			Data:    map[string]any{"limit_per_minute": s.limiter.capacityFor()},
		}
	}
	env := s.opts.Env
	env.AllowMutations = sess.AllowMutations
	if s.opts.Audit != nil {
		env.Audit = s.opts.Audit
	}
	if env.Actor == "" {
		env.Actor = "mcp:" + sess.ID
	}
	result, callErr := executeTool(ctx, s.registry, p.Name, p.Arguments, env)
	if callErr != nil {
		return nil, callErr
	}
	return result, nil
}

// handleResourcesRead routes a resources/read frame onto the provider.
func (s *Server) handleResourcesRead(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	type readParams struct {
		URI string `json:"uri"`
	}
	var p readParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "invalid resources/read params: " + err.Error()}
	}
	if p.URI == "" {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "resources/read: uri is required"}
	}
	content, err := s.resources.Read(ctx, p.URI)
	if err != nil {
		if errors.Is(err, ErrResourceNotFound) {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
		}
		return nil, &rpcError{Code: rpcErrInternal, Message: "resources/read failed: " + err.Error()}
	}
	return map[string]any{
		"contents": []map[string]any{
			{
				"uri":      content.URI,
				"mimeType": content.MIMEType,
				"text":     content.Text,
			},
		},
	}, nil
}

// handlePromptsGet renders a server-side prompt template.
func (s *Server) handlePromptsGet(params json.RawMessage) (any, *rpcError) {
	type getParams struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments,omitempty"`
	}
	var p getParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "invalid prompts/get params: " + err.Error()}
	}
	rendered, prompt, err := s.prompts.get(p.Name, p.Arguments)
	if err != nil {
		if errors.Is(err, ErrUnknownPrompt) {
			return nil, &rpcError{Code: rpcErrMethodNotFound, Message: err.Error()}
		}
		var missing ErrMissingRequiredArg
		if errors.As(err, &missing) {
			return nil, &rpcError{Code: rpcErrInvalidParams, Message: err.Error()}
		}
		return nil, &rpcError{Code: rpcErrInternal, Message: err.Error()}
	}
	return map[string]any{
		"description": prompt.Description,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": map[string]any{
					"type": "text",
					"text": rendered,
				},
			},
		},
	}, nil
}

// encodeResult / encodeError serialize a JSON-RPC frame. id is passed
// through verbatim so the client's correlation works.
func (s *Server) encodeResult(id json.RawMessage, result any) json.RawMessage {
	resp := rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
	b, err := json.Marshal(resp)
	if err != nil {
		// Fall back to a hand-crafted internal error rather than
		// panic; the original id may not round-trip cleanly.
		return s.encodeError(id, rpcErrInternal, "result marshal failed: "+err.Error(), nil)
	}
	return b
}

func (s *Server) encodeError(id json.RawMessage, code int, message string, data any) json.RawMessage {
	resp := rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message, Data: data},
	}
	b, _ := json.Marshal(resp)
	return b
}

// writeRPCError is the helper used before dispatch (parse errors,
// batch parse failures) — it writes a single JSON-RPC error response
// straight to w with a 200 status (per JSON-RPC convention; transport
// errors are inside the envelope).
func (s *Server) writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.encodeError(id, code, message, data))
}
