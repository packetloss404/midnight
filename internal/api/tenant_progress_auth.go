// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const tenantOperationStreamRevalidationInterval = 5 * time.Second

// tenantOperationScope is the exact canonical deployment lineage that
// authorized a human operation read. Keeping the whole scope, rather than only
// a boolean grant, lets downstream log reads and stream revalidation remain
// pinned to the same deployment even if the operation lineage changes.
type tenantOperationScope struct {
	OperationID   orailtypes.ID
	DeploymentID  string
	ServiceID     string
	ProjectID     string
	EnvironmentID string
}

func (s tenantOperationScope) valid() bool {
	return strings.TrimSpace(string(s.OperationID)) != "" &&
		strings.TrimSpace(s.DeploymentID) != "" &&
		strings.TrimSpace(s.ServiceID) != "" &&
		strings.TrimSpace(s.ProjectID) != "" &&
		strings.TrimSpace(s.EnvironmentID) != ""
}

// tenantOperationAuthorizer proves that an operation is linked through
// exactly one canonical deployment to an environment the current human
// session may read. A missing operation, an ambiguous lineage, and a denied
// operation deliberately share one concealed result.
type tenantOperationAuthorizer interface {
	AuthorizeOperation(
		context.Context,
		identity.Principal,
		identity.Session,
		orailtypes.ID,
		identity.Capability,
		time.Time,
	) (tenantOperationScope, error)
}

type tenantOperationAuthorizerFunc func(
	context.Context,
	identity.Principal,
	identity.Session,
	orailtypes.ID,
	identity.Capability,
	time.Time,
) (tenantOperationScope, error)

func (f tenantOperationAuthorizerFunc) AuthorizeOperation(
	ctx context.Context,
	principal identity.Principal,
	session identity.Session,
	operationID orailtypes.ID,
	capability identity.Capability,
	now time.Time,
) (tenantOperationScope, error) {
	return f(ctx, principal, session, operationID, capability, now)
}

type postgresTenantOperationAuthorizer struct {
	Pool *pg.Pool
}

func newPostgresTenantOperationAuthorizer(
	operations OperationStore,
	deployments DeploymentStore,
	access identity.AccessStore,
) (tenantOperationAuthorizer, bool) {
	operationStore, operationOK := operations.(*PostgresOperationStore)
	deploymentStore, deploymentOK := deployments.(*PostgresDeploymentStore)
	accessStore, accessOK := access.(*identity.PostgresStore)
	if !operationOK || !deploymentOK || !accessOK ||
		operationStore.Pool == nil ||
		deploymentStore.Pool == nil ||
		accessStore.Pool == nil ||
		operationStore.Pool != deploymentStore.Pool ||
		operationStore.Pool != accessStore.Pool {
		return nil, false
	}
	return postgresTenantOperationAuthorizer{Pool: operationStore.Pool}, true
}

func (a postgresTenantOperationAuthorizer) AuthorizeOperation(
	ctx context.Context,
	principal identity.Principal,
	session identity.Session,
	operationID orailtypes.ID,
	capability identity.Capability,
	now time.Time,
) (tenantOperationScope, error) {
	if a.Pool == nil ||
		strings.TrimSpace(principal.UserID) == "" ||
		strings.TrimSpace(session.ID) == "" ||
		session.UserID != principal.UserID ||
		strings.TrimSpace(string(operationID)) == "" ||
		!identity.ValidCapability(capability) {
		return tenantOperationScope{}, ErrOperationNotFound
	}
	workspaceRoles, projectRoles := tenantRoleNames(capability)
	scope := tenantOperationScope{OperationID: operationID}
	var authorized bool
	err := a.Pool.QueryRow(ctx, `
		WITH canonical_deployments AS (
			SELECT
				d.id,
				d.service_id,
				d.project_id,
				d.environment_id
			FROM deployments d
			WHERE d.operation_id=$3
			LIMIT 2
		),
		exact_scope AS (
			SELECT
				MIN(id) AS deployment_id,
				MIN(service_id) AS service_id,
				MIN(project_id) AS project_id,
				MIN(environment_id) AS environment_id
			FROM canonical_deployments
			HAVING COUNT(*)=1
		)
		SELECT
			scope.deployment_id,
			scope.service_id,
			scope.project_id,
			scope.environment_id,
			EXISTS (
			SELECT 1
			FROM exact_scope authorized_scope
			JOIN auth_sessions ses
			  ON ses.id=$2
			 AND ses.user_id=$1
			 AND ses.revoked_at IS NULL
			 AND ses.expires_at>$4
			 AND ses.idle_expires_at>$4
			JOIN users u
			  ON u.id=ses.user_id
			 AND u.deleted_at IS NULL
			 AND u.is_disabled=FALSE
			 AND u.must_rotate_password=FALSE
			 AND u.session_generation=ses.session_generation
			JOIN deployments d
			  ON d.id=authorized_scope.deployment_id
			 AND d.service_id=authorized_scope.service_id
			 AND d.project_id=authorized_scope.project_id
			 AND d.environment_id=authorized_scope.environment_id
			 AND d.operation_id=$3
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
			WHERE (
				EXISTS (
					SELECT 1
					FROM workspace_members wm
					WHERE wm.workspace_id=p.workspace_id
					  AND wm.user_id=u.id
					  AND wm.accepted_at IS NOT NULL
					  AND wm.role=ANY($5::text[])
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
						  AND pm.role=ANY($6::text[])
					)
				)
			)
			  )
		FROM exact_scope scope`,
		principal.UserID,
		session.ID,
		string(operationID),
		now.UTC(),
		workspaceRoles,
		projectRoles,
	).Scan(
		&scope.DeploymentID,
		&scope.ServiceID,
		&scope.ProjectID,
		&scope.EnvironmentID,
		&authorized,
	)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !authorized) {
		return tenantOperationScope{}, ErrOperationNotFound
	}
	if err != nil {
		return tenantOperationScope{}, fmt.Errorf("tenant operation authorization: %w", err)
	}
	if !scope.valid() {
		return tenantOperationScope{}, ErrOperationNotFound
	}
	return scope, nil
}

type tenantOperationScopeContextKey struct{}

func withTenantOperationScope(
	ctx context.Context,
	scope tenantOperationScope,
) context.Context {
	return context.WithValue(ctx, tenantOperationScopeContextKey{}, scope)
}

func tenantOperationScopeFromContext(
	ctx context.Context,
) (tenantOperationScope, bool) {
	scope, ok := ctx.Value(tenantOperationScopeContextKey{}).(tenantOperationScope)
	return scope, ok && scope.valid()
}

type operationStreamAccessGuard struct {
	Check    func(context.Context) error
	Interval time.Duration
}

type operationStreamAccessGuardContextKey struct{}

func withOperationStreamAccessGuard(
	ctx context.Context,
	guard operationStreamAccessGuard,
) context.Context {
	return context.WithValue(ctx, operationStreamAccessGuardContextKey{}, guard)
}

func operationStreamAccessGuardFromContext(
	ctx context.Context,
) (operationStreamAccessGuard, bool) {
	guard, ok := ctx.Value(operationStreamAccessGuardContextKey{}).(operationStreamAccessGuard)
	return guard, ok && guard.Check != nil
}

// humanOperationReadAuthorizationMiddleware is the only human-session
// admission seam for operation status, events, and logs. It rechecks the
// canonical operation -> deployment -> environment lineage and current
// session/grants before the handler runs. Live streams receive the same guard
// in context so they close when the session or grant stops authorizing.
func humanOperationReadAuthorizationMiddleware(
	authorizer tenantOperationAuthorizer,
	now func() time.Time,
	next http.Handler,
) http.Handler {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		productPrincipal, ok := productPrincipalFromContext(r.Context())
		if !ok || productPrincipal.Kind != productPrincipalHuman ||
			r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		if authorizer == nil {
			writeRequestError(
				w,
				r,
				http.StatusServiceUnavailable,
				failurecodes.InternalError,
				"tenant operation authorization is unavailable",
				nil,
			)
			return
		}
		operationID, action, pathOK := parseOperationPath(r.URL.Path)
		if !pathOK || action == "cancel" || action == "fail" {
			writeRequestError(
				w,
				r,
				http.StatusNotFound,
				failurecodes.OperationNotFound,
				"operation not found",
				nil,
			)
			return
		}
		capability := identity.CapabilityServiceRead
		if action == "logs" {
			capability = identity.CapabilityLogsRead
		}
		principal, principalOK := identityPrincipalFromContext(r.Context())
		session, sessionOK := identitySessionFromContext(r.Context())
		if !principalOK || !sessionOK {
			writeRequestError(
				w,
				r,
				http.StatusUnauthorized,
				failurecodes.AuthSessionInvalid,
				"session is invalid, expired, or revoked",
				nil,
			)
			return
		}
		scope, err := authorizer.AuthorizeOperation(
			r.Context(),
			principal,
			session,
			orailtypes.ID(operationID),
			capability,
			now().UTC(),
		)
		if errors.Is(err, ErrOperationNotFound) {
			writeRequestError(
				w,
				r,
				http.StatusNotFound,
				failurecodes.OperationNotFound,
				"operation not found",
				map[string]any{"operation_id": operationID},
			)
			return
		} else if err != nil {
			writeRequestError(
				w,
				r,
				http.StatusServiceUnavailable,
				failurecodes.InternalError,
				"tenant operation authorization is unavailable",
				nil,
			)
			return
		}
		check := func(ctx context.Context) error {
			currentScope, authorizeErr := authorizer.AuthorizeOperation(
				ctx,
				principal,
				session,
				orailtypes.ID(operationID),
				capability,
				now().UTC(),
			)
			if authorizeErr != nil {
				return authorizeErr
			}
			if currentScope != scope {
				return ErrOperationNotFound
			}
			return nil
		}
		ctx := withTenantOperationScope(r.Context(), scope)
		ctx = withOperationStreamAccessGuard(ctx, operationStreamAccessGuard{
			Check:    check,
			Interval: tenantOperationStreamRevalidationInterval,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
