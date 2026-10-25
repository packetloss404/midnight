// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// This is the persistence layer for canonical deployment records (migration
// 0014). Product truth lives here, NOT in reconstructed operation-event strings.
// Until the DB FSM trigger is attached (a gated follow-up), state transitions
// are enforced app-side by deploy.ValidateTransition plus a WHERE state=<from>
// compare-and-swap so a concurrent writer cannot double-transition.

var (
	// ErrDeploymentNotFound is returned when a deployment id has no row.
	ErrDeploymentNotFound = errors.New("deployment not found")
	// ErrDeploymentTransitionConflict is returned when a CAS state transition
	// affected zero rows (the row moved out from under us or is in a different
	// state than expected).
	ErrDeploymentTransitionConflict = errors.New("deployment transition conflict")
	// ErrPromotionConflict is returned when the active pointer drifted between
	// readiness and promotion (the expected-active CAS target no longer holds).
	ErrPromotionConflict = errors.New("deployment promotion conflict")
)

// DeploymentRecord is the persistence model for a row in `deployments`.
type DeploymentRecord struct {
	ID                    string
	ProjectID             string
	ServiceID             string
	EnvironmentID         string
	Action                deploy.DeploymentAction
	SourceType            string
	Repo                  string
	Ref                   string
	SHA                   string
	RootDirectory         string
	DockerfilePath        string
	Builder               string
	ImageRef              string
	ImageDigest           string
	State                 deploy.DeploymentState
	Active                bool
	OperationID           string
	TriggerKey            string
	RollbackSourceID      string
	ExpectedActiveID      string
	ConfigurationRevision int64
	FailureCode           string
	FailureMessage        string
	RuntimeDesiredState   string
	RuntimeObservedState  string
	RuntimeGeneration     int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	StartedAt             *time.Time
	CompletedAt           *time.Time
}

// DeploymentEventRecord is one row in the typed `deployment_events` ledger.
type DeploymentEventRecord struct {
	DeploymentID string
	EventType    string // state | step | failure | log
	Step         string
	State        string
	FailureCode  string
	Message      string
	Data         []byte // JSON; defaults to {}
}

// DeploymentStore owns canonical deployment product truth. Intake must create
// a record before queueing work; workers then advance the same record through
// build, deploy, and activation boundaries.
type DeploymentStore interface {
	CreateDeployment(ctx context.Context, rec DeploymentRecord) (DeploymentRecord, error)
	GetDeploymentByOperation(ctx context.Context, operationID string) (DeploymentRecord, error)
	ListDeploymentsByService(ctx context.Context, serviceID string) ([]DeploymentRecord, error)
	MarkDeploymentBuilding(ctx context.Context, id string) error
	RecordDeploymentBuild(ctx context.Context, id, sourceSHA, builder, imageRef, imageDigest string) error
	MarkDeploymentDeploying(ctx context.Context, id string, from deploy.DeploymentState) error
	FailDeployment(ctx context.Context, id string, from deploy.DeploymentState, code, message string) error
	CancelDeployment(ctx context.Context, id string, from deploy.DeploymentState) error
	PromoteDeployment(ctx context.Context, id, expectedActiveID string) error
	CreateRollbackDeployment(ctx context.Context, sourceID string) (DeploymentRecord, error)
	AppendDeploymentEvent(ctx context.Context, evt DeploymentEventRecord) error
}

// CanonicalDeploymentOperationCancelStore atomically terminalizes the
// operation and its one canonical in-flight deployment. Workers use this
// boundary at a cancellation safe point so process death cannot leave the two
// durable state machines disagreeing.
type CanonicalDeploymentOperationCancelStore interface {
	CancelDeploymentOperation(
		context.Context,
		orailtypes.ID,
	) (orailtypes.Operation, error)
}

// DeploymentReadFilter is the bounded keyset query used by the product
// deployment read API. BeforeCreatedAt + BeforeID form one exclusive cursor
// over the stable (created_at DESC, id DESC) order.
type DeploymentReadFilter struct {
	ServiceID       string
	EnvironmentID   string
	DeploymentID    string
	ActiveOnly      bool
	BeforeCreatedAt time.Time
	BeforeID        string
	Limit           int
}

// DeploymentRecordPage contains at most filter.Limit records. HasMore means a
// subsequent keyset page exists after the last returned record.
type DeploymentRecordPage struct {
	Records []DeploymentRecord
	HasMore bool
}

// DeploymentReadStore is an optional, query-bounded read seam. Mutation stores
// retain ListDeploymentsByService for worker/control-plane callers, while the
// product API requires this interface for database-backed pagination.
type DeploymentReadStore interface {
	ReadDeploymentPage(context.Context, DeploymentReadFilter) (DeploymentRecordPage, error)
}

var _ DeploymentStore = (*PostgresDeploymentStore)(nil)
var _ DeploymentReadStore = (*PostgresDeploymentStore)(nil)

// PostgresDeploymentStore persists canonical deployment records + their event
// ledger and owns the atomic promote/supersede swap.
type PostgresDeploymentStore struct {
	Pool           *pg.Pool
	IDFactory      func() string
	EventIDFactory func() string
	Now            func() time.Time
}

// NewPostgresDeploymentStore constructs the store with sensible defaults.
func NewPostgresDeploymentStore(pool *pg.Pool) *PostgresDeploymentStore {
	return &PostgresDeploymentStore{
		Pool:           pool,
		IDFactory:      func() string { return mintID(orailtypes.IDPrefixDeployment) },
		EventIDFactory: func() string { return mintID(orailtypes.IDPrefixEvent) },
		Now:            func() time.Time { return time.Now().UTC() },
	}
}

// mintID generates a "<prefix>_<hex16>" id, matching defaultOperationID's shape.
func mintID(prefix orailtypes.IDPrefix) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b[:]))
}

func (s *PostgresDeploymentStore) ready() error {
	if s == nil || s.Pool == nil {
		return errors.New("PostgresDeploymentStore: pool not initialized")
	}
	return nil
}

// CreateDeployment inserts a new deployment. A deployment may only be created in
// state 'queued' (the sole legal insert state); the state field is forced.
func (s *PostgresDeploymentStore) CreateDeployment(ctx context.Context, rec DeploymentRecord) (DeploymentRecord, error) {
	if err := ctx.Err(); err != nil {
		return DeploymentRecord{}, err
	}
	if err := s.ready(); err != nil {
		return DeploymentRecord{}, err
	}
	if !deploy.ValidAction(rec.Action) {
		return DeploymentRecord{}, fmt.Errorf("deployment: invalid action %q", rec.Action)
	}
	if rec.ID == "" {
		rec.ID = s.IDFactory()
	}
	rec.State = deploy.DeploymentQueued
	rec.Active = false
	now := s.Now()
	rec.CreatedAt, rec.UpdatedAt = now, now
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO deployments (
		    id, project_id, service_id, environment_id, action, source_type, repo, ref, sha,
		    root_directory, dockerfile_path, builder, image_ref, image_digest, state, active,
		    operation_id, trigger_key, rollback_source_deployment_id, expected_active_deployment_id,
		    created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'queued',FALSE,
		    $15,$16,$17,$18,$19,$19)`,
		rec.ID, rec.ProjectID, rec.ServiceID, rec.EnvironmentID, string(rec.Action),
		rec.SourceType, rec.Repo, rec.Ref, rec.SHA, rec.RootDirectory, rec.DockerfilePath,
		rec.Builder, rec.ImageRef, rec.ImageDigest,
		nullableString(rec.OperationID), nullableString(rec.TriggerKey),
		nullableString(rec.RollbackSourceID), nullableString(rec.ExpectedActiveID), now,
	)
	if err != nil {
		return DeploymentRecord{}, fmt.Errorf("deployment insert: %w", err)
	}
	return rec, nil
}

// GetDeploymentByOperation returns the most recent deployment linked to an
// operation id (the intake dual-write sets operation_id). Used by the worker to
// find the canonical row for a deploy job. Returns ErrDeploymentNotFound when
// no linked row exists (memory-mode, or a source-build re-entry whose deploy
// operation differs from the intake operation the mirror was linked to).
func (s *PostgresDeploymentStore) GetDeploymentByOperation(ctx context.Context, operationID string) (DeploymentRecord, error) {
	if err := ctx.Err(); err != nil {
		return DeploymentRecord{}, err
	}
	if err := s.ready(); err != nil {
		return DeploymentRecord{}, err
	}
	var rec DeploymentRecord
	var state, action string
	err := s.Pool.QueryRow(ctx,
		`SELECT id, project_id, service_id, environment_id, action, state,
		        image_ref, image_digest,
		        COALESCE(expected_active_deployment_id, ''),
		        configuration_revision,
		        failure_code, failure_message, created_at, updated_at
		   FROM deployments
		  WHERE operation_id = $1
		  ORDER BY created_at DESC
		  LIMIT 1`,
		operationID,
	).Scan(
		&rec.ID,
		&rec.ProjectID,
		&rec.ServiceID,
		&rec.EnvironmentID,
		&action,
		&state,
		&rec.ImageRef,
		&rec.ImageDigest,
		&rec.ExpectedActiveID,
		&rec.ConfigurationRevision,
		&rec.FailureCode,
		&rec.FailureMessage,
		&rec.CreatedAt,
		&rec.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DeploymentRecord{}, ErrDeploymentNotFound
		}
		return DeploymentRecord{}, fmt.Errorf("deployment by operation: %w", err)
	}
	rec.State = deploy.DeploymentState(state)
	rec.Action = deploy.DeploymentAction(action)
	rec.OperationID = operationID
	return rec, nil
}

// ListDeploymentsByService returns a service's deployments, newest first, from
// the canonical table. Used by the guarded read-flip in deployments.go.
func (s *PostgresDeploymentStore) ListDeploymentsByService(ctx context.Context, serviceID string) ([]DeploymentRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT id, project_id, service_id, environment_id, action,
		        source_type, repo, ref, sha, root_directory, dockerfile_path, builder,
		        image_ref, image_digest,
		        state, active, COALESCE(operation_id, ''), COALESCE(trigger_key, ''),
		        COALESCE(expected_active_deployment_id, ''), configuration_revision,
		        failure_code, failure_message,
		        runtime_desired_state, runtime_observed_state, runtime_generation,
		        created_at, updated_at, started_at, completed_at
		   FROM deployments WHERE service_id = $1 ORDER BY created_at DESC`,
		serviceID,
	)
	if err != nil {
		return nil, fmt.Errorf("deployments by service: %w", err)
	}
	return scanDeploymentRecordRows(rows, "deployments by service")
}

// ReadDeploymentPage applies exact scope and keyset bounds in PostgreSQL. It
// fetches one sentinel row beyond the requested page so HasMore never relies
// on an unstable count or offset.
func (s *PostgresDeploymentStore) ReadDeploymentPage(
	ctx context.Context,
	filter DeploymentReadFilter,
) (DeploymentRecordPage, error) {
	if err := ctx.Err(); err != nil {
		return DeploymentRecordPage{}, err
	}
	if err := s.ready(); err != nil {
		return DeploymentRecordPage{}, err
	}
	if err := validateDeploymentReadFilter(filter); err != nil {
		return DeploymentRecordPage{}, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT d.id, d.project_id, d.service_id, d.environment_id, d.action,
		       d.source_type, d.repo, d.ref, d.sha, d.root_directory,
		       d.dockerfile_path, d.builder,
		       d.image_ref, d.image_digest, d.state, d.active,
		       COALESCE(d.operation_id, ''), COALESCE(d.trigger_key, ''),
		       COALESCE(d.expected_active_deployment_id, ''),
		       d.configuration_revision,
		       d.failure_code, d.failure_message,
		       d.runtime_desired_state, d.runtime_observed_state,
		       d.runtime_generation, d.created_at, d.updated_at,
		       d.started_at, d.completed_at
		FROM deployments d
		WHERE d.service_id=$1
		  AND ($2='' OR d.environment_id=$2)
		  AND ($3='' OR d.id=$3)
		  AND (NOT $4 OR d.active)
		  AND (
			$5::timestamptz IS NULL
			OR (d.created_at, d.id) < ($5::timestamptz, $6)
		  )
		ORDER BY d.created_at DESC, d.id DESC
		LIMIT $7`,
		filter.ServiceID,
		filter.EnvironmentID,
		filter.DeploymentID,
		filter.ActiveOnly,
		nullableTime(filter.BeforeCreatedAt),
		filter.BeforeID,
		filter.Limit+1,
	)
	if err != nil {
		return DeploymentRecordPage{}, fmt.Errorf("deployment page: %w", err)
	}
	records, err := scanDeploymentRecordRows(rows, "deployment page")
	if err != nil {
		return DeploymentRecordPage{}, err
	}
	return boundDeploymentRecordPage(records, filter.Limit), nil
}

// tenantListDeploymentsByService applies the current human grant and
// environment-restriction predicates inside PostgreSQL. The caller has
// already authenticated the session, but this query still resolves current
// user and membership state rather than filtering an installation-wide list
// in application memory.
func (s *PostgresDeploymentStore) tenantListDeploymentsByService(
	ctx context.Context,
	userID string,
	serviceID string,
) ([]DeploymentRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ready(); err != nil {
		return nil, err
	}
	workspaceRoles, projectRoles := tenantRoleNames(identity.CapabilityServiceRead)
	rows, err := s.Pool.Query(ctx, `
		SELECT d.id, d.project_id, d.service_id, d.environment_id, d.action,
		       d.source_type, d.repo, d.ref, d.sha, d.root_directory,
		       d.dockerfile_path, d.builder,
		       d.image_ref, d.image_digest, d.state, d.active,
		       COALESCE(d.operation_id, ''), COALESCE(d.trigger_key, ''),
		       COALESCE(d.expected_active_deployment_id, ''),
		       d.configuration_revision,
		       d.failure_code, d.failure_message,
		       d.runtime_desired_state, d.runtime_observed_state,
		       d.runtime_generation, d.created_at, d.updated_at,
		       d.started_at, d.completed_at
		FROM deployments d
		JOIN services svc
		  ON svc.id=d.service_id
		 AND svc.project_id=d.project_id
		 AND svc.deleted_at IS NULL
		JOIN projects p
		  ON p.id=d.project_id
		 AND p.deleted_at IS NULL
		JOIN workspaces w
		  ON w.id=p.workspace_id
		 AND w.deleted_at IS NULL
		JOIN environments e
		  ON e.id=d.environment_id
		 AND e.project_id=p.id
		 AND e.deleted_at IS NULL
		JOIN users u
		  ON u.id=$1
		 AND u.deleted_at IS NULL
		 AND u.is_disabled=FALSE
		WHERE d.service_id=$2
		  AND (
			EXISTS (
				SELECT 1
				FROM workspace_members wm
				WHERE wm.workspace_id=p.workspace_id
				  AND wm.user_id=u.id
				  AND wm.accepted_at IS NOT NULL
				  AND wm.role=ANY($3::text[])
				  AND (NOT e.access_restricted OR wm.role='admin')
			)
			OR (
				NOT e.access_restricted
				AND EXISTS (
					SELECT 1
					FROM project_members pm
					WHERE pm.project_id=p.id
					  AND pm.user_id=u.id
					  AND pm.accepted_at IS NOT NULL
					  AND pm.role=ANY($4::text[])
				)
			)
		  )
		ORDER BY d.created_at DESC, d.id DESC`,
		userID,
		serviceID,
		workspaceRoles,
		projectRoles,
	)
	if err != nil {
		return nil, fmt.Errorf("tenant deployments by service: %w", err)
	}
	return scanDeploymentRecordRows(rows, "tenant deployments by service")
}

// tenantReadDeploymentPage combines the same bounded keyset query with live
// user, membership, role, environment restriction, and soft-delete checks in
// one SQL statement. A cursor cannot widen this grant predicate.
func (s *PostgresDeploymentStore) tenantReadDeploymentPage(
	ctx context.Context,
	userID string,
	filter DeploymentReadFilter,
) (DeploymentRecordPage, error) {
	if err := ctx.Err(); err != nil {
		return DeploymentRecordPage{}, err
	}
	if err := s.ready(); err != nil {
		return DeploymentRecordPage{}, err
	}
	if strings.TrimSpace(userID) == "" {
		return DeploymentRecordPage{}, identity.ErrAuthorizationDenied
	}
	if err := validateDeploymentReadFilter(filter); err != nil {
		return DeploymentRecordPage{}, err
	}
	workspaceRoles, projectRoles := tenantRoleNames(identity.CapabilityServiceRead)
	rows, err := s.Pool.Query(ctx, `
		SELECT d.id, d.project_id, d.service_id, d.environment_id, d.action,
		       d.source_type, d.repo, d.ref, d.sha, d.root_directory,
		       d.dockerfile_path, d.builder,
		       d.image_ref, d.image_digest, d.state, d.active,
		       COALESCE(d.operation_id, ''), COALESCE(d.trigger_key, ''),
		       COALESCE(d.expected_active_deployment_id, ''),
		       d.configuration_revision,
		       d.failure_code, d.failure_message,
		       d.runtime_desired_state, d.runtime_observed_state,
		       d.runtime_generation, d.created_at, d.updated_at,
		       d.started_at, d.completed_at
		FROM deployments d
		JOIN services svc
		  ON svc.id=d.service_id
		 AND svc.project_id=d.project_id
		 AND svc.deleted_at IS NULL
		JOIN projects p
		  ON p.id=d.project_id
		 AND p.deleted_at IS NULL
		JOIN workspaces w
		  ON w.id=p.workspace_id
		 AND w.deleted_at IS NULL
		JOIN environments e
		  ON e.id=d.environment_id
		 AND e.project_id=p.id
		 AND e.deleted_at IS NULL
		JOIN users u
		  ON u.id=$1
		 AND u.deleted_at IS NULL
		 AND u.is_disabled=FALSE
		WHERE d.service_id=$2
		  AND ($3='' OR d.environment_id=$3)
		  AND ($4='' OR d.id=$4)
		  AND (NOT $5 OR d.active)
		  AND (
			$6::timestamptz IS NULL
			OR (d.created_at, d.id) < ($6::timestamptz, $7)
		  )
		  AND (
			EXISTS (
				SELECT 1
				FROM workspace_members wm
				WHERE wm.workspace_id=p.workspace_id
				  AND wm.user_id=u.id
				  AND wm.accepted_at IS NOT NULL
				  AND wm.role=ANY($8::text[])
				  AND (NOT e.access_restricted OR wm.role='admin')
			)
			OR (
				NOT e.access_restricted
				AND EXISTS (
					SELECT 1
					FROM project_members pm
					WHERE pm.project_id=p.id
					  AND pm.user_id=u.id
					  AND pm.accepted_at IS NOT NULL
					  AND pm.role=ANY($9::text[])
				)
			)
		  )
		ORDER BY d.created_at DESC, d.id DESC
		LIMIT $10`,
		userID,
		filter.ServiceID,
		filter.EnvironmentID,
		filter.DeploymentID,
		filter.ActiveOnly,
		nullableTime(filter.BeforeCreatedAt),
		filter.BeforeID,
		workspaceRoles,
		projectRoles,
		filter.Limit+1,
	)
	if err != nil {
		return DeploymentRecordPage{}, fmt.Errorf("tenant deployment page: %w", err)
	}
	records, err := scanDeploymentRecordRows(rows, "tenant deployment page")
	if err != nil {
		return DeploymentRecordPage{}, err
	}
	return boundDeploymentRecordPage(records, filter.Limit), nil
}

func validateDeploymentReadFilter(filter DeploymentReadFilter) error {
	if strings.TrimSpace(filter.ServiceID) == "" {
		return errors.New("deployment read service_id is required")
	}
	if filter.Limit < 1 || filter.Limit > maxDeploymentPageLimit {
		return fmt.Errorf("deployment read limit must be between 1 and %d", maxDeploymentPageLimit)
	}
	if filter.BeforeCreatedAt.IsZero() != (strings.TrimSpace(filter.BeforeID) == "") {
		return errors.New("deployment read cursor timestamp and id must be provided together")
	}
	return nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func scanDeploymentRecordRows(rows pgx.Rows, label string) ([]DeploymentRecord, error) {
	defer rows.Close()
	out := make([]DeploymentRecord, 0)
	for rows.Next() {
		var rec DeploymentRecord
		var action, state string
		if err := rows.Scan(
			&rec.ID,
			&rec.ProjectID,
			&rec.ServiceID,
			&rec.EnvironmentID,
			&action,
			&rec.SourceType,
			&rec.Repo,
			&rec.Ref,
			&rec.SHA,
			&rec.RootDirectory,
			&rec.DockerfilePath,
			&rec.Builder,
			&rec.ImageRef,
			&rec.ImageDigest,
			&state,
			&rec.Active,
			&rec.OperationID,
			&rec.TriggerKey,
			&rec.ExpectedActiveID,
			&rec.ConfigurationRevision,
			&rec.FailureCode,
			&rec.FailureMessage,
			&rec.RuntimeDesiredState,
			&rec.RuntimeObservedState,
			&rec.RuntimeGeneration,
			&rec.CreatedAt,
			&rec.UpdatedAt,
			&rec.StartedAt,
			&rec.CompletedAt,
		); err != nil {
			return nil, fmt.Errorf("%s scan: %w", label, err)
		}
		rec.Action = deploy.DeploymentAction(action)
		rec.State = deploy.DeploymentState(state)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s rows: %w", label, err)
	}
	return out, nil
}

func boundDeploymentRecordPage(
	records []DeploymentRecord,
	limit int,
) DeploymentRecordPage {
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	if records == nil {
		records = make([]DeploymentRecord, 0)
	}
	return DeploymentRecordPage{Records: records, HasMore: hasMore}
}

// AppendDeploymentEvent appends a typed event to a deployment's ledger with the
// next monotonic seq. Status is never derived from these rows.
func (s *PostgresDeploymentStore) AppendDeploymentEvent(ctx context.Context, evt DeploymentEventRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ready(); err != nil {
		return err
	}
	if evt.EventType == "" {
		return errors.New("deployment event: event_type required")
	}
	data := evt.Data
	if len(data) == 0 {
		data = []byte("{}")
	}
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO deployment_events (id, deployment_id, seq, event_type, step, state, failure_code, message, data)
		 SELECT $1, $2, COALESCE(MAX(seq), 0) + 1, $3, $4, $5, $6, $7, $8::jsonb
		   FROM deployment_events WHERE deployment_id = $2`,
		s.EventIDFactory(), evt.DeploymentID, evt.EventType, evt.Step, evt.State,
		evt.FailureCode, evt.Message, string(data),
	)
	if err != nil {
		return fmt.Errorf("deployment event append: %w", err)
	}
	return nil
}

// transition performs a CAS state move from `from` to `to`, validating the
// transition against the FSM first. mutate holds extra "col = val" SET clauses
// and their args (used for failure code/message and completed_at). It errors
// with ErrDeploymentTransitionConflict when no row matched (wrong prior state).
func (s *PostgresDeploymentStore) transition(ctx context.Context, id string, from, to deploy.DeploymentState, extraSet string, extraArgs ...any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ready(); err != nil {
		return err
	}
	if err := deploy.ValidateTransition(from, to); err != nil {
		return err
	}
	now := s.Now()
	// Args: $1 state(to), $2 updated_at, then extraArgs..., then id + from at the end.
	setClause := "state = $1, updated_at = $2, active = (($1) = 'active')"
	if extraSet != "" {
		setClause += ", " + extraSet
	}
	args := []any{string(to), now}
	args = append(args, extraArgs...)
	idPos := len(args) + 1
	fromPos := len(args) + 2
	args = append(args, id, string(from))
	sql := fmt.Sprintf(
		"UPDATE deployments SET %s WHERE id = $%d AND state = $%d",
		setClause, idPos, fromPos,
	)
	tag, err := s.Pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("deployment transition %s->%s: %w", from, to, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDeploymentTransitionConflict
	}
	return nil
}

// MarkDeploymentBuilding moves queued -> building (source builds only).
func (s *PostgresDeploymentStore) MarkDeploymentBuilding(ctx context.Context, id string) error {
	return s.transition(ctx, id, deploy.DeploymentQueued, deploy.DeploymentBuilding,
		"started_at = COALESCE(started_at, $3), build_started_at = COALESCE(build_started_at, $3)", s.Now())
}

// RecordDeploymentBuild persists immutable source and image truth after the
// registry push has returned a digest, while the deployment remains building.
// It cannot overwrite an existing image result.
func (s *PostgresDeploymentStore) RecordDeploymentBuild(ctx context.Context, id, sourceSHA, builder, imageRef, imageDigest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ready(); err != nil {
		return err
	}
	if imageRef == "" || imageDigest == "" {
		return errors.New("deployment build result requires image_ref and image_digest")
	}
	tag, err := s.Pool.Exec(ctx,
		`UPDATE deployments
		    SET sha = $2, builder = $3, image_ref = $4, image_digest = $5,
		        build_completed_at = COALESCE(build_completed_at,$6),
		        updated_at = $6, resource_version = resource_version + 1
		  WHERE id = $1 AND state = 'building' AND image_ref = '' AND image_digest = ''`,
		id, sourceSHA, builder, imageRef, imageDigest, s.Now())
	if err != nil {
		return fmt.Errorf("record deployment build: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDeploymentTransitionConflict
	}
	return nil
}

// MarkDeploymentDeploying moves queued|building -> deploying. The prior state is
// resolved so both the image path (queued->deploying) and source path
// (building->deploying) are covered.
func (s *PostgresDeploymentStore) MarkDeploymentDeploying(ctx context.Context, id string, from deploy.DeploymentState) error {
	return s.transition(ctx, id, from, deploy.DeploymentDeploying,
		"started_at = COALESCE(started_at, $3)", s.Now())
}

// FailDeployment moves an in-flight deployment to terminal failed with a code.
func (s *PostgresDeploymentStore) FailDeployment(ctx context.Context, id string, from deploy.DeploymentState, code, message string) error {
	if code == "" {
		return errors.New("deployment failure requires a failure_code")
	}
	now := s.Now()
	extra := "failure_code = $3, failure_message = $4, completed_at = $5"
	if from == deploy.DeploymentBuilding {
		extra += ", build_completed_at = CASE WHEN build_started_at IS NOT NULL THEN COALESCE(build_completed_at,$5) ELSE build_completed_at END"
	}
	return s.transition(ctx, id, from, deploy.DeploymentFailed, extra, code, message, now)
}

// CancelDeployment moves an in-flight deployment to terminal canceled.
func (s *PostgresDeploymentStore) CancelDeployment(ctx context.Context, id string, from deploy.DeploymentState) error {
	now := s.Now()
	extra := "completed_at = $3"
	if from == deploy.DeploymentBuilding {
		extra += ", build_completed_at = CASE WHEN build_started_at IS NOT NULL THEN COALESCE(build_completed_at,$3) ELSE build_completed_at END"
	}
	return s.transition(ctx, id, from, deploy.DeploymentCanceled, extra, now)
}

// CancelDeploymentOperation atomically moves one canonical in-flight
// deployment and its queued/running operation to their canceled terminal
// states. It is the worker-side completion seam for an already accepted cancel
// request; request authorization/idempotency/audit remain API responsibilities.
func (s *PostgresDeploymentStore) CancelDeploymentOperation(
	ctx context.Context,
	operationID orailtypes.ID,
) (orailtypes.Operation, error) {
	if err := ctx.Err(); err != nil {
		return orailtypes.Operation{}, err
	}
	if err := s.ready(); err != nil {
		return orailtypes.Operation{}, err
	}
	now := s.Now().UTC().Truncate(time.Microsecond)
	var operation orailtypes.Operation
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := s.Pool.WithTxOpts(
			ctx,
			pgx.TxOptions{IsoLevel: pgx.Serializable},
			func(tx *pg.Tx) error {
				var err error
				operation, err = lockOperationTx(ctx, tx, operationID)
				if err != nil {
					return err
				}
				if operation.State != orailtypes.OperationQueued &&
					operation.State != orailtypes.OperationRunning {
					return ErrOperationInvalidTransition
				}
				target, err := loadTenantOperationCancelTargetTx(
					ctx,
					tx,
					operationID,
					true,
				)
				if err != nil {
					return err
				}
				if err := cancelCanonicalDeploymentAndOperationTx(
					ctx,
					tx,
					target,
					operation,
					now,
					randomOutboxID("dev"),
					"canceled at worker safe point before traffic promotion",
				); err != nil {
					return err
				}
				operation.State = orailtypes.OperationCanceled
				return nil
			},
		)
		if err == nil {
			return operation, nil
		}
		if !isTenantMutationRetryable(err) ||
			attempt == tenantMutationSerializableAttempts-1 {
			return orailtypes.Operation{}, err
		}
	}
	return orailtypes.Operation{},
		errors.New("deployment operation cancellation retries exhausted")
}

var _ CanonicalDeploymentOperationCancelStore = (*PostgresDeploymentStore)(nil)

// PromoteDeployment atomically promotes a candidate (state=deploying) to active
// and supersedes the previous active for the same (service, environment),
// updating service_environment_state's pointer. The DEFERRABLE one-active
// constraint tolerates the momentary two-active window inside the tx.
//
// If expectedActiveID is non-empty it is a server-resolved CAS guard: promotion
// fails with ErrPromotionConflict if the current active is not that deployment
// (used by rollback so a racing deploy cannot be clobbered).
func (s *PostgresDeploymentStore) PromoteDeployment(ctx context.Context, id, expectedActiveID string) error {
	return s.promoteDeployment(ctx, id, expectedActiveID, nil)
}

// DeploymentPromotionCommitted verifies the exact durable winner after an
// indeterminate COMMIT acknowledgement. It is intentionally read-only with
// respect to product state: only a candidate that is still the canonical
// active deployment for the same operation and whose complete public route
// set retains ready publication proof is accepted.
func (s *PostgresDeploymentStore) DeploymentPromotionCommitted(
	ctx context.Context,
	operationID string,
	id string,
	expectedActiveID string,
) (bool, error) {
	return s.deploymentPromotionCommitted(
		ctx,
		operationID,
		id,
		expectedActiveID,
		nil,
	)
}

// PromoteTemplateDeployment commits the canonical active pointer and the
// verified template saga's terminal state in the same PostgreSQL transaction.
// A worker must have already removed both secret-bearing smoke containers.
func (s *PostgresDeploymentStore) PromoteTemplateDeployment(ctx context.Context, id, expectedActiveID string) error {
	return s.promoteDeployment(ctx, id, expectedActiveID, func(ctx context.Context, tx *pg.Tx, now time.Time) error {
		tag, err := tx.Exec(ctx, `
			UPDATE template_provision_runs
			   SET state='succeeded', finished_at=COALESCE(finished_at,$2), updated_at=$2
			 WHERE deployment_id=$1
			   AND state IN ('queued','running','succeeded')
			   AND cleanup_completed
			   AND connectivity_verified
			   AND persistence_verified
			   AND write_probe_container_id <> ''
			   AND read_probe_container_id <> ''
			   AND marker_sha256 <> ''`, id, now)
		if err != nil {
			return fmt.Errorf("promote: complete template provision run: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("promote: verified template provision run for deployment %q was not found", id)
		}
		return nil
	})
}

// TemplateDeploymentPromotionCommitted adds the verified template saga's
// atomic terminal proof to the ordinary canonical deployment proof.
func (s *PostgresDeploymentStore) TemplateDeploymentPromotionCommitted(
	ctx context.Context,
	operationID string,
	id string,
	expectedActiveID string,
) (bool, error) {
	return s.deploymentPromotionCommitted(
		ctx,
		operationID,
		id,
		expectedActiveID,
		func(ctx context.Context, tx *pg.Tx) (bool, error) {
			var proved bool
			err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1
					  FROM template_provision_runs
					 WHERE deployment_id=$1
					   AND state='succeeded'
					   AND finished_at IS NOT NULL
					   AND cleanup_completed
					   AND connectivity_verified
					   AND persistence_verified
					   AND write_probe_container_id <> ''
					   AND read_probe_container_id <> ''
					   AND marker_sha256 <> ''
				)`,
				id,
			).Scan(&proved)
			if err != nil {
				return false, fmt.Errorf(
					"promote: verify committed template evidence: %w",
					err,
				)
			}
			return proved, nil
		},
	)
}

// PromoteCronDeployment atomically enables the immutable cron cursor/history
// transition and moves the canonical active pointer. A retry observes either
// both effects or neither; no worker can run a cursor for a deployment that
// lost its promotion CAS.
func (s *PostgresDeploymentStore) PromoteCronDeployment(ctx context.Context, id, expectedActiveID string, activation deploy.CronActivation) error {
	if activation.Candidate.DeploymentID != id || activation.Snapshot.DeploymentID != id {
		return fmt.Errorf("promote: cron activation deployment does not match candidate %q", id)
	}
	if err := deploy.ValidateCronActivation(activation); err != nil {
		return fmt.Errorf("promote: validate cron activation: %w", err)
	}
	err := s.promoteDeployment(ctx, id, expectedActiveID, func(ctx context.Context, tx *pg.Tx, now time.Time) error {
		if err := deploy.ActivateCronTx(ctx, tx, activation, now); err != nil {
			return fmt.Errorf("promote: activate cron deployment: %w", err)
		}
		return nil
	})
	if !errors.Is(err, ErrDeploymentTransitionConflict) {
		return err
	}
	// A worker may crash after this transaction commits but before it
	// acknowledges the queue job. Accept that retry only when every canonical
	// and cursor field still proves this exact immutable activation won.
	matched, verifyErr := s.cronDeploymentAlreadyPromoted(ctx, id, activation)
	if verifyErr != nil {
		return errors.Join(err, verifyErr)
	}
	if matched {
		return nil
	}
	return err
}

func (s *PostgresDeploymentStore) cronDeploymentAlreadyPromoted(ctx context.Context, id string, activation deploy.CronActivation) (bool, error) {
	var (
		state, activeID                                                   string
		active, enabled                                                   bool
		projectID, serviceID, environmentID, snapshotSHA, image, schedule string
		timeoutSeconds, maxAttempts, retryDelaySeconds                    int64
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT d.state,d.active,COALESCE(ses.active_deployment_id,''),
		       c.project_id,c.service_id,c.environment_id,c.snapshot_sha256,
		       c.image_ref,c.schedule,c.timeout_seconds,c.max_attempts,
		       c.retry_delay_seconds,c.enabled
		  FROM deployments d
		  JOIN service_environment_state ses
		    ON ses.service_id=d.service_id
		   AND ses.environment_id=d.environment_id
		  JOIN cron_schedule_cursors c
		    ON c.deployment_id=d.id
		 WHERE d.id=$1`,
		id,
	).Scan(
		&state, &active, &activeID,
		&projectID, &serviceID, &environmentID, &snapshotSHA,
		&image, &schedule, &timeoutSeconds, &maxAttempts,
		&retryDelaySeconds, &enabled,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("promote: verify committed cron activation: %w", err)
	}
	candidate, snapshot, cfg := activation.Candidate, activation.Snapshot, activation.Snapshot.Config
	return state == string(deploy.DeploymentActive) &&
		active &&
		activeID == id &&
		enabled &&
		projectID == candidate.ProjectID &&
		serviceID == candidate.ServiceID &&
		environmentID == candidate.EnvironmentID &&
		snapshotSHA == snapshot.SHA256 &&
		image == candidate.Image &&
		schedule == cfg.CronSchedule &&
		timeoutSeconds == int64(cfg.CronTimeout/time.Second) &&
		maxAttempts == int64(cfg.CronMaxAttempts) &&
		retryDelaySeconds == int64(cfg.CronRetryDelay/time.Second), nil
}

func (s *PostgresDeploymentStore) deploymentPromotionCommitted(
	ctx context.Context,
	operationID string,
	id string,
	expectedActiveID string,
	finalizeProof func(context.Context, *pg.Tx) (bool, error),
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := s.ready(); err != nil {
		return false, err
	}
	operationID = strings.TrimSpace(operationID)
	id = strings.TrimSpace(id)
	expectedActiveID = strings.TrimSpace(expectedActiveID)
	if operationID == "" || id == "" {
		return false, errors.New(
			"promote: operation and deployment are required for commit recovery",
		)
	}
	// Discover only the immutable lock scope before taking the dedicated
	// connection's session lock. Acquiring an xact advisory lock inside
	// REPEATABLE READ is insufficient here: the lock statement can establish
	// the transaction snapshot before it blocks, allowing a concurrent winner
	// to remain invisible after the lock is finally granted.
	var discoveredProjectID, discoveredServiceID, discoveredEnvironmentID string
	if err := s.Pool.QueryRow(ctx, `
		SELECT project_id,service_id,environment_id
		  FROM deployments
		 WHERE id=$1
		   AND operation_id=$2`,
		id,
		operationID,
	).Scan(
		&discoveredProjectID,
		&discoveredServiceID,
		&discoveredEnvironmentID,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf(
			"promote: discover committed candidate scope: %w",
			err,
		)
	}
	lockKey := "service-environment-deploy:" +
		discoveredServiceID + ":" + discoveredEnvironmentID
	var matched bool
	err := s.Pool.WithSessionAdvisoryLockTxOpts(
		ctx,
		lockKey,
		pgx.TxOptions{
			IsoLevel:   pgx.RepeatableRead,
			AccessMode: pgx.ReadOnly,
		},
		func(tx *pg.Tx) error {
			var projectID, serviceID, environmentID, state, activeID string
			var active bool
			err := tx.QueryRow(ctx, `
				SELECT d.project_id,d.service_id,d.environment_id,d.state,d.active,
				       COALESCE(ses.active_deployment_id,'')
				  FROM deployments d
				  LEFT JOIN service_environment_state ses
				    ON ses.service_id=d.service_id
				   AND ses.environment_id=d.environment_id
				 WHERE d.id=$1
				   AND d.operation_id=$2`,
				id,
				operationID,
			).Scan(
				&projectID,
				&serviceID,
				&environmentID,
				&state,
				&active,
				&activeID,
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return fmt.Errorf(
					"promote: verify committed candidate: %w",
					err,
				)
			}
			if projectID != discoveredProjectID ||
				serviceID != discoveredServiceID ||
				environmentID != discoveredEnvironmentID ||
				state != string(deploy.DeploymentActive) ||
				!active ||
				activeID != id {
				return nil
			}
			if expectedActiveID != "" && expectedActiveID != id {
				var previousProjectID, previousServiceID, previousEnvironmentID string
				var previousState string
				var previousActive bool
				err = tx.QueryRow(ctx, `
					SELECT project_id,service_id,environment_id,state,active
					  FROM deployments
					 WHERE id=$1`,
					expectedActiveID,
				).Scan(
					&previousProjectID,
					&previousServiceID,
					&previousEnvironmentID,
					&previousState,
					&previousActive,
				)
				if errors.Is(err, pgx.ErrNoRows) {
					return nil
				}
				if err != nil {
					return fmt.Errorf(
						"promote: verify committed previous deployment: %w",
						err,
					)
				}
				if previousProjectID != projectID ||
					previousServiceID != serviceID ||
					previousEnvironmentID != environmentID ||
					previousState != string(deploy.DeploymentSuperseded) ||
					previousActive {
					return nil
				}
			}
			if err := requireServiceManagedRoutesPublishedForPromotionInTx(
				ctx,
				tx,
				id,
				projectID,
				environmentID,
				serviceID,
			); err != nil {
				if errors.Is(err, ErrPromotionConflict) {
					return nil
				}
				return err
			}
			proved, err := deploymentPublicRoutesRetainPromotionProofInTx(
				ctx,
				tx,
				id,
				projectID,
				environmentID,
				serviceID,
			)
			if err != nil {
				return err
			}
			if !proved {
				return nil
			}
			if finalizeProof != nil {
				proved, err = finalizeProof(ctx, tx)
				if err != nil {
					return err
				}
				if !proved {
					return nil
				}
			}
			matched = true
			return nil
		},
	)
	return committedPromotionVerificationResult(matched, err)
}

func committedPromotionVerificationResult(matched bool, err error) (bool, error) {
	if err == nil {
		return matched, nil
	}
	// The proof transaction is read-only. Once every exact check completed,
	// losing only its COMMIT acknowledgement cannot invalidate the observed
	// snapshot and must not trigger route compensation.
	if matched && errors.Is(err, pg.ErrTxCommitIndeterminate) {
		return true, nil
	}
	return false, err
}

func (s *PostgresDeploymentStore) promoteDeployment(
	ctx context.Context,
	id, expectedActiveID string,
	finalize func(context.Context, *pg.Tx, time.Time) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ready(); err != nil {
		return err
	}
	now := s.Now()
	return s.Pool.WithTxOpts(ctx, pgx.TxOptions{}, func(tx *pg.Tx) error {
		// Read the candidate scope without a row lock so promotion can acquire
		// the same service/environment advisory lock as deploy intake and
		// generated-domain binding before any path locks the candidate or state
		// rows. Re-read and validate the scope after the advisory lock.
		var discoveredProjectID, discoveredServiceID, discoveredEnvironmentID string
		if err := tx.QueryRow(ctx,
			`SELECT project_id, service_id, environment_id
			   FROM deployments
			  WHERE id = $1`,
			id,
		).Scan(
			&discoveredProjectID,
			&discoveredServiceID,
			&discoveredEnvironmentID,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDeploymentNotFound
			}
			return fmt.Errorf("promote: discover candidate scope: %w", err)
		}
		lockKey := "service-environment-deploy:" +
			discoveredServiceID + ":" + discoveredEnvironmentID
		if _, err := tx.Exec(
			ctx,
			`SELECT pg_advisory_xact_lock(hashtext($1))`,
			lockKey,
		); err != nil {
			return fmt.Errorf("promote: service/environment advisory lock: %w", err)
		}

		// Lock the candidate and prove it still belongs to the discovered
		// scope. Deployment scope is immutable by contract, but this explicit
		// check keeps the lock-order preflight fail closed if that changes.
		var projectID, serviceID, environmentID, state string
		if err := tx.QueryRow(ctx,
			`SELECT project_id, service_id, environment_id, state
			   FROM deployments
			  WHERE id = $1
			  FOR UPDATE`,
			id,
		).Scan(&projectID, &serviceID, &environmentID, &state); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDeploymentNotFound
			}
			return fmt.Errorf("promote: load candidate: %w", err)
		}
		if projectID != discoveredProjectID ||
			serviceID != discoveredServiceID ||
			environmentID != discoveredEnvironmentID {
			return fmt.Errorf(
				"%w: candidate %q scope changed during promotion",
				ErrPromotionConflict,
				id,
			)
		}
		if !deploy.CanPromote(deploy.DeploymentState(state)) {
			return fmt.Errorf("%w: candidate %s is in state %s, not deploying", ErrDeploymentTransitionConflict, id, state)
		}

		// Lock the (service, env) state row, creating it if absent.
		if _, err := tx.Exec(ctx,
			`INSERT INTO service_environment_state (service_id, environment_id)
			 VALUES ($1, $2) ON CONFLICT (service_id, environment_id) DO NOTHING`,
			serviceID, environmentID,
		); err != nil {
			return fmt.Errorf("promote: ensure state row: %w", err)
		}
		var currentActive *string
		var sesVersion, lastSeq int64
		if err := tx.QueryRow(ctx,
			`SELECT active_deployment_id, version, last_deployment_seq FROM service_environment_state
			 WHERE service_id = $1 AND environment_id = $2 FOR UPDATE`,
			serviceID, environmentID,
		).Scan(&currentActive, &sesVersion, &lastSeq); err != nil {
			return fmt.Errorf("promote: lock state row: %w", err)
		}

		// CAS guard against active-pointer drift (rollback / concurrent deploy).
		cur := ""
		if currentActive != nil {
			cur = *currentActive
		}
		if expectedActiveID != "" && cur != expectedActiveID {
			return fmt.Errorf("%w: expected active %q, found %q", ErrPromotionConflict, expectedActiveID, cur)
		}
		if err := requireServiceManagedRoutesPublishedForPromotionInTx(
			ctx,
			tx,
			id,
			projectID,
			environmentID,
			serviceID,
		); err != nil {
			return err
		}

		// Supersede the previous active (if any).
		if cur != "" && cur != id {
			tag, err := tx.Exec(ctx,
				`UPDATE deployments
				    SET state = 'superseded', active = FALSE,
				        runtime_desired_state='stopped',runtime_observed_state='stopped',
				        runtime_generation=runtime_generation+1,updated_at = $2
				 WHERE id = $1 AND state = 'active'`, cur, now)
			if err != nil {
				return fmt.Errorf("promote: supersede previous: %w", err)
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("%w: previous active %q was not in state active", ErrPromotionConflict, cur)
			}
		}

		// Promote the candidate (CAS on deploying).
		tag, err := tx.Exec(ctx,
			`UPDATE deployments
			    SET state = 'active', active = TRUE,
			        runtime_desired_state='running',runtime_observed_state='running',
			        updated_at = $2, completed_at = $2
			 WHERE id = $1 AND state = 'deploying'`, id, now)
		if err != nil {
			return fmt.Errorf("promote: activate candidate: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: candidate %q left deploying before promotion", ErrDeploymentTransitionConflict, id)
		}

		// Move the active pointer + bump optimistic version and seq.
		if _, err := tx.Exec(ctx,
			`UPDATE service_environment_state
			   SET active_deployment_id = $3, version = version + 1,
			       last_deployment_seq = last_deployment_seq + 1, updated_at = $4
			 WHERE service_id = $1 AND environment_id = $2`,
			serviceID, environmentID, id, now,
		); err != nil {
			return fmt.Errorf("promote: move active pointer: %w", err)
		}
		if finalize != nil {
			if err := finalize(ctx, tx, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func requireServiceManagedRoutesPublishedForPromotionInTx(
	ctx context.Context,
	tx *pg.Tx,
	deploymentID string,
	projectID string,
	environmentID string,
	serviceID string,
) error {
	var unproven int64
	err := tx.QueryRow(ctx, `
		SELECT count(*)
		  FROM service_domain_bindings b
		  JOIN domains d
		    ON d.id=b.domain_id
		   AND d.project_id=b.project_id
		   AND d.environment_id=b.environment_id
		   AND d.service_id=b.service_id
		  JOIN http_routes r
		    ON r.id=b.route_id
		   AND r.domain_id=b.domain_id
		   AND r.project_id=b.project_id
		   AND r.environment_id=b.environment_id
		   AND r.service_id=b.service_id
		 WHERE b.project_id=$1
		   AND b.environment_id=$2
		   AND b.service_id=$3
		   AND `+router.ManagedServiceRouteEligibilityPredicateSQL+`
		   AND (
		       NOT EXISTS (
		           SELECT 1
		             FROM deployment_route_intents i
		            WHERE i.deployment_id=$4
		              AND i.route_id=b.route_id
		              AND i.domain_id=b.domain_id
		              AND i.project_id=b.project_id
		              AND i.environment_id=b.environment_id
		              AND i.service_id=b.service_id
		       )
		       OR NOT EXISTS (
		           SELECT 1
		             FROM route_backends rb
		            WHERE rb.route_id=b.route_id
		              AND rb.project_id=b.project_id
		              AND rb.environment_id=b.environment_id
		              AND rb.service_id=b.service_id
		              AND rb.deployment_id=$4
		              AND rb.ready=TRUE
		       )
		       OR NOT EXISTS (
		           SELECT 1
		             FROM route_controller_groups g
		             JOIN route_publication_observations o
		               ON o.project_id=g.project_id
		              AND o.environment_id=g.environment_id
		              AND o.desired_revision=g.desired_revision
		              AND o.artifact_hash=g.artifact_hash
		              AND o.state='ready'
		              AND o.traefik_acknowledged=TRUE
		              AND o.ingress_probe_succeeded=TRUE
		            WHERE g.project_id=b.project_id
		              AND g.environment_id=b.environment_id
		              AND g.apply_state='ready'
		              AND g.desired_revision=g.observed_revision
		              AND g.desired_revision=g.applied_revision
		       )
		   )`,
		strings.TrimSpace(projectID),
		strings.TrimSpace(environmentID),
		strings.TrimSpace(serviceID),
		strings.TrimSpace(deploymentID),
	).Scan(&unproven)
	if err != nil {
		return fmt.Errorf(
			"promote: validate service-managed route publication: %w",
			err,
		)
	}
	if unproven > 0 {
		return fmt.Errorf(
			"%w: candidate %q has %d unbound or unverified service-managed routes",
			ErrPromotionConflict,
			deploymentID,
			unproven,
		)
	}
	return nil
}

func deploymentPublicRoutesRetainPromotionProofInTx(
	ctx context.Context,
	tx *pg.Tx,
	deploymentID string,
	projectID string,
	environmentID string,
	serviceID string,
) (bool, error) {
	var scopeMismatch bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM deployment_route_intents
			 WHERE deployment_id=$1
			   AND (
			       project_id<>$2
			       OR environment_id<>$3
			       OR service_id<>$4
			   )
		)`,
		strings.TrimSpace(deploymentID),
		strings.TrimSpace(projectID),
		strings.TrimSpace(environmentID),
		strings.TrimSpace(serviceID),
	).Scan(&scopeMismatch); err != nil {
		return false, fmt.Errorf(
			"promote: verify committed route scope: %w",
			err,
		)
	}
	if scopeMismatch {
		return false, nil
	}

	var unproven int64
	err := tx.QueryRow(ctx, `
		SELECT count(*)
		  FROM deployment_route_intents i
		  JOIN http_routes r
		    ON r.id=i.route_id
		   AND r.domain_id=i.domain_id
		   AND r.project_id=i.project_id
		   AND r.environment_id=i.environment_id
		   AND r.service_id=i.service_id
		  JOIN domains d
		    ON d.id=i.domain_id
		   AND d.project_id=i.project_id
		   AND d.environment_id=i.environment_id
		   AND d.service_id=i.service_id
		 WHERE i.deployment_id=$1
		   AND i.project_id=$2
		   AND i.environment_id=$3
		   AND i.service_id=$4
		   AND (
		       NOT (`+router.ManagedServiceRouteEligibilityPredicateSQL+`)
		       OR NOT EXISTS (
		           SELECT 1
		             FROM route_controller_groups g
		             JOIN route_publication_observations o
		               ON o.project_id=g.project_id
		              AND o.environment_id=g.environment_id
		              AND o.desired_revision=g.desired_revision
		              AND o.artifact_hash=g.artifact_hash
		              AND o.state='ready'
		              AND o.traefik_acknowledged=TRUE
		              AND o.ingress_probe_succeeded=TRUE
		            WHERE g.project_id=i.project_id
		              AND g.environment_id=i.environment_id
		              AND g.apply_state='ready'
		              AND g.desired_revision=g.observed_revision
		              AND g.desired_revision=g.applied_revision
		              AND EXISTS (
		                  SELECT 1
		                    FROM route_backends rb
		                   WHERE rb.route_id=i.route_id
		                     AND rb.project_id=i.project_id
		                     AND rb.environment_id=i.environment_id
		                     AND rb.service_id=i.service_id
		                     AND rb.deployment_id=$1
		                     AND rb.ready=TRUE
		                     AND rb.desired_revision=g.desired_revision
		              )
		              AND NOT EXISTS (
		                  SELECT 1
		                    FROM route_backends rb
		                   WHERE rb.route_id=i.route_id
		                     AND (
		                         rb.project_id<>i.project_id
		                         OR rb.environment_id<>i.environment_id
		                         OR rb.service_id<>i.service_id
		                         OR rb.deployment_id<>$1
		                         OR rb.ready=FALSE
		                         OR rb.desired_revision<>g.desired_revision
		                     )
		              )
		       )
		   )`,
		strings.TrimSpace(deploymentID),
		strings.TrimSpace(projectID),
		strings.TrimSpace(environmentID),
		strings.TrimSpace(serviceID),
	).Scan(&unproven)
	if err != nil {
		return false, fmt.Errorf(
			"promote: verify committed deployment route publication: %w",
			err,
		)
	}
	return unproven == 0, nil
}

// CreateRollbackDeployment creates a NEW queued deployment that re-pulls the
// immutable digest of a prior deployment (sourceID). It NEVER rebuilds. The
// caller promotes it via PromoteDeployment with expectedActiveID set to the
// current active so a racing deploy cannot be clobbered.
func (s *PostgresDeploymentStore) CreateRollbackDeployment(ctx context.Context, sourceID string) (DeploymentRecord, error) {
	if err := ctx.Err(); err != nil {
		return DeploymentRecord{}, err
	}
	if err := s.ready(); err != nil {
		return DeploymentRecord{}, err
	}
	rec := DeploymentRecord{
		ID: s.IDFactory(), Action: deploy.ActionRollbackDigest,
		RollbackSourceID: sourceID, State: deploy.DeploymentQueued,
	}
	now := s.Now()
	rec.CreatedAt, rec.UpdatedAt = now, now
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var expectedActive *string
		var snapshotRevision *int64
		if err := tx.QueryRow(ctx,
			`SELECT d.project_id,d.service_id,d.environment_id,d.image_ref,d.image_digest,
			        ses.active_deployment_id,snap.configuration_revision
			   FROM deployments d
			   LEFT JOIN service_environment_state ses
			     ON ses.service_id=d.service_id AND ses.environment_id=d.environment_id
			   LEFT JOIN deployment_config_snapshots snap ON snap.deployment_id=d.id
			  WHERE d.id=$1
			  FOR UPDATE OF d`, sourceID,
		).Scan(&rec.ProjectID, &rec.ServiceID, &rec.EnvironmentID, &rec.ImageRef, &rec.ImageDigest, &expectedActive, &snapshotRevision); errors.Is(err, pgx.ErrNoRows) {
			return ErrDeploymentNotFound
		} else if err != nil {
			return fmt.Errorf("rollback: load source: %w", err)
		}
		if rec.ImageDigest == "" {
			return fmt.Errorf("rollback: source %s has no recorded image digest", sourceID)
		}
		if expectedActive != nil {
			rec.ExpectedActiveID = *expectedActive
		}
		if snapshotRevision != nil {
			rec.ConfigurationRevision = *snapshotRevision
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO deployments (
				id,project_id,service_id,environment_id,action,image_ref,image_digest,
				state,active,rollback_source_deployment_id,expected_active_deployment_id,
				configuration_revision,created_at,updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,'queued',FALSE,$8,$9,$10,$11,$11)`,
			rec.ID, rec.ProjectID, rec.ServiceID, rec.EnvironmentID, string(rec.Action),
			rec.ImageRef, rec.ImageDigest, sourceID, nullableString(rec.ExpectedActiveID),
			rec.ConfigurationRevision, rec.CreatedAt); err != nil {
			return fmt.Errorf("rollback: deployment insert: %w", err)
		}
		if snapshotRevision == nil {
			// Compatibility for an old deployment whose worker never captured a
			// snapshot: retain honest revision zero and let ordinary resolution
			// create the first immutable snapshot if this legacy seam is executed.
			return nil
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO deployment_config_snapshots (
				deployment_id,project_id,service_id,environment_id,configuration_revision,
				config_version,effective_config,variable_versions,snapshot_sha256,created_at
			)
			SELECT $1,project_id,service_id,environment_id,configuration_revision,
			       config_version,effective_config,variable_versions,snapshot_sha256,$2
			  FROM deployment_config_snapshots WHERE deployment_id=$3`,
			rec.ID, rec.CreatedAt, sourceID); err != nil {
			return fmt.Errorf("rollback: snapshot clone: %w", err)
		}
		return nil
	})
	if err != nil {
		return DeploymentRecord{}, err
	}
	return rec, nil
}
