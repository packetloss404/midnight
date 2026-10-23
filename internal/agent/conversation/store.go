// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package conversation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Store is the persistence contract. Both the in-memory and
// Postgres-backed implementations satisfy it. Callers SHOULD use the
// Service wrapper rather than touching the store directly.
type Store interface {
	// CreateConversation inserts a new conversation row. The caller
	// MAY pre-set ID; otherwise the store allocates a conv_* id.
	CreateConversation(ctx context.Context, conv Conversation) (Conversation, error)

	// GetConversation returns a conversation by id. ErrNotFound is
	// returned for misses.
	GetConversation(ctx context.Context, id string) (Conversation, error)

	// ListConversations returns a page of conversations matching
	// filter, ordered by immutable (created_at,id) descending.
	ListConversations(ctx context.Context, filter ListFilter) (ListResult, error)

	// UpdateConversation refreshes title / model / metadata /
	// updated_at. The store guarantees updated_at is bumped to the
	// current clock time.
	UpdateConversation(ctx context.Context, conv Conversation) (Conversation, error)

	// DeleteConversation removes the conversation and cascades to all
	// messages. Idempotent: returns nil on missing conversations so
	// REST DELETE can return 204 without a Get-before-Delete race.
	DeleteConversation(ctx context.Context, id string) error

	// AppendMessage appends a message to a conversation. The store is
	// responsible for allocating the next monotonic seq and the msg_*
	// id when the caller leaves them empty. AppendMessage also bumps
	// the parent conversation's updated_at.
	AppendMessage(ctx context.Context, msg Message) (Message, error)

	// ListMessages returns a page of messages ordered by seq.
	ListMessages(ctx context.Context, conversationID string, opts MessageListOptions) (MessagePage, error)
}

// generateID returns a deterministic-looking but unique id of the form
// "<prefix>_<20-hex>" — wide enough that random collisions are
// effectively impossible while keeping the wire shape compact.
func generateID(prefix string) string {
	var buf [10]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Fall back to a nanosecond suffix; randomness failure is
		// non-fatal because the (conversation_id, seq) unique
		// constraint catches any collision at insert time.
		return fmt.Sprintf("%s_%020x", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(buf[:]))
}

// NewConversationID returns a fresh `conv_*` id.
func NewConversationID() string { return generateID(ConversationIDPrefix) }

// NewMessageID returns a fresh `msg_*` id.
func NewMessageID() string { return generateID(MessageIDPrefix) }
