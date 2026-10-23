// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolConfig controls Pool tuning. Zero-value fields fall back to the
// pgxpool defaults; the panel-cited statement_timeout/idle limits live
// in pg_hba/role grants and are not duplicated here.
type PoolConfig struct {
	DSN             string
	MaxConns        int32
	MinConns        int32
	HealthCheckTime time.Duration
	ConnectTimeout  time.Duration
}

// Pool is a thin wrapper around pgxpool.Pool. It exists so callers can
// see a single import path for "the Postgres seam" without the rest of
// the codebase taking a hard dependency on pgx types beyond the small
// surface re-exported by this package.
type Pool struct {
	inner *pgxpool.Pool
}

// ErrTxCommitIndeterminate marks a COMMIT whose acknowledgement was lost
// after it may have reached PostgreSQL. Callers must verify exact durable state
// before deciding whether to compensate an external side effect.
var ErrTxCommitIndeterminate = errors.New("postgres transaction commit outcome is indeterminate")

type txCommitIndeterminateError struct {
	cause error
}

func (e *txCommitIndeterminateError) Error() string {
	return ErrTxCommitIndeterminate.Error() + ": " + e.cause.Error()
}

func (e *txCommitIndeterminateError) Unwrap() []error {
	return []error{ErrTxCommitIndeterminate, e.cause}
}

func commitOutcomeIndeterminate(err error) bool {
	if err == nil ||
		errors.Is(err, pgx.ErrTxCommitRollback) ||
		pgconn.SafeToRetry(err) {
		return false
	}
	var postgresError *pgconn.PgError
	return !errors.As(err, &postgresError)
}

// Open opens a Pool against the supplied PoolConfig.DSN. The caller
// MUST call Close when shutting down.
func Open(ctx context.Context, cfg PoolConfig) (*Pool, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("pg.Open: DSN is required")
	}
	pgxConfig, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("pg.Open: parse dsn: %w", err)
	}
	if cfg.MaxConns > 0 {
		pgxConfig.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		pgxConfig.MinConns = cfg.MinConns
	}
	if cfg.HealthCheckTime > 0 {
		pgxConfig.HealthCheckPeriod = cfg.HealthCheckTime
	}
	if cfg.ConnectTimeout > 0 {
		pgxConfig.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	}
	pool, err := pgxpool.NewWithConfig(ctx, pgxConfig)
	if err != nil {
		return nil, fmt.Errorf("pg.Open: new pool: %w", err)
	}
	return &Pool{inner: pool}, nil
}

// Close drains the pool. Safe to call on a nil Pool.
func (p *Pool) Close() {
	if p == nil || p.inner == nil {
		return
	}
	p.inner.Close()
}

// Ping verifies the pool can hand out a connection.
func (p *Pool) Ping(ctx context.Context) error {
	if p == nil || p.inner == nil {
		return errors.New("pg.Pool: not initialized")
	}
	return p.inner.Ping(ctx)
}

// Inner exposes the underlying pgxpool.Pool. Callers should prefer the
// methods on Pool/Tx and reach for Inner() only when they need a
// feature pgx exposes that this wrapper does not.
func (p *Pool) Inner() *pgxpool.Pool {
	if p == nil {
		return nil
	}
	return p.inner
}

// Exec mirrors pgxpool.Pool.Exec.
func (p *Pool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return p.inner.Exec(ctx, sql, args...)
}

// Query mirrors pgxpool.Pool.Query.
func (p *Pool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return p.inner.Query(ctx, sql, args...)
}

// QueryRow mirrors pgxpool.Pool.QueryRow.
func (p *Pool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return p.inner.QueryRow(ctx, sql, args...)
}

// Acquire mirrors pgxpool.Pool.Acquire.
func (p *Pool) Acquire(ctx context.Context) (*pgxpool.Conn, error) {
	return p.inner.Acquire(ctx)
}

// WithTx runs fn using pgx/PostgreSQL's default transaction options (currently
// READ COMMITTED). Callers that require a stronger isolation guarantee must
// use WithTxOpts explicitly. The wrapper rolls back on panic and on any
// non-nil error returned by fn. Callers MUST NOT begin a nested transaction
// inside fn; pgx supports savepoints if that is ever needed.
func (p *Pool) WithTx(ctx context.Context, fn func(tx *Tx) error) error {
	return p.WithTxOpts(ctx, pgx.TxOptions{}, fn)
}

// WithTxOpts is the explicit-options variant. Most callers want the
// default WithTx; this seam exists for callers (notably the migration
// runner) that need a specific isolation level.
func (p *Pool) WithTxOpts(ctx context.Context, opts pgx.TxOptions, fn func(tx *Tx) error) error {
	if p == nil || p.inner == nil {
		return errors.New("pg.Pool: not initialized")
	}
	return withTxOpts(ctx, p.inner, opts, fn)
}

// WithSessionAdvisoryLockTxOpts acquires one PostgreSQL session advisory lock
// on a dedicated pooled connection before beginning the requested transaction.
// This is intentionally distinct from acquiring pg_advisory_xact_lock inside a
// REPEATABLE READ transaction: the lock statement itself can establish that
// transaction's snapshot before it blocks. Callers that need a post-lock
// snapshot can use this helper and perform every proof read in fn.
//
// The session lock is always released before the connection returns to the
// pool. If explicit unlock cannot be proved, the connection is removed from the
// pool and closed so a leaked session lock cannot be handed to another caller.
func (p *Pool) WithSessionAdvisoryLockTxOpts(
	ctx context.Context,
	lockKey string,
	opts pgx.TxOptions,
	fn func(tx *Tx) error,
) (returnErr error) {
	if p == nil || p.inner == nil {
		return errors.New("pg.Pool: not initialized")
	}
	if lockKey == "" {
		return errors.New("pg.Pool: advisory lock key is required")
	}
	conn, err := p.inner.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("pg.WithSessionAdvisoryLockTx: acquire: %w", err)
	}
	locked := false
	poisoned := false
	defer func() {
		if poisoned {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			raw := conn.Hijack()
			if closeErr := raw.Close(closeCtx); returnErr == nil && closeErr != nil {
				returnErr = fmt.Errorf(
					"pg.WithSessionAdvisoryLockTx: close poisoned connection: %w",
					closeErr,
				)
			}
			return
		}
		if !locked {
			conn.Release()
			return
		}
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		unlockErr := conn.QueryRow(
			unlockCtx,
			`SELECT pg_advisory_unlock(hashtext($1))`,
			lockKey,
		).Scan(&unlocked)
		if unlockErr == nil && unlocked {
			conn.Release()
			return
		}
		raw := conn.Hijack()
		closeErr := raw.Close(unlockCtx)
		if returnErr == nil {
			switch {
			case unlockErr != nil:
				returnErr = fmt.Errorf(
					"pg.WithSessionAdvisoryLockTx: unlock: %w",
					unlockErr,
				)
			case !unlocked:
				returnErr = errors.New(
					"pg.WithSessionAdvisoryLockTx: advisory lock was not held at unlock",
				)
			case closeErr != nil:
				returnErr = fmt.Errorf(
					"pg.WithSessionAdvisoryLockTx: close poisoned connection: %w",
					closeErr,
				)
			}
		}
	}()
	if _, err := conn.Exec(
		ctx,
		`SELECT pg_advisory_lock(hashtext($1))`,
		lockKey,
	); err != nil {
		// A canceled or lost acknowledgement cannot prove that PostgreSQL did
		// not acquire the session lock. Never return this connection to the
		// pool; closing its backend is the only safe unlock.
		poisoned = true
		return fmt.Errorf("pg.WithSessionAdvisoryLockTx: lock: %w", err)
	}
	locked = true
	return withTxOpts(ctx, conn, opts, fn)
}

type txBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

func withTxOpts(
	ctx context.Context,
	beginner txBeginner,
	opts pgx.TxOptions,
	fn func(tx *Tx) error,
) error {
	pgxTx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("pg.WithTx: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = pgxTx.Rollback(ctx)
		}
	}()
	tx := &Tx{inner: pgxTx}
	if err := fn(tx); err != nil {
		return err
	}
	if err := pgxTx.Commit(ctx); err != nil {
		if commitOutcomeIndeterminate(err) {
			err = &txCommitIndeterminateError{cause: err}
		}
		return fmt.Errorf("pg.WithTx: commit: %w", err)
	}
	committed = true
	return nil
}
