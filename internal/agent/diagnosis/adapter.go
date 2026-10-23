// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package diagnosis

import (
	"context"
	"errors"
	"strings"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/conversation"
)

// This file is the bridge between the diagnosis runner's narrow local
// seams and the real agent + chat-backend packages.
//
// The runner declares its own Caller / CallRequest / CallResult and its
// own ConversationStore. That was originally a build-order concession —
// the package had to compile before internal/agent existed — and the
// comment on those types claimed they were "field-compatible" with
// agent.CallRequest. They never were: System vs SystemPreamble,
// []Message{Role, Content string} vs []agent.Message{Role, Content
// []ContentBlock}, Text vs FinalText. Nothing ever noticed, because the
// runner was constructed with a nil Caller and bailed out of every run
// before reaching the call.
//
// Keeping the narrow seams is still worth it — the runner's tests fake
// two tiny interfaces instead of a provider — so the mismatch is fixed
// where it belongs: in an adapter that is itself tested.

// ErrCallerRequired is returned by NewAgentCaller when no underlying
// agent.Caller was supplied.
var ErrCallerRequired = errors.New("diagnosis: agent caller is required")

// AgentCallerOptions tunes the adapter.
type AgentCallerOptions struct {
	// AllowedTools scopes the tool set the diagnosis loop may use. It
	// MUST contain read-only tools only; the adapter additionally pins
	// Env.AllowMutations to false so a mutating tool cannot execute even
	// if one is named here by mistake.
	AllowedTools []string
	// Env supplies the platform handles the read-only tools need. The
	// zero value disables tool use entirely, which is a supported
	// configuration: the runner already renders logs and service
	// context into the prompt.
	Env agent.Env
	// MaxIterations caps the tool loop. 0 uses the agent default.
	MaxIterations int
}

// ReadOnlyDiagnosisTools is the tool set a diagnosis loop may be given.
// Every entry is a read: the loop produces a report, and a report that
// changed something while nobody was watching is not a report.
var ReadOnlyDiagnosisTools = []string{
	"list_services",
	"get_service",
	"get_deployment",
	"get_logs",
	"get_metrics",
}

// agentCallerAdapter implements the local Caller seam on top of the real
// agent.Caller.
type agentCallerAdapter struct {
	inner agent.Caller
	opts  AgentCallerOptions
}

// NewAgentCaller adapts an agent.Caller onto the diagnosis Caller seam.
func NewAgentCaller(inner agent.Caller, opts AgentCallerOptions) (Caller, error) {
	if inner == nil {
		return nil, ErrCallerRequired
	}
	// Mutations are impossible by construction, not by convention.
	opts.Env.AllowMutations = false
	if opts.Env.Actor == "" {
		opts.Env.Actor = "agent:diagnosis"
	}
	return &agentCallerAdapter{inner: inner, opts: opts}, nil
}

// Send translates the runner's request, calls the real agent, and
// translates the answer back.
func (a *agentCallerAdapter) Send(ctx context.Context, req CallRequest) (CallResult, error) {
	agentReq, err := a.toAgentRequest(req)
	if err != nil {
		return CallResult{}, err
	}
	result, err := a.inner.Send(ctx, agentReq)
	if err != nil {
		return CallResult{}, err
	}
	return CallResult{
		Text:       strings.TrimSpace(result.FinalText),
		StopReason: result.StopReason,
	}, nil
}

// Stream exists to satisfy the seam. The diagnosis runner is
// non-streaming by design — there is no client attached to watch — so
// this forwards without translating incremental frames.
func (a *agentCallerAdapter) Stream(ctx context.Context, req CallRequest, out chan<- StreamEvent) error {
	defer close(out)
	result, err := a.Send(ctx, req)
	if err != nil {
		out <- StreamEvent{Type: "error", Text: err.Error()}
		return err
	}
	out <- StreamEvent{Type: "text", Text: result.Text}
	return nil
}

func (a *agentCallerAdapter) toAgentRequest(req CallRequest) (agent.CallRequest, error) {
	conv := make([]agent.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := agent.RoleUser
		if strings.EqualFold(m.Role, string(agent.RoleAssistant)) {
			role = agent.RoleAssistant
		}
		conv = append(conv, agent.TextMessage(role, m.Content))
	}
	if len(conv) == 0 {
		return agent.CallRequest{}, errors.New("diagnosis: call request has no messages")
	}
	return agent.CallRequest{
		Conversation: conv,
		// The runner's `System` is the seam's SystemPreamble. Getting
		// this wrong is silent: the system text simply never reaches the
		// model, and the answer is merely worse, not broken.
		SystemPreamble: req.System,
		Model:          req.Model,
		AllowedTools:   a.opts.AllowedTools,
		MaxIterations:  a.opts.MaxIterations,
		Env:            a.opts.Env,
	}, nil
}

// conversationStoreAdapter implements the runner's ConversationStore on
// top of the chat backend's service, so a diagnosis lands in the same
// place a user's own chat does and the console can open it.
type conversationStoreAdapter struct {
	service *conversation.Service
	actor   string
	model   string
}

// NewConversationStore adapts the chat backend's conversation service
// onto the runner's store seam. actor is the audit actor recorded as the
// conversation's owner; model is recorded on the conversation so the
// console can show what produced the diagnosis.
func NewConversationStore(service *conversation.Service, actor, model string) ConversationStore {
	if service == nil {
		return nil
	}
	if strings.TrimSpace(actor) == "" {
		actor = "agent:diagnosis"
	}
	return &conversationStoreAdapter{service: service, actor: actor, model: model}
}

// StartConversation opens the conversation the diagnosis is written
// into. The runner's tags carry the operation / service / deployment
// ids; they become conversation metadata so the dashboard can find the
// diagnosis for a given failed operation.
func (a *conversationStoreAdapter) StartConversation(ctx context.Context, title string, tags map[string]string) (Conversation, error) {
	metadata := make(map[string]any, len(tags))
	for k, v := range tags {
		metadata[k] = v
	}
	conv, err := a.service.Start(ctx, conversation.StartInput{
		Title:     title,
		ActorID:   a.actor,
		ProjectID: tags["midnight.project_id"],
		ServiceID: tags["midnight.service_id"],
		Model:     a.model,
		Metadata:  metadata,
	})
	if err != nil {
		return Conversation{}, err
	}
	return Conversation{ID: conv.ID}, nil
}

// AppendMessage writes the diagnosis body into the conversation.
//
// It goes through the store rather than Service.Send: Send would call
// the model again, and this text is the model's answer, already paid
// for.
func (a *conversationStoreAdapter) AppendMessage(ctx context.Context, conversationID, role, content string) error {
	agentRole := agent.RoleAssistant
	if strings.EqualFold(role, string(agent.RoleUser)) {
		agentRole = agent.RoleUser
	}
	return a.service.AppendStoredMessage(ctx, conversationID, agentRole, content)
}
