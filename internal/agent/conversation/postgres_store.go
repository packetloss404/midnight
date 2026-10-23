// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/db/pg"
)

// PostgresStore is the Lane 1 persistence implementation. The schema
// is owned by migrations/0010_agent_conversations.up.sql.
type PostgresStore struct {
	Pool *pg.Pool
	Now  func() time.Time
}

// NewPostgresStore wires a PostgresStore using time.Now for timestamps.
func NewPostgresStore(pool *pg.Pool) *PostgresStore {
	return &PostgresStore{
		Pool: pool,
		Now:  func() time.Time { return time.Now().UTC() },
	}
}

func (s *PostgresStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// CreateConversation implements Store.
func (s *PostgresStore) CreateConversation(ctx context.Context, conv Conversation) (Conversation, error) {
	if err := ctx.Err(); err != nil {
		return Conversation{}, err
	}
	if conv.ID == "" {
		conv.ID = NewConversationID()
	}
	now := s.now()
	if conv.CreatedAt.IsZero() {
		conv.CreatedAt = now
	}
	conv.UpdatedAt = now
	if conv.Metadata == nil {
		conv.Metadata = map[string]any{}
	}
	metadata, err := json.Marshal(conv.Metadata)
	if err != nil {
		return Conversation{}, fmt.Errorf("conversation marshal metadata: %w", err)
	}
	_, err = s.Pool.Exec(ctx,
		`INSERT INTO agent_conversations
		   (id, project_id, service_id, actor_id, title, model, created_at, updated_at, metadata)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb)`,
		conv.ID,
		nullableString(conv.ProjectID),
		nullableString(conv.ServiceID),
		conv.ActorID,
		conv.Title,
		conv.Model,
		conv.CreatedAt,
		conv.UpdatedAt,
		string(metadata),
	)
	if err != nil {
		return Conversation{}, fmt.Errorf("conversation insert: %w", err)
	}
	return conv, nil
}

// GetConversation implements Store.
func (s *PostgresStore) GetConversation(ctx context.Context, id string) (Conversation, error) {
	if err := ctx.Err(); err != nil {
		return Conversation{}, err
	}
	return s.getConversation(ctx, s.Pool, id)
}

func (s *PostgresStore) getConversation(ctx context.Context, conn pg.Conn, id string) (Conversation, error) {
	var c Conversation
	var projectID, serviceID *string
	var metadata []byte
	err := conn.QueryRow(ctx,
		`SELECT id, project_id, service_id, actor_id, title, model, created_at, updated_at, metadata::text
		 FROM agent_conversations WHERE id = $1`,
		id,
	).Scan(&c.ID, &projectID, &serviceID, &c.ActorID, &c.Title, &c.Model, &c.CreatedAt, &c.UpdatedAt, &metadata)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("conversation get: %w", err)
	}
	if projectID != nil {
		c.ProjectID = *projectID
	}
	if serviceID != nil {
		c.ServiceID = *serviceID
	}
	c.CreatedAt = c.CreatedAt.UTC()
	c.UpdatedAt = c.UpdatedAt.UTC()
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &c.Metadata); err != nil {
			return Conversation{}, fmt.Errorf("conversation metadata decode: %w", err)
		}
	}
	if c.Metadata == nil {
		c.Metadata = map[string]any{}
	}
	return c, nil
}

// ListConversations implements Store.
func (s *PostgresStore) ListConversations(ctx context.Context, filter ListFilter) (ListResult, error) {
	if err := ctx.Err(); err != nil {
		return ListResult{}, err
	}
	if filter.After != nil && (filter.After.CreatedAt.IsZero() || filter.After.ID == "") {
		return ListResult{}, fmt.Errorf("%w: cursor", ErrInvalid)
	}
	limit, err := normalizePageLimit(filter.Limit)
	if err != nil {
		return ListResult{}, err
	}
	query, args := conversationListQuery(filter, limit+1)
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return ListResult{}, fmt.Errorf("conversation list: %w", err)
	}
	defer rows.Close()
	items := make([]Conversation, 0, limit)
	for rows.Next() {
		var c Conversation
		var metadata []byte
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.ServiceID, &c.ActorID, &c.Title, &c.Model,
			&c.CreatedAt, &c.UpdatedAt, &metadata); err != nil {
			return ListResult{}, fmt.Errorf("conversation scan: %w", err)
		}
		c.CreatedAt = c.CreatedAt.UTC()
		c.UpdatedAt = c.UpdatedAt.UTC()
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &c.Metadata); err != nil {
				return ListResult{}, fmt.Errorf("conversation metadata decode: %w", err)
			}
		}
		if c.Metadata == nil {
			c.Metadata = map[string]any{}
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}
	var next *ConversationListPosition
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next = &ConversationListPosition{CreatedAt: last.CreatedAt.UTC(), ID: last.ID}
	}
	return ListResult{Items: items, Next: next}, nil
}

// UpdateConversation implements Store.
func (s *PostgresStore) UpdateConversation(ctx context.Context, conv Conversation) (Conversation, error) {
	if err := ctx.Err(); err != nil {
		return Conversation{}, err
	}
	now := s.now()
	metadata, err := json.Marshal(conv.Metadata)
	if err != nil {
		return Conversation{}, fmt.Errorf("conversation marshal metadata: %w", err)
	}
	tag, err := s.Pool.Exec(ctx,
		`UPDATE agent_conversations
		 SET title = COALESCE(NULLIF($2,''), title),
		     model = COALESCE(NULLIF($3,''), model),
		     metadata = CASE WHEN $4::jsonb IS NULL OR $4::jsonb = 'null'::jsonb THEN metadata ELSE $4::jsonb END,
		     updated_at = $5
		 WHERE id = $1`,
		conv.ID, conv.Title, conv.Model, string(metadata), now,
	)
	if err != nil {
		return Conversation{}, fmt.Errorf("conversation update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Conversation{}, ErrNotFound
	}
	return s.GetConversation(ctx, conv.ID)
}

// DeleteConversation implements Store.
func (s *PostgresStore) DeleteConversation(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM agent_conversations WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("conversation delete: %w", err)
	}
	return nil
}

// AppendMessage implements Store. The append is run inside a
// transaction that first takes pg_advisory_xact_lock keyed on
// hashtext('midnight_agent_conv:<conv_id>') so concurrent appends to
// the same conversation cannot interleave.
func (s *PostgresStore) AppendMessage(ctx context.Context, msg Message) (Message, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	if msg.ConversationID == "" {
		return Message{}, fmt.Errorf("%w: conversation_id is required", ErrInvalid)
	}
	if msg.ID == "" {
		msg.ID = NewMessageID()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = s.now()
	}
	contentJSON, err := marshalContent(msg.Content)
	if err != nil {
		return Message{}, err
	}
	var usageJSON *string
	if msg.Usage != nil {
		usageBytes, err := json.Marshal(msg.Usage)
		if err != nil {
			return Message{}, fmt.Errorf("message marshal usage: %w", err)
		}
		usageStr := string(usageBytes)
		usageJSON = &usageStr
	}

	lockKey := advisoryLockKey(msg.ConversationID)
	err = s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey); err != nil {
			return fmt.Errorf("acquire advisory lock: %w", err)
		}
		// Verify the conversation exists under the lock.
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM agent_conversations WHERE id = $1)`,
			msg.ConversationID,
		).Scan(&exists); err != nil {
			return fmt.Errorf("conversation exists check: %w", err)
		}
		if !exists {
			return ErrNotFound
		}
		// Resolve next seq if the caller did not supply one.
		if msg.Seq == 0 {
			var current *int64
			if err := tx.QueryRow(ctx,
				`SELECT MAX(seq) FROM agent_messages WHERE conversation_id = $1`,
				msg.ConversationID,
			).Scan(&current); err != nil {
				return fmt.Errorf("seq lookup: %w", err)
			}
			next := int64(1)
			if current != nil {
				next = *current + 1
			}
			msg.Seq = next
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO agent_messages
			   (id, conversation_id, seq, role, content, usage, created_at)
			 VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7)`,
			msg.ID, msg.ConversationID, msg.Seq, string(msg.Role), contentJSON, usageJSON, msg.CreatedAt,
		)
		if err != nil {
			return fmt.Errorf("message insert: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE agent_conversations SET updated_at = $2 WHERE id = $1`,
			msg.ConversationID, s.now(),
		); err != nil {
			return fmt.Errorf("conversation touch: %w", err)
		}
		return nil
	})
	if err != nil {
		return Message{}, err
	}
	return msg, nil
}

// ListMessages implements Store.
func (s *PostgresStore) ListMessages(ctx context.Context, conversationID string, opts MessageListOptions) (MessagePage, error) {
	if err := ctx.Err(); err != nil {
		return MessagePage{}, err
	}
	// Validate conversation exists so callers can distinguish "no
	// messages yet" from "no such conversation".
	var exists bool
	if err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM agent_conversations WHERE id = $1)`,
		conversationID,
	).Scan(&exists); err != nil {
		return MessagePage{}, fmt.Errorf("conversation exists check: %w", err)
	}
	if !exists {
		return MessagePage{}, ErrNotFound
	}
	if opts.AfterSeq != nil && *opts.AfterSeq <= 0 {
		return MessagePage{}, fmt.Errorf("%w: cursor", ErrInvalid)
	}

	limit, err := normalizePageLimit(opts.Limit)
	if err != nil {
		return MessagePage{}, err
	}
	order := "ASC"
	comparison := ">"
	if opts.Reverse {
		order = "DESC"
		comparison = "<"
	}
	query, args := messageListQuery(conversationID, opts.AfterSeq, comparison, order, limit+1)
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return MessagePage{}, fmt.Errorf("message list: %w", err)
	}
	defer rows.Close()
	items := make([]Message, 0, limit)
	for rows.Next() {
		var m Message
		var roleStr string
		var content []byte
		var usage *string
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Seq, &roleStr, &content, &usage, &m.CreatedAt); err != nil {
			return MessagePage{}, fmt.Errorf("message scan: %w", err)
		}
		m.Role = agent.MessageRole(roleStr)
		m.CreatedAt = m.CreatedAt.UTC()
		if len(content) > 0 {
			if err := json.Unmarshal(content, &m.Content); err != nil {
				return MessagePage{}, fmt.Errorf("message content decode: %w", err)
			}
		}
		if usage != nil && *usage != "" {
			var u agent.Usage
			if err := json.Unmarshal([]byte(*usage), &u); err != nil {
				return MessagePage{}, fmt.Errorf("message usage decode: %w", err)
			}
			m.Usage = &u
		}
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return MessagePage{}, err
	}
	var next *int64
	if len(items) > limit {
		items = items[:limit]
		value := items[len(items)-1].Seq
		next = &value
	}
	return MessagePage{Items: items, NextSeq: next}, nil
}

func conversationListQuery(filter ListFilter, fetchLimit int) (string, []any) {
	const selectSQL = `SELECT id, COALESCE(project_id, ''), COALESCE(service_id, ''), actor_id, title, model,
       created_at, updated_at, metadata::text
FROM agent_conversations`

	clauses := make([]string, 0, 4)
	args := make([]any, 0, 6)
	addEquality := func(column, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	addEquality("project_id", filter.ProjectID)
	addEquality("service_id", filter.ServiceID)
	addEquality("actor_id", filter.ActorID)
	if filter.After != nil {
		args = append(args, filter.After.CreatedAt.UTC(), filter.After.ID)
		clauses = append(clauses, fmt.Sprintf("(created_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}

	var query strings.Builder
	query.WriteString(selectSQL)
	if len(clauses) > 0 {
		query.WriteString("\nWHERE ")
		query.WriteString(strings.Join(clauses, " AND "))
	}
	args = append(args, fetchLimit)
	fmt.Fprintf(&query, "\nORDER BY created_at DESC, id DESC\nLIMIT $%d", len(args))
	return query.String(), args
}

func messageListQuery(conversationID string, afterSeq *int64, comparison, order string, fetchLimit int) (string, []any) {
	query := `SELECT id, conversation_id, seq, role, content::text, usage::text, created_at
FROM agent_messages
WHERE conversation_id = $1`
	args := []any{conversationID}
	if afterSeq != nil {
		args = append(args, *afterSeq)
		query += fmt.Sprintf("\n  AND seq %s $%d", comparison, len(args))
	}
	args = append(args, fetchLimit)
	query += fmt.Sprintf("\nORDER BY seq %s\nLIMIT $%d", order, len(args))
	return query, args
}

func marshalContent(blocks []agent.ContentBlock) (string, error) {
	if blocks == nil {
		blocks = []agent.ContentBlock{}
	}
	data, err := json.Marshal(blocks)
	if err != nil {
		return "", fmt.Errorf("content marshal: %w", err)
	}
	return string(data), nil
}

// advisoryLockKey computes a stable int64 from "midnight_agent_conv:<id>".
// pg_advisory_xact_lock requires a bigint; we map the FNV-1a 64-bit
// hash directly so distinct conversation ids contend on distinct locks
// while equal ids serialise.
func advisoryLockKey(conversationID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("midnight_agent_conv:" + conversationID))
	return int64(h.Sum64()) //nolint:gosec // intentional bit reinterpretation
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
