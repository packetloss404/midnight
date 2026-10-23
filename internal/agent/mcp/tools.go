// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package mcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/tools"
)

// registryView is the narrow read-only surface the MCP server consumes
// off [*tools.Registry]. Declaring it here (rather than reaching back
// into internal/agent/tools) keeps server_test.go able to swap in a
// fake without depending on the concrete registry type.
//
// Per scope boundary: this is a *one-way* interface inside the MCP
// package; we never add the same shape to internal/agent/tools.
type registryView interface {
	Names() []string
	Get(name string) (tools.Tool, bool)
	Execute(ctx context.Context, name string, input json.RawMessage, env agent.Env) (any, error)
}

// mcpTool is the wire-shape one tool record advertised over
// tools/list. We keep our own struct rather than reuse the SDK Tool
// to keep the hand-rolled wire self-contained.
type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	// Annotations.Destructive carries the mutation hint per MCP spec.
	Annotations *toolAnnotations `json:"annotations,omitempty"`
}

type toolAnnotations struct {
	Title           string `json:"title,omitempty"`
	DestructiveHint bool   `json:"destructiveHint,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint,omitempty"`
}

// listTools walks the registry and emits one mcpTool record per
// registered tool. When allowMutations is false, tools with
// Mutates=true are filtered out — the model never even sees them. The
// emitted order matches Registry.Names() (sorted, stable).
func listTools(reg registryView, allowMutations bool) []mcpTool {
	names := reg.Names()
	out := make([]mcpTool, 0, len(names))
	for _, n := range names {
		t, ok := reg.Get(n)
		if !ok {
			continue
		}
		if t.Mutates && !allowMutations {
			continue
		}
		annotations := &toolAnnotations{
			DestructiveHint: t.Mutates,
			ReadOnlyHint:    !t.Mutates,
		}
		out = append(out, mcpTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Annotations: annotations,
		})
	}
	return out
}

// callToolContent is one block of CallToolResult.content. The hand-
// rolled wire only emits "text" blocks (the entire registry surface
// returns JSON-encodable values).
type callToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// callToolResult is the wire shape returned from tools/call.
type callToolResult struct {
	Content []callToolContent `json:"content"`
	IsError bool              `json:"isError,omitempty"`
}

// executeTool unpacks params.arguments, applies the registry's
// mutation gate, and JSON-encodes the result. The (rpcErrCode,
// rpcErrMessage) return pair maps onto a JSON-RPC error response when
// non-zero; otherwise the *callToolResult is the success payload.
//
// JSON-RPC error mapping enforced by the caller:
//
//	-32601  tool name not registered (ErrToolNotFound)
//	-32030  MIDNIGHT_MUTATION_DISALLOWED — Mutates=true && !allow
//	-32602  arguments JSON-unmarshalable
//	-32603  generic execute failure (the tool itself returned a
//	        non-mutation error)
func executeTool(ctx context.Context, reg registryView, name string, args json.RawMessage, env agent.Env) (*callToolResult, *rpcError) {
	if args == nil || len(args) == 0 {
		args = json.RawMessage(`{}`)
	} else if !json.Valid(args) {
		return nil, &rpcError{Code: rpcErrInvalidParams, Message: "invalid arguments JSON"}
	}
	t, ok := reg.Get(name)
	if !ok {
		return nil, &rpcError{Code: rpcErrMethodNotFound, Message: "unknown tool: " + name}
	}
	if t.Mutates && !env.AllowMutations {
		return nil, &rpcError{
			Code:    rpcErrMutationDisallowed,
			Message: "MIDNIGHT_MUTATION_DISALLOWED: tool " + name + " mutates state but this session has allow_mutations=false",
			Data: map[string]any{
				"code":            "MIDNIGHT_MUTATION_DISALLOWED",
				"tool":            name,
				"allow_mutations": false,
			},
		}
	}
	result, err := reg.Execute(ctx, name, args, env)
	if err != nil {
		// Mutation-gate error from the registry layer (when the env
		// passes through but the registry sees AllowMutations=false)
		// should map to the same -32030 code.
		if errors.Is(err, agent.ErrMutationsDisabled) {
			return nil, &rpcError{
				Code:    rpcErrMutationDisallowed,
				Message: "MIDNIGHT_MUTATION_DISALLOWED: " + err.Error(),
				Data: map[string]any{
					"code":            "MIDNIGHT_MUTATION_DISALLOWED",
					"tool":            name,
					"allow_mutations": false,
				},
			}
		}
		if errors.Is(err, agent.ErrToolNotFound) {
			return nil, &rpcError{Code: rpcErrMethodNotFound, Message: err.Error()}
		}
		// Tool execute failure surfaces inside the result envelope per
		// MCP spec (isError=true) so the model can read it; the JSON-
		// RPC layer reports success.
		return &callToolResult{
			Content: []callToolContent{{Type: "text", Text: err.Error()}},
			IsError: true,
		}, nil
	}
	b, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return nil, &rpcError{Code: rpcErrInternal, Message: "tool result not JSON-encodable: " + marshalErr.Error()}
	}
	return &callToolResult{
		Content: []callToolContent{{Type: "text", Text: string(b)}},
	}, nil
}
