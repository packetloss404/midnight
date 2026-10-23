// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/metering"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/internal/volume"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const tenantMutationSerializableAttempts = 3

var (
	errTenantProjectConflictRace = errors.New("tenant project conflict raced transaction")
	errTenantServiceConflictRace = errors.New("tenant service conflict raced transaction")
)

// TenantProjectCreateRequest contains only server-verified identity metadata,
// validated product input, and the canonical idempotency fingerprint.
type TenantProjectCreateRequest struct {
	Principal   identity.Principal
	Session     identity.Session
	Project     Project
	Key         idempotency.Key
	Fingerprint idempotency.RequestFingerprint
	RequestID   string
	SourceIP    string
	UserAgent   string
}

// TenantServiceCreateRequest contains a server-verified human identity tuple,
// validated service input, and the canonical request fingerprint. Project and
// workspace lineage are always resolved again inside the transaction.
type TenantServiceCreateRequest struct {
	Principal   identity.Principal
	Session     identity.Session
	Service     Service
	Key         idempotency.Key
	Fingerprint idempotency.RequestFingerprint
	RequestID   string
	SourceIP    string
	UserAgent   string
}

// TenantEnvironmentCreateRequest contains a server-verified human identity
// tuple, ordinary environment metadata, and the canonical request
// fingerprint. Protected-environment policy is intentionally outside this
// first human lifecycle slice.
type TenantEnvironmentCreateRequest struct {
	Principal   identity.Principal
	Session     identity.Session
	Environment Environment
	Key         idempotency.Key
	Fingerprint idempotency.RequestFingerprint
	RequestID   string
	SourceIP    string
	UserAgent   string
}

// TenantEnvironmentUpdate contains one strict partial metadata patch. The
// project, protected state, access restriction, and lifecycle state are
// resolved from the canonical row inside the transaction.
type TenantEnvironmentUpdate struct {
	Slug            *string
	Name            *string
	Tier            *string
	ExpectedVersion int64
}

// TenantEnvironmentUpdateRequest carries the verified human tuple, exact
// environment route, strict metadata patch, and replay/audit metadata.
type TenantEnvironmentUpdateRequest struct {
	Principal     identity.Principal
	Session       identity.Session
	EnvironmentID string
	Update        TenantEnvironmentUpdate
	Key           idempotency.Key
	Fingerprint   idempotency.RequestFingerprint
	RequestID     string
	SourceIP      string
	UserAgent     string
}

// TenantServiceConfigReadRequest carries the verified human tuple and exact
// service/environment route. Canonical project/workspace lineage is resolved
// again under transaction locks.
type TenantServiceConfigReadRequest struct {
	Principal     identity.Principal
	Session       identity.Session
	ServiceID     string
	EnvironmentID string
}

// TenantServiceConfigPutRequest contains a validated desired configuration
// plus the exact request fingerprint and audit metadata required to commit
// config, replay, and evidence as one transaction.
type TenantServiceConfigPutRequest struct {
	Principal       identity.Principal
	Session         identity.Session
	Config          serviceconfig.Config
	ExpectedVersion int64
	Key             idempotency.Key
	Fingerprint     idempotency.RequestFingerprint
	RequestID       string
	SourceIP        string
	UserAgent       string
}

// TenantMutationResult is the exact response committed beside the product
// mutation. Replayed is transport metadata and is not part of the stored body.
type TenantMutationResult struct {
	Response idempotency.StoredResponse
	Replayed bool
}

// TenantMutationCoordinator is the only human-product-write seam. Routes not
// represented here remain blocked by the human product gate.
type TenantMutationCoordinator interface {
	CreateProject(context.Context, TenantProjectCreateRequest) (TenantMutationResult, error)
	CreateService(context.Context, TenantServiceCreateRequest) (TenantMutationResult, error)
	CreateEnvironment(context.Context, TenantEnvironmentCreateRequest) (TenantMutationResult, error)
}

// TenantEnvironmentUpdateCoordinator independently admits the exact human
// PATCH environment metadata route. Installing it cannot authorize protected
// state changes, deletion, duplication, or any other lifecycle mutation.
type TenantEnvironmentUpdateCoordinator interface {
	UpdateEnvironment(context.Context, TenantEnvironmentUpdateRequest) (TenantMutationResult, error)
}

// TenantServiceConfigCoordinator is an independently admitted human
// service/environment configuration seam. Installing it cannot widen any
// other product mutation.
type TenantServiceConfigCoordinator interface {
	GetServiceConfig(context.Context, TenantServiceConfigReadRequest) (ServiceConfigView, error)
	PutServiceConfig(context.Context, TenantServiceConfigPutRequest) (TenantMutationResult, error)
}

// PostgresTenantMutationCoordinator linearizes current session validity,
// membership authorization, idempotency, product rows, and audit evidence.
type PostgresTenantMutationCoordinator struct {
	Pool              *pg.Pool
	Resources         *PostgresResourceStore
	ServiceConfigs    *serviceconfig.Store
	Volumes           *volume.PostgresStore
	Metering          *metering.PostgresStore
	DeploymentActions *PostgresDeploymentActionStore
	DeployOutbox      *PostgresDeployOutbox
	// Routes and AppsDomain are the single durable authority used by the
	// HumanSession generated-domain lifecycle. They are intentionally absent
	// from memory-mode coordinators so that surface fails closed.
	Routes     *router.PostgresDurableRouteStore
	AppsDomain string
	// VariableKeyProvider seals tenant variable payloads before the authorized
	// transaction persists ciphertext. VariableIntentMACKey fingerprints the
	// value-bearing request with an independent keyed digest.
	VariableKeyProvider  secrets.KeyProvider
	VariableIntentMACKey []byte
	Idempotency          *idempotency.PostgresStore
	Audit                *audit.PostgresChain
	Now                  func() time.Time
	IDFactory            func(prefix orailtypes.IDPrefix) orailtypes.ID
	// CustomDNSResolver is the bounded TXT observation seam for tenant custom
	// hostnames. A nil value uses the process resolver with a short deadline;
	// no route or HTTPS reachability is inferred from this evidence.
	CustomDNSResolver CustomDNSResolver
}

func NewPostgresTenantMutationCoordinator(pool *pg.Pool, chain *audit.PostgresChain, now func() time.Time) *PostgresTenantMutationCoordinator {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	resources := NewPostgresResourceStore(pool)
	resources.Now = now
	serviceConfigs := serviceconfig.NewStore(pool)
	serviceConfigs.Now = now
	usage := metering.NewPostgresStore(pool)
	usage.Audit = chain
	usage.Now = now
	return &PostgresTenantMutationCoordinator{
		Pool:           pool,
		Resources:      resources,
		ServiceConfigs: serviceConfigs,
		Metering:       usage,
		Idempotency:    idempotency.NewPostgresStore(pool),
		Audit:          chain,
		Now:            now,
		IDFactory:      defaultResourceID,
	}
}

func (c *PostgresTenantMutationCoordinator) CreateProject(ctx context.Context, request TenantProjectCreateRequest) (TenantMutationResult, error) {
	if err := c.validateProjectCreate(request); err != nil {
		return TenantMutationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return TenantMutationResult{}, err
	}

	// IDs are minted once outside the retry loop. A commit whose result is
	// ambiguous can safely replay the same scope and exact response.
	project := request.Project
	project.ID = c.IDFactory(orailtypes.IDPrefixProject)
	environmentID := c.IDFactory(orailtypes.IDPrefixEnvironment)
	now := c.now().Truncate(time.Microsecond)
	project.CreatedAt = now
	project.UpdatedAt = now
	request.Project = project

	tenantCtx := idempotency.WithTenant(ctx, idempotency.Tenant(project.WorkspaceID))
	tenantCtx = idempotency.WithActor(tenantCtx, idempotency.UserActor(request.Principal.UserID))

	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(tenantCtx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			result, err = c.createProjectInTx(tenantCtx, tx, request, environmentID, now)
			return err
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, err
		}
	}
	return TenantMutationResult{}, errors.New("tenant mutation serializable retries exhausted")
}

// CreateService linearizes current session validity, canonical project access,
// idempotency, the service row, exact response, and audit evidence. The service
// identity and timestamp are prepared once so a SERIALIZABLE retry cannot
// create a second logical resource or return a different representation.
func (c *PostgresTenantMutationCoordinator) CreateService(ctx context.Context, request TenantServiceCreateRequest) (TenantMutationResult, error) {
	if err := c.validateServiceCreate(request); err != nil {
		return TenantMutationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return TenantMutationResult{}, err
	}

	service := request.Service
	service.ID = c.IDFactory(orailtypes.IDPrefixService)
	now := c.now().Truncate(time.Microsecond)
	service.CreatedAt = now
	service.UpdatedAt = now
	request.Service = service

	var result TenantMutationResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			result, err = c.createServiceInTx(ctx, tx, request, now)
			return err
		})
		if err == nil {
			return cloneTenantMutationResult(result), nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantMutationResult{}, err
		}
	}
	return TenantMutationResult{}, errors.New("tenant service mutation serializable retries exhausted")
}

func (c *PostgresTenantMutationCoordinator) createServiceInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantServiceCreateRequest,
	now time.Time,
) (TenantMutationResult, error) {
	projectID := string(request.Service.ProjectID)
	workspaceID, err := resolveTenantServiceProjectWorkspace(ctx, tx, projectID)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:"+workspaceID); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant service workspace membership lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "project-members:"+projectID); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant service project membership lock: %w", err)
	}

	mutationCtx := idempotency.WithActor(ctx, idempotency.UserActor(request.Principal.UserID))
	mutationCtx = idempotency.WithTenant(mutationCtx, projectScopedIdempotencyTenant(workspaceID, projectID))
	if err := revalidateTenantMutationSessionTuple(
		mutationCtx,
		tx,
		request.Principal,
		request.Session,
		now,
	); err != nil {
		return TenantMutationResult{}, err
	}
	canonicalWorkspaceID, workspaceRole, projectRole, err := lockTenantServiceProjectAccess(
		mutationCtx,
		tx,
		projectID,
		workspaceID,
		request.Principal.UserID,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if canonicalWorkspaceID != workspaceID {
		return TenantMutationResult{}, identity.ErrAccessNotFound
	}
	decision := identity.EvaluateAccess(identity.AccessRequest{
		WorkspaceRole: workspaceRole,
		ProjectRole:   projectRole,
		Capability:    identity.CapabilityServiceWrite,
	})
	if !decision.Allowed {
		return TenantMutationResult{}, identity.ErrAuthorizationDenied
	}

	reservation, err := c.Idempotency.ReserveInTx(
		mutationCtx,
		tx,
		request.Key,
		request.Fingerprint,
		24*time.Hour,
	)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return TenantMutationResult{}, errors.New("tenant service replay has no stored response")
		}
		return TenantMutationResult{Response: reservation.Response.Clone(), Replayed: true}, nil
	}

	service := request.Service
	var existingServiceID string
	err = tx.QueryRow(mutationCtx, `
		SELECT id
		FROM services
		WHERE project_id=$1
		  AND slug=$2`,
		projectID,
		service.Slug,
	).Scan(&existingServiceID)
	if err == nil {
		response, responseErr := serviceConflictStoredResponse(request.RequestID, service)
		if responseErr != nil {
			return TenantMutationResult{}, responseErr
		}
		if _, completeErr := c.Idempotency.CompleteFailureInTx(
			mutationCtx,
			tx,
			reservation.Reference(),
			response,
		); completeErr != nil {
			return TenantMutationResult{}, fmt.Errorf("tenant service conflict idempotency complete: %w", completeErr)
		}
		if _, auditErr := c.Audit.AppendInTx(mutationCtx, tx, tenantServiceAuditRow(
			request,
			now,
			workspaceID,
			workspaceRole,
			projectRole,
			existingServiceID,
			string(failurecodes.ResourceConflict),
			"denied",
		)); auditErr != nil {
			return TenantMutationResult{}, fmt.Errorf("tenant service conflict audit append: %w", auditErr)
		}
		return TenantMutationResult{Response: response}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return TenantMutationResult{}, fmt.Errorf("tenant service conflict lookup: %w", err)
	}

	service, err = c.Resources.CreateServiceInTx(mutationCtx, tx, service)
	if err != nil {
		if errors.Is(err, ErrResourceConflict) {
			// A compatibility writer can race without taking the project
			// membership lock. Retrying makes its committed row visible to the
			// exact conflict-response path above.
			return TenantMutationResult{}, fmt.Errorf("%w: %v", errTenantServiceConflictRace, err)
		}
		return TenantMutationResult{}, fmt.Errorf("tenant service insert: %w", err)
	}
	response, err := serviceCreatedStoredResponse(service)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(
		mutationCtx,
		tx,
		reservation.Reference(),
		response,
	); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant service idempotency complete: %w", err)
	}
	if _, err := c.Audit.AppendInTx(mutationCtx, tx, tenantServiceAuditRow(
		request,
		now,
		workspaceID,
		workspaceRole,
		projectRole,
		string(service.ID),
		"",
		"allowed",
	)); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant service audit append: %w", err)
	}
	return TenantMutationResult{Response: response}, nil
}

func (c *PostgresTenantMutationCoordinator) createProjectInTx(
	ctx context.Context,
	tx *pg.Tx,
	request TenantProjectCreateRequest,
	environmentID orailtypes.ID,
	now time.Time,
) (TenantMutationResult, error) {
	workspaceID := request.Project.WorkspaceID
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:"+workspaceID); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant project workspace lock: %w", err)
	}
	if err := revalidateTenantMutationSession(ctx, tx, request, now); err != nil {
		return TenantMutationResult{}, err
	}
	role, err := lockTenantWorkspaceMembership(ctx, tx, workspaceID, request.Principal.UserID)
	if err != nil {
		return TenantMutationResult{}, err
	}
	decision := identity.EvaluateAccess(identity.AccessRequest{
		WorkspaceRole: role,
		Capability:    identity.CapabilityProjectCreate,
	})
	if !decision.Allowed {
		return TenantMutationResult{}, identity.ErrAuthorizationDenied
	}

	reservation, err := c.Idempotency.ReserveInTx(ctx, tx, request.Key, request.Fingerprint, 24*time.Hour)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return TenantMutationResult{}, errors.New("tenant project replay has no stored response")
		}
		return TenantMutationResult{Response: reservation.Response.Clone(), Replayed: true}, nil
	}

	project := request.Project
	var existingProjectID string
	err = tx.QueryRow(ctx, `
		SELECT id
		FROM projects
		WHERE workspace_id=$1
		  AND slug=$2
		  AND deleted_at IS NULL`,
		project.WorkspaceID,
		project.Slug,
	).Scan(&existingProjectID)
	if err == nil {
		response, responseErr := projectConflictStoredResponse(request.RequestID, project)
		if responseErr != nil {
			return TenantMutationResult{}, responseErr
		}
		if _, completeErr := c.Idempotency.CompleteFailureInTx(ctx, tx, reservation.Reference(), response); completeErr != nil {
			return TenantMutationResult{}, fmt.Errorf("tenant project conflict idempotency complete: %w", completeErr)
		}
		if _, auditErr := c.Audit.AppendInTx(ctx, tx, audit.Row{
			RequestID:      request.RequestID,
			OccurredAt:     now,
			RecordedAt:     now,
			ActorID:        request.Principal.UserID,
			ActorType:      "user",
			AuthMethod:     request.Principal.AuthMethod,
			SourceIP:       normalizedMutationSourceIP(request.SourceIP),
			UserAgent:      request.UserAgent,
			WorkspaceID:    project.WorkspaceID,
			ProjectID:      existingProjectID,
			ResourceKind:   "project",
			ResourceID:     existingProjectID,
			Action:         "project.create",
			ReasonCode:     string(failurecodes.ResourceConflict),
			PolicyDecision: "denied",
			Attrs: map[string]any{
				"capability":     string(identity.CapabilityProjectCreate),
				"workspace_role": string(role),
				"session_id":     request.Principal.SessionID,
				"slug":           project.Slug,
			},
		}); auditErr != nil {
			return TenantMutationResult{}, fmt.Errorf("tenant project conflict audit append: %w", auditErr)
		}
		return TenantMutationResult{Response: response}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return TenantMutationResult{}, fmt.Errorf("tenant project conflict lookup: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO projects (
			id, workspace_id, slug, display_name, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$5)`,
		string(project.ID), project.WorkspaceID, project.Slug, project.DisplayName, now,
	); err != nil {
		if isTenantUniqueViolation(err) {
			// A compatibility writer can race the preflight lookup without
			// taking the coordinator's workspace lock. Retry the whole
			// transaction so the now-visible conflict can be stored exactly.
			return TenantMutationResult{}, fmt.Errorf("%w: %v", errTenantProjectConflictRace, err)
		}
		return TenantMutationResult{}, fmt.Errorf("tenant project insert: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO environments (
			id, project_id, slug, name, tier, is_protected, created_at, updated_at
		) VALUES ($1,$2,'production','Production','production',TRUE,$3,$3)`,
		string(environmentID), string(project.ID), now,
	); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant default environment insert: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO project_members (
			project_id, user_id, role, invited_by, invited_at, accepted_at,
			created_at, updated_at, resource_version
		) VALUES ($1,$2,'owner',$2,$3,$3,$3,$3,1)`,
		string(project.ID), request.Principal.UserID, now,
	); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant project owner insert: %w", err)
	}

	response, err := projectCreatedStoredResponse(project)
	if err != nil {
		return TenantMutationResult{}, err
	}
	if _, err := c.Idempotency.CompleteSuccessInTx(ctx, tx, reservation.Reference(), response); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant project idempotency complete: %w", err)
	}
	if _, err := c.Audit.AppendInTx(ctx, tx, audit.Row{
		RequestID:      request.RequestID,
		OccurredAt:     now,
		RecordedAt:     now,
		ActorID:        request.Principal.UserID,
		ActorType:      "user",
		AuthMethod:     request.Principal.AuthMethod,
		SourceIP:       normalizedMutationSourceIP(request.SourceIP),
		UserAgent:      request.UserAgent,
		WorkspaceID:    project.WorkspaceID,
		ProjectID:      string(project.ID),
		ResourceKind:   "project",
		ResourceID:     string(project.ID),
		Action:         "project.create",
		PolicyDecision: "allowed",
		After: map[string]any{
			"slug":           project.Slug,
			"display_name":   project.DisplayName,
			"environment_id": string(environmentID),
		},
		Attrs: map[string]any{
			"capability":     string(identity.CapabilityProjectCreate),
			"workspace_role": string(role),
			"session_id":     request.Principal.SessionID,
		},
	}); err != nil {
		return TenantMutationResult{}, fmt.Errorf("tenant project audit append: %w", err)
	}
	return TenantMutationResult{Response: response}, nil
}

func (c *PostgresTenantMutationCoordinator) validateProjectCreate(request TenantProjectCreateRequest) error {
	if c == nil || c.Pool == nil || c.Idempotency == nil || c.Audit == nil || c.IDFactory == nil {
		return errors.New("tenant project mutation coordinator is not fully configured")
	}
	if request.Principal.Kind != "user" ||
		request.Principal.UserID == "" ||
		request.Principal.SessionID == "" ||
		request.Session.ID != request.Principal.SessionID ||
		request.Session.UserID != request.Principal.UserID ||
		request.Session.SessionGeneration <= 0 {
		return identity.ErrSessionInvalid
	}
	if strings.TrimSpace(request.Project.WorkspaceID) == "" ||
		strings.TrimSpace(request.Project.Slug) == "" ||
		strings.TrimSpace(request.Project.DisplayName) == "" {
		return fmt.Errorf("%w: workspace_id, slug, and display_name are required", ErrResourceInvalid)
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	return idempotency.ValidateFingerprint(request.Fingerprint)
}

func (c *PostgresTenantMutationCoordinator) validateServiceCreate(request TenantServiceCreateRequest) error {
	if c == nil ||
		c.Pool == nil ||
		c.Resources == nil ||
		c.Resources.Pool == nil ||
		c.Resources.Pool != c.Pool ||
		c.Idempotency == nil ||
		c.Idempotency.Pool == nil ||
		c.Idempotency.Pool != c.Pool ||
		c.Audit == nil ||
		c.Audit.Pool == nil ||
		c.Audit.Pool != c.Pool ||
		c.IDFactory == nil {
		return errors.New("tenant service mutation coordinator does not share one postgres authority")
	}
	if request.Principal.Kind != "user" ||
		request.Principal.UserID == "" ||
		request.Principal.SessionID == "" ||
		request.Session.ID != request.Principal.SessionID ||
		request.Session.UserID != request.Principal.UserID ||
		request.Session.SessionGeneration <= 0 {
		return identity.ErrSessionInvalid
	}
	if err := validateServiceCreateFields(request.Service, true); err != nil {
		return err
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	return idempotency.ValidateFingerprint(request.Fingerprint)
}

func revalidateTenantMutationSession(
	ctx context.Context,
	tx *pg.Tx,
	request TenantProjectCreateRequest,
	now time.Time,
) error {
	return revalidateTenantMutationSessionTuple(ctx, tx, request.Principal, request.Session, now)
}

func revalidateTenantMutationSessionTuple(
	ctx context.Context,
	tx *pg.Tx,
	principal identity.Principal,
	session identity.Session,
	now time.Time,
) error {
	var valid bool
	err := tx.QueryRow(ctx, `
		SELECT TRUE
		FROM auth_sessions s
		JOIN users u ON u.id=s.user_id
		WHERE s.id=$1
		  AND s.user_id=$2
		  AND s.session_generation=$3
		  AND u.session_generation=$3
		  AND s.revoked_at IS NULL
		  AND s.expires_at>$4
		  AND s.idle_expires_at>$4
		  AND u.deleted_at IS NULL
		  AND u.is_disabled=FALSE
		  AND u.must_rotate_password=FALSE
		FOR UPDATE OF s, u`,
		session.ID,
		principal.UserID,
		session.SessionGeneration,
		now,
	).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.ErrSessionInvalid
	}
	if err != nil {
		return fmt.Errorf("tenant mutation session revalidation: %w", err)
	}
	if !valid {
		return identity.ErrSessionInvalid
	}
	return nil
}

func resolveTenantServiceProjectWorkspace(ctx context.Context, tx *pg.Tx, projectID string) (string, error) {
	var workspaceID string
	err := tx.QueryRow(ctx, `
		SELECT p.workspace_id
		FROM projects p
		JOIN workspaces w ON w.id=p.workspace_id
		WHERE p.id=$1
		  AND p.deleted_at IS NULL
		  AND w.deleted_at IS NULL`,
		projectID,
	).Scan(&workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", identity.ErrAccessNotFound
	}
	if err != nil {
		return "", fmt.Errorf("tenant service project lineage resolve: %w", err)
	}
	return workspaceID, nil
}

func lockTenantServiceProjectAccess(
	ctx context.Context,
	tx *pg.Tx,
	projectID string,
	workspaceID string,
	userID string,
) (string, identity.WorkspaceRole, identity.ProjectRole, error) {
	var canonicalWorkspaceID string
	err := tx.QueryRow(ctx, `
		SELECT p.workspace_id
		FROM projects p
		JOIN workspaces w ON w.id=p.workspace_id
		WHERE p.id=$1
		  AND p.workspace_id=$2
		  AND p.deleted_at IS NULL
		  AND w.deleted_at IS NULL
		FOR UPDATE OF p, w`,
		projectID,
		workspaceID,
	).Scan(&canonicalWorkspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", identity.ErrAccessNotFound
	}
	if err != nil {
		return "", "", "", fmt.Errorf("tenant service project lineage lock: %w", err)
	}

	workspaceRole, err := lockOptionalTenantWorkspaceRole(ctx, tx, canonicalWorkspaceID, userID)
	if err != nil {
		return "", "", "", err
	}
	projectRole, err := lockOptionalTenantProjectRole(ctx, tx, projectID, userID)
	if err != nil {
		return "", "", "", err
	}
	if workspaceRole == "" && projectRole == "" {
		return "", "", "", identity.ErrAccessNotFound
	}
	return canonicalWorkspaceID, workspaceRole, projectRole, nil
}

func lockOptionalTenantWorkspaceRole(
	ctx context.Context,
	tx *pg.Tx,
	workspaceID string,
	userID string,
) (identity.WorkspaceRole, error) {
	var rawRole string
	err := tx.QueryRow(ctx, `
		SELECT role
		FROM workspace_members
		WHERE workspace_id=$1
		  AND user_id=$2
		  AND accepted_at IS NOT NULL
		FOR UPDATE`,
		workspaceID,
		userID,
	).Scan(&rawRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("tenant service workspace membership lock: %w", err)
	}
	role, ok := identity.ParseWorkspaceRole(rawRole)
	if !ok {
		return "", fmt.Errorf("tenant service workspace membership has invalid role %q", rawRole)
	}
	return role, nil
}

func lockOptionalTenantProjectRole(
	ctx context.Context,
	tx *pg.Tx,
	projectID string,
	userID string,
) (identity.ProjectRole, error) {
	var rawRole string
	err := tx.QueryRow(ctx, `
		SELECT role
		FROM project_members
		WHERE project_id=$1
		  AND user_id=$2
		  AND accepted_at IS NOT NULL
		FOR UPDATE`,
		projectID,
		userID,
	).Scan(&rawRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("tenant service project membership lock: %w", err)
	}
	role, ok := identity.ParseProjectRole(rawRole)
	if !ok {
		return "", fmt.Errorf("tenant service project membership has invalid role %q", rawRole)
	}
	return role, nil
}

func lockTenantWorkspaceMembership(ctx context.Context, tx *pg.Tx, workspaceID, userID string) (identity.WorkspaceRole, error) {
	var role identity.WorkspaceRole
	err := tx.QueryRow(ctx, `
		SELECT wm.role
		FROM workspace_members wm
		JOIN workspaces w ON w.id=wm.workspace_id
		WHERE wm.workspace_id=$1
		  AND wm.user_id=$2
		  AND wm.accepted_at IS NOT NULL
		  AND w.deleted_at IS NULL
		FOR UPDATE OF wm, w`,
		workspaceID,
		userID,
	).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", identity.ErrAccessNotFound
	}
	if err != nil {
		return "", fmt.Errorf("tenant workspace membership lock: %w", err)
	}
	return role, nil
}

func projectCreatedStoredResponse(project Project) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(project)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant project response encode: %w", err)
	}
	body = append(body, '\n')
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Location", v1alphaPrefix+resourceCollectionProjects+"/"+string(project.ID))
	return idempotency.StoredResponse{
		StatusCode: http.StatusCreated,
		Header:     headers,
		Body:       body,
	}, nil
}

func serviceCreatedStoredResponse(service Service) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(service)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant service response encode: %w", err)
	}
	body = append(body, '\n')
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Location", v1alphaPrefix+resourceCollectionServices+"/"+string(service.ID))
	return idempotency.StoredResponse{
		StatusCode: http.StatusCreated,
		Header:     headers,
		Body:       body,
	}, nil
}

func projectConflictStoredResponse(requestID string, project Project) (idempotency.StoredResponse, error) {
	message := fmt.Sprintf(
		"%s: project slug %q already exists in workspace %q",
		ErrResourceConflict,
		project.Slug,
		project.WorkspaceID,
	)
	body, err := marshalErrorResponse(http.StatusConflict, failurecodes.ResourceConflict, message, requestID, map[string]any{"slug": project.Slug})
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant project conflict response encode: %w", err)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/problem+json")
	return idempotency.StoredResponse{
		StatusCode: http.StatusConflict,
		Header:     headers,
		Body:       body,
	}, nil
}

func serviceConflictStoredResponse(requestID string, service Service) (idempotency.StoredResponse, error) {
	message := fmt.Sprintf(
		"%s: service slug %q already exists in project %q",
		ErrResourceConflict,
		service.Slug,
		service.ProjectID,
	)
	body, err := marshalErrorResponse(http.StatusConflict, failurecodes.ResourceConflict, message, requestID, map[string]any{"slug": service.Slug})
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant service conflict response encode: %w", err)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/problem+json")
	return idempotency.StoredResponse{
		StatusCode: http.StatusConflict,
		Header:     headers,
		Body:       body,
	}, nil
}

func tenantServiceAuditRow(
	request TenantServiceCreateRequest,
	now time.Time,
	workspaceID string,
	workspaceRole identity.WorkspaceRole,
	projectRole identity.ProjectRole,
	resourceID string,
	reasonCode string,
	policyDecision string,
) audit.Row {
	row := audit.Row{
		RequestID:      request.RequestID,
		OccurredAt:     now,
		RecordedAt:     now,
		ActorID:        request.Principal.UserID,
		ActorType:      "user",
		AuthMethod:     request.Principal.AuthMethod,
		SourceIP:       normalizedMutationSourceIP(request.SourceIP),
		UserAgent:      request.UserAgent,
		WorkspaceID:    workspaceID,
		ProjectID:      string(request.Service.ProjectID),
		ResourceKind:   "service",
		ResourceID:     resourceID,
		Action:         "service.create",
		ReasonCode:     reasonCode,
		PolicyDecision: policyDecision,
		Attrs: map[string]any{
			"capability":     string(identity.CapabilityServiceWrite),
			"workspace_role": string(workspaceRole),
			"project_role":   string(projectRole),
			"session_id":     request.Principal.SessionID,
		},
	}
	if policyDecision == "allowed" {
		row.After = map[string]any{
			"project_id":   string(request.Service.ProjectID),
			"slug":         request.Service.Slug,
			"display_name": request.Service.DisplayName,
		}
	}
	return row
}

func cloneTenantMutationResult(result TenantMutationResult) TenantMutationResult {
	result.Response = result.Response.Clone()
	return result
}

func normalizedMutationSourceIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(remoteAddr)
}

func (c *PostgresTenantMutationCoordinator) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func isTenantMutationRetryable(err error) bool {
	if errors.Is(err, errTenantProjectConflictRace) ||
		errors.Is(err, errTenantServiceConflictRace) ||
		errors.Is(err, errTenantEnvironmentConflictRace) ||
		errors.Is(err, errEnvironmentSourceAuthorityChanged) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

func isTenantUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
