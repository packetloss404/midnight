// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package conversation

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore is the dev/test implementation of Store. Lookups are O(1) and
// list pagination follows the same exclusive keyset semantics as Postgres.
type MemoryStore struct {
	mu            sync.Mutex
	now           func() time.Time
	conversations map[string]*Conversation
	order         []string
	messages      map[string][]Message
}

// NewMemoryStore returns an empty in-memory store. The supplied clock
// is used for timestamping; pass nil for time.Now.
func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemoryStore{
		now:           now,
		conversations: make(map[string]*Conversation),
		messages:      make(map[string][]Message),
	}
}

// CreateConversation implements Store.
func (s *MemoryStore) CreateConversation(ctx context.Context, conv Conversation) (Conversation, error) {
	if err := ctx.Err(); err != nil {
		return Conversation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if conv.ID == "" {
		conv.ID = NewConversationID()
	}
	if _, exists := s.conversations[conv.ID]; exists {
		return Conversation{}, fmt.Errorf("%w: id %q", ErrConflict, conv.ID)
	}
	now := s.now().UTC()
	if conv.CreatedAt.IsZero() {
		conv.CreatedAt = now
	}
	conv.UpdatedAt = now
	if conv.Metadata == nil {
		conv.Metadata = map[string]any{}
	}
	stored := conv
	s.conversations[conv.ID] = &stored
	s.order = append(s.order, conv.ID)
	return stored, nil
}

// GetConversation implements Store.
func (s *MemoryStore) GetConversation(ctx context.Context, id string) (Conversation, error) {
	if err := ctx.Err(); err != nil {
		return Conversation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conv, ok := s.conversations[id]
	if !ok {
		return Conversation{}, ErrNotFound
	}
	return *conv, nil
}

// ListConversations implements Store.
func (s *MemoryStore) ListConversations(ctx context.Context, filter ListFilter) (ListResult, error) {
	if err := ctx.Err(); err != nil {
		return ListResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if filter.After != nil && (filter.After.CreatedAt.IsZero() || filter.After.ID == "") {
		return ListResult{}, fmt.Errorf("%w: cursor", ErrInvalid)
	}

	items := make([]Conversation, 0, len(s.order))
	for _, id := range s.order {
		c := s.conversations[id]
		if filter.ProjectID != "" && c.ProjectID != filter.ProjectID {
			continue
		}
		if filter.ServiceID != "" && c.ServiceID != filter.ServiceID {
			continue
		}
		if filter.ActorID != "" && c.ActorID != filter.ActorID &&
			!(filter.IncludeSystemDiagnosis && c.ActorID == "agent:diagnosis" && c.Metadata["midnight.kind"] == "diagnosis") {
			continue
		}
		if filter.After != nil && (c.CreatedAt.After(filter.After.CreatedAt) ||
			(c.CreatedAt.Equal(filter.After.CreatedAt) && c.ID >= filter.After.ID)) {
			continue
		}
		items = append(items, *c)
	}
	// Latest created first; ties broken by ID for a total immutable order.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})

	limit, err := normalizePageLimit(filter.Limit)
	if err != nil {
		return ListResult{}, err
	}
	end := limit
	var next *ConversationListPosition
	if end < len(items) {
		last := items[end-1]
		next = &ConversationListPosition{CreatedAt: last.CreatedAt.UTC(), ID: last.ID}
	}
	if end > len(items) {
		end = len(items)
	}
	return ListResult{Items: items[:end], Next: next}, nil
}

// UpdateConversation implements Store.
func (s *MemoryStore) UpdateConversation(ctx context.Context, conv Conversation) (Conversation, error) {
	if err := ctx.Err(); err != nil {
		return Conversation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.conversations[conv.ID]
	if !ok {
		return Conversation{}, ErrNotFound
	}
	if conv.Title != "" {
		stored.Title = conv.Title
	}
	if conv.Model != "" {
		stored.Model = conv.Model
	}
	if conv.Metadata != nil {
		stored.Metadata = conv.Metadata
	}
	stored.UpdatedAt = s.now().UTC()
	return *stored, nil
}

// DeleteConversation implements Store.
func (s *MemoryStore) DeleteConversation(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conversations[id]; !ok {
		return nil
	}
	delete(s.conversations, id)
	delete(s.messages, id)
	for i, existing := range s.order {
		if existing == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

// AppendMessage implements Store.
func (s *MemoryStore) AppendMessage(ctx context.Context, msg Message) (Message, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conv, ok := s.conversations[msg.ConversationID]
	if !ok {
		return Message{}, ErrNotFound
	}
	if msg.ID == "" {
		msg.ID = NewMessageID()
	}
	existing := s.messages[conv.ID]
	if msg.Seq == 0 {
		msg.Seq = int64(len(existing) + 1)
	}
	for _, prior := range existing {
		if prior.Seq == msg.Seq {
			return Message{}, fmt.Errorf("%w: seq %d", ErrConflict, msg.Seq)
		}
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = s.now().UTC()
	}
	if msg.Content == nil {
		msg.Content = nil
	}
	s.messages[conv.ID] = append(existing, msg)
	conv.UpdatedAt = s.now().UTC()
	return msg, nil
}

// ListMessages implements Store.
func (s *MemoryStore) ListMessages(ctx context.Context, conversationID string, opts MessageListOptions) (MessagePage, error) {
	if err := ctx.Err(); err != nil {
		return MessagePage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.conversations[conversationID]; !ok {
		return MessagePage{}, ErrNotFound
	}
	if opts.AfterSeq != nil && *opts.AfterSeq <= 0 {
		return MessagePage{}, fmt.Errorf("%w: cursor", ErrInvalid)
	}
	src := s.messages[conversationID]
	items := make([]Message, 0, len(src))
	for _, message := range src {
		if opts.AfterSeq != nil && ((!opts.Reverse && message.Seq <= *opts.AfterSeq) ||
			(opts.Reverse && message.Seq >= *opts.AfterSeq)) {
			continue
		}
		items = append(items, message)
	}
	if opts.Reverse {
		sort.SliceStable(items, func(i, j int) bool { return items[i].Seq > items[j].Seq })
	} else {
		sort.SliceStable(items, func(i, j int) bool { return items[i].Seq < items[j].Seq })
	}

	limit, err := normalizePageLimit(opts.Limit)
	if err != nil {
		return MessagePage{}, err
	}
	end := limit
	var next *int64
	if end < len(items) {
		value := items[end-1].Seq
		next = &value
	}
	if end > len(items) {
		end = len(items)
	}
	return MessagePage{Items: items[:end], NextSeq: next}, nil
}
