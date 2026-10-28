// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
)

// AgentConversationsPrefix is the v1alpha root of the AI agent chat
// REST surface. Every endpoint below lives beneath it.
const AgentConversationsPrefix = v1alphaPrefix + "/agent/conversations"

// RegisterAgentConversationHandlers attaches the AI agent chat REST
// endpoints to the supplied mux. The function is intentionally
// side-effect free apart from mux registration so it can be unit
// tested in isolation. The wireup is invoked from
// newRouterWithDataStack when a non-nil ConversationService is
// configured on DataStack.
func RegisterAgentConversationHandlers(mux *http.ServeMux, deps AgentConversationHandlerDeps) {
	if mux == nil || deps.Service == nil {
		return
	}
	handler := agentConversationsHandler(deps)
	mux.Handle(AgentConversationsPrefix, handler)
	mux.Handle(AgentConversationsPrefix+"/", handler)
}

// AgentConversationHandlerDeps bundles the runtime deps the handler
// chain needs. ConversationService is required; IdempotencyStore is
// optional and enables Idempotency-Key replay on POST /conversations.
type AgentConversationHandlerDeps struct {
	Service          *conversation.Service
	IdempotencyStore idempotency.Store
	// Now overrides the wall clock used for SSE heartbeats and replay
	// fingerprints. Tests pass a fixed clock for deterministic frames.
	Now func() time.Time
	// PrincipalFromRequest extracts the authenticated principal id
	// (actor) from r. Production supplies a server-derived context extractor.
	// When nil, the handler uses the server-derived idempotency actor. Tests and
	// embedders that use another authentication system must opt in explicitly.
	PrincipalFromRequest func(r *http.Request) string
	CursorCodec          CursorCodec
}

func (d AgentConversationHandlerDeps) clock() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}

func (d AgentConversationHandlerDeps) principal(r *http.Request) string {
	if d.PrincipalFromRequest != nil {
		return d.PrincipalFromRequest(r)
	}
	return string(idempotency.ActorFromContext(r.Context()))
}

func agentConversationsHandler(deps AgentConversationHandlerDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ok := strings.CutPrefix(r.URL.Path, AgentConversationsPrefix)
		if !ok {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "agent conversation route not found", nil)
			return
		}
		path = strings.TrimPrefix(path, "/")

		if path == "" {
			switch r.Method {
			case http.MethodPost:
				createConversation(w, r, deps)
				return
			case http.MethodGet:
				listConversations(w, r, deps)
				return
			default:
				w.Header().Set("Allow", "GET, POST")
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
				return
			}
		}

		parts := strings.Split(path, "/")
		convID := parts[0]
		if convID == "" {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "agent conversation not found", nil)
			return
		}
		if len(parts) == 1 {
			switch r.Method {
			case http.MethodGet:
				getConversation(w, r, deps, convID)
				return
			case http.MethodDelete:
				deleteConversation(w, r, deps, convID)
				return
			default:
				w.Header().Set("Allow", "GET, DELETE")
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"conversation_id": convID})
				return
			}
		}
		if len(parts) == 2 {
			switch parts[1] {
			case "messages":
				switch r.Method {
				case http.MethodGet:
					listMessages(w, r, deps, convID)
					return
				case http.MethodPost:
					sendMessage(w, r, deps, convID)
					return
				default:
					w.Header().Set("Allow", "GET, POST")
					writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"conversation_id": convID})
					return
				}
			case "stream":
				if r.Method != http.MethodGet {
					w.Header().Set("Allow", http.MethodGet)
					writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"conversation_id": convID})
					return
				}
				streamConversation(w, r, deps, convID)
				return
			}
		}
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "agent conversation route not found", map[string]any{"conversation_id": convID})
	})
}

// startConversationRequest is the body for POST /conversations.
type startConversationRequest struct {
	Title          string         `json:"title,omitempty"`
	ProjectID      string         `json:"project_id,omitempty"`
	ServiceID      string         `json:"service_id,omitempty"`
	EnvironmentID  string         `json:"environment_id,omitempty"`
	Model          string         `json:"model,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	SystemPreamble string         `json:"system_preamble,omitempty"`
}

func createConversation(w http.ResponseWriter, r *http.Request, deps AgentConversationHandlerDeps) {
	body, ok := readBoundedJSONRequestBody(w, r, "invalid conversation request body")
	if !ok {
		return
	}
	var request startConversationRequest
	if len(body) > 0 {
		if err := strictJSONDecode(body, &request); err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid conversation request body", map[string]any{"reason": err.Error()})
			return
		}
	}

	actor := deps.principal(r)
	input := conversation.StartInput{
		Title:          request.Title,
		ProjectID:      request.ProjectID,
		ServiceID:      request.ServiceID,
		EnvironmentID:  request.EnvironmentID,
		Model:          request.Model,
		ActorID:        actor,
		Metadata:       request.Metadata,
		SystemPreamble: request.SystemPreamble,
	}
	if err := deps.Service.AuthorizeStart(r.Context(), input); err != nil {
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "principal is not authorised for this scope", nil)
		return
	}
	ref, replayed := agentReserveIdempotency(w, r, deps.IdempotencyStore, body)
	if replayed {
		return
	}
	conv, err := deps.Service.Start(r.Context(), input)
	if errors.Is(err, conversation.ErrUnauthorized) {
		agentTerminalError(w, r, deps.IdempotencyStore, ref, http.StatusForbidden, failurecodes.AuthRequiredMissing, "principal is not authorised for this scope", nil)
		return
	}
	if errors.Is(err, conversation.ErrInvalid) {
		agentTerminalError(w, r, deps.IdempotencyStore, ref, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	if err != nil {
		agentTerminalError(w, r, deps.IdempotencyStore, ref, http.StatusInternalServerError, failurecodes.InternalError, "conversation create failed", nil)
		return
	}

	body, err = json.Marshal(conv)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "conversation marshal failed", nil)
		return
	}
	body = append(body, '\n')
	if ref.Key != "" {
		location := AgentConversationsPrefix + "/" + conv.ID
		stored := idempotency.StoredResponse{
			StatusCode: http.StatusCreated,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"Location":     []string{location},
			},
			Body: body,
		}
		if _, err := deps.IdempotencyStore.CompleteSuccess(r.Context(), ref, stored); err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency completion failed", nil)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", AgentConversationsPrefix+"/"+conv.ID)
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(body)
}

func listConversations(w http.ResponseWriter, r *http.Request, deps AgentConversationHandlerDeps) {
	q, ok := parseAgentConversationListQuery(w, r, true)
	if !ok {
		return
	}
	filter := conversation.ListFilter{
		ProjectID: q.values.Get("project_id"),
		ServiceID: q.values.Get("service_id"),
		ActorID:   q.values.Get("actor_id"),
		Limit:     q.limit,
	}
	actor := deps.principal(r)
	binding := agentConversationListBinding(r, actor, filter, "created_at.desc,id.desc", q.limit)
	if q.cursor != "" {
		position, err := deps.CursorCodec.decodeAgentConversationCursor(q.cursor, binding, deps.clock())
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid cursor", nil)
			return
		}
		filter.After = &position
	}
	page, err := deps.Service.List(r.Context(), filter, actor)
	if errors.Is(err, conversation.ErrUnauthorized) {
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthRequiredMissing, "principal is not authorised for this scope", nil)
		return
	}
	if errors.Is(err, conversation.ErrInvalid) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid cursor", nil)
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "conversation list failed", nil)
		return
	}
	var nextCursor string
	if page.Next != nil {
		nextCursor, err = deps.CursorCodec.encodeAgentConversationCursor(*page.Next, binding, deps.clock())
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "cursor encode failed", nil)
			return
		}
	}
	WriteJSON(w, http.StatusOK, conversationsPage{Data: page.Items, Page: cursorPage{NextCursor: nextCursor}})
}

type agentConversationListQuery struct {
	values url.Values
	limit  int
	cursor string
}

func parseAgentConversationListQuery(w http.ResponseWriter, r *http.Request, conversations bool) (agentConversationListQuery, bool) {
	allowed := map[string]bool{"limit": true, "cursor": true}
	if conversations {
		allowed["project_id"] = true
		allowed["service_id"] = true
		allowed["actor_id"] = true
	} else {
		allowed["order"] = true
	}
	values := r.URL.Query()
	for key, entries := range values {
		if !allowed[key] || len(entries) != 1 || strings.TrimSpace(entries[0]) == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid pagination query", map[string]any{"parameter": key})
			return agentConversationListQuery{}, false
		}
	}
	if !conversations {
		order := values.Get("order")
		if order != "" && order != "asc" && order != "desc" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid message order", nil)
			return agentConversationListQuery{}, false
		}
	}
	limit := conversation.DefaultPageLimit
	if raw := values.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid page limit", nil)
			return agentConversationListQuery{}, false
		}
		if parsed > conversation.MaxPageLimit {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.PaginationLimitExceeded, "page limit exceeds maximum", map[string]any{"max": conversation.MaxPageLimit})
			return agentConversationListQuery{}, false
		}
		limit = parsed
	}
	return agentConversationListQuery{values: values, limit: limit, cursor: values.Get("cursor")}, true
}

func getConversation(w http.ResponseWriter, r *http.Request, deps AgentConversationHandlerDeps, id string) {
	conv, err := deps.Service.Get(r.Context(), id, deps.principal(r))
	if errors.Is(err, conversation.ErrNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "agent conversation not found", map[string]any{"conversation_id": id})
		return
	}
	if errors.Is(err, conversation.ErrUnauthorized) {
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthRequiredMissing, "principal is not authorised for this scope", nil)
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "conversation get failed", map[string]any{"conversation_id": id})
		return
	}
	WriteJSON(w, http.StatusOK, conv)
}

func deleteConversation(w http.ResponseWriter, r *http.Request, deps AgentConversationHandlerDeps, id string) {
	if err := deps.Service.Delete(r.Context(), id, deps.principal(r)); err != nil {
		if errors.Is(err, conversation.ErrNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "agent conversation not found", map[string]any{"conversation_id": id})
			return
		}
		if errors.Is(err, conversation.ErrUnauthorized) {
			writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthRequiredMissing, "principal is not authorised for this scope", nil)
			return
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "conversation delete failed", map[string]any{"conversation_id": id})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func listMessages(w http.ResponseWriter, r *http.Request, deps AgentConversationHandlerDeps, id string) {
	q, ok := parseAgentConversationListQuery(w, r, false)
	if !ok {
		return
	}
	// Authorise via Get so unauthorised callers don't learn about the
	// existence of conversations they cannot see.
	if _, err := deps.Service.Get(r.Context(), id, deps.principal(r)); err != nil {
		if errors.Is(err, conversation.ErrNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "agent conversation not found", map[string]any{"conversation_id": id})
			return
		}
		if errors.Is(err, conversation.ErrUnauthorized) {
			writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthRequiredMissing, "principal is not authorised for this scope", nil)
			return
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "conversation get failed", map[string]any{"conversation_id": id})
		return
	}
	actor := deps.principal(r)
	order := q.values.Get("order")
	if order == "" {
		order = "asc"
	}
	opts := conversation.MessageListOptions{Limit: q.limit, Reverse: order == "desc"}
	binding := agentMessageListBinding(r, actor, id, "seq."+order, q.limit)
	if q.cursor != "" {
		seq, err := deps.CursorCodec.decodeAgentMessageCursor(q.cursor, binding, deps.clock())
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid cursor", nil)
			return
		}
		opts.AfterSeq = &seq
	}
	page, err := deps.Service.ListMessages(r.Context(), id, actor, opts)
	if errors.Is(err, conversation.ErrNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "agent conversation not found", map[string]any{"conversation_id": id})
		return
	}
	if errors.Is(err, conversation.ErrUnauthorized) {
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthRequiredMissing, "principal is not authorised for this scope", nil)
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "conversation messages list failed", map[string]any{"conversation_id": id})
		return
	}
	var nextCursor string
	if page.NextSeq != nil {
		nextCursor, err = deps.CursorCodec.encodeAgentMessageCursor(*page.NextSeq, binding, deps.clock())
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "cursor encode failed", nil)
			return
		}
	}
	WriteJSON(w, http.StatusOK, messagesPage{Data: page.Items, Page: cursorPage{NextCursor: nextCursor}})
}

// sendMessageRequest is the body for POST /conversations/{id}/messages.
type sendMessageRequest struct {
	Content        string   `json:"content"`
	AllowedTools   []string `json:"allowed_tools,omitempty"`
	AllowMutations bool     `json:"allow_mutations,omitempty"`
}

// sendMessageResponse is the body returned by POST .../messages once
// the assistant turn finishes. Clients that want token-level deltas
// SHOULD open GET .../stream before issuing the POST.
type sendMessageResponse struct {
	UserMessage      conversation.Message `json:"user_message"`
	AssistantMessage conversation.Message `json:"assistant_message"`
	StopReason       string               `json:"stop_reason,omitempty"`
	StreamURL        string               `json:"stream_url"`
}

func sendMessage(w http.ResponseWriter, r *http.Request, deps AgentConversationHandlerDeps, id string) {
	body, ok := readBoundedJSONRequestBody(w, r, "invalid send message request body")
	if !ok {
		return
	}
	var request sendMessageRequest
	if !decodeOptionalStrictJSONRequestBytes(w, r, body, &request, "invalid send message request body") {
		return
	}
	if strings.TrimSpace(request.Content) == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "content is required", nil)
		return
	}

	actor := deps.principal(r)
	result, err := deps.Service.Send(r.Context(), conversation.SendInput{
		ConversationID: id,
		ActorID:        actor,
		Content:        request.Content,
		AllowedTools:   request.AllowedTools,
		AllowMutations: request.AllowMutations,
	}, nil)
	if errors.Is(err, conversation.ErrNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "agent conversation not found", map[string]any{"conversation_id": id})
		return
	}
	if errors.Is(err, conversation.ErrUnauthorized) {
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthRequiredMissing, "principal is not authorised for this scope", nil)
		return
	}
	if errors.Is(err, conversation.ErrCallerNotConfigured) {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.AgentNotConfigured, "agent caller is not configured on this server: set ANTHROPIC_API_KEY (or the key for MIDNIGHT_AGENT_PROVIDER) and restart midnight-server", map[string]any{"conversation_id": id})
		return
	}
	if errors.Is(err, conversation.ErrConversationRevoked) {
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, err.Error(), map[string]any{"conversation_id": id})
		return
	}
	var budgetErr *conversation.BudgetError
	if errors.As(err, &budgetErr) {
		details := map[string]any{
			"conversation_id":              id,
			"conversation_tokens_used":     budgetErr.Used.ConversationTokens,
			"conversation_tokens_limit":    budgetErr.Limit.ConversationTokens,
			"conversation_cost_cents_used": budgetErr.Used.ConversationCostCents,
			"conversation_cost_cents_cap":  budgetErr.Limit.ConversationCostCents,
			"host_daily_cost_cents_used":   budgetErr.Used.HostDailyCostCents,
			"host_daily_cost_cents_cap":    budgetErr.Limit.HostDailyCostCents,
		}
		if !budgetErr.RetryAt.IsZero() {
			// Only the host-daily cap rolls over; the per-conversation
			// caps do not, so Retry-After would be a lie for those.
			retryAfter := int(time.Until(budgetErr.RetryAt).Seconds())
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			details["retry_at"] = budgetErr.RetryAt.UTC().Format(time.RFC3339)
		}
		writeRequestError(w, r, http.StatusTooManyRequests, failurecodes.RequestRateLimited, budgetErr.Error(), details)
		return
	}
	if err != nil {
		log.Printf("agent send failed conversation=%s: %v", id, err)
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "agent send failed", map[string]any{"conversation_id": id, "reason": err.Error()})
		return
	}

	WriteJSON(w, http.StatusOK, sendMessageResponse{
		UserMessage:      result.UserMessage,
		AssistantMessage: result.AssistantMessage,
		StopReason:       result.StopReason,
		StreamURL:        AgentConversationsPrefix + "/" + id + "/stream",
	})
}

// streamConversation publishes the live SSE feed for the conversation.
// Frame schema (one event per `\n\n`-terminated block):
//
//	event: <type>      // delta|tool_use|tool_result|done|error|heartbeat
//	data:  <json>      // the StreamEvent payload (omitted for heartbeat)
//
// A `:heartbeat` SSE comment is written every 15 seconds so proxies do
// not idle-out the connection.
func streamConversation(w http.ResponseWriter, r *http.Request, deps AgentConversationHandlerDeps, id string) {
	if _, err := deps.Service.Get(r.Context(), id, deps.principal(r)); err != nil {
		if errors.Is(err, conversation.ErrNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "agent conversation not found", map[string]any{"conversation_id": id})
			return
		}
		if errors.Is(err, conversation.ErrUnauthorized) {
			writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthRequiredMissing, "principal is not authorised for this scope", nil)
			return
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "conversation get failed", map[string]any{"conversation_id": id})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "streaming not supported", nil)
		return
	}
	// Register the subscriber before making the successful handshake visible.
	// Clients use the flushed response headers as their readiness boundary; if
	// subscription happens afterwards, an immediate POST can publish and finish
	// the turn in that gap, permanently losing the terminal event.
	events, cancel := deps.Service.Subscribe(r.Context(), id, 64)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	revalidate := time.NewTicker(time.Second)
	defer revalidate.Stop()
	stillAuthorized := func() bool {
		_, err := deps.Service.Get(r.Context(), id, deps.principal(r))
		return err == nil
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-revalidate.C:
			if !stillAuthorized() {
				return
			}
		case <-heartbeat.C:
			if !stillAuthorized() {
				return
			}
			if _, err := io.WriteString(w, ":heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			if !stillAuthorized() {
				return
			}
			if err := writeSSEEvent(w, ev); err != nil {
				return
			}
			flusher.Flush()
			if ev.Kind == agent.EventDone || ev.Kind == agent.EventError {
				return
			}
		}
	}
}

func writeSSEEvent(w io.Writer, ev agent.StreamEvent) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "event: "+string(ev.Kind)+"\n"); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "data: "); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n\n")
	return err
}

// --- helpers ----------------------------------------------------------------

type cursorPage struct {
	NextCursor string `json:"next_cursor,omitempty"`
}

type conversationsPage struct {
	Data []conversation.Conversation `json:"data"`
	Page cursorPage                  `json:"page"`
}

type messagesPage struct {
	Data []conversation.Message `json:"data"`
	Page cursorPage             `json:"page"`
}

// agentReserveIdempotency is the optional-key variant of
// reserveIdempotencyOrAbort used by conversation start. When the
// caller omits Idempotency-Key the handler proceeds without
// deduplication. When the key is supplied, terminal replays return
// the stored response with `Idempotent-Replayed: true`.
//
// Returns (reference, replayed). replayed=true means the response has been
// fully written from cache and the caller MUST NOT continue.
func agentReserveIdempotency(w http.ResponseWriter, r *http.Request, store idempotency.Store, body []byte) (idempotency.Reference, bool) {
	key := idempotency.Key(r.Header.Get("Idempotency-Key"))
	if key == "" || store == nil {
		return idempotency.Reference{}, false
	}
	if err := idempotency.ValidateKey(key); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
		return idempotency.Reference{}, true
	}
	fingerprint := idempotencyRequestFingerprintForRoute(
		r,
		AgentConversationsPrefix,
		idempotency.BodyHashSHA256(body),
	)
	reservation, err := store.Reserve(r.Context(), key, fingerprint, 24*time.Hour)
	if errors.Is(err, idempotency.ErrKeyReusedDifferentBody) {
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
		return idempotency.Reference{}, true
	}
	if errors.Is(err, idempotency.ErrKeyReused) {
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
		return idempotency.Reference{}, true
	}
	if errors.Is(err, idempotency.ErrRequestInProgress) {
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
		return idempotency.Reference{}, true
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency reservation failed", nil)
		return idempotency.Reference{}, true
	}
	if reservation.IsTerminal() && reservation.Response != nil {
		stored := reservation.Response.Clone()
		w.Header().Set("Idempotent-Replayed", "true")
		for k, values := range stored.Header {
			for _, value := range values {
				w.Header().Add(k, value)
			}
		}
		status := stored.StatusCode
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(stored.Body)
		return idempotency.Reference{}, true
	}
	return reservation.Reference(), false
}

// agentTerminalError writes a JSON error response and, when an
// idempotency key is present, stores it for verbatim replay.
func agentTerminalError(w http.ResponseWriter, r *http.Request, store idempotency.Store, ref idempotency.Reference, status int, code failurecodes.Code, message string, details map[string]any) {
	body, err := marshalErrorResponse(status, code, message, middleware.FromContext(r.Context()), details)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "error response encode failed", details)
		return
	}
	stored := idempotency.StoredResponse{
		StatusCode: responseStatus(body, status),
		Header:     http.Header{"Content-Type": []string{"application/problem+json"}},
		Body:       body,
	}
	if ref.Key != "" && store != nil {
		if _, err := store.CompleteFailure(r.Context(), ref, stored); err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency failure completion failed", details)
			return
		}
	}
	for k, values := range stored.Header {
		for _, value := range values {
			w.Header().Add(k, value)
		}
	}
	w.WriteHeader(stored.StatusCode)
	_, _ = w.Write(body)
}
