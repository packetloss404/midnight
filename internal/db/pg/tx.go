// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package pg

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Tx is a thin wrapper over pgx.Tx that exposes the same Exec/Query/
// QueryRow contract as Pool. Callers that need to acquire a single
// connection without a transaction can use Pool's methods directly.
type Tx struct {
	inner pgx.Tx
}

// Exec proxies to pgx.Tx.Exec.
func (t *Tx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.inner.Exec(ctx, sql, args...)
}

// Query proxies to pgx.Tx.Query.
func (t *Tx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return t.inner.Query(ctx, sql, args...)
}

// QueryRow proxies to pgx.Tx.QueryRow.
func (t *Tx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return t.inner.QueryRow(ctx, sql, args...)
}

// Inner returns the underlying pgx.Tx. Callers should prefer the
// wrapper methods.
func (t *Tx) Inner() pgx.Tx { return t.inner }

// Conn is the smallest contract every Postgres-backed store accepts. A
// *Pool, a *Tx, or a *pgxpool.Conn all satisfy it. Stores accept Conn
// rather than *Pool so the same query helper composes with both
// transactional and non-transactional callers — exactly the seam
// INTERFACES_CONTROL_PLANE.md "Tx" type prescribes for repositories.
type Conn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
