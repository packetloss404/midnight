package tcpproxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	dbpg "github.com/midnight/midnight/internal/db/pg"
)

type PostgresStore struct {
	Pool       *dbpg.Pool
	Ports      PortRange
	Now        func() time.Time
	NewID      func() (string, error)
	BaseDomain string
}

func NewPostgresStore(pool *dbpg.Pool) *PostgresStore {
	return &PostgresStore{
		Pool:  pool,
		Ports: PortRange{Start: DefaultExternalPortStart, End: DefaultExternalPortEnd},
		Now:   func() time.Time { return time.Now().UTC() },
		NewID: newEndpointID,
	}
}

func (s *PostgresStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *PostgresStore) CreateEndpoint(ctx context.Context, input CreateEndpointInput) (Endpoint, error) {
	if s == nil || s.Pool == nil {
		return Endpoint{}, errors.New("tcp proxy postgres store pool is required")
	}
	if err := input.Scope.Validate(); err != nil {
		return Endpoint{}, err
	}
	baseDomain := strings.TrimSpace(input.BaseDomain)
	if baseDomain == "" {
		baseDomain = strings.TrimSpace(s.BaseDomain)
	}
	var created Endpoint
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('midnight_tcp_proxy_allocation'))`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT host, external_port
			  FROM tcp_proxy_endpoints
			 WHERE desired_enabled OR listener_bound
			 ORDER BY external_port, id`)
		if err != nil {
			return err
		}
		var existing []ExistingEndpoint
		for rows.Next() {
			var value ExistingEndpoint
			if err := rows.Scan(&value.Host, &value.ExternalPort); err != nil {
				rows.Close()
				return err
			}
			existing = append(existing, value)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		plan, err := PlanEndpoint(PlanInput{
			Request: EndpointRequest{
				Enabled: true, ProjectID: input.ProjectID, EnvironmentID: input.EnvironmentID,
				ServiceID: input.ServiceID, BaseDomain: baseDomain,
				RequestedHost: input.RequestedHost, RequestedExternalPort: input.RequestedExternalPort,
				BackendHost: "active-deployment.internal", BackendPort: input.BackendPort,
				SourceIPPolicy: SourceIPPolicy{AllowedCIDRs: input.AllowedCIDRs},
			},
			Existing: existing,
			Ports:    s.Ports,
		})
		if err != nil {
			return fmt.Errorf("%w: %v", ErrEndpointInvalid, err)
		}
		idSource := s.NewID
		if idSource == nil {
			idSource = newEndpointID
		}
		id, err := idSource()
		if err != nil {
			return fmt.Errorf("generate tcp proxy endpoint id: %w", err)
		}
		now := s.now()
		row := tx.QueryRow(ctx, `
			INSERT INTO tcp_proxy_endpoints (
				id, project_id, environment_id, service_id, host, external_port,
				backend_port, allowed_source_cidrs,
				desired_enabled, apply_state, listener_bound, backend_reachable,
				created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,TRUE,'pending',FALSE,FALSE,$9,$9)
			RETURNING `+endpointColumns,
			id, input.ProjectID, input.EnvironmentID, input.ServiceID,
			strings.ToLower(strings.TrimSpace(plan.Host)), plan.ExternalPort,
			plan.BackendPort, plan.SourceIPPolicy.AllowedCIDRs, now,
		)
		created, err = scanEndpoint(row)
		return err
	})
	if err != nil {
		if isEndpointConflict(err) {
			return Endpoint{}, ErrEndpointConflict
		}
		if errors.Is(err, ErrEndpointInvalid) {
			return Endpoint{}, err
		}
		return Endpoint{}, fmt.Errorf("create tcp proxy endpoint: %w", err)
	}
	return created, nil
}

func (s *PostgresStore) GetEndpoint(ctx context.Context, id string) (Endpoint, error) {
	if s == nil || s.Pool == nil {
		return Endpoint{}, errors.New("tcp proxy postgres store pool is required")
	}
	value, err := scanEndpoint(s.Pool.QueryRow(ctx, `
		SELECT `+endpointColumns+` FROM tcp_proxy_endpoints WHERE id=$1`, strings.TrimSpace(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Endpoint{}, ErrEndpointNotFound
	}
	if err != nil {
		return Endpoint{}, fmt.Errorf("get tcp proxy endpoint: %w", err)
	}
	return value, nil
}

func (s *PostgresStore) ListEndpoints(ctx context.Context, scope Scope) ([]Endpoint, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("tcp proxy postgres store pool is required")
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT `+endpointColumns+`
		  FROM tcp_proxy_endpoints
		 WHERE project_id=$1 AND environment_id=$2 AND service_id=$3
		 ORDER BY created_at, id`, scope.ProjectID, scope.EnvironmentID, scope.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("list tcp proxy endpoints: %w", err)
	}
	defer rows.Close()
	return scanEndpoints(rows)
}

func (s *PostgresStore) ListRuntimeEndpoints(ctx context.Context) ([]Endpoint, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("tcp proxy postgres store pool is required")
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT `+endpointColumns+`
		  FROM tcp_proxy_endpoints
		 WHERE desired_enabled OR listener_bound OR apply_state <> 'withdrawn'
		 ORDER BY external_port, id`)
	if err != nil {
		return nil, fmt.Errorf("list tcp proxy runtime endpoints: %w", err)
	}
	defer rows.Close()
	return scanEndpoints(rows)
}

func (s *PostgresStore) DisableEndpoint(ctx context.Context, id string) (Endpoint, error) {
	if s == nil || s.Pool == nil {
		return Endpoint{}, errors.New("tcp proxy postgres store pool is required")
	}
	value, err := scanEndpoint(s.Pool.QueryRow(ctx, `
		UPDATE tcp_proxy_endpoints
		   SET desired_enabled=FALSE,
		       apply_state=CASE WHEN listener_bound THEN 'pending' ELSE 'withdrawn' END,
		       backend_reachable=FALSE,
		       updated_at=$2
		 WHERE id=$1
		RETURNING `+endpointColumns, strings.TrimSpace(id), s.now()))
	if errors.Is(err, pgx.ErrNoRows) {
		return Endpoint{}, ErrEndpointNotFound
	}
	if err != nil {
		return Endpoint{}, fmt.Errorf("disable tcp proxy endpoint: %w", err)
	}
	return value, nil
}

func (s *PostgresStore) RecordObservation(ctx context.Context, id string, expectedDesired bool, observation Observation) (bool, error) {
	if s == nil || s.Pool == nil {
		return false, errors.New("tcp proxy postgres store pool is required")
	}
	if !validApplyState(observation.State) {
		return false, fmt.Errorf("%w: invalid apply state %q", ErrEndpointInvalid, observation.State)
	}
	at := observation.ObservedAt.UTC()
	if at.IsZero() {
		at = s.now()
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE tcp_proxy_endpoints
		   SET apply_state=$1, listener_bound=$2, backend_reachable=$3,
		       controller_id=$4, last_error_code=$5, last_error_detail=$6,
		       observed_at=$7, updated_at=$7
		 WHERE id=$8 AND desired_enabled=$9`,
		string(observation.State), observation.ListenerBound, observation.BackendReachable,
		boundedString(observation.ControllerID, 128), boundedString(observation.ErrorCode, 96),
		boundedString(observation.ErrorDetail, 512), at, strings.TrimSpace(id), expectedDesired,
	)
	if err != nil {
		return false, fmt.Errorf("record tcp proxy observation: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

const endpointColumns = `
	id, project_id, environment_id, service_id, host, external_port,
	backend_port, allowed_source_cidrs, desired_enabled,
	apply_state, listener_bound, backend_reachable, controller_id,
	last_error_code, last_error_detail, observed_at, created_at, updated_at`

type rowScanner interface {
	Scan(...any) error
}

func scanEndpoint(row rowScanner) (Endpoint, error) {
	var value Endpoint
	err := row.Scan(
		&value.ID, &value.ProjectID, &value.EnvironmentID, &value.ServiceID,
		&value.Host, &value.ExternalPort, &value.BackendPort,
		&value.AllowedCIDRs, &value.DesiredEnabled, &value.ApplyState,
		&value.ListenerBound, &value.BackendReachable, &value.ControllerID,
		&value.LastErrorCode, &value.LastErrorDetail, &value.ObservedAt,
		&value.CreatedAt, &value.UpdatedAt,
	)
	return value, err
}

func scanEndpoints(rows pgx.Rows) ([]Endpoint, error) {
	var out []Endpoint
	for rows.Next() {
		value, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func validApplyState(state ApplyState) bool {
	switch state {
	case ApplyPending, ApplyActive, ApplyWithdrawn, ApplyFailed:
		return true
	default:
		return false
	}
}

func isEndpointConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func newEndpointID() (string, error) {
	return endpointIDFrom(rand.Reader)
}

func endpointIDFrom(source io.Reader) (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(source, value[:]); err != nil {
		return "", err
	}
	return "tcp_" + hex.EncodeToString(value[:]), nil
}

func boundedString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
