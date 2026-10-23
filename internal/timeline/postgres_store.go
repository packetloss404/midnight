// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package timeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
)

// PostgresStore is the Lane 1 persistence implementation. It exposes
// the same Append / List / Query surface as MemoryStore so callers can
// switch by replacing the constructor.
type PostgresStore struct {
	Pool *pg.Pool
}

// NewPostgresStore wires a PostgresStore.
func NewPostgresStore(pool *pg.Pool) *PostgresStore {
	return &PostgresStore{Pool: pool}
}

// Append persists one event. Concurrency: the (deployment_id, event_seq)
// PK enforces "strictly greater than previous seq" — duplicate seqs
// fail with the unique-violation error which is surfaced to the caller.
func (s *PostgresStore) Append(event Event) error {
	if s == nil || s.Pool == nil {
		return errors.New("timeline.PostgresStore: pool not initialized")
	}
	if event.DeploymentID == "" {
		return errors.New("deployment ID is required")
	}
	if event.EventSeq <= 0 {
		return errors.New("event_seq must be greater than zero")
	}
	occurred := event.At
	if occurred.IsZero() {
		occurred = time.Now().UTC()
	}
	_, err := s.Pool.Exec(context.Background(),
		`INSERT INTO timeline_events (deployment_id, event_seq, event_type, tenant_id, route_name, host, occurred_at, message)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		event.DeploymentID, event.EventSeq, string(event.Type), event.TenantID, event.RouteName, event.Host, occurred, event.Message,
	)
	if err != nil {
		return fmt.Errorf("timeline append: %w", err)
	}
	return nil
}

// List returns all events for a deployment id in seq order.
func (s *PostgresStore) List(deploymentID string) []Event {
	if s == nil || s.Pool == nil || deploymentID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.Pool.Query(ctx,
		`SELECT deployment_id, event_seq, event_type, tenant_id, route_name, host, occurred_at, message
		 FROM timeline_events WHERE deployment_id = $1 ORDER BY event_seq ASC`,
		deploymentID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]Event, 0)
	for rows.Next() {
		var e Event
		var typ string
		if err := rows.Scan(&e.DeploymentID, &e.EventSeq, &typ, &e.TenantID, &e.RouteName, &e.Host, &e.At, &e.Message); err != nil {
			return nil
		}
		e.Type = EventType(typ)
		e.At = e.At.UTC()
		out = append(out, e)
	}
	return out
}

// DeploymentIDs returns the distinct deployment ids in ascending order.
func (s *PostgresStore) DeploymentIDs() []string {
	if s == nil || s.Pool == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT deployment_id FROM timeline_events ORDER BY deployment_id ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil
		}
		out = append(out, id)
	}
	return out
}

// Query is the cursor-paginated query interface. The Postgres
// implementation pushes the filters into SQL.
func (s *PostgresStore) Query(filter Filter, cursor QueryCursor, pageSize int) (QueryResult, error) {
	if s == nil || s.Pool == nil {
		return QueryResult{}, errors.New("timeline.PostgresStore: pool not initialized")
	}
	if err := filter.Validate(); err != nil {
		return QueryResult{}, err
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	args := make([]any, 0, 12)
	clauses := make([]string, 0, 8)

	if filter.DeploymentID != "" {
		args = append(args, filter.DeploymentID)
		clauses = append(clauses, fmt.Sprintf("deployment_id = $%d", len(args)))
	}
	if cursor.DeploymentID != "" && cursor.AfterSeq > 0 {
		args = append(args, cursor.DeploymentID, cursor.AfterSeq)
		clauses = append(clauses, fmt.Sprintf("(deployment_id > $%d OR (deployment_id = $%d AND event_seq > $%d))", len(args)-1, len(args)-1, len(args)))
	} else if cursor.DeploymentID != "" {
		args = append(args, cursor.DeploymentID)
		clauses = append(clauses, fmt.Sprintf("deployment_id >= $%d", len(args)))
	}
	if len(filter.Types) > 0 {
		typeArgs := make([]string, 0, len(filter.Types))
		for _, t := range filter.Types {
			args = append(args, string(t))
			typeArgs = append(typeArgs, fmt.Sprintf("$%d", len(args)))
		}
		clauses = append(clauses, "event_type IN ("+strings.Join(typeArgs, ",")+")")
	}
	if len(filter.TenantIDs) > 0 {
		tenantArgs := make([]string, 0, len(filter.TenantIDs))
		for _, id := range filter.TenantIDs {
			args = append(args, id)
			tenantArgs = append(tenantArgs, fmt.Sprintf("$%d", len(args)))
		}
		clauses = append(clauses, "tenant_id IN ("+strings.Join(tenantArgs, ",")+")")
	}
	if len(filter.RouteNames) > 0 {
		routeArgs := make([]string, 0, len(filter.RouteNames))
		for _, n := range filter.RouteNames {
			args = append(args, n)
			routeArgs = append(routeArgs, fmt.Sprintf("$%d", len(args)))
		}
		clauses = append(clauses, "route_name IN ("+strings.Join(routeArgs, ",")+")")
	}
	if len(filter.Hosts) > 0 {
		hostArgs := make([]string, 0, len(filter.Hosts))
		for _, h := range filter.Hosts {
			args = append(args, h)
			hostArgs = append(hostArgs, fmt.Sprintf("$%d", len(args)))
		}
		clauses = append(clauses, "host IN ("+strings.Join(hostArgs, ",")+")")
	}
	if !filter.Since.IsZero() {
		args = append(args, filter.Since)
		clauses = append(clauses, fmt.Sprintf("occurred_at >= $%d", len(args)))
	}
	if !filter.Until.IsZero() {
		args = append(args, filter.Until)
		clauses = append(clauses, fmt.Sprintf("occurred_at <= $%d", len(args)))
	}
	if filter.FreeText != "" {
		args = append(args, "%"+strings.ToLower(filter.FreeText)+"%")
		clauses = append(clauses, fmt.Sprintf("LOWER(message) LIKE $%d", len(args)))
	}
	args = append(args, pageSize+1)
	limitPlaceholder := fmt.Sprintf("$%d", len(args))

	whereClause := ""
	if len(clauses) > 0 {
		whereClause = "WHERE " + strings.Join(clauses, " AND ")
	}

	query := fmt.Sprintf(
		`SELECT deployment_id, event_seq, event_type, tenant_id, route_name, host, occurred_at, message
		 FROM timeline_events %s
		 ORDER BY deployment_id ASC, event_seq ASC
		 LIMIT %s`,
		whereClause, limitPlaceholder,
	)
	rows, err := s.Pool.Query(context.Background(), query, args...)
	if err != nil {
		return QueryResult{}, fmt.Errorf("timeline query: %w", err)
	}
	defer rows.Close()
	collected := make([]Event, 0, pageSize+1)
	for rows.Next() {
		var e Event
		var typ string
		if err := rows.Scan(&e.DeploymentID, &e.EventSeq, &typ, &e.TenantID, &e.RouteName, &e.Host, &e.At, &e.Message); err != nil {
			return QueryResult{}, fmt.Errorf("timeline scan: %w", err)
		}
		e.Type = EventType(typ)
		e.At = e.At.UTC()
		if !filter.Match(e) {
			continue
		}
		collected = append(collected, e)
	}
	if err := rows.Err(); err != nil {
		return QueryResult{}, err
	}
	result := QueryResult{Events: collected}
	if len(collected) > pageSize {
		last := collected[pageSize-1]
		result.Events = collected[:pageSize]
		result.NextCursor = QueryCursor{DeploymentID: last.DeploymentID, AfterSeq: last.EventSeq}
		result.Truncated = true
	}
	return result, nil
}

// IsUniqueViolation reports whether err is the Postgres duplicate-key
// error from a (deployment_id, event_seq) collision; useful for callers
// that want to surface a typed reason code.
func IsUniqueViolation(err error) bool {
	var pgErr interface {
		SQLState() string
	}
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	_ = pgx.ErrNoRows
	return false
}
