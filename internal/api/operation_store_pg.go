// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// PostgresOperationStore is the Lane 1 persistence implementation of
// OperationStore backed by the operations + operation_events tables.
// It satisfies the same OperationStore interface as MemoryOperationStore
// so the router stays unchanged when wiring real Postgres.
type PostgresOperationStore struct {
	Pool *pg.Pool
	// IDFactory generates Operation IDs. Defaults to a 26-char base16
	// suffix on the panel-locked op_ prefix.
	IDFactory func() orailtypes.ID
	Now       func() time.Time
}

// NewPostgresOperationStore constructs the store with sensible defaults.
func NewPostgresOperationStore(pool *pg.Pool) *PostgresOperationStore {
	return &PostgresOperationStore{
		Pool:      pool,
		IDFactory: defaultOperationID,
		Now:       func() time.Time { return time.Now().UTC() },
	}
}

func defaultOperationID() orailtypes.ID {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Falling back to a timestamp keeps the contract intact even if
		// the kernel RNG is unavailable; the higher layer logs it.
		return orailtypes.ID(fmt.Sprintf("%s_%d", orailtypes.IDPrefixOperation, time.Now().UnixNano()))
	}
	return orailtypes.ID(fmt.Sprintf("%s_%s", orailtypes.IDPrefixOperation, hex.EncodeToString(b[:])))
}

// Create inserts a new operation row.
func (s *PostgresOperationStore) Create(ctx context.Context, operation orailtypes.Operation) (orailtypes.Operation, error) {
	if err := ctx.Err(); err != nil {
		return orailtypes.Operation{}, err
	}
	if s == nil || s.Pool == nil {
		return orailtypes.Operation{}, errors.New("PostgresOperationStore: pool not initialized")
	}
	if operation.State == "" {
		operation.State = orailtypes.OperationPending
	}
	if operation.ID == "" {
		operation.ID = s.IDFactory()
	}
	if err := operation.Validate(); err != nil {
		return orailtypes.Operation{}, err
	}
	now := s.Now()
	expiresAt := now.Add(24 * time.Hour)
	err := s.Pool.QueryRow(ctx,
		`INSERT INTO operations (id, kind, state, code, reason, expires_at, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		 ON CONFLICT (id) DO UPDATE SET
		    kind = EXCLUDED.kind,
		    state = EXCLUDED.state,
		    code = EXCLUDED.code,
		    reason = EXCLUDED.reason,
		    updated_at = EXCLUDED.updated_at
		 RETURNING created_at, updated_at`,
		string(operation.ID), operation.Kind, string(operation.State),
		nullableString(string(operation.Code)), nullableString(operation.Reason),
		expiresAt, now,
	).Scan(&operation.CreatedAt, &operation.UpdatedAt)
	if err != nil {
		return orailtypes.Operation{}, fmt.Errorf("operation insert: %w", err)
	}
	return operation, nil
}

// Get fetches an operation by id.
func (s *PostgresOperationStore) Get(ctx context.Context, id orailtypes.ID) (orailtypes.Operation, error) {
	if err := ctx.Err(); err != nil {
		return orailtypes.Operation{}, err
	}
	if s == nil || s.Pool == nil {
		return orailtypes.Operation{}, errors.New("PostgresOperationStore: pool not initialized")
	}
	var op orailtypes.Operation
	var kindNullable, codeNullable, reasonNullable string
	var stateRaw string
	err := s.Pool.QueryRow(ctx,
		`SELECT id, COALESCE(kind, ''), state, COALESCE(code, ''), COALESCE(reason, ''), created_at, updated_at
		 FROM operations WHERE id = $1`,
		string(id),
	).Scan(&op.ID, &kindNullable, &stateRaw, &codeNullable, &reasonNullable, &op.CreatedAt, &op.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return orailtypes.Operation{}, ErrOperationNotFound
	}
	if err != nil {
		return orailtypes.Operation{}, fmt.Errorf("operation get: %w", err)
	}
	op.Kind = kindNullable
	op.State = orailtypes.OperationState(stateRaw)
	op.Code = orailtypes.FailureCode(codeNullable)
	op.Reason = reasonNullable
	return op, nil
}

// List returns operations in creation order, optionally filtered.
func (s *PostgresOperationStore) List(ctx context.Context, filter OperationListFilter) ([]orailtypes.Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.Pool == nil {
		return nil, errors.New("PostgresOperationStore: pool not initialized")
	}
	statusArg := ""
	if filter.Status != nil {
		statusArg = string(*filter.Status)
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT id, COALESCE(kind, ''), state, COALESCE(code,''), COALESCE(reason,''), created_at, updated_at
		 FROM operations
		 WHERE ($1::text = '' OR kind = $1)
		   AND ($2::text = '' OR state = $2)
		 ORDER BY created_at ASC, id ASC`,
		filter.Kind, statusArg,
	)
	if err != nil {
		return nil, fmt.Errorf("operation list: %w", err)
	}
	defer rows.Close()
	out := make([]orailtypes.Operation, 0)
	for rows.Next() {
		var op orailtypes.Operation
		var stateRaw, codeRaw, reasonRaw, kindRaw string
		if err := rows.Scan(&op.ID, &kindRaw, &stateRaw, &codeRaw, &reasonRaw, &op.CreatedAt, &op.UpdatedAt); err != nil {
			return nil, fmt.Errorf("operation scan: %w", err)
		}
		op.Kind = kindRaw
		op.State = orailtypes.OperationState(stateRaw)
		op.Code = orailtypes.FailureCode(codeRaw)
		op.Reason = reasonRaw
		out = append(out, op)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListPage executes an indexable keyset read. It asks PostgreSQL for one extra
// row so HasMore is known without COUNT(*) or loading the full collection.
func (s *PostgresOperationStore) ListPage(ctx context.Context, filter OperationListFilter, after *OperationListCursor, limit int) (OperationListPage, error) {
	if err := ctx.Err(); err != nil {
		return OperationListPage{}, err
	}
	if s == nil || s.Pool == nil {
		return OperationListPage{}, errors.New("PostgresOperationStore: pool not initialized")
	}
	if limit <= 0 {
		return OperationListPage{}, orailtypes.ErrInvalidCursorPage
	}
	if after != nil {
		if after.CreatedAt.IsZero() || after.ID == "" {
			return OperationListPage{}, orailtypes.ErrInvalidCursorPage
		}
	}
	query, args := operationListPageSQL(filter, after, limit)
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return OperationListPage{}, fmt.Errorf("operation list page: %w", err)
	}
	defer rows.Close()
	records := make([]OperationListRecord, 0, limit+1)
	for rows.Next() {
		var record OperationListRecord
		var stateRaw, codeRaw, reasonRaw, kindRaw string
		if err := rows.Scan(
			&record.Operation.ID,
			&kindRaw,
			&stateRaw,
			&codeRaw,
			&reasonRaw,
			&record.CreatedAt,
			&record.Operation.UpdatedAt,
		); err != nil {
			return OperationListPage{}, fmt.Errorf("operation page scan: %w", err)
		}
		record.Operation.Kind = kindRaw
		record.Operation.State = orailtypes.OperationState(stateRaw)
		record.Operation.Code = orailtypes.FailureCode(codeRaw)
		record.Operation.Reason = reasonRaw
		record.CreatedAt = record.CreatedAt.UTC()
		record.Operation.CreatedAt = record.CreatedAt
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return OperationListPage{}, fmt.Errorf("operation list page rows: %w", err)
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	return OperationListPage{Records: records, HasMore: hasMore}, nil
}

// operationListPageSQL selects one of four fixed equality-predicate shapes.
// Avoiding `($n = ” OR column = $n)` lets PostgreSQL use the matching
// composite index even after a prepared statement switches to a generic plan.
func operationListPageSQL(filter OperationListFilter, after *OperationListCursor, limit int) (string, []any) {
	const projection = `SELECT id, COALESCE(kind, ''), state, COALESCE(code,''), COALESCE(reason,''), created_at, updated_at
		 FROM operations`
	query := projection
	args := make([]any, 0, 5)
	hasPredicate := false
	switch {
	case filter.Kind != "" && filter.Status != nil:
		query += ` WHERE kind = $1 AND state = $2`
		args = append(args, filter.Kind, string(*filter.Status))
		hasPredicate = true
	case filter.Kind != "":
		query += ` WHERE kind = $1`
		args = append(args, filter.Kind)
		hasPredicate = true
	case filter.Status != nil:
		query += ` WHERE state = $1`
		args = append(args, string(*filter.Status))
		hasPredicate = true
	}
	if after != nil {
		if hasPredicate {
			query += ` AND `
		} else {
			query += ` WHERE `
		}
		query += fmt.Sprintf(`(created_at, id) > ($%d, $%d)`, len(args)+1, len(args)+2)
		args = append(args, after.CreatedAt.UTC(), string(after.ID))
	}
	query += fmt.Sprintf(` ORDER BY created_at ASC, id ASC LIMIT $%d`, len(args)+1)
	args = append(args, limit+1)
	return query, args
}

// Transition updates the operation state with the public-state matrix
// guard.
func (s *PostgresOperationStore) Transition(ctx context.Context, id orailtypes.ID, state orailtypes.OperationState) (orailtypes.Operation, error) {
	if err := ctx.Err(); err != nil {
		return orailtypes.Operation{}, err
	}
	var op orailtypes.Operation
	err := s.Pool.WithTxOpts(ctx, pgx.TxOptions{}, func(tx *pg.Tx) error {
		var current string
		var kindRaw, codeRaw, reasonRaw string
		err := tx.QueryRow(ctx,
			`SELECT state, COALESCE(kind,''), COALESCE(code,''), COALESCE(reason,''), created_at
			 FROM operations WHERE id = $1 FOR UPDATE`,
			string(id),
		).Scan(&current, &kindRaw, &codeRaw, &reasonRaw, &op.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOperationNotFound
		}
		if err != nil {
			return fmt.Errorf("operation lock: %w", err)
		}
		from := orailtypes.OperationState(current)
		if err := orailtypes.ValidateOperationTransition(from, state); err != nil || isTerminalOperationState(from) {
			return ErrOperationInvalidTransition
		}
		op.ID = id
		op.Kind = kindRaw
		op.State = state
		op.Code = orailtypes.FailureCode(codeRaw)
		op.Reason = reasonRaw
		if err := op.Validate(); err != nil {
			return err
		}
		now := s.Now()
		if _, err := tx.Exec(ctx,
			`UPDATE operations SET state = $1, updated_at = $2,
			   resource_version = resource_version + 1
			 WHERE id = $3`,
			string(state), now, string(id),
		); err != nil {
			return fmt.Errorf("operation transition: %w", err)
		}
		op.UpdatedAt = now
		return nil
	})
	if err != nil {
		return orailtypes.Operation{}, err
	}
	return op, nil
}

// Fail records a terminal failed transition with code + reason.
func (s *PostgresOperationStore) Fail(ctx context.Context, id orailtypes.ID, code orailtypes.FailureCode, reason string) (orailtypes.Operation, error) {
	if err := ctx.Err(); err != nil {
		return orailtypes.Operation{}, err
	}
	var op orailtypes.Operation
	err := s.Pool.WithTxOpts(ctx, pgx.TxOptions{}, func(tx *pg.Tx) error {
		var current string
		var kindRaw string
		err := tx.QueryRow(ctx,
			`SELECT state, COALESCE(kind,''), created_at FROM operations WHERE id = $1 FOR UPDATE`,
			string(id),
		).Scan(&current, &kindRaw, &op.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOperationNotFound
		}
		if err != nil {
			return err
		}
		from := orailtypes.OperationState(current)
		if err := orailtypes.ValidateOperationTransition(from, orailtypes.OperationFailed); err != nil || isTerminalOperationState(from) {
			return ErrOperationInvalidTransition
		}
		op.ID = id
		op.Kind = kindRaw
		op.State = orailtypes.OperationFailed
		op.Code = code
		op.Reason = reason
		if err := op.Validate(); err != nil {
			return err
		}
		now := s.Now()
		if _, err := tx.Exec(ctx,
			`UPDATE operations SET state = $1, code = $2, reason = $3, updated_at = $4,
			   resource_version = resource_version + 1
			 WHERE id = $5`,
			string(orailtypes.OperationFailed), nullableString(string(code)), nullableString(reason),
			now, string(id),
		); err != nil {
			return err
		}
		op.UpdatedAt = now
		return nil
	})
	if err != nil {
		return orailtypes.Operation{}, err
	}
	return op, nil
}

// AppendEvent inserts an event row for the operation.
func (s *PostgresOperationStore) AppendEvent(ctx context.Context, event OperationEvent) (OperationEvent, error) {
	if err := ctx.Err(); err != nil {
		return OperationEvent{}, err
	}
	if event.OperationID == "" {
		return OperationEvent{}, ErrOperationNotFound
	}
	now := s.Now()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	err := s.Pool.WithTxOpts(ctx, pgx.TxOptions{}, func(tx *pg.Tx) error {
		// Serialize sequence allocation on the parent operation row. A
		// MAX(seq)+1 calculation without this lock lets concurrent writers
		// allocate the same sequence and race on the unique constraint.
		var lockedID string
		if err := tx.QueryRow(ctx,
			`SELECT id FROM operations WHERE id = $1 FOR UPDATE`,
			string(event.OperationID),
		).Scan(&lockedID); errors.Is(err, pgx.ErrNoRows) {
			return ErrOperationNotFound
		} else if err != nil {
			return fmt.Errorf("event op lock: %w", err)
		}
		var seq int
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(MAX(seq), 0) + 1 FROM operation_events WHERE operation_id = $1`,
			string(event.OperationID),
		).Scan(&seq); err != nil {
			return fmt.Errorf("event seq: %w", err)
		}
		event.Seq = seq
		if event.ID == "" {
			event.ID = fmt.Sprintf("%s-%d", event.OperationID, seq)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO operation_events (id, operation_id, seq, state, message, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			event.ID, string(event.OperationID), event.Seq, string(event.State), event.Message, event.CreatedAt,
		); err != nil {
			return fmt.Errorf("event insert: %w", err)
		}
		return nil
	})
	if err != nil {
		return OperationEvent{}, err
	}
	return event, nil
}

// ListEvents returns all events for an operation in seq order.
func (s *PostgresOperationStore) ListEvents(ctx context.Context, operationID orailtypes.ID) ([]OperationEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var exists bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operations WHERE id = $1)`, string(operationID)).Scan(&exists); err != nil {
		return nil, fmt.Errorf("event list exists: %w", err)
	}
	if !exists {
		return nil, ErrOperationNotFound
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT id, operation_id, seq, state, message, created_at
		 FROM operation_events WHERE operation_id = $1 ORDER BY seq ASC`,
		string(operationID),
	)
	if err != nil {
		return nil, fmt.Errorf("event list: %w", err)
	}
	defer rows.Close()
	out := make([]OperationEvent, 0)
	for rows.Next() {
		var e OperationEvent
		var stateRaw string
		if err := rows.Scan(&e.ID, &e.OperationID, &e.Seq, &stateRaw, &e.Message, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("event scan: %w", err)
		}
		e.State = orailtypes.OperationState(stateRaw)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
