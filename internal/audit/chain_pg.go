// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
)

// auditChainAdvisoryLockKey is hashtext('midnight_audit_chain'). The
// PostgresChain acquires this transaction-scoped advisory lock for
// every Append + InsertCheckpoint so concurrent writers serialize
// through the chain head per ADR-011 §Decision item 2 ("Append
// acquires an advisory lock on the chain head, computes row_hash from
// the previous row, and inserts atomically").
//
// The literal value was derived offline via
//
//	SELECT hashtext('midnight_audit_chain')::bigint
//
// against Postgres 16, matching the SCHEMA_NOTES.md panel-locked
// pattern used by the migration runner's own advisory key. The hash
// is stable across Postgres versions per the documented contract.
const auditChainAdvisoryLockKey int64 = 4_911_374_188_220_140_519

// PostgresChain is the Lane 5 implementation of AuditChain backed by
// the audit_logs + audit_checkpoints tables Lane 1 migrated in
// 0001_alpha_core.up.sql (plus the partition_label column added in
// 0006_audit_checkpoint_partition_label.up.sql).
//
// Append + InsertCheckpoint serialize through pg_advisory_xact_lock so
// concurrent writers cannot race the chain head. The row sequence is
// computed inside the same transaction as the insert via
// COALESCE(MAX(id), 0) + 1 so canonical_row (which includes the
// row id) is byte-identical to what the verifier later recomputes.
type PostgresChain struct {
	Pool *pg.Pool
	Now  func() time.Time
}

// NewPostgresChain constructs a chain with sensible defaults.
func NewPostgresChain(pool *pg.Pool) *PostgresChain {
	return &PostgresChain{Pool: pool, Now: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the default UTC clock used to stamp recorded_at
// when callers do not pre-populate it. Tests use this to keep
// timestamps deterministic.
func (c *PostgresChain) SetClock(fn func() time.Time) {
	if c == nil {
		return
	}
	c.Now = fn
}

// Append implements AuditChain. The serialized contract:
//
//  1. Acquire pg_advisory_xact_lock(auditChainAdvisoryLockKey).
//  2. SELECT max(id), row_hash from audit_logs ORDER BY id DESC LIMIT 1
//     to pick up the chain head (or GenesisHash() when empty).
//  3. Set r.Sequence = max+1, fill prev_hash + row_hash via FillChain.
//  4. INSERT the row with the explicit id.
//
// The whole sequence runs inside a single transaction; rollbacks on
// any failure leave the chain head untouched.
func (c *PostgresChain) Append(ctx context.Context, r Row) (Row, error) {
	if c == nil || c.Pool == nil {
		return Row{}, errors.New("audit.PostgresChain: pool not initialized")
	}
	r = c.prepareRow(r)

	var inserted Row
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{}, func(tx *pg.Tx) error {
		var err error
		inserted, err = c.AppendInTx(ctx, tx, r)
		return err
	})
	if err != nil {
		return Row{}, err
	}
	return inserted, nil
}

// AppendInTx appends an integrity-protected row using a transaction owned by
// the caller. It exists for product mutations whose authorization, domain
// write, idempotent response, and audit evidence must commit atomically.
//
// The method acquires the same chain-head advisory lock as Append. Callers
// should acquire product-scope locks before invoking it and must not append
// another audit row through a separate connection while the transaction is
// open.
func (c *PostgresChain) AppendInTx(ctx context.Context, tx *pg.Tx, r Row) (Row, error) {
	if c == nil || c.Pool == nil || tx == nil {
		return Row{}, errors.New("audit.PostgresChain: pool and transaction are required")
	}
	r = c.prepareRow(r)

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", auditChainAdvisoryLockKey); err != nil {
		return Row{}, fmt.Errorf("acquire advisory lock: %w", err)
	}
	var maxID int64
	var prev []byte
	err := tx.QueryRow(ctx,
		`SELECT id, row_hash FROM audit_logs ORDER BY id DESC LIMIT 1`,
	).Scan(&maxID, &prev)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			prev = GenesisHash()
			maxID = 0
		} else {
			return Row{}, fmt.Errorf("read chain head: %w", err)
		}
	}
	r.Sequence = maxID + 1
	if err := FillChain(&r, prev); err != nil {
		return Row{}, fmt.Errorf("fill chain: %w", err)
	}

	beforeJSON, err := jsonOrNull(r.Before)
	if err != nil {
		return Row{}, fmt.Errorf("marshal before: %w", err)
	}
	afterJSON, err := jsonOrNull(r.After)
	if err != nil {
		return Row{}, fmt.Errorf("marshal after: %w", err)
	}
	attrsJSON, err := jsonOrNull(r.Attrs)
	if err != nil {
		return Row{}, fmt.Errorf("marshal attrs: %w", err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_logs (
                id, prev_hash, row_hash, canonical_form_version,
                request_id, trace_id, occurred_at, recorded_at,
                actor_id, actor_type, auth_method, source_ip, user_agent,
                workspace_id, project_id, environment_id,
                resource_kind, resource_id, action,
                before, after, reason_code, policy_decision, attrs
             ) VALUES (
                $1, $2, $3, $4,
                $5, $6, $7, $8,
                $9, $10, $11, $12, $13,
                $14, $15, $16,
                $17, $18, $19,
                $20, $21, $22, $23, $24
             )`,
		r.Sequence, r.PrevHash, r.RowHash, r.CanonicalFormVersion,
		r.RequestID, r.TraceID, r.OccurredAt, r.RecordedAt,
		nullableString(r.ActorID), r.ActorType, nullableString(r.AuthMethod), r.SourceIP, r.UserAgent,
		nullableString(r.WorkspaceID), nullableString(r.ProjectID), nullableString(r.EnvironmentID),
		r.ResourceKind, nullableString(r.ResourceID), r.Action,
		beforeJSON, afterJSON, nullableString(r.ReasonCode), nullableString(r.PolicyDecision), attrsJSON,
	)
	if err != nil {
		return Row{}, fmt.Errorf("insert audit_logs: %w", err)
	}
	return r, nil
}

func (c *PostgresChain) prepareRow(r Row) Row {
	if r.RecordedAt.IsZero() {
		now := time.Now
		if c.Now != nil {
			now = c.Now
		}
		r.RecordedAt = now().UTC()
	} else {
		r.RecordedAt = r.RecordedAt.UTC()
	}
	if r.OccurredAt.IsZero() {
		r.OccurredAt = r.RecordedAt
	} else {
		r.OccurredAt = r.OccurredAt.UTC()
	}
	// Postgres TIMESTAMPTZ is microsecond-precision; truncate to
	// microseconds so the canonical_row we hash here matches the
	// canonical_row Verify recomputes from the SELECT-returned
	// timestamp. Lane 4's canonical encoder formats with
	// RFC 3339 Nano, so any sub-microsecond drift breaks the chain.
	r.RecordedAt = r.RecordedAt.Truncate(time.Microsecond)
	r.OccurredAt = r.OccurredAt.Truncate(time.Microsecond)
	if r.ActorType == "" {
		r.ActorType = "system"
	}
	if r.RequestID == "" {
		r.RequestID = "system"
	}
	return r
}

// Read implements AuditChain.
func (c *PostgresChain) Read(ctx context.Context, q Query) ([]Row, error) {
	if c == nil || c.Pool == nil {
		return nil, errors.New("audit.PostgresChain: pool not initialized")
	}
	args := []any{q.SinceID}
	sql := `SELECT
            id, prev_hash, row_hash, canonical_form_version,
            request_id, COALESCE(trace_id,''), occurred_at, recorded_at,
            COALESCE(actor_id,''), actor_type, COALESCE(auth_method,''), COALESCE(source_ip,''), COALESCE(user_agent,''),
            COALESCE(workspace_id,''), COALESCE(project_id,''), COALESCE(environment_id,''),
            resource_kind, COALESCE(resource_id,''), action,
            before, after, COALESCE(reason_code,''), COALESCE(policy_decision,''), attrs,
            COALESCE(checkpoint_id, 0)
         FROM audit_logs
         WHERE id > $1
         ORDER BY id ASC`
	if q.Limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", q.Limit)
	}
	rows, err := c.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("audit.PostgresChain: read: %w", err)
	}
	return scanAuditRows(rows)
}

// ReadProject performs an indexable, latest-first tenant read directly in
// PostgreSQL. Project filtering happens in the query, before pagination.
func (c *PostgresChain) ReadProject(ctx context.Context, q ProjectQuery) ([]Row, error) {
	if c == nil || c.Pool == nil {
		return nil, errors.New("audit.PostgresChain: pool not initialized")
	}
	if err := q.Validate(); err != nil {
		return nil, err
	}
	rows, err := c.Pool.Query(ctx, `SELECT
            id, prev_hash, row_hash, canonical_form_version,
            request_id, COALESCE(trace_id,''), occurred_at, recorded_at,
            COALESCE(actor_id,''), actor_type, COALESCE(auth_method,''), COALESCE(source_ip,''), COALESCE(user_agent,''),
            COALESCE(workspace_id,''), COALESCE(project_id,''), COALESCE(environment_id,''),
            resource_kind, COALESCE(resource_id,''), action,
            before, after, COALESCE(reason_code,''), COALESCE(policy_decision,''), attrs,
            COALESCE(checkpoint_id, 0)
         FROM audit_logs
         WHERE project_id = $1
           AND ($2::bigint = 0 OR id < $2)
         ORDER BY id DESC
         LIMIT $3`, q.ProjectID, q.BeforeID, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("audit.PostgresChain: read project: %w", err)
	}
	return scanAuditRows(rows)
}

func scanAuditRows(rows pgx.Rows) ([]Row, error) {
	defer rows.Close()
	out := make([]Row, 0)
	for rows.Next() {
		var r Row
		var beforeRaw, afterRaw, attrsRaw []byte
		var traceID, actorID, authMethod, sourceIP, userAgent string
		var workspace, project, env string
		var resourceID, reasonCode, policyDecision string
		var checkpointID int64
		err := rows.Scan(
			&r.Sequence, &r.PrevHash, &r.RowHash, &r.CanonicalFormVersion,
			&r.RequestID, &traceID, &r.OccurredAt, &r.RecordedAt,
			&actorID, &r.ActorType, &authMethod, &sourceIP, &userAgent,
			&workspace, &project, &env,
			&r.ResourceKind, &resourceID, &r.Action,
			&beforeRaw, &afterRaw, &reasonCode, &policyDecision, &attrsRaw,
			&checkpointID,
		)
		if err != nil {
			return nil, fmt.Errorf("audit.PostgresChain: scan: %w", err)
		}
		r.TraceID = traceID
		r.ActorID = actorID
		r.AuthMethod = authMethod
		r.SourceIP = sourceIP
		r.UserAgent = userAgent
		r.WorkspaceID = workspace
		r.ProjectID = project
		r.EnvironmentID = env
		r.ResourceID = resourceID
		r.ReasonCode = reasonCode
		r.PolicyDecision = policyDecision
		r.CheckpointID = checkpointID
		r.OccurredAt = r.OccurredAt.UTC()
		r.RecordedAt = r.RecordedAt.UTC()
		if len(beforeRaw) > 0 {
			if err := json.Unmarshal(beforeRaw, &r.Before); err != nil {
				return nil, fmt.Errorf("audit.PostgresChain: unmarshal before: %w", err)
			}
		}
		if len(afterRaw) > 0 {
			if err := json.Unmarshal(afterRaw, &r.After); err != nil {
				return nil, fmt.Errorf("audit.PostgresChain: unmarshal after: %w", err)
			}
		}
		if len(attrsRaw) > 0 {
			if err := json.Unmarshal(attrsRaw, &r.Attrs); err != nil {
				return nil, fmt.Errorf("audit.PostgresChain: unmarshal attrs: %w", err)
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertCheckpoint implements AuditChain.
func (c *PostgresChain) InsertCheckpoint(ctx context.Context, cp Checkpoint) (Checkpoint, error) {
	if c == nil || c.Pool == nil {
		return Checkpoint{}, errors.New("audit.PostgresChain: pool not initialized")
	}
	var inserted Checkpoint
	err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{}, func(tx *pg.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", auditChainAdvisoryLockKey); err != nil {
			return fmt.Errorf("acquire advisory lock: %w", err)
		}
		var maxID int64
		err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM audit_checkpoints`).Scan(&maxID)
		if err != nil {
			return fmt.Errorf("read checkpoint head: %w", err)
		}
		cp.ID = maxID + 1
		var prevID any
		if cp.PrevCheckpointID > 0 {
			prevID = cp.PrevCheckpointID
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO audit_checkpoints (
                id, partition_label, started_at, ended_at,
                first_row_id, last_row_id, last_row_hash,
                signature_alg, signing_key_fingerprint, signature,
                prev_checkpoint_id
             ) VALUES (
                $1, $2, $3, $4,
                $5, $6, $7,
                $8, $9, $10,
                $11
             )`,
			cp.ID, cp.PartitionLabel, cp.StartedAt.UTC(), cp.EndedAt.UTC(),
			cp.FirstRowID, cp.LastRowID, cp.LastRowHash,
			cp.SignatureAlg, cp.SigningKeyFingerprint, cp.Signature,
			prevID,
		)
		if err != nil {
			return fmt.Errorf("insert audit_checkpoints: %w", err)
		}
		inserted = cp
		return nil
	})
	if err != nil {
		return Checkpoint{}, err
	}
	return inserted, nil
}

// LatestCheckpoint implements AuditChain.
func (c *PostgresChain) LatestCheckpoint(ctx context.Context) (Checkpoint, error) {
	if c == nil || c.Pool == nil {
		return Checkpoint{}, errors.New("audit.PostgresChain: pool not initialized")
	}
	row := c.Pool.QueryRow(ctx,
		`SELECT id, COALESCE(partition_label, ''), started_at, ended_at,
                first_row_id, last_row_id, last_row_hash,
                COALESCE(signature_alg, ''), COALESCE(signing_key_fingerprint, ''), signature,
                COALESCE(prev_checkpoint_id, 0)
           FROM audit_checkpoints
          ORDER BY id DESC LIMIT 1`,
	)
	cp, err := scanCheckpoint(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Checkpoint{}, ErrNoCheckpoints
	}
	return cp, err
}

// ListCheckpoints implements AuditChain.
func (c *PostgresChain) ListCheckpoints(ctx context.Context) ([]Checkpoint, error) {
	if c == nil || c.Pool == nil {
		return nil, errors.New("audit.PostgresChain: pool not initialized")
	}
	rows, err := c.Pool.Query(ctx,
		`SELECT id, COALESCE(partition_label, ''), started_at, ended_at,
                first_row_id, last_row_id, last_row_hash,
                COALESCE(signature_alg, ''), COALESCE(signing_key_fingerprint, ''), signature,
                COALESCE(prev_checkpoint_id, 0)
           FROM audit_checkpoints ORDER BY id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("audit.PostgresChain: list checkpoints: %w", err)
	}
	defer rows.Close()
	out := make([]Checkpoint, 0)
	for rows.Next() {
		cp, err := scanCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCheckpoint(row scanner) (Checkpoint, error) {
	var cp Checkpoint
	var prev int64
	if err := row.Scan(
		&cp.ID, &cp.PartitionLabel, &cp.StartedAt, &cp.EndedAt,
		&cp.FirstRowID, &cp.LastRowID, &cp.LastRowHash,
		&cp.SignatureAlg, &cp.SigningKeyFingerprint, &cp.Signature,
		&prev,
	); err != nil {
		return Checkpoint{}, err
	}
	cp.PrevCheckpointID = prev
	cp.StartedAt = cp.StartedAt.UTC()
	cp.EndedAt = cp.EndedAt.UTC()
	return cp, nil
}

func jsonOrNull(m map[string]any) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return json.Marshal(m)
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
