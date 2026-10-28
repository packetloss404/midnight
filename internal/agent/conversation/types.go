// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package conversation persists Midnight's AI agent chat panel:
// conversations, messages, content blocks, and token usage. The HTTP
// surface lives in internal/api/agent.go; the model loop lives in
// internal/agent/agent.go (Caller seam).
package conversation

import (
	"errors"
	"fmt"
	"time"

	"github.com/midnight/midnight/internal/agent"
)

// ID prefixes for the two resource kinds owned by this package. The
// taxonomy is documented in ADR-0024.
const (
	ConversationIDPrefix = "conv"
	MessageIDPrefix      = "msg"
	DefaultPageLimit     = 50
	MaxPageLimit         = 200
)

// Sentinel errors returned by the Store and Service layers.
var (
	// ErrNotFound is returned by Get / List filters that miss.
	ErrNotFound = errors.New("conversation: not found")
	// ErrInvalid is returned when caller input fails validation at the
	// store boundary.
	ErrInvalid = errors.New("conversation: invalid")
	// ErrConflict is returned when an append would violate the
	// (conversation_id, seq) uniqueness invariant.
	ErrConflict = errors.New("conversation: conflict")
	// ErrUnauthorized is returned when a ProjectScopeAuthorizer denies
	// access to a conversation.
	ErrUnauthorized = errors.New("conversation: unauthorized")
)

// Conversation is the persisted shell. Messages live in a separate
// table addressed by `(conversation_id, seq)`.
type Conversation struct {
	ID        string         `json:"id"`
	ProjectID string         `json:"project_id,omitempty"`
	ServiceID string         `json:"service_id,omitempty"`
	ActorID   string         `json:"actor_id,omitempty"`
	Title     string         `json:"title,omitempty"`
	Model     string         `json:"model,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Message is one append-only row in a conversation. Content is an
// array of agent.ContentBlock values so the (text | tool_use |
// tool_result) shape is preserved without flattening.
type Message struct {
	ID             string               `json:"id"`
	ConversationID string               `json:"conversation_id"`
	Seq            int64                `json:"seq"`
	Role           agent.MessageRole    `json:"role"`
	Content        []agent.ContentBlock `json:"content"`
	Usage          *agent.Usage         `json:"usage,omitempty"`
	CreatedAt      time.Time            `json:"created_at"`
}

// ToAgentMessage converts a persisted Message into the model-facing
// shape consumed by agent.Caller. The shape is intentionally identical
// in field names so JSON marshalling round-trips cleanly.
func (m Message) ToAgentMessage() agent.Message {
	return agent.Message{Role: m.Role, Content: append([]agent.ContentBlock(nil), m.Content...)}
}

// ListFilter narrows ListConversations. ProjectID and ServiceID empty
// values are treated as "no filter on this field". After is an exclusive
// (created_at,id) keyset position in the fixed newest-first ordering.
type ListFilter struct {
	ProjectID string
	ServiceID string
	ActorID   string
	// IncludeSystemDiagnosis is set only by the trusted scope policy, never
	// from HTTP input. Shared reports are still individually authorized.
	IncludeSystemDiagnosis        bool
	ReaderUserID                  string // trusted durable-policy input; never accepted from HTTP
	SharedDiagnosisWorkspaceRoles []string
	SharedDiagnosisProjectRoles   []string
	Limit                         int
	After                         *ConversationListPosition
}

// ConversationListPosition is the immutable storage key embedded in the
// HTTP layer's integrity-protected conversation-list cursor.
type ConversationListPosition struct {
	CreatedAt time.Time
	ID        string
}

// ListResult bundles a page of conversations and the next-cursor token.
// The Cursor is empty when no further pages are available.
type ListResult struct {
	Items []Conversation
	Next  *ConversationListPosition
}

// MessagePage is the paginated message-history shape.
type MessagePage struct {
	Items   []Message
	NextSeq *int64
}

// MessageListOptions narrows ListMessages.
type MessageListOptions struct {
	Limit int
	// AfterSeq is an exclusive keyset position. In ascending order rows must
	// have seq > AfterSeq; in reverse order rows must have seq < AfterSeq.
	AfterSeq *int64
	// Reverse returns the latest messages first. Default is ascending
	// by seq for replay-friendly transport.
	Reverse bool
}

func normalizePageLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultPageLimit, nil
	}
	if limit < 1 || limit > MaxPageLimit {
		return 0, fmt.Errorf("%w: limit", ErrInvalid)
	}
	return limit, nil
}
