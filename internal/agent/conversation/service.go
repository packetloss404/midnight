// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package conversation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/midnight/midnight/internal/agent"
	agentauth "github.com/midnight/midnight/internal/agent/auth"
)

// DefaultModel is the model name used when callers do not specify one
// on StartInput. The chat backend has no opinion on which Anthropic
// model is "right" — the value is overridable from the env-driven
// runtime config the concurrent core track owns.
const DefaultModel = "claude-opus-5"

// Service is the conversation use-case layer. It wraps the Store and
// the agent.Caller seam and fans live events to interested SSE
// subscribers.
type Service struct {
	store        Store
	caller       agent.Caller
	authorizer   ProjectScopeAuthorizer
	now          func() time.Time
	defaultModel string
	env          agent.Env
	budget       agentauth.SessionBudget
	revoker      agentauth.SessionRevoker
	hostID       string

	// streams holds active subscribers keyed by conversation id. Each
	// conversation has at most one live writer (the Send goroutine);
	// any number of readers can subscribe to receive forwarded events.
	streamsMu sync.Mutex
	streams   map[string]*conversationHub
}

// Config tunes a Service. Zero-value fields fall back to safe
// defaults.
type Config struct {
	Store        Store
	Caller       agent.Caller
	Authorizer   ProjectScopeAuthorizer
	Now          func() time.Time
	DefaultModel string
	// Env is the tool environment every Send passes to the Caller. Its
	// AllowMutations field is the operator-level gate; the per-message
	// toggle from SendInput is ANDed with it.
	Env agent.Env
	// Budget caps cumulative token + cost spend per conversation and
	// per host-day. Optional: a nil Budget means unmetered, which is
	// only appropriate for tests. Production stacks always supply one —
	// an agent that can spend money and change infrastructure needs a
	// ceiling that survives a process restart.
	Budget agentauth.SessionBudget
	// Revoker is the operator kill switch. Send checks it before every
	// turn, so a revoke takes effect at the next turn boundary rather
	// than whenever the current conversation happens to end. Optional.
	Revoker agentauth.SessionRevoker
	// HostID identifies this host for the per-host daily cost cap.
	// Defaults to the OS hostname.
	HostID string
}

// Budget / kill-switch failures. Callers map these onto the wire:
// ErrBudgetExhausted is a 429, ErrConversationRevoked a 403.
var (
	// ErrBudgetExhausted is returned when a conversation (or the host
	// for the day) has spent its cap. The message names which cap and
	// what was consumed so the user is told why, not just "no".
	ErrBudgetExhausted = errors.New("agent budget exhausted")
	// ErrConversationRevoked is returned when an operator has revoked
	// this conversation.
	ErrConversationRevoked = errors.New("agent conversation revoked")
)

// BudgetError carries the breached limits alongside ErrBudgetExhausted
// so the surface can tell the user how much of what was used.
type BudgetError struct {
	Limit   agentauth.BudgetLimit
	Used    agentauth.BudgetUsage
	RetryAt time.Time
}

func (e *BudgetError) Error() string {
	msg := fmt.Sprintf(
		"agent budget exhausted: this conversation has used %d tokens (cap %d) and %d cents (cap %d); this host has used %d cents today (cap %d)",
		e.Used.ConversationTokens, e.Limit.ConversationTokens,
		e.Used.ConversationCostCents, e.Limit.ConversationCostCents,
		e.Used.HostDailyCostCents, e.Limit.HostDailyCostCents,
	)
	if !e.RetryAt.IsZero() {
		msg += "; the daily cap resets at " + e.RetryAt.UTC().Format(time.RFC3339)
	}
	return msg
}

func (e *BudgetError) Is(target error) bool { return target == ErrBudgetExhausted }

// RevokedError carries who revoked the conversation and when.
type RevokedError struct {
	Record agentauth.RevokedRecord
}

func (e *RevokedError) Error() string {
	msg := "this conversation was stopped by an operator"
	if e.Record.ActorID != "" {
		msg += " (" + e.Record.ActorID + ")"
	}
	if !e.Record.RevokedAt.IsZero() {
		msg += " at " + e.Record.RevokedAt.UTC().Format(time.RFC3339)
	}
	if e.Record.Reason != "" {
		msg += ": " + e.Record.Reason
	}
	return msg
}

func (e *RevokedError) Is(target error) bool { return target == ErrConversationRevoked }

// ErrCallerNotConfigured is returned by Send when no provider Caller was
// wired (typically because the provider API key is unset).
var ErrCallerNotConfigured = errors.New("conversation service: caller is not configured")

// NewService constructs a Service. Store is required; Caller is
// required for Send() but may be nil for read-only deployments.
func NewService(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("conversation service: store is required")
	}
	if cfg.Authorizer == nil {
		cfg.Authorizer = AllowAllAuthorizer{}
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = DefaultModel
	}
	hostID := strings.TrimSpace(cfg.HostID)
	if hostID == "" {
		hostID, _ = os.Hostname()
	}
	// The caps default to ON. A spend limit that only exists when the
	// caller remembers to pass one is the kind of safety feature that
	// quietly ends up unwired — which is exactly the state this service
	// was in before. Operators tune the caps through the
	// MIDNIGHT_AGENT_CONVERSATION_TOKEN_CAP / _COST_CENTS /
	// MIDNIGHT_AGENT_HOST_DAILY_COST_CENTS environment variables; a
	// stack that has a durable budget (or a shared kill switch) passes
	// it explicitly and that instance wins.
	if cfg.Budget == nil {
		cfg.Budget = agentauth.NewMemoryBudget(agentauth.LoadBudgetConfig())
	}
	if cfg.Revoker == nil {
		cfg.Revoker = agentauth.NewMemorySessionRevoker()
	}
	return &Service{
		store:        cfg.Store,
		caller:       cfg.Caller,
		authorizer:   cfg.Authorizer,
		now:          cfg.Now,
		defaultModel: cfg.DefaultModel,
		env:          cfg.Env,
		budget:       cfg.Budget,
		revoker:      cfg.Revoker,
		hostID:       hostID,
		streams:      make(map[string]*conversationHub),
	}, nil
}

// StartInput captures the create-a-conversation request.
type StartInput struct {
	Title          string
	ProjectID      string
	ServiceID      string
	ActorID        string
	Model          string
	Metadata       map[string]any
	SystemPreamble string
}

// Start creates a new conversation row.
func (s *Service) Start(ctx context.Context, input StartInput) (Conversation, error) {
	scope := Scope{
		ProjectID: input.ProjectID,
		ServiceID: input.ServiceID,
		ActorID:   input.ActorID,
		Action:    "create",
	}
	if err := s.authorizer.Authorize(ctx, scope); err != nil {
		return Conversation{}, wrapAuthErr(err)
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = s.defaultModel
	}
	conv := Conversation{
		ProjectID: input.ProjectID,
		ServiceID: input.ServiceID,
		ActorID:   input.ActorID,
		Title:     input.Title,
		Model:     model,
		Metadata:  input.Metadata,
	}
	if input.SystemPreamble != "" {
		if conv.Metadata == nil {
			conv.Metadata = map[string]any{}
		}
		conv.Metadata["system_preamble"] = input.SystemPreamble
	}
	return s.store.CreateConversation(ctx, conv)
}

// Get returns a conversation after running an authorisation check.
func (s *Service) Get(ctx context.Context, id, actorID string) (Conversation, error) {
	conv, err := s.store.GetConversation(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	if err := s.authorizer.Authorize(ctx, scopeFor(conv, actorID, "read")); err != nil {
		return Conversation{}, wrapAuthErr(err)
	}
	return conv, nil
}

// List returns a page of conversations after filter-time
// authorisation.
func (s *Service) List(ctx context.Context, filter ListFilter, actorID string) (ListResult, error) {
	scope := Scope{
		ProjectID: filter.ProjectID,
		ServiceID: filter.ServiceID,
		ActorID:   actorID,
		Action:    "list",
	}
	if err := s.authorizer.Authorize(ctx, scope); err != nil {
		return ListResult{}, wrapAuthErr(err)
	}
	return s.store.ListConversations(ctx, filter)
}

// Delete removes a conversation.
func (s *Service) Delete(ctx context.Context, id, actorID string) error {
	conv, err := s.store.GetConversation(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.authorizer.Authorize(ctx, scopeFor(conv, actorID, "delete")); err != nil {
		return wrapAuthErr(err)
	}
	return s.store.DeleteConversation(ctx, id)
}

// Replay returns the conversation history in seq order, ready to feed
// agent.Caller.
func (s *Service) Replay(ctx context.Context, conversationID, actorID string) ([]Message, error) {
	conv, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if err := s.authorizer.Authorize(ctx, scopeFor(conv, actorID, "read")); err != nil {
		return nil, wrapAuthErr(err)
	}
	return s.allMessages(ctx, conversationID)
}

// ListMessages returns one keyset page after authorizing the exact
// conversation scope. Unlike Replay it preserves the caller's bounded page
// size and ordering.
func (s *Service) ListMessages(ctx context.Context, conversationID, actorID string, opts MessageListOptions) (MessagePage, error) {
	conv, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return MessagePage{}, err
	}
	if err := s.authorizer.Authorize(ctx, scopeFor(conv, actorID, "read")); err != nil {
		return MessagePage{}, wrapAuthErr(err)
	}
	return s.store.ListMessages(ctx, conversationID, opts)
}

// SendInput captures the append-a-user-message request.
type SendInput struct {
	ConversationID string
	ActorID        string
	Content        string
	AllowedTools   []string
	// AllowMutations toggles whether the Caller is allowed to invoke
	// state-changing tools on behalf of the user. It is ANDed with the
	// operator-level Config.Env.AllowMutations gate before the call.
	AllowMutations bool
}

// SendResult is what Send returns once the assistant turn finishes (or
// fails). The chat backend persists the assistant message before
// returning, so callers get the final shape even when they did not
// subscribe to the live stream.
type SendResult struct {
	UserMessage      Message
	AssistantMessage Message
	StopReason       string
}

// Send appends a user message, calls Caller.Stream, persists the
// assistant turn, and fans every event to live SSE subscribers.
// `stream`, when non-nil, receives the same events as the SSE
// subscribers. Closing of stream is the caller's responsibility.
func (s *Service) Send(ctx context.Context, input SendInput, stream chan<- agent.StreamEvent) (SendResult, error) {
	if s.caller == nil {
		return SendResult{}, ErrCallerNotConfigured
	}
	conv, err := s.store.GetConversation(ctx, input.ConversationID)
	if err != nil {
		return SendResult{}, err
	}
	if err := s.authorizer.Authorize(ctx, scopeFor(conv, input.ActorID, "send")); err != nil {
		return SendResult{}, wrapAuthErr(err)
	}

	session := s.sessionContext(conv, input)
	// Both checks run before the user's message is persisted: a turn
	// that will not happen should not leave a message in the transcript
	// that looks like it went unanswered.
	if err := s.checkRevoked(ctx, session); err != nil {
		return SendResult{}, err
	}
	if err := s.checkBudget(ctx, session); err != nil {
		return SendResult{}, err
	}

	userBlocks := []agent.ContentBlock{{Kind: agent.BlockText, Text: input.Content}}
	userMsg := Message{
		ConversationID: conv.ID,
		Role:           agent.RoleUser,
		Content:        userBlocks,
		CreatedAt:      s.now(),
	}
	storedUser, err := s.store.AppendMessage(ctx, userMsg)
	if err != nil {
		return SendResult{}, fmt.Errorf("append user message: %w", err)
	}

	history, err := s.allMessages(ctx, conv.ID)
	if err != nil {
		return SendResult{}, fmt.Errorf("replay history: %w", err)
	}
	callMessages := make([]agent.Message, 0, len(history))
	for _, m := range history {
		callMessages = append(callMessages, m.ToAgentMessage())
	}

	systemPreamble, _ := conv.Metadata["system_preamble"].(string)

	env := s.env
	env.AllowMutations = s.env.AllowMutations && input.AllowMutations
	if strings.TrimSpace(input.ActorID) != "" {
		env.Actor = strings.TrimSpace(firstNonEmpty(env.Actor, "agent:chat")) + ":" + strings.TrimSpace(input.ActorID)
	}
	if strings.TrimSpace(conv.ProjectID) != "" {
		env.ProjectScope = conv.ProjectID
	}
	req := agent.CallRequest{
		Conversation:   callMessages,
		SystemPreamble: systemPreamble,
		Model:          conv.Model,
		AllowedTools:   input.AllowedTools,
		Env:            env,
	}

	hub := s.acquireHub(conv.ID)
	defer s.releaseHub(conv.ID, hub)

	events := make(chan agent.StreamEvent, 32)
	streamErr := make(chan error, 1)
	go func() {
		streamErr <- s.caller.Stream(ctx, req, events)
	}()

	var (
		textBuf      strings.Builder
		toolBlocks   []agent.ContentBlock
		finalUsage   *agent.Usage
		finalMessage *agent.Message
		stopReason   string
	)
	for ev := range events {
		switch ev.Kind {
		case agent.EventTextDelta:
			textBuf.WriteString(ev.Delta)
		case agent.EventToolCall:
			if ev.ToolCall != nil {
				toolBlocks = append(toolBlocks, agent.ContentBlock{Kind: agent.BlockToolUse, ToolUse: ev.ToolCall})
			}
		case agent.EventToolResult:
			if ev.ToolResult != nil {
				toolBlocks = append(toolBlocks, agent.ContentBlock{Kind: agent.BlockToolResult, ToolResult: ev.ToolResult})
			}
		case agent.EventDone:
			if ev.Usage != nil {
				usage := *ev.Usage
				finalUsage = &usage
			}
			stopReason = ev.StopReason
		case agent.EventError:
			// Forward the error to subscribers and abandon the turn.
			s.forward(stream, hub, ev)
			msg := ev.Delta
			if msg == "" {
				msg = "agent stream error"
			}
			return SendResult{UserMessage: storedUser}, fmt.Errorf("agent stream: %s", msg)
		}
		s.forward(stream, hub, ev)
	}
	_ = finalMessage // populated by future core extensions; currently unused.
	if err := <-streamErr; err != nil {
		return SendResult{UserMessage: storedUser}, fmt.Errorf("agent stream: %w", err)
	}

	assistantContent := buildAssistantContent(finalMessage, textBuf.String(), toolBlocks)
	assistantMsg := Message{
		ConversationID: conv.ID,
		Role:           agent.RoleAssistant,
		Content:        assistantContent,
		Usage:          finalUsage,
		CreatedAt:      s.now(),
	}
	storedAssistant, err := s.store.AppendMessage(ctx, assistantMsg)
	if err != nil {
		return SendResult{UserMessage: storedUser}, fmt.Errorf("append assistant message: %w", err)
	}

	// Record what the turn actually cost. This is deliberately after the
	// turn rather than a pre-charge: the caps are about not letting a
	// conversation run away, not about pre-authorizing each turn.
	s.recordUsage(ctx, session, finalUsage)

	return SendResult{
		UserMessage:      storedUser,
		AssistantMessage: storedAssistant,
		StopReason:       stopReason,
	}, nil
}

// sessionContext builds the redaction-safe per-turn view the budget and
// kill switch key off.
func (s *Service) sessionContext(conv Conversation, input SendInput) agentauth.SessionContext {
	return agentauth.SessionContext{
		ConversationID: conv.ID,
		AllowMutations: s.env.AllowMutations && input.AllowMutations,
		HostID:         s.hostID,
		Principal: agentauth.Principal{
			Kind:    agentauth.PrincipalKindAgent,
			ID:      input.ActorID,
			Subject: input.ActorID,
		},
	}
}

// checkRevoked consults the operator kill switch. A revoker that errors
// fails closed: an operator who hit stop is entitled to assume it took.
func (s *Service) checkRevoked(ctx context.Context, session agentauth.SessionContext) error {
	if s.revoker == nil {
		return nil
	}
	key := session.SessionKey()
	if key == "" {
		return nil
	}
	revoked, err := s.revoker.IsRevoked(ctx, key)
	if err != nil {
		return fmt.Errorf("%w: revocation state is unreadable: %s", ErrConversationRevoked, err.Error())
	}
	if !revoked {
		return nil
	}
	record, err := s.revoker.RevokedAt(ctx, key)
	if err != nil {
		record = agentauth.RevokedRecord{Revoked: true, SessionID: key}
	}
	return &RevokedError{Record: record}
}

// checkBudget consults the cumulative spend caps. Unlike the kill
// switch, a budget backend that errors fails open: refusing to answer
// because an accounting table was briefly unreadable is a worse failure
// than one turn of unmetered spend, and the cap is re-checked next turn.
func (s *Service) checkBudget(ctx context.Context, session agentauth.SessionContext) error {
	if s.budget == nil || session.SessionKey() == "" {
		return nil
	}
	decision, err := s.budget.Allow(ctx, session)
	if err != nil {
		return nil
	}
	if decision.Allow {
		return nil
	}
	return &BudgetError{Limit: decision.Limit, Used: decision.Used, RetryAt: decision.RetryAt}
}

// recordUsage persists the turn's spend against the conversation and
// host-day buckets. Failures are swallowed: the turn already happened,
// and the next Allow re-reads whatever did land.
func (s *Service) recordUsage(ctx context.Context, session agentauth.SessionContext, usage *agent.Usage) {
	if s.budget == nil || usage == nil || session.SessionKey() == "" {
		return
	}
	_ = s.budget.Record(ctx, session, agentauth.Usage{
		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
		CacheReadTokens:     usage.CacheReadTokens,
		// Costs are estimated in USD by the provider layer; the budget
		// tracks whole cents, rounding up so a long tail of sub-cent
		// turns still moves the counter.
		CostCents: usdToCents(usage.CostUSDEstimated),
		Model:     usage.Model,
	})
}

// usdToCents converts an estimated dollar cost into whole cents,
// rounding any non-zero fraction up to 1 so repeated tiny turns are not
// accounted as free.
func usdToCents(usd float64) int64 {
	if usd <= 0 {
		return 0
	}
	cents := int64(usd * 100)
	if float64(cents) < usd*100 {
		cents++
	}
	if cents == 0 {
		cents = 1
	}
	return cents
}

// AppendStoredMessage writes a message straight into a conversation
// without invoking the model.
//
// The build-failure diagnosis worker needs this: it already has the
// model's answer (it called Send on the agent seam itself, outside any
// conversation) and only needs somewhere durable to put it. Routing that
// through Service.Send would call the model a second time to "answer" a
// message that is itself the answer.
func (s *Service) AppendStoredMessage(ctx context.Context, conversationID string, role agent.MessageRole, content string) error {
	if strings.TrimSpace(conversationID) == "" {
		return errors.New("conversation service: conversation id is required")
	}
	if _, err := s.store.GetConversation(ctx, conversationID); err != nil {
		return err
	}
	_, err := s.store.AppendMessage(ctx, Message{
		ConversationID: conversationID,
		Role:           role,
		Content:        []agent.ContentBlock{{Kind: agent.BlockText, Text: content}},
		CreatedAt:      s.now(),
	})
	return err
}

func (s *Service) allMessages(ctx context.Context, conversationID string) ([]Message, error) {
	items := make([]Message, 0)
	var after *int64
	for {
		page, err := s.store.ListMessages(ctx, conversationID, MessageListOptions{Limit: MaxPageLimit, AfterSeq: after})
		if err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		if page.NextSeq == nil {
			return items, nil
		}
		if after != nil && *page.NextSeq <= *after {
			return nil, fmt.Errorf("conversation message pagination did not advance")
		}
		after = page.NextSeq
	}
}

// Subscribe returns a channel that receives every StreamEvent emitted
// for the supplied conversation while the subscription is alive. The
// returned cancel function MUST be called by the consumer (e.g. the
// SSE handler) when its request context ends.
func (s *Service) Subscribe(ctx context.Context, conversationID string, buffer int) (<-chan agent.StreamEvent, func()) {
	if buffer <= 0 {
		buffer = 32
	}
	hub := s.acquireHub(conversationID)
	ch := make(chan agent.StreamEvent, buffer)
	sub := hub.subscribe(ch)
	cancel := func() {
		hub.unsubscribe(sub)
		s.releaseHub(conversationID, hub)
	}
	return ch, cancel
}

// buildAssistantContent merges the streamed deltas + tool blocks into
// the persisted content array. When the Caller surfaces an explicit
// finalMessage on the "done" event, that wins (the Caller is the
// source of truth). Otherwise we reconstruct from deltas.
func buildAssistantContent(final *agent.Message, text string, tools []agent.ContentBlock) []agent.ContentBlock {
	if final != nil && len(final.Content) > 0 {
		return append([]agent.ContentBlock(nil), final.Content...)
	}
	blocks := make([]agent.ContentBlock, 0, 1+len(tools))
	if text != "" {
		blocks = append(blocks, agent.ContentBlock{Kind: agent.BlockText, Text: text})
	}
	blocks = append(blocks, tools...)
	return blocks
}

func scopeFor(c Conversation, actorID, action string) Scope {
	return Scope{
		ConversationID: c.ID,
		ProjectID:      c.ProjectID,
		ServiceID:      c.ServiceID,
		ActorID:        actorID,
		Action:         action,
	}
}

func wrapAuthErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrUnauthorized) {
		return err
	}
	return fmt.Errorf("%w: %s", ErrUnauthorized, err.Error())
}

// --- conversation event hub -------------------------------------------------

// conversationHub fans a single producer's events out to N subscribers.
type conversationHub struct {
	mu     sync.Mutex
	refcnt int
	subs   map[*subscription]struct{}
}

type subscription struct{ ch chan agent.StreamEvent }

func newConversationHub() *conversationHub {
	return &conversationHub{subs: make(map[*subscription]struct{})}
}

func (h *conversationHub) subscribe(ch chan agent.StreamEvent) *subscription {
	sub := &subscription{ch: ch}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs[sub] = struct{}{}
	return sub
}

func (h *conversationHub) unsubscribe(sub *subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[sub]; ok {
		delete(h.subs, sub)
		close(sub.ch)
	}
}

func (h *conversationHub) broadcast(ev agent.StreamEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		select {
		case sub.ch <- ev:
		default:
			// Drop on full buffer to keep slow subscribers from
			// blocking the producer. SSE clients can replay via the
			// REST /messages endpoint.
		}
	}
}

func (s *Service) acquireHub(conversationID string) *conversationHub {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	hub, ok := s.streams[conversationID]
	if !ok {
		hub = newConversationHub()
		s.streams[conversationID] = hub
	}
	hub.refcnt++
	return hub
}

func (s *Service) releaseHub(conversationID string, hub *conversationHub) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	hub.refcnt--
	if hub.refcnt <= 0 {
		hub.mu.Lock()
		for sub := range hub.subs {
			delete(hub.subs, sub)
			close(sub.ch)
		}
		hub.mu.Unlock()
		delete(s.streams, conversationID)
	}
}

func (s *Service) forward(direct chan<- agent.StreamEvent, hub *conversationHub, ev agent.StreamEvent) {
	if direct != nil {
		select {
		case direct <- ev:
		default:
		}
	}
	hub.broadcast(ev)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
