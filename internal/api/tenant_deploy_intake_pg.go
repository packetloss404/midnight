// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/puddle/v2"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const tenantDeployIntakePathTemplate = v1alphaPrefix + "/services/{service_id}/deployments"

// TenantImageDeployRequest contains a server-verified human identity tuple,
// one validated immutable-image intent, and the canonical HTTP idempotency
// scope. The coordinator resolves every resource identifier and authorization
// input again inside its transaction.
type TenantImageDeployRequest struct {
	Principal   identity.Principal
	Session     identity.Session
	ServiceID   orailtypes.ID
	Intent      DeployIntakeRequest
	Key         idempotency.Key
	Fingerprint idempotency.RequestFingerprint
	RequestID   string
	SourceIP    string
	UserAgent   string
}

// TenantDeployIntakeResult is the exact response committed beside the
// operation, deployment, outbox intent, and audit evidence.
type TenantDeployIntakeResult struct {
	Response idempotency.StoredResponse
	Replayed bool
}

// TenantDeployIntakeCoordinator is intentionally separate from route wiring.
// A human deploy route must remain closed until the production stack and
// product-auth boundary explicitly install this authority.
type TenantDeployIntakeCoordinator interface {
	DeployImmutableImage(context.Context, TenantImageDeployRequest) (TenantDeployIntakeResult, error)
}

// TenantSourceArtifact is the redaction-safe, content-addressed archive
// metadata admitted by the human `up` route. The handler stages the gzip
// archive; the coordinator publishes it while holding quota/lifecycle locks
// and later records this exact metadata and operation/deployment ownership.
type TenantSourceArtifact struct {
	Digest            string
	URI               string
	StorageKey        string
	CompressedBytes   int64
	UncompressedBytes int64
	EntryCount        int
}

// TenantSourceUpPreflightRequest contains only the authenticated identity and
// canonical target needed to authorize an upload before the API reads a
// potentially large request body. Final authorization is always repeated in
// the accepting SERIALIZABLE transaction.
type TenantSourceUpPreflightRequest struct {
	Principal       identity.Principal
	Session         identity.Session
	ServiceID       orailtypes.ID
	EnvironmentID   string
	CommitMessage   string
	Key             idempotency.Key
	ExpectedDigest  string
	CompressedBytes int64
}

// TenantSourceUpAdmission is either the opaque, durable quota reservation
// returned before the handler reads an upload body or an exact terminal replay
// coalesced behind an already-admitted request. Replay followers never own or
// finish the leader's quota reservation.
type TenantSourceUpAdmission struct {
	ReservationID string
	IntentHash    string
	Replay        bool
}

// TenantSourceUpPublishRequest binds one validated staging object to the exact
// preflight authority and reservation that admitted its body.
type TenantSourceUpPublishRequest struct {
	Preflight TenantSourceUpPreflightRequest
	Admission TenantSourceUpAdmission
	Staged    sourceartifact.Staged
}

// TenantSourceUpRequest is a prepared, content-addressed source deployment.
// CommitMessage is bound into the fingerprint and nullable ownership row; it
// is never placed in the source URI, worker payload, operation event, or audit
// body.
type TenantSourceUpRequest struct {
	Principal     identity.Principal
	Session       identity.Session
	ServiceID     orailtypes.ID
	EnvironmentID string
	CommitMessage string
	Artifact      TenantSourceArtifact
	Admission     TenantSourceUpAdmission
	Key           idempotency.Key
	Fingerprint   idempotency.RequestFingerprint
	RequestID     string
	SourceIP      string
	UserAgent     string
}

// TenantUpIntakeCoordinator is a deliberately separate route capability even
// though the Postgres implementation shares the immutable-image coordinator's
// canonical authorization/idempotency/deployment transaction.
type TenantUpIntakeCoordinator interface {
	PreflightSourceUp(context.Context, TenantSourceUpPreflightRequest) (TenantSourceUpAdmission, error)
	FinalizeSourceUpReplay(context.Context, TenantSourceUpPreflightRequest) (idempotency.StoredResponse, error)
	PublishSourceUp(context.Context, TenantSourceUpPublishRequest) (TenantSourceArtifact, error)
	DeploySourceArtifact(context.Context, TenantSourceUpRequest) (TenantDeployIntakeResult, error)
	FinishSourceUpAdmission(context.Context, TenantSourceUpAdmission, bool) error
}

type tenantDeployTransactionRequest struct {
	Principal   identity.Principal
	Session     identity.Session
	ServiceID   orailtypes.ID
	Intent      DeployIntakeRequest
	Key         idempotency.Key
	Fingerprint idempotency.RequestFingerprint
	RequestID   string
	SourceIP    string
	UserAgent   string

	OperationKind string
	Queue         queue.Name
	ResponseKind  string
	Artifact      *TenantSourceArtifact
	Admission     *TenantSourceUpAdmission
	HasMessage    bool
	Message       string
}

// PostgresTenantDeployIntakeCoordinator linearizes current human authority,
// actor/project/route-scoped idempotency, canonical deployment truth, the
// durable worker outbox, exact response replay, and audit evidence.
type PostgresTenantDeployIntakeCoordinator struct {
	Pool                *pg.Pool
	Idempotency         *idempotency.PostgresStore
	Audit               *audit.PostgresChain
	Outbox              *PostgresDeployOutbox
	SourceArtifacts     *sourceartifact.PostgresStore
	SourceArtifactFS    *sourceartifact.FSStore
	SourceArtifactQuota *sourceartifact.QuotaStore
	Routes              *router.PostgresDurableRouteStore
	AppsDomain          string
	Now                 func() time.Time
	OperationIDFactory  func() orailtypes.ID
	DeploymentIDFactory func() string
	DomainIDFactory     func() string
	OutboxIDFactory     func() string
	EventIDFactory      func() string
	graphQLBeforeCommit func() error
}

// NewPostgresTenantDeployIntakeCoordinator constructs the backend authority.
// It does not register a route or add a capability to any transport.
func NewPostgresTenantDeployIntakeCoordinator(
	pool *pg.Pool,
	chain *audit.PostgresChain,
	now func() time.Time,
) *PostgresTenantDeployIntakeCoordinator {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	idempotencyStore := idempotency.NewPostgresStore(pool)
	idempotencyStore.Now = now
	outbox := NewPostgresDeployOutbox(pool)
	outbox.Now = now
	return &PostgresTenantDeployIntakeCoordinator{
		Pool:                pool,
		Idempotency:         idempotencyStore,
		Audit:               chain,
		Outbox:              outbox,
		SourceArtifacts:     sourceartifact.NewPostgresStore(pool),
		Routes:              router.NewPostgresDurableRouteStore(pool),
		Now:                 now,
		OperationIDFactory:  defaultOperationID,
		DeploymentIDFactory: func() string { return mintID(orailtypes.IDPrefixDeployment) },
		DomainIDFactory:     func() string { return mintID(orailtypes.IDPrefixDomain) },
		OutboxIDFactory:     func() string { return randomOutboxID("dox") },
		EventIDFactory:      func() string { return randomOutboxID("dev") },
	}
}

// DeployImmutableImage accepts one human-triggered, digest-pinned image
// deployment. IDs and time are prepared once so SERIALIZABLE retries preserve
// one logical operation, deployment, outbox intent, event, and response.
func (c *PostgresTenantDeployIntakeCoordinator) DeployImmutableImage(
	ctx context.Context,
	request TenantImageDeployRequest,
) (TenantDeployIntakeResult, error) {
	if err := c.validateRequest(request); err != nil {
		return TenantDeployIntakeResult{}, err
	}
	return c.deployTenantIntent(ctx, tenantDeployTransactionRequest{
		Principal:     request.Principal,
		Session:       request.Session,
		ServiceID:     request.ServiceID,
		Intent:        request.Intent,
		Key:           request.Key,
		Fingerprint:   request.Fingerprint,
		RequestID:     request.RequestID,
		SourceIP:      request.SourceIP,
		UserAgent:     request.UserAgent,
		OperationKind: "deploy",
		Queue:         queue.Deploy,
		ResponseKind:  "image",
	})
}

// deployTenantIntent is the single transactional acceptance loop shared by
// human immutable-image deploys and content-addressed source uploads. IDs and
// time are prepared once so SERIALIZABLE retries preserve one logical
// operation, deployment, outbox intent, event, response, and artifact owner.
func (c *PostgresTenantDeployIntakeCoordinator) deployTenantIntent(
	ctx context.Context,
	request tenantDeployTransactionRequest,
) (TenantDeployIntakeResult, error) {
	if err := ctx.Err(); err != nil {
		return TenantDeployIntakeResult{}, err
	}

	operationID := c.OperationIDFactory()
	deploymentID := c.DeploymentIDFactory()
	proposedDomainID := ""
	if request.Intent.Route != nil {
		// Keep one generated-domain incarnation identity across transaction
		// retries. Existing service-managed or compatible recovery state may
		// make this proposal unnecessary, but it must never be reminted inside
		// an attempt.
		proposedDomainID = c.DomainIDFactory()
	}
	outboxID := c.OutboxIDFactory()
	eventID := c.EventIDFactory()
	now := c.now().Truncate(time.Microsecond)

	var result TenantDeployIntakeResult
	for attempt := 0; attempt < tenantMutationSerializableAttempts; attempt++ {
		err := c.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
			var err error
			// Session expiry is evaluated after the transaction has acquired a
			// connection. Artifact timestamps remain stable across retries, but
			// current authority must never be checked against pool-wait time.
			authorizationNow := c.now().Truncate(time.Microsecond)
			result, err = c.deployTenantIntentInTx(
				ctx,
				tx,
				request,
				operationID,
				deploymentID,
				proposedDomainID,
				outboxID,
				eventID,
				now,
				authorizationNow,
			)
			return err
		})
		if err == nil {
			result.Response = result.Response.Clone()
			return result, nil
		}
		if !isTenantMutationRetryable(err) || attempt == tenantMutationSerializableAttempts-1 {
			return TenantDeployIntakeResult{}, classifyTenantDeployCoordinatorError(err)
		}
	}
	return TenantDeployIntakeResult{}, errors.New("tenant deploy intake serializable retries exhausted")
}

func classifyTenantDeployCoordinatorError(err error) error {
	if err == nil ||
		errors.Is(err, identity.ErrSessionInvalid) ||
		errors.Is(err, identity.ErrAccessNotFound) ||
		errors.Is(err, identity.ErrAuthorizationDenied) ||
		errors.Is(err, idempotency.ErrInvalidKey) ||
		errors.Is(err, idempotency.ErrInvalidFingerprint) ||
		errors.Is(err, idempotency.ErrKeyReused) ||
		errors.Is(err, idempotency.ErrKeyReusedDifferentBody) ||
		errors.Is(err, idempotency.ErrRequestInProgress) ||
		errors.Is(err, ErrResourceInvalid) ||
		errors.Is(err, context.Canceled) {
		return err
	}
	if tenantDeployDependencyError(err) {
		// Preserve the underlying cause for logs/tests while exposing only the
		// stable dependency sentinel to the HTTP classifier.
		return errors.Join(ErrTenantDeployIntakeUnavailable, err)
	}
	return err
}

func tenantDeployDependencyError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, puddle.ErrClosedPool) ||
		pgconn.SafeToRetry(err) ||
		pgconn.Timeout(err) {
		return true
	}
	var connectError *pgconn.ConnectError
	if errors.As(err, &connectError) {
		return true
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return false
	}
	switch postgresError.Code {
	case "40001", // serialization_failure after the bounded retry loop
		"40P01",                                     // deadlock_detected after the bounded retry loop
		"53000", "53100", "53200", "53300", "53400", // insufficient resources
		"57P01", "57P02", "57P03", // shutdown/crash/cannot_connect_now
		"58000", "58030": // system / IO error
		return true
	default:
		return strings.HasPrefix(postgresError.Code, "08") // connection exception class
	}
}

func (c *PostgresTenantDeployIntakeCoordinator) deployTenantIntentInTx(
	ctx context.Context,
	tx *pg.Tx,
	request tenantDeployTransactionRequest,
	operationID orailtypes.ID,
	deploymentID string,
	proposedDomainID string,
	outboxID string,
	eventID string,
	now time.Time,
	authorizationNow time.Time,
) (TenantDeployIntakeResult, error) {
	mutationCtx, authorization, err := authorizeTenantDeployInTx(
		ctx,
		tx,
		request.Principal,
		request.Session,
		string(request.ServiceID),
		request.Intent.EnvironmentID,
		authorizationNow,
	)
	if err != nil {
		return TenantDeployIntakeResult{}, err
	}
	projectID := authorization.ProjectID
	workspaceID := authorization.WorkspaceID
	workspaceRole := authorization.WorkspaceRole
	projectRole := authorization.ProjectRole
	service := authorization.Service

	if request.Artifact != nil {
		if request.Admission == nil {
			return TenantDeployIntakeResult{}, sourceartifact.ErrReservationInvalid
		}
		intentHash, err := tenantUpPreflightIntentHash(TenantSourceUpPreflightRequest{
			Principal:       request.Principal,
			Session:         request.Session,
			ServiceID:       request.ServiceID,
			EnvironmentID:   request.Intent.EnvironmentID,
			CommitMessage:   request.Message,
			Key:             request.Key,
			ExpectedDigest:  request.Artifact.Digest,
			CompressedBytes: request.Artifact.CompressedBytes,
		})
		if err != nil {
			return TenantDeployIntakeResult{}, err
		}
		if request.Admission.IntentHash != intentHash {
			return TenantDeployIntakeResult{}, sourceartifact.ErrReservationInvalid
		}
		if _, err := c.SourceArtifactQuota.ValidateInTx(
			mutationCtx,
			tx,
			request.Admission.ReservationID,
			sourceartifact.ReservationRequest{
				WorkspaceID:     workspaceID,
				ProjectID:       projectID,
				ServiceID:       string(service.ID),
				EnvironmentID:   request.Intent.EnvironmentID,
				ActorUserID:     request.Principal.UserID,
				SessionID:       request.Session.ID,
				IdempotencyKey:  string(request.Key),
				IntentHash:      intentHash,
				ExpectedDigest:  request.Artifact.Digest,
				CompressedBytes: request.Artifact.CompressedBytes,
			},
		); err != nil {
			return TenantDeployIntakeResult{}, err
		}
	}

	reservation, err := c.Idempotency.ReserveInTx(
		mutationCtx,
		tx,
		request.Key,
		request.Fingerprint,
		24*time.Hour,
	)
	if err != nil {
		return TenantDeployIntakeResult{}, err
	}
	if reservation.IsTerminal() {
		if reservation.Response == nil {
			return TenantDeployIntakeResult{}, errors.New("tenant deploy replay has no stored response")
		}
		if request.Admission != nil {
			err := c.SourceArtifactQuota.ChargeWorkspaceDigestInTx(
				mutationCtx,
				tx,
				workspaceID,
				request.Artifact.Digest,
				request.Artifact.CompressedBytes,
			)
			if err != nil {
				return TenantDeployIntakeResult{}, err
			}
			err = c.SourceArtifactQuota.ConsumeInTx(
				mutationCtx,
				tx,
				request.Admission.ReservationID,
			)
			if err != nil {
				return TenantDeployIntakeResult{}, err
			}
		}
		return TenantDeployIntakeResult{
			Response: reservation.Response.Clone(),
			Replayed: true,
		}, nil
	}

	if err := router.AcquireRouteEnvironmentMutationLock(
		mutationCtx,
		tx,
		projectID,
		request.Intent.EnvironmentID,
	); err != nil {
		return TenantDeployIntakeResult{}, err
	}
	previousActiveID, err := lockTenantDeployActiveState(
		mutationCtx,
		tx,
		string(service.ID),
		request.Intent.EnvironmentID,
		now,
	)
	if err != nil {
		return TenantDeployIntakeResult{}, err
	}

	intent := request.Intent
	intent.EnvironmentID = strings.TrimSpace(intent.EnvironmentID)
	intent.Image = strings.TrimSpace(intent.Image)
	intent.DeploymentID = deploymentID
	intent.InstanceID = deploymentID + "-r0"
	intent.PreviousActive = nil
	generatedHostname := ""
	generatedDomainID := ""
	generatedRouteID := ""
	hasManagedRoutes, err := hasServiceManagedGeneratedRoutesInTx(
		mutationCtx,
		tx,
		projectID,
		intent.EnvironmentID,
		string(service.ID),
	)
	if err != nil {
		return TenantDeployIntakeResult{}, fmt.Errorf(
			"tenant service-managed route lookup: %w",
			err,
		)
	}
	if intent.Route != nil && !hasManagedRoutes {
		targetPort, err := tenantDeployDesiredTargetPortInTx(
			mutationCtx,
			tx,
			projectID,
			intent.EnvironmentID,
			string(service.ID),
		)
		if err != nil {
			return TenantDeployIntakeResult{}, err
		}
		domainValue, routeValue, err := router.BuildDomainRouteIntent(router.DomainRouteIntentInput{
			Scope: router.RouteScope{
				ProjectID:     projectID,
				EnvironmentID: intent.EnvironmentID,
				ServiceID:     string(service.ID),
			},
			Kind:       router.DomainKindGenerated,
			AppsDomain: c.AppsDomain,
			PathPrefix: "/",
			TargetPort: targetPort,
			Public:     true,
			TLSDesired: false,
		})
		if err != nil {
			return TenantDeployIntakeResult{}, fmt.Errorf("tenant generated route intent: %w", err)
		}
		domainValue, routeValue, err = prepareProductGeneratedDomainInTx(
			mutationCtx,
			tx,
			domainValue,
			routeValue,
			proposedDomainID,
		)
		if err != nil {
			return TenantDeployIntakeResult{}, err
		}
		domainValue, err = c.Routes.PutDomainInTx(mutationCtx, tx, domainValue)
		if err != nil {
			return TenantDeployIntakeResult{}, fmt.Errorf("tenant generated domain persist: %w", err)
		}
		if err := c.Routes.EnsureDomainLifecycleInTx(mutationCtx, tx, domainValue); err != nil {
			return TenantDeployIntakeResult{}, fmt.Errorf("tenant generated domain lifecycle: %w", err)
		}
		routeValue, err = c.Routes.PutHTTPRouteInTx(mutationCtx, tx, routeValue)
		if err != nil {
			return TenantDeployIntakeResult{}, fmt.Errorf("tenant generated route persist: %w", err)
		}
		if err := c.Routes.BindServiceDomainInTx(
			mutationCtx,
			tx,
			domainValue.ID,
			routeValue.ID,
			routeValue.RouteScope,
			now,
		); err != nil {
			return TenantDeployIntakeResult{}, fmt.Errorf(
				"tenant generated service binding: %w",
				err,
			)
		}
		// Host remains empty because exact service/deployment bindings, not a
		// reconstructible payload identity, are authoritative for generated
		// product routes.
		intent.Route = &DeployRouteIntent{
			Name:   routeValue.Name,
			Path:   routeValue.PathPrefix,
			Public: true,
		}
		generatedHostname = domainValue.NormalizedHostname
		generatedDomainID = domainValue.ID
		generatedRouteID = routeValue.ID
		hasManagedRoutes = true
	} else if hasManagedRoutes {
		if err := tx.QueryRow(mutationCtx, `
			SELECT d.normalized_hostname
			  FROM service_domain_bindings b
			  JOIN http_routes r
			    ON r.id=b.route_id
			   AND r.domain_id=b.domain_id
			   AND r.project_id=b.project_id
			   AND r.environment_id=b.environment_id
			   AND r.service_id=b.service_id
			  JOIN domains d
			    ON d.id=b.domain_id
			   AND d.project_id=b.project_id
			   AND d.environment_id=b.environment_id
			   AND d.service_id=b.service_id
			 WHERE b.project_id=$1
			   AND b.environment_id=$2
			   AND b.service_id=$3
			   AND d.kind='generated'
			   AND `+router.ManagedServiceRouteEligibilityPredicateSQL+`
			 ORDER BY d.normalized_hostname, d.id
			 LIMIT 1`,
			projectID,
			intent.EnvironmentID,
			service.ID,
		).Scan(&generatedHostname); err != nil {
			return TenantDeployIntakeResult{}, fmt.Errorf(
				"tenant service-managed route hostname: %w",
				err,
			)
		}
	}
	if hasManagedRoutes {
		// The payload's singular route field is now only the worker's public
		// routing signal. Exact route identities come from the deployment's
		// durable bindings, populated after its canonical row exists.
		intent.Route = &DeployRouteIntent{Public: true}
	}
	if previousActiveID != "" {
		intent.PreviousActive = &DeployActiveInstanceRef{
			DeploymentID: previousActiveID,
			InstanceID:   previousActiveID + "-r0",
		}
	}
	operation := orailtypes.Operation{
		ID:        operationID,
		Kind:      request.OperationKind,
		State:     orailtypes.OperationPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	deployment, err := canonicalDeploymentRecord(
		service,
		intent,
		operation.ID,
		acceptedDeploymentIntentKey(operation.ID),
	)
	if err != nil {
		return TenantDeployIntakeResult{}, fmt.Errorf("tenant deploy canonical record: %w", err)
	}
	deployment.ID = deploymentID
	deployment.ExpectedActiveID = previousActiveID

	payload, err := buildDeployJobPayload(operation.ID, service, intent, now)
	if err != nil {
		return TenantDeployIntakeResult{}, fmt.Errorf("tenant deploy payload encode: %w", err)
	}
	enqueue := buildDeployEnqueueRequest(operation.ID, service, now, payload)
	enqueue.Queue = request.Queue
	var response idempotency.StoredResponse
	switch request.ResponseKind {
	case "image":
		response, err = tenantDeployAcceptedStoredResponse(operation)
	case "up":
		response, err = tenantUpAcceptedStoredResponse(operation, deployment.ID)
	default:
		err = fmt.Errorf("tenant deploy response kind %q is invalid", request.ResponseKind)
	}
	if err != nil {
		return TenantDeployIntakeResult{}, err
	}
	commit, err := c.Outbox.CommitDeployIntakeInTx(
		mutationCtx,
		tx,
		DeployIntakeCommit{
			Operation:      operation,
			OperationActor: request.Principal.UserID,
			TargetKind:     "service",
			TargetID:       string(service.ID),
			Deployment:     deployment,
			IdempotencyKey: request.Key,
			IdempotencyRef: reservation.Reference(),
			Response:       response,
			Enqueue:        enqueue,
			InitialMessage: durableDeployIntakeMessage(service, intent, request.Queue),
			Now:            now,
		},
		outboxID,
		eventID,
	)
	if err != nil {
		return TenantDeployIntakeResult{}, err
	}
	if generatedRouteID != "" {
		if err := bindDeploymentRouteIntentInTx(
			mutationCtx,
			tx,
			commit.Deployment.ID,
			generatedDomainID,
			generatedRouteID,
			router.RouteScope{
				ProjectID:     projectID,
				EnvironmentID: intent.EnvironmentID,
				ServiceID:     string(service.ID),
			},
			now,
		); err != nil {
			return TenantDeployIntakeResult{}, fmt.Errorf("tenant deployment route bind: %w", err)
		}
	}
	if _, err := bindServiceManagedGeneratedRoutesInTx(
		mutationCtx,
		tx,
		commit.Deployment.ID,
		projectID,
		intent.EnvironmentID,
		string(service.ID),
		now,
	); err != nil {
		return TenantDeployIntakeResult{}, fmt.Errorf(
			"tenant service-managed deployment route bind: %w",
			err,
		)
	}
	if request.Artifact != nil {
		if err := c.bindTenantSourceArtifactInTx(
			mutationCtx,
			tx,
			*request.Artifact,
			commit,
			projectID,
			string(service.ID),
			intent.EnvironmentID,
			request.Message,
			now,
		); err != nil {
			return TenantDeployIntakeResult{}, err
		}
		if err := c.SourceArtifactQuota.ChargeWorkspaceDigestInTx(
			mutationCtx,
			tx,
			workspaceID,
			request.Artifact.Digest,
			request.Artifact.CompressedBytes,
		); err != nil {
			return TenantDeployIntakeResult{}, err
		}
		if err := c.SourceArtifactQuota.ConsumeInTx(
			mutationCtx,
			tx,
			request.Admission.ReservationID,
		); err != nil {
			return TenantDeployIntakeResult{}, err
		}
	}
	var auditRow audit.Row
	switch request.ResponseKind {
	case "image":
		auditRow = tenantDeployAcceptedAuditRow(
			TenantImageDeployRequest{
				Principal:   request.Principal,
				Session:     request.Session,
				ServiceID:   request.ServiceID,
				Intent:      request.Intent,
				Key:         request.Key,
				Fingerprint: request.Fingerprint,
				RequestID:   request.RequestID,
				SourceIP:    request.SourceIP,
				UserAgent:   request.UserAgent,
			},
			now,
			workspaceID,
			projectID,
			workspaceRole,
			projectRole,
			service,
			intent,
			commit,
			generatedHostname,
		)
	case "up":
		auditRow = tenantUpAcceptedAuditRow(
			request,
			now,
			workspaceID,
			projectID,
			workspaceRole,
			projectRole,
			service,
			intent,
			commit,
		)
	default:
		return TenantDeployIntakeResult{}, fmt.Errorf("tenant deploy audit response kind %q is invalid", request.ResponseKind)
	}
	if _, err := c.Audit.AppendInTx(
		mutationCtx,
		tx,
		auditRow,
	); err != nil {
		return TenantDeployIntakeResult{}, fmt.Errorf("tenant deploy audit append: %w", err)
	}
	return TenantDeployIntakeResult{Response: response}, nil
}

func (c *PostgresTenantDeployIntakeCoordinator) validateRequest(request TenantImageDeployRequest) error {
	if err := c.validateCoordinatorAuthority(); err != nil {
		return err
	}
	if err := validateTenantHumanTuple(request.Principal, request.Session); err != nil {
		return err
	}
	if strings.TrimSpace(string(request.ServiceID)) == "" {
		return fmt.Errorf("%w: service id is required", ErrResourceInvalid)
	}
	if request.Intent.Source != nil ||
		strings.TrimSpace(request.Intent.SourceRef) != "" ||
		strings.TrimSpace(request.Intent.Mode) != "" {
		return fmt.Errorf("%w: tenant deploy intake currently accepts immutable images only", ErrResourceInvalid)
	}
	if request.Intent.Route != nil {
		routeValue := request.Intent.Route
		if !routeValue.Public ||
			routeValue.TLS ||
			strings.TrimSpace(routeValue.Name) != "" ||
			strings.TrimSpace(routeValue.Group) != "" ||
			strings.TrimSpace(routeValue.Host) != "" ||
			strings.TrimSpace(routeValue.Path) != "" {
			return fmt.Errorf("%w: human deploys may request only one server-generated public route", ErrResourceInvalid)
		}
		if strings.TrimSpace(c.AppsDomain) == "" {
			return ErrTenantGeneratedRouteUnavailable
		}
	}
	if strings.TrimSpace(request.Intent.DeploymentID) != "" ||
		strings.TrimSpace(request.Intent.InstanceID) != "" ||
		request.Intent.PreviousActive != nil {
		return fmt.Errorf("%w: deployment identity and active predecessor are server-owned", ErrResourceInvalid)
	}
	if code, message, _, ok := validateDeployIntakeRequest(request.Intent); !ok {
		return fmt.Errorf("%w: %s (%s)", ErrResourceInvalid, message, code)
	}
	if err := idempotency.ValidateKey(request.Key); err != nil {
		return err
	}
	if err := idempotency.ValidateFingerprint(request.Fingerprint); err != nil {
		return err
	}
	canonicalPath := tenantDeployIdempotencyPath(request.ServiceID)
	if request.Fingerprint.Method != http.MethodPost ||
		request.Fingerprint.Path != canonicalPath {
		return fmt.Errorf(
			"%w: tenant deploy fingerprint must use concrete POST %s (template %s)",
			idempotency.ErrInvalidFingerprint,
			canonicalPath,
			tenantDeployIntakePathTemplate,
		)
	}
	return nil
}

// tenantDeployIdempotencyPath binds a client key to the canonical service as
// well as actor and project. The request body does not necessarily contain the
// service id, so a templated route alone would permit a same-key/body replay
// from one service to another inside the project.
func tenantDeployIdempotencyPath(serviceID orailtypes.ID) string {
	return v1alphaPrefix + "/services/" + string(serviceID) + "/deployments"
}

type tenantDeployAuthorization struct {
	ProjectID             string
	WorkspaceID           string
	WorkspaceRole         identity.WorkspaceRole
	ProjectRole           identity.ProjectRole
	EnvironmentRestricted bool
	Service               Service
}

// authorizeTenantDeployInTx is the one current-authority implementation used
// by upload preflight and both final deployment admission modes. Callers must
// still repeat it in the accepting transaction after any external staging.
func authorizeTenantDeployInTx(
	ctx context.Context,
	tx *pg.Tx,
	principal identity.Principal,
	session identity.Session,
	serviceID string,
	environmentID string,
	authorizationNow time.Time,
) (context.Context, tenantDeployAuthorization, error) {
	return authorizeTenantDeployCapabilityInTx(
		ctx,
		tx,
		principal,
		session,
		serviceID,
		environmentID,
		authorizationNow,
		identity.CapabilityDeploymentTriggerCLI,
	)
}

func authorizeTenantDeployCapabilityInTx(
	ctx context.Context,
	tx *pg.Tx,
	principal identity.Principal,
	session identity.Session,
	serviceID string,
	environmentID string,
	authorizationNow time.Time,
	capability identity.Capability,
) (context.Context, tenantDeployAuthorization, error) {
	projectID, workspaceID, err := resolveTenantDeployLineage(
		ctx,
		tx,
		serviceID,
		environmentID,
	)
	if err != nil {
		return ctx, tenantDeployAuthorization{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:"+workspaceID); err != nil {
		return ctx, tenantDeployAuthorization{}, fmt.Errorf("tenant deploy workspace membership lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "project-members:"+projectID); err != nil {
		return ctx, tenantDeployAuthorization{}, fmt.Errorf("tenant deploy project membership lock: %w", err)
	}

	mutationCtx := idempotency.WithActor(ctx, idempotency.UserActor(principal.UserID))
	mutationCtx = idempotency.WithTenant(
		mutationCtx,
		projectScopedIdempotencyTenant(workspaceID, projectID),
	)
	if err := revalidateTenantMutationSessionTuple(
		mutationCtx,
		tx,
		principal,
		session,
		authorizationNow,
	); err != nil {
		return mutationCtx, tenantDeployAuthorization{}, err
	}
	canonicalWorkspaceID, workspaceRole, projectRole, err := lockTenantServiceProjectAccess(
		mutationCtx,
		tx,
		projectID,
		workspaceID,
		principal.UserID,
	)
	if err != nil {
		return mutationCtx, tenantDeployAuthorization{}, err
	}
	if canonicalWorkspaceID != workspaceID {
		return mutationCtx, tenantDeployAuthorization{}, identity.ErrAccessNotFound
	}
	service, environmentRestricted, err := lockTenantDeployServiceEnvironment(
		mutationCtx,
		tx,
		serviceID,
		projectID,
		environmentID,
	)
	if err != nil {
		return mutationCtx, tenantDeployAuthorization{}, err
	}
	decision := identity.EvaluateAccess(identity.AccessRequest{
		WorkspaceRole:         workspaceRole,
		ProjectRole:           projectRole,
		Capability:            capability,
		EnvironmentRestricted: environmentRestricted,
	})
	if !decision.Allowed {
		if decision.Reason == "environment-restricted" {
			return mutationCtx, tenantDeployAuthorization{}, identity.ErrAccessNotFound
		}
		return mutationCtx, tenantDeployAuthorization{}, identity.ErrAuthorizationDenied
	}
	return mutationCtx, tenantDeployAuthorization{
		ProjectID:             projectID,
		WorkspaceID:           workspaceID,
		WorkspaceRole:         workspaceRole,
		ProjectRole:           projectRole,
		EnvironmentRestricted: environmentRestricted,
		Service:               service,
	}, nil
}

func resolveTenantDeployLineage(
	ctx context.Context,
	tx *pg.Tx,
	serviceID string,
	environmentID string,
) (string, string, error) {
	var projectID, workspaceID string
	err := tx.QueryRow(ctx, `
		SELECT s.project_id, p.workspace_id
		FROM services s
		JOIN projects p ON p.id=s.project_id
		JOIN workspaces w ON w.id=p.workspace_id
		JOIN environments e ON e.id=$2 AND e.project_id=p.id
		WHERE s.id=$1
		  AND s.deleted_at IS NULL
		  AND p.deleted_at IS NULL
		  AND w.deleted_at IS NULL
		  AND e.deleted_at IS NULL`,
		serviceID,
		strings.TrimSpace(environmentID),
	).Scan(&projectID, &workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", identity.ErrAccessNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("tenant deploy lineage resolve: %w", err)
	}
	return projectID, workspaceID, nil
}

func lockTenantDeployServiceEnvironment(
	ctx context.Context,
	tx *pg.Tx,
	serviceID string,
	projectID string,
	environmentID string,
) (Service, bool, error) {
	var service Service
	var restricted bool
	err := tx.QueryRow(ctx, `
		SELECT s.id, s.project_id, s.slug, s.display_name, s.created_at, s.updated_at,
		       e.access_restricted
		FROM services s
		JOIN environments e ON e.id=$3 AND e.project_id=s.project_id
		WHERE s.id=$1
		  AND s.project_id=$2
		  AND s.deleted_at IS NULL
		  AND e.deleted_at IS NULL
		FOR UPDATE OF s, e`,
		serviceID,
		projectID,
		strings.TrimSpace(environmentID),
	).Scan(
		&service.ID,
		&service.ProjectID,
		&service.Slug,
		&service.DisplayName,
		&service.CreatedAt,
		&service.UpdatedAt,
		&restricted,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Service{}, false, identity.ErrAccessNotFound
	}
	if err != nil {
		return Service{}, false, fmt.Errorf("tenant deploy service/environment lock: %w", err)
	}
	service.CreatedAt = service.CreatedAt.UTC()
	service.UpdatedAt = service.UpdatedAt.UTC()
	return service, restricted, nil
}

func lockTenantDeployActiveState(
	ctx context.Context,
	tx *pg.Tx,
	serviceID string,
	environmentID string,
	now time.Time,
) (string, error) {
	lockKey := "service-environment-deploy:" + serviceID + ":" + strings.TrimSpace(environmentID)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return "", fmt.Errorf("tenant deploy active-state advisory lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO service_environment_state (
			service_id, environment_id, active_deployment_id,
			last_deployment_seq, version, updated_at
		) VALUES ($1,$2,NULL,0,1,$3)
		ON CONFLICT (service_id, environment_id) DO NOTHING`,
		serviceID,
		strings.TrimSpace(environmentID),
		now,
	); err != nil {
		return "", fmt.Errorf("tenant deploy active-state initialize: %w", err)
	}
	var activeID string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(active_deployment_id, '')
		FROM service_environment_state
		WHERE service_id=$1 AND environment_id=$2
		FOR UPDATE`,
		serviceID,
		strings.TrimSpace(environmentID),
	).Scan(&activeID); err != nil {
		return "", fmt.Errorf("tenant deploy active-state lock: %w", err)
	}
	if activeID == "" {
		return "", nil
	}
	var valid bool
	err := tx.QueryRow(ctx, `
		SELECT TRUE
		FROM deployments
		WHERE id=$1
		  AND service_id=$2
		  AND environment_id=$3
		  AND state=$4
		  AND active=TRUE
		FOR SHARE`,
		activeID,
		serviceID,
		strings.TrimSpace(environmentID),
		string(deploy.DeploymentActive),
	).Scan(&valid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errors.New("tenant deploy active pointer does not reference the canonical active deployment")
		}
		return "", fmt.Errorf("tenant deploy active deployment validate: %w", err)
	}
	if !valid {
		return "", errors.New("tenant deploy active pointer is invalid")
	}
	return activeID, nil
}

func hasServiceManagedGeneratedRoutesInTx(
	ctx context.Context,
	tx *pg.Tx,
	projectID string,
	environmentID string,
	serviceID string,
) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM service_domain_bindings b
			  JOIN http_routes r
			    ON r.id=b.route_id
			   AND r.domain_id=b.domain_id
			   AND r.project_id=b.project_id
			   AND r.environment_id=b.environment_id
			   AND r.service_id=b.service_id
			  JOIN domains d
			    ON d.id=b.domain_id
			   AND d.project_id=b.project_id
			   AND d.environment_id=b.environment_id
			   AND d.service_id=b.service_id
			 WHERE b.project_id=$1
			   AND b.environment_id=$2
			   AND b.service_id=$3
			   AND d.kind='generated'
			   AND `+router.ManagedServiceRouteEligibilityPredicateSQL+`
		)`,
		strings.TrimSpace(projectID),
		strings.TrimSpace(environmentID),
		strings.TrimSpace(serviceID),
	).Scan(&exists)
	return exists, err
}

func tenantDeployDesiredTargetPortInTx(
	ctx context.Context,
	tx *pg.Tx,
	projectID string,
	environmentID string,
	serviceID string,
) (int, error) {
	var targetPort int
	err := tx.QueryRow(ctx, `
		SELECT target_port
		  FROM service_environment_configs
		 WHERE project_id=$1
		   AND environment_id=$2
		   AND service_id=$3
		 FOR SHARE`,
		strings.TrimSpace(projectID),
		strings.TrimSpace(environmentID),
		strings.TrimSpace(serviceID),
	).Scan(&targetPort)
	if errors.Is(err, pgx.ErrNoRows) {
		return serviceconfig.DefaultTargetPort, nil
	}
	if err != nil {
		return 0, fmt.Errorf(
			"tenant generated desired target port: %w",
			err,
		)
	}
	if targetPort < 1 || targetPort > 65535 {
		return 0, errors.New(
			"tenant generated desired target port is invalid",
		)
	}
	return targetPort, nil
}

func bindDeploymentRouteIntentInTx(
	ctx context.Context,
	tx *pg.Tx,
	deploymentID string,
	domainID string,
	routeID string,
	scope router.RouteScope,
	createdAt time.Time,
) error {
	if tx == nil ||
		strings.TrimSpace(deploymentID) == "" ||
		strings.TrimSpace(domainID) == "" ||
		strings.TrimSpace(routeID) == "" ||
		strings.TrimSpace(scope.ProjectID) == "" ||
		strings.TrimSpace(scope.EnvironmentID) == "" ||
		strings.TrimSpace(scope.ServiceID) == "" {
		return errors.New("deployment route binding requires complete identity and scope")
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO deployment_route_intents (
			deployment_id,route_id,domain_id,project_id,
			environment_id,service_id,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (deployment_id,route_id) DO NOTHING`,
		strings.TrimSpace(deploymentID),
		strings.TrimSpace(routeID),
		strings.TrimSpace(domainID),
		strings.TrimSpace(scope.ProjectID),
		strings.TrimSpace(scope.EnvironmentID),
		strings.TrimSpace(scope.ServiceID),
		createdAt.UTC(),
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var exact bool
	err = tx.QueryRow(ctx, `
		SELECT TRUE
		  FROM deployment_route_intents
		 WHERE deployment_id=$1
		   AND route_id=$2
		   AND domain_id=$3
		   AND project_id=$4
		   AND environment_id=$5
		   AND service_id=$6`,
		strings.TrimSpace(deploymentID),
		strings.TrimSpace(routeID),
		strings.TrimSpace(domainID),
		strings.TrimSpace(scope.ProjectID),
		strings.TrimSpace(scope.EnvironmentID),
		strings.TrimSpace(scope.ServiceID),
	).Scan(&exact)
	if errors.Is(err, pgx.ErrNoRows) || !exact {
		return router.ErrDurableRouteScopeMismatch
	}
	return err
}

func bindServiceManagedGeneratedRoutesInTx(
	ctx context.Context,
	tx *pg.Tx,
	deploymentID string,
	projectID string,
	environmentID string,
	serviceID string,
	createdAt time.Time,
) (int64, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO deployment_route_intents (
			deployment_id,route_id,domain_id,project_id,
			environment_id,service_id,created_at
		)
		SELECT $1,b.route_id,b.domain_id,b.project_id,
		       b.environment_id,b.service_id,$5
		  FROM service_domain_bindings b
		  JOIN http_routes r
		    ON r.id=b.route_id
		   AND r.domain_id=b.domain_id
		   AND r.project_id=b.project_id
		   AND r.environment_id=b.environment_id
		   AND r.service_id=b.service_id
		  JOIN domains d
		    ON d.id=b.domain_id
		   AND d.project_id=b.project_id
		   AND d.environment_id=b.environment_id
		   AND d.service_id=b.service_id
		 WHERE b.project_id=$2
		   AND b.environment_id=$3
		   AND b.service_id=$4
		   AND `+router.ManagedServiceRouteEligibilityPredicateSQL+`
		ON CONFLICT (deployment_id,route_id) DO NOTHING`,
		strings.TrimSpace(deploymentID),
		strings.TrimSpace(projectID),
		strings.TrimSpace(environmentID),
		strings.TrimSpace(serviceID),
		createdAt.UTC(),
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func tenantDeployAcceptedStoredResponse(
	operation orailtypes.Operation,
) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(operation)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("tenant deploy response encode: %w", err)
	}
	body = append(body, '\n')
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Location", v1alphaPrefix+"/operations/"+string(operation.ID))
	headers.Set("X-Midnight-Delivery-State", "pending")
	return idempotency.StoredResponse{
		StatusCode: http.StatusAccepted,
		Header:     headers,
		Body:       body,
	}, nil
}

func tenantDeployAcceptedAuditRow(
	request TenantImageDeployRequest,
	now time.Time,
	workspaceID string,
	projectID string,
	workspaceRole identity.WorkspaceRole,
	projectRole identity.ProjectRole,
	service Service,
	intent DeployIntakeRequest,
	commit DeployIntakeCommitResult,
	generatedHostname string,
) audit.Row {
	after := map[string]any{
		"service_id":          string(service.ID),
		"deployment_id":       commit.Deployment.ID,
		"operation_id":        string(commit.Operation.ID),
		"outbox_id":           commit.OutboxID,
		"queue":               string(queue.Deploy),
		"delivery_state":      "pending",
		"expected_active_id":  commit.Deployment.ExpectedActiveID,
		"immutable_image_ref": intent.Image,
	}
	if generatedHostname != "" {
		after["generated_hostname"] = generatedHostname
		after["route_apply_state"] = string(router.RouteApplyPending)
	}
	return audit.Row{
		RequestID:      request.RequestID,
		OccurredAt:     now,
		RecordedAt:     now,
		ActorID:        request.Principal.UserID,
		ActorType:      "user",
		AuthMethod:     request.Principal.AuthMethod,
		SourceIP:       normalizedMutationSourceIP(request.SourceIP),
		UserAgent:      request.UserAgent,
		WorkspaceID:    workspaceID,
		ProjectID:      projectID,
		EnvironmentID:  intent.EnvironmentID,
		ResourceKind:   "operation",
		ResourceID:     string(commit.Operation.ID),
		Action:         "deploy.intake.accept",
		PolicyDecision: "allowed",
		After:          after,
		Attrs: map[string]any{
			"capability":     string(identity.CapabilityDeploymentTriggerCLI),
			"workspace_role": string(workspaceRole),
			"project_role":   string(projectRole),
			"session_id":     request.Principal.SessionID,
		},
	}
}

func (c *PostgresTenantDeployIntakeCoordinator) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
