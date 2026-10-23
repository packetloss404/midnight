// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/registry"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/pkg/orailtypes"
	"golang.org/x/net/idna"
)

const deployJobPayloadSchema = "midnight.dev/deploy-job/v1alpha1"

const (
	// DeployTrustProfileGitHubTrusted is the ordinary signed GitHub App build
	// boundary. It may use the service cache and the immutable variable
	// versions captured for the deployment.
	DeployTrustProfileGitHubTrusted = "github-trusted"
	// DeployTrustProfileGitHubManual is a server-owned manual build from a
	// canonical GitHub link. Unlike signed-webhook intake, it remains valid when
	// auto-deploy is disabled; current link identity and grants are still
	// revalidated before checkout and token minting.
	DeployTrustProfileGitHubManual = "github-manual"
	// DeployTrustProfileGitHubForkPreview is the untrusted pull-request build
	// boundary. Workers must not inspect or decrypt build variables, must not
	// use a shared cache, and must publish only below the quarantine namespace.
	DeployTrustProfileGitHubForkPreview = "github-fork-preview"
)

// DeploySourceProviderLegacyLocal is reserved for the trusted compatibility
// `POST /services/{id}/up` extractor. Generic deploy intake rejects it; the
// build worker accepts it only with an owned local:// checkout confined below
// the configured upload root.
const DeploySourceProviderLegacyLocal = "legacy-local"

// DeployIntakeRequest is the first durable deploy intent shape. It is
// intentionally narrow until the deployment table/FSM lands.
//
// Two image-supply modes are accepted:
//
//   - Image: an immutable digest-pinned reference, e.g.
//     `registry.example/web@sha256:<64hex>`. Validation enforces the digest.
//   - Source: a structured `{repo, ref, subpath?}` object that triggers the
//     build pipeline. The build worker (cmd/midnight-buildkit worker) resolves
//     the source into an image digest then re-enters the deploy path.
//
// The legacy `source_ref` string field is intentionally still rejected with
// `DeploySourceRefUnsupported`. Callers that want source-based deploys must
// move to the structured `source` field. This keeps the historical contract
// honest: a caller passing `source_ref` was relying on a never-implemented
// shape, while a caller passing the new `source` object is opting into the
// build pipeline explicitly.
type DeployIntakeRequest struct {
	EnvironmentID string              `json:"environment_id,omitempty"`
	SourceRef     string              `json:"source_ref,omitempty"`
	Source        *DeploySourceIntent `json:"source,omitempty"`
	Image         string              `json:"image,omitempty"`
	// DeployNow asks intake to derive the deploy intent from durable state
	// instead of the request body: the service's linked source, or the last
	// image it ran. It is the "Deploy now" console/CLI action and is mutually
	// exclusive with image and source. Intake resolves it into one of those
	// two shapes before validation, so every downstream contract (RBAC,
	// idempotency, operation and deployment records, the queue payload) is
	// exactly the one an explicit request would produce.
	DeployNow      bool                     `json:"deploy_now,omitempty"`
	Mode           string                   `json:"mode,omitempty"`
	DeploymentID   string                   `json:"deployment_id,omitempty"`
	InstanceID     string                   `json:"instance_id,omitempty"`
	Route          *DeployRouteIntent       `json:"route,omitempty"`
	PreviousActive *DeployActiveInstanceRef `json:"previous_active,omitempty"`
}

// DeploySourceIntent describes the repository to build into a digest-pinned
// image before the deploy proceeds. It is the structured replacement for the
// legacy `source_ref` string field.
type DeploySourceIntent struct {
	// Provider identifies the source integration. Empty means an ordinary
	// public git checkout. "github" is reserved for server-owned canonical
	// signed-webhook intake; generic deploy requests must never be able to turn
	// caller-supplied App coordinates into a privileged worker token.
	Provider string `json:"provider,omitempty"`
	// Repository is the provider-native, non-secret repository identity (for
	// GitHub, canonical owner/name). It is kept separate from Repo so the
	// worker can bind a credential to the expected repository rather than
	// trusting an arbitrary clone URL.
	Repository string `json:"repository,omitempty"`
	// WorkspaceID is the canonical Midnight workspace that owned the GitHub
	// installation/link when signed-webhook intake committed this job.
	WorkspaceID string `json:"workspace_id,omitempty"`
	// InstallationID is a non-secret GitHub App installation identifier. The
	// token itself is never part of this durable payload.
	InstallationID int64 `json:"installation_id,omitempty"`
	// RepositoryID is GitHub's immutable numeric repository identity. GitHub
	// checkout workers use it to mint a token scoped to exactly this repository.
	RepositoryID int64 `json:"repository_id,omitempty"`
	// LinkResourceVersion fences queued GitHub work to the exact canonical
	// service link revalidated during signed-webhook intake. The build worker
	// checks it again before checkout and before any project secret is used.
	LinkResourceVersion int64 `json:"link_resource_version,omitempty"`
	// AuthorityBranch is the canonical service-link branch revalidated before
	// checkout and again before the solve. Ref may be an immutable commit SHA;
	// treating that checkout ref as source-link authority would reject valid
	// signed pushes and previews.
	AuthorityBranch string `json:"authority_branch,omitempty"`
	// TrustProfile is assigned only by signed-webhook intake. Generic API
	// callers cannot opt into or out of worker isolation policy.
	TrustProfile string `json:"trust_profile,omitempty"`
	// Repo is the git URL the build worker should clone, e.g.
	// `https://github.com/midnight/sample`. Generic API intake requires a
	// public-host HTTPS URL; trusted upload paths use a separate handler.
	Repo string `json:"repo"`
	// Ref is the git ref (branch, tag, or commit sha) to check out.
	Ref string `json:"ref"`
	// FetchRef is an optional provider-owned remote ref used to make Ref
	// reachable without weakening the exact commit pin. Generic deploy intake
	// cannot set it; GitHub PR previews use refs/pull/<number>/head.
	FetchRef string `json:"fetch_ref,omitempty"`
	// Subpath, when non-empty, narrows the build context to a subdirectory of
	// the checkout. Deprecated compatibility spelling for RootDirectory.
	Subpath string `json:"subpath,omitempty"`
	// RootDirectory is the monorepo build context. It is deliberately
	// independent from webhook watch paths and ConfigPath.
	RootDirectory string `json:"root_directory,omitempty"`
	// ConfigPath is an optional repository-relative platform config file.
	// This slice retains it as durable source metadata; builder-specific
	// config interpretation remains a separate capability.
	ConfigPath string `json:"config_path,omitempty"`
}

type deployJobPayload struct {
	SchemaVersion  string                   `json:"schema_version"`
	OperationID    string                   `json:"operation_id"`
	ServiceID      string                   `json:"service_id"`
	ProjectID      string                   `json:"project_id,omitempty"`
	EnvironmentID  string                   `json:"environment_id,omitempty"`
	Request        DeployIntakeRequest      `json:"request"`
	Candidate      deployJobCandidate       `json:"candidate,omitempty"`
	Route          *DeployRouteIntent       `json:"route,omitempty"`
	PreviousActive *DeployActiveInstanceRef `json:"previous_active,omitempty"`
	EnqueuedAt     time.Time                `json:"enqueued_at"`
}

type deployJobCandidate struct {
	DeploymentID   string            `json:"deployment_id,omitempty"`
	InstanceID     string            `json:"instance_id,omitempty"`
	ProjectID      string            `json:"project_id,omitempty"`
	EnvironmentID  string            `json:"environment_id,omitempty"`
	ServiceID      string            `json:"service_id,omitempty"`
	Image          string            `json:"image,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

type DeployRouteIntent struct {
	Name   string `json:"name,omitempty"`
	Group  string `json:"group,omitempty"`
	Host   string `json:"host,omitempty"`
	Path   string `json:"path,omitempty"`
	Public bool   `json:"public"`
	TLS    bool   `json:"tls"`
}

type DeployActiveInstanceRef struct {
	DeploymentID string `json:"deployment_id,omitempty"`
	InstanceID   string `json:"instance_id,omitempty"`
}

func createDeployIntake(w http.ResponseWriter, r *http.Request, serviceID orailtypes.ID, serviceStore ServiceStore, operationStore OperationStore, queueStore queue.Store, deploymentStore DeploymentStore, durableIntake DurableDeployIntake, idem idempotency.Store, emitter *auditEmitter, clock func() time.Time, routeStore router.DomainWorkflowStore, scopeResolvers ...MutationScopeResolver) {
	if queueStore == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "deploy intake queue is not wired", map[string]any{"service_id": serviceID})
		return
	}
	if operationStore == nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation store is not wired", map[string]any{"service_id": serviceID})
		return
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}

	body, ok := readBoundedJSONRequestBody(w, r, "invalid deploy request body")
	if !ok {
		return
	}

	reservation, ok := reserveDeployIdempotency(w, r, idem, body, serviceID)
	if !ok {
		return
	}
	key := idempotency.Key(r.Header.Get("Idempotency-Key"))
	ref := reservation.Reference()
	if reservation.OperationID != "" {
		operation, err := operationStore.Get(r.Context(), orailtypes.ID(reservation.OperationID))
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation replay failed", map[string]any{"operation_id": reservation.OperationID})
			return
		}
		writeOperationAccepted(w, operation, true)
		return
	}

	var request DeployIntakeRequest
	if len(body) > 0 {
		if err := strictJSONDecode(body, &request); err != nil {
			writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid deploy request body", map[string]any{"reason": err.Error(), "service_id": serviceID})
			return
		}
	}

	service, err := serviceStore.GetService(r.Context(), serviceID)
	if errors.Is(err, ErrResourceNotFound) {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusNotFound, failurecodes.ResourceNotFound, "service not found", map[string]any{"service_id": serviceID})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service lookup failed", map[string]any{"service_id": serviceID})
		return
	}
	// "Deploy now" is resolved from durable state first, so the request that
	// reaches validation is an ordinary image or source intent.
	if failure := resolveDeployNowIntent(r.Context(), service, deploymentStore, &request); failure != nil {
		writeDeployIntakeTerminalError(w, r, idem, ref, failure.Status, failure.Code, failure.Message, failure.Details)
		return
	}
	// Image admission runs before validation so a mutable tag arrives at the
	// validator already pinned to the digest Midnight will run, and a digest
	// the registry does not hold never reaches a durable row or the queue.
	admissionCtx, cancelAdmission := context.WithTimeout(r.Context(), deployImageAdmissionTimeout)
	failure := admitDeployIntakeImage(admissionCtx, &request)
	cancelAdmission()
	if failure != nil {
		writeDeployIntakeTerminalError(w, r, idem, ref, failure.Status, failure.Code, failure.Message, failure.Details)
		return
	}
	if code, message, details, ok := validateDeployIntakeRequest(request); !ok {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusBadRequest, code, message, details)
		return
	}
	if scopeResolver := firstMutationScopeResolver(scopeResolvers); scopeResolver != nil {
		_, err := scopeResolver.ResolveMutationScope(r.Context(), MutationScopeRequest{
			ProjectID:          string(service.ProjectID),
			ServiceID:          string(service.ID),
			EnvironmentID:      request.EnvironmentID,
			RequireProject:     true,
			RequireService:     true,
			RequireEnvironment: true,
		})
		if err != nil {
			status, code, message, details := mutationScopeHTTPError(err)
			writeDeployIntakeTerminalError(w, r, idem, ref, status, code, message, details)
			return
		}
	}
	if request.Route != nil {
		resolved, err := resolveManagedDeployRouteSelector(
			r.Context(),
			routeStore,
			router.RouteScope{
				ProjectID: string(service.ProjectID), EnvironmentID: request.EnvironmentID, ServiceID: string(service.ID),
			},
			*request.Route,
		)
		if err != nil {
			message := "route selector did not match one eligible service binding"
			details := map[string]any{
				"field":           "route",
				"reason":          "routes must already be owned by the service and have complete ownership/TLS evidence",
				"required_action": "create or attach the route through the service domain lifecycle, complete ownership and TLS verification, then retry with an exact selector",
			}
			if strings.TrimSpace(request.Route.Host) != "" {
				details["field"] = "route.host"
			}
			if errors.Is(err, router.ErrDurableRouteAmbiguous) {
				message = "route selector is ambiguous"
				details["reason"] = "add an exact host, name, or path selector"
			}
			writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusBadRequest, failurecodes.ResourceValidationFailed, message, details)
			return
		}
		request.Route = &resolved
	}

	// Two-mode intake: a structured `source` block triggers the build pipeline
	// (a `build` operation + `build` queue job). On success the build worker
	// re-enters the deploy path with the resulting `image@sha256:<digest>`.
	// The plain digest-pinned image path skips the build queue and enqueues a
	// `deploy` operation directly, preserving every existing v1alpha test.
	mode := "deploy"
	queueName := queue.Deploy
	if request.Source != nil {
		mode = "build"
		queueName = queue.Build
	}
	// Postgres timestamps have microsecond precision. Use that exact instant for
	// the stored response as well as the durable rows so a replay and later GET
	// cannot differ only because the response retained nanoseconds.
	enqueuedAt := clock().UTC().Truncate(time.Microsecond)

	if durableIntake != nil {
		operation := orailtypes.Operation{
			ID:        defaultOperationID(),
			Kind:      mode,
			State:     orailtypes.OperationPending,
			CreatedAt: enqueuedAt,
			UpdatedAt: enqueuedAt,
		}
		canonical, err := canonicalDeploymentRecord(
			service,
			request,
			operation.ID,
			acceptedDeploymentIntentKey(operation.ID),
		)
		if err != nil {
			writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "canonical deployment prepare failed", map[string]any{"operation_id": operation.ID})
			return
		}
		canonical.ID = mintID(orailtypes.IDPrefixDeployment)
		request.DeploymentID = canonical.ID
		request.InstanceID = canonical.ID + "-r0"
		request.PreviousActive = resolvePreviousActiveDeployment(r.Context(), deploymentStore, service.ID, request.EnvironmentID, canonical.ID)
		canonical.ExpectedActiveID = previousActiveDeploymentID(request.PreviousActive)
		payload, err := buildDeployJobPayload(operation.ID, service, request, enqueuedAt)
		if err != nil {
			writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "deploy job payload encode failed", map[string]any{"operation_id": operation.ID})
			return
		}
		enqueueRequest := buildDeployEnqueueRequest(operation.ID, service, enqueuedAt, payload)
		enqueueRequest.Queue = queueName
		response, err := tenantDeployAcceptedStoredResponse(operation)
		if err != nil {
			writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "deploy response encode failed", map[string]any{"operation_id": operation.ID})
			return
		}
		result, err := durableIntake.CommitDeployIntake(r.Context(), DeployIntakeCommit{
			Operation:      operation,
			Deployment:     canonical,
			IdempotencyKey: key,
			IdempotencyRef: ref,
			Response:       response,
			Enqueue:        enqueueRequest,
			InitialMessage: durableDeployIntakeMessage(service, request, queueName),
			Now:            enqueuedAt,
		})
		if err != nil {
			writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "durable deploy intake commit failed", map[string]any{"operation_id": operation.ID})
			return
		}
		// The transaction made this intent durable. Delivery belongs to the
		// independently retrying dispatcher; never claim that this specific
		// row was delivered merely because a global dispatch pass moved some
		// (possibly older) row.
		deliveryState := "pending"
		emitter.emit(r.Context(), audit.Row{
			ResourceKind: "operation",
			ResourceID:   string(result.Operation.ID),
			ProjectID:    string(service.ProjectID),
			Action:       "deploy.intake.accept",
			After: map[string]any{
				"service_id":          string(service.ID),
				"project_id":          string(service.ProjectID),
				"queue":               string(queueName),
				"outbox_id":           result.OutboxID,
				"delivery_state":      deliveryState,
				"deployment_id":       result.Deployment.ID,
				"operation_id":        string(result.Operation.ID),
				"parent_operation_id": string(result.Operation.ID),
				"operation_kind":      result.Operation.Kind,
			},
		})
		writeStoredResponse(w, response)
		return
	}

	capture := newIdempotencyResponseCapture(w)
	w = capture
	defer capture.Complete(r, idem, ref)

	operation, err := CreateQueuedOperation(r.Context(), operationStore, mode)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation create failed", map[string]any{"service_id": serviceID})
		return
	}
	if _, err := idem.AttachOperation(r.Context(), ref, string(operation.ID)); err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency operation attach failed", map[string]any{"operation_id": operation.ID})
		return
	}

	canonical, err := createCanonicalDeployment(
		r.Context(),
		deploymentStore,
		service,
		request,
		operation.ID,
		acceptedDeploymentIntentKey(operation.ID),
	)
	if err != nil {
		failDeployIntakeOperation(r, operationStore, operation.ID, failurecodes.InternalError, "canonical deployment create failed")
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "canonical deployment create failed", map[string]any{"operation_id": operation.ID})
		return
	}
	request.DeploymentID = canonical.ID
	request.InstanceID = canonical.ID + "-r0"
	request.PreviousActive = resolvePreviousActiveDeployment(r.Context(), deploymentStore, service.ID, request.EnvironmentID, canonical.ID)
	payload, err := buildDeployJobPayload(operation.ID, service, request, enqueuedAt)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "deploy job payload encode failed", map[string]any{"operation_id": operation.ID})
		return
	}
	enqueueRequest := buildDeployEnqueueRequest(operation.ID, service, enqueuedAt, payload)
	enqueueRequest.Queue = queueName
	job, err := queueStore.Enqueue(r.Context(), enqueueRequest)
	if err != nil {
		failDeployIntakeOperation(r, operationStore, operation.ID, failurecodes.DeployJobEnqueueFailed, "deploy job enqueue failed")
		_ = deploymentStore.FailDeployment(r.Context(), canonical.ID, deploy.DeploymentQueued, string(failurecodes.DeployJobEnqueueFailed), "deploy job enqueue failed")
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.DeployJobEnqueueFailed, "deploy job enqueue failed", map[string]any{"operation_id": operation.ID})
		return
	}
	enqueueLabel := "deploy"
	if queueName == queue.Build {
		enqueueLabel = "build"
	}
	if _, err := operationStore.AppendEvent(r.Context(), OperationEvent{OperationID: operation.ID, State: operation.State, Message: enqueueLabel + " job enqueued: job_id=" + job.ID + " operation_id=" + string(operation.ID)}); err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "operation event append failed", map[string]any{"operation_id": operation.ID, "job_id": job.ID})
		return
	}
	if _, err := operationStore.AppendEvent(r.Context(), OperationEvent{
		OperationID: operation.ID,
		State:       operation.State,
		Message:     deployIntakeQueuedEventMessage(service, request, job),
		CreatedAt:   enqueuedAt,
	}); err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "deploy intake metadata event append failed", map[string]any{"operation_id": operation.ID, "job_id": job.ID, "service_id": service.ID})
		return
	}
	emitter.emit(r.Context(), audit.Row{
		ResourceKind: "operation",
		ResourceID:   string(operation.ID),
		ProjectID:    string(service.ProjectID),
		Action:       "deploy.intake.accept",
		After: map[string]any{
			"service_id":          string(service.ID),
			"project_id":          string(service.ProjectID),
			"queue":               string(queueName),
			"job_id":              job.ID,
			"operation_id":        string(operation.ID),
			"parent_operation_id": string(operation.ID),
			"operation_kind":      operation.Kind,
		},
	})
	writeOperationAccepted(w, operation, false)
}

func reserveDeployIdempotency(w http.ResponseWriter, r *http.Request, idem idempotency.Store, body []byte, serviceID orailtypes.ID) (idempotency.Reservation, bool) {
	key := idempotency.Key(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyRequired, "Idempotency-Key header is required", map[string]any{"service_id": serviceID})
		return idempotency.Reservation{}, false
	}
	fingerprint := idempotencyRequestFingerprintForRoute(
		r,
		v1alphaPrefix+"/services/{service_id}/deployments",
		idempotency.BodyHashSHA256(body),
	)
	reservation, err := idem.Reserve(r.Context(), key, fingerprint, 24*time.Hour)
	if errors.Is(err, idempotency.ErrInvalidKey) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", map[string]any{"service_id": serviceID})
		return idempotency.Reservation{}, false
	}
	if errors.Is(err, idempotency.ErrKeyReusedDifferentBody) {
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", map[string]any{"service_id": serviceID})
		return idempotency.Reservation{}, false
	}
	if errors.Is(err, idempotency.ErrKeyReused) {
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", map[string]any{"service_id": serviceID})
		return idempotency.Reservation{}, false
	}
	if errors.Is(err, idempotency.ErrRequestInProgress) {
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", map[string]any{"service_id": serviceID, "operation_id": reservation.OperationID})
		return idempotency.Reservation{}, false
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency reservation failed", map[string]any{"service_id": serviceID})
		return idempotency.Reservation{}, false
	}
	if reservationHasExactStoredResponse(reservation) {
		stored := reservation.Response.Clone()
		w.Header().Set("Idempotent-Replayed", "true")
		writeStoredResponse(w, stored)
		return idempotency.Reservation{}, false
	}
	return reservation, true
}

func buildDeployEnqueueRequest(operationID orailtypes.ID, service Service, enqueuedAt time.Time, payload []byte) queue.EnqueueRequest {
	return queue.EnqueueRequest{
		Queue:             queue.Deploy,
		PartitionKey:      deployPartitionKey(service),
		ParentOperationID: string(operationID),
		AvailableAt:       enqueuedAt,
		Payload:           payload,
	}
}

// createCanonicalDeployment creates product truth before any work becomes
// visible to workers. A missing deployment store is a hard configuration
// error, never a reason to fall back to event-message reconstruction.
func createCanonicalDeployment(ctx context.Context, store DeploymentStore, service Service, request DeployIntakeRequest, operationID orailtypes.ID, key idempotency.Key) (DeploymentRecord, error) {
	if store == nil {
		return DeploymentRecord{}, errors.New("deployment store is not configured")
	}
	rec, err := canonicalDeploymentRecord(service, request, operationID, key)
	if err != nil {
		return DeploymentRecord{}, err
	}
	return store.CreateDeployment(ctx, rec)
}

func canonicalDeploymentRecord(service Service, request DeployIntakeRequest, operationID orailtypes.ID, key idempotency.Key) (DeploymentRecord, error) {
	env := strings.TrimSpace(request.EnvironmentID)
	project := strings.TrimSpace(string(service.ProjectID))
	if env == "" || project == "" {
		return DeploymentRecord{}, errors.New("canonical deployment scope is incomplete")
	}
	rec := DeploymentRecord{
		ProjectID:     project,
		ServiceID:     string(service.ID),
		EnvironmentID: env,
		Action:        deploy.ActionDeployImage,
		ImageRef:      strings.TrimSpace(request.Image),
		OperationID:   string(operationID),
		TriggerKey:    canonicalDeploymentTriggerKey(service, request, key),
	}
	if rec.ImageRef != "" {
		parsed, err := registry.ParseImageReference(rec.ImageRef)
		if err != nil || parsed.Digest == "" {
			return DeploymentRecord{}, errors.New("canonical deployment image must be an immutable digest reference")
		}
		rec.ImageDigest = parsed.Digest
	}
	if request.PreviousActive != nil {
		rec.ExpectedActiveID = strings.TrimSpace(request.PreviousActive.DeploymentID)
	}
	if request.Source != nil {
		rec.Action = deploy.ActionDeploySource
		rec.SourceType = firstNonEmpty(strings.TrimSpace(request.Source.Provider), "git")
		rec.Repo = strings.TrimSpace(request.Source.Repo)
		rec.Ref = strings.TrimSpace(request.Source.Ref)
		rec.RootDirectory = deploySourceRootDirectory(request.Source)
		rec.ImageRef = ""
	}
	return rec, nil
}

func previousActiveDeploymentID(previous *DeployActiveInstanceRef) string {
	if previous == nil {
		return ""
	}
	return strings.TrimSpace(previous.DeploymentID)
}

func durableDeployIntakeMessage(service Service, request DeployIntakeRequest, queueName queue.Name) string {
	fields := []string{
		"deploy intent durably accepted",
		"queue=" + string(queueName),
		"project_id=" + strings.TrimSpace(string(service.ProjectID)),
		"service_id=" + strings.TrimSpace(string(service.ID)),
		"environment_id=" + strings.TrimSpace(request.EnvironmentID),
		"deployment_id=" + strings.TrimSpace(request.DeploymentID),
	}
	return strings.Join(fields, " ")
}

func canonicalDeploymentTriggerKey(service Service, request DeployIntakeRequest, key idempotency.Key) string {
	mode := "image"
	if request.Source != nil {
		mode = "source"
	}
	raw := strings.Join([]string{
		string(service.ProjectID),
		string(service.ID),
		strings.TrimSpace(request.EnvironmentID),
		mode,
		string(key),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return "trigger:sha256:" + fmt.Sprintf("%x", sum[:])
}

// acceptedDeploymentIntentKey separates client replay identity from durable
// work identity. The idempotency reservation owns client-key replay; a
// globally unique server operation owns deployment/outbox deduplication.
// Reusing a client key in another actor, route, or generation must therefore
// never collide with already accepted work.
func acceptedDeploymentIntentKey(operationID orailtypes.ID) idempotency.Key {
	return idempotency.Key("accepted-operation:" + string(operationID))
}

func resolvePreviousActiveDeployment(ctx context.Context, store DeploymentStore, serviceID orailtypes.ID, environmentID, excludeID string) *DeployActiveInstanceRef {
	if store == nil {
		return nil
	}
	records, err := store.ListDeploymentsByService(ctx, string(serviceID))
	if err != nil {
		return nil
	}
	for _, record := range records {
		if record.ID != excludeID &&
			record.EnvironmentID == environmentID &&
			record.Active &&
			record.State == deploy.DeploymentActive {
			return &DeployActiveInstanceRef{DeploymentID: record.ID}
		}
	}
	return nil
}

func buildDeployJobPayload(operationID orailtypes.ID, service Service, request DeployIntakeRequest, enqueuedAt time.Time) ([]byte, error) {
	return json.Marshal(deployJobPayload{
		SchemaVersion: deployJobPayloadSchema,
		OperationID:   string(operationID),
		ServiceID:     string(service.ID),
		ProjectID:     string(service.ProjectID),
		EnvironmentID: request.EnvironmentID,
		Request:       request,
		Candidate: deployJobCandidate{
			DeploymentID:   request.DeploymentID,
			InstanceID:     request.InstanceID,
			ProjectID:      string(service.ProjectID),
			EnvironmentID:  request.EnvironmentID,
			ServiceID:      string(service.ID),
			Image:          request.Image,
			IdempotencyKey: string(operationID),
		},
		Route:          request.Route,
		PreviousActive: request.PreviousActive,
		EnqueuedAt:     enqueuedAt.UTC(),
	})
}

func deployPartitionKey(service Service) string {
	if service.ProjectID != "" {
		return string(service.ProjectID)
	}
	return string(service.ID)
}

func failDeployIntakeOperation(r *http.Request, operationStore OperationStore, operationID orailtypes.ID, code failurecodes.Code, message string) {
	operation, err := operationStore.Fail(r.Context(), operationID, orailtypes.FailureCode(code), message)
	if err != nil {
		return
	}
	_, _ = operationStore.AppendEvent(r.Context(), OperationEvent{OperationID: operation.ID, State: operation.State, Message: message})
}

func deployIntakeQueuedEventMessage(service Service, request DeployIntakeRequest, job queue.Job) string {
	fields := []string{
		"service_id=" + strings.TrimSpace(string(service.ID)),
		"project_id=" + strings.TrimSpace(string(service.ProjectID)),
		"queue=" + strings.TrimSpace(string(job.Queue)),
		"job_id=" + strings.TrimSpace(job.ID),
		"job_state=" + strings.TrimSpace(string(job.State)),
		"deployment_id=" + strings.TrimSpace(request.DeploymentID),
		"instance_id=" + strings.TrimSpace(request.InstanceID),
		"environment_id=" + strings.TrimSpace(request.EnvironmentID),
		"image=" + strings.TrimSpace(request.Image),
		"source_repo=" + deployIntakeSourceField(request.Source, sourceFieldRepo),
		"source_ref=" + deployIntakeSourceField(request.Source, sourceFieldRef),
		"source_subpath=" + deployIntakeSourceField(request.Source, sourceFieldSubpath),
		"route_intent=" + deployIntakeRouteSignal(request.Route),
	}
	compact := fields[:0]
	for _, field := range fields {
		_, value, ok := strings.Cut(field, "=")
		if ok && strings.TrimSpace(value) == "" {
			continue
		}
		compact = append(compact, field)
	}
	return "deploy.intake " + strings.Join(compact, " ")
}

func deployIntakeRouteSignal(route *DeployRouteIntent) string {
	if route == nil {
		return ""
	}
	return firstNonEmpty(route.Host, route.Name, route.Group, route.Path)
}

type sourceField int

const (
	sourceFieldRepo sourceField = iota
	sourceFieldRef
	sourceFieldSubpath
)

func deployIntakeSourceField(source *DeploySourceIntent, field sourceField) string {
	if source == nil {
		return ""
	}
	switch field {
	case sourceFieldRepo:
		return strings.TrimSpace(source.Repo)
	case sourceFieldRef:
		return strings.TrimSpace(source.Ref)
	case sourceFieldSubpath:
		return deploySourceRootDirectory(source)
	}
	return ""
}

func validateDeployIntakeRequest(request DeployIntakeRequest) (failurecodes.Code, string, map[string]any, bool) {
	image := strings.TrimSpace(request.Image)
	sourceRef := strings.TrimSpace(request.SourceRef)
	// `deploy_now` is a resolution instruction, never a durable intent. Intake
	// clears it once it has derived the concrete image or source; reaching the
	// validator with it still set means a caller path has not adopted the
	// resolution step, and queueing that request would persist an intent with
	// no image and no source at all.
	if request.DeployNow {
		return failurecodes.ResourceValidationFailed, "deploy_now is not accepted on this deploy path", map[string]any{
			"field":                "deploy_now",
			"reason":               "deploy_now is resolved from durable service state by the service deployments intake route",
			"accepted_alternative": "pass an explicit image or source",
		}, false
	}
	// Legacy `source_ref` string is permanently retired: callers must move to
	// the structured `source` object. The error code is reused so existing
	// CLI/dashboard surfaces keep their helpful copy.
	if sourceRef != "" {
		return failurecodes.DeploySourceRefUnsupported, "source_ref deploys require build integration and are not wired yet", map[string]any{
			"field":                "source_ref",
			"reason":               "legacy source_ref string is not supported; pass the structured `source` object",
			"required_capability":  "source-build-to-digest-image",
			"required_output":      "candidate.image must be an immutable sha256 digest produced by the build pipeline",
			"accepted_alternative": "pass a digest-pinned image without source_ref",
		}, false
	}
	// Structured source path: enqueue a build job that produces a digest, then
	// re-enters the deploy path with the resulting `image@sha256:<digest>`.
	if request.Source != nil {
		repo := strings.TrimSpace(request.Source.Repo)
		ref := strings.TrimSpace(request.Source.Ref)
		rootDirectory := deploySourceRootDirectory(request.Source)
		configPath := strings.TrimSpace(request.Source.ConfigPath)
		if repo == "" || ref == "" {
			return failurecodes.DeploySourceRefUnsupported, "source deploy requires repo and ref", map[string]any{
				"field":           "source",
				"reason":          "missing required fields",
				"required_fields": []string{"source.repo", "source.ref"},
			}, false
		}
		if request.Source.Repo != repo {
			return failurecodes.ResourceValidationFailed, "source repo must not contain surrounding whitespace", map[string]any{
				"field":  "source.repo",
				"reason": "source.repo is persisted exactly and must use a canonical whitespace-free value",
			}, false
		}
		if request.Source.Ref != ref {
			return failurecodes.ResourceValidationFailed, "source ref must not contain surrounding whitespace", map[string]any{
				"field":  "source.ref",
				"reason": "source.ref is persisted exactly and must use a canonical whitespace-free value",
			}, false
		}
		if scheme := reservedDeploySourceScheme(repo); scheme != "" {
			return failurecodes.ResourceValidationFailed, "source repository uses a reserved internal scheme", map[string]any{
				"field":  "source.repo",
				"reason": scheme + " sources may only be created by Midnight's trusted upload intake",
			}, false
		}
		if parsed, err := url.Parse(repo); err == nil && parsed.Scheme != "" {
			if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return failurecodes.ResourceValidationFailed, "source repository URL must not contain credentials, query, or fragment", map[string]any{
					"field":  "source.repo",
					"reason": "credential-bearing source URLs cannot be persisted safely; use a provider installation descriptor",
				}, false
			}
		}
		if strings.TrimSpace(request.Source.Subpath) != "" &&
			strings.TrimSpace(request.Source.RootDirectory) != "" &&
			strings.TrimSpace(request.Source.Subpath) != strings.TrimSpace(request.Source.RootDirectory) {
			return failurecodes.ResourceValidationFailed, "source subpath and root_directory disagree", map[string]any{
				"field":  "source.root_directory",
				"reason": "use root_directory; deprecated subpath may only repeat the same value",
			}, false
		}
		for _, candidate := range []struct {
			field string
			value string
		}{
			{field: "source.root_directory", value: rootDirectory},
			{field: "source.config_path", value: configPath},
		} {
			field, value := candidate.field, candidate.value
			if strings.HasPrefix(value, "/") || value == ".." || strings.HasPrefix(value, "../") || strings.Contains(value, "/../") {
				return failurecodes.DeploySourceRefUnsupported, "source paths must be relative paths inside the checkout", map[string]any{
					"field":  field,
					"reason": "path must not start with / or escape with ..",
				}, false
			}
		}
		provider := strings.ToLower(strings.TrimSpace(request.Source.Provider))
		if request.Source.Provider != provider {
			return failurecodes.ResourceValidationFailed, "source provider must use its canonical lowercase spelling", map[string]any{
				"field":  "source.provider",
				"reason": "provider values are persisted exactly; use git or omit the field; github is reserved for canonical signed-webhook intake",
			}, false
		}
		switch provider {
		case "", "git":
			if request.Source.InstallationID != 0 ||
				request.Source.RepositoryID != 0 ||
				request.Source.LinkResourceVersion != 0 ||
				strings.TrimSpace(request.Source.AuthorityBranch) != "" ||
				strings.TrimSpace(request.Source.TrustProfile) != "" ||
				strings.TrimSpace(request.Source.FetchRef) != "" ||
				strings.TrimSpace(request.Source.WorkspaceID) != "" ||
				strings.TrimSpace(request.Source.Repository) != "" {
				return failurecodes.ResourceValidationFailed, "provider identity is not accepted by generic deploy intake", map[string]any{
					"field":  "source.provider",
					"reason": "GitHub installation and repository coordinates must come from Midnight's canonical signed-webhook source authority",
				}, false
			}
			if err := ValidateGenericGitSourceRepo(repo); err != nil {
				return failurecodes.ResourceValidationFailed, "git source repo must be a canonical public-host HTTPS URL", map[string]any{
					"field":  "source.repo",
					"reason": err.Error(),
				}, false
			}
		case "github":
			return failurecodes.ResourceValidationFailed, "GitHub App sources are not accepted by generic deploy intake", map[string]any{
				"field":    "source.provider",
				"provider": provider,
				"reason":   "GitHub installation and repository coordinates must come from Midnight's canonical signed-webhook source authority",
			}, false
		default:
			return failurecodes.ResourceValidationFailed, "source provider is not supported", map[string]any{"field": "source.provider", "provider": provider}, false
		}
		if image != "" {
			return failurecodes.ResourceValidationFailed, "deploy request must include either image OR source, not both", map[string]any{
				"field":  "image",
				"reason": "image and source are mutually exclusive",
			}, false
		}
		return validateDeployRouteAndPass(request)
	}
	if image == "" {
		return failurecodes.ResourceValidationFailed, "deploy request requires image", map[string]any{"field": "image"}, false
	}
	parsed, err := registry.ParseImageReference(image)
	if err != nil || parsed.Digest == "" {
		// Deploy intake pins a mutable tag to its registry digest before it
		// reaches this validator, so a tag arriving here means either the
		// instance has no image admission configured or the caller used a
		// deploy path that has not adopted the resolution step. Say which.
		reason := "expected image reference ending in @sha256:<64 lowercase hex chars>"
		if deployImageAdmissionAvailable() {
			reason += "; POST /services/{service_id}/deployments resolves a mutable tag to its digest, other deploy paths require the digest"
		} else {
			reason += "; configure a registry endpoint (MIDNIGHT_REGISTRY_ENDPOINT) or MIDNIGHT_DEPLOY_IMAGE_RESOLVE to deploy by tag" // not-a-failure-code
		}
		return failurecodes.ResourceValidationFailed, "deploy image must be a valid immutable sha256 digest reference", map[string]any{"field": "image", "reason": reason}, false
	}
	return validateDeployRouteAndPass(request)
}

// reservedDeploySourceScheme rejects the two URI namespaces whose contents
// are meaningful only after trusted API-side staging. Generic operator and
// automation deploy intake must never be able to smuggle a filesystem path or
// an unowned CAS digest into the worker payload.
//
// We deliberately normalize case, surrounding/embedded whitespace and
// percent-escaped delimiters before inspecting the scheme. Those spellings
// are not useful git remotes, and accepting them would leave the admission
// boundary dependent on a downstream parser's canonicalization choices.
func reservedDeploySourceScheme(raw string) string {
	normalized := strings.TrimSpace(raw)
	for range 2 {
		decoded, err := url.PathUnescape(normalized)
		if err != nil || decoded == normalized {
			break
		}
		normalized = decoded
	}
	colon := strings.IndexByte(normalized, ':')
	if colon < 0 {
		return ""
	}
	scheme := strings.Map(func(value rune) rune {
		if unicode.IsSpace(value) || unicode.IsControl(value) {
			return -1
		}
		return unicode.ToLower(value)
	}, normalized[:colon])
	switch scheme {
	case "local", "sourceartifact":
		return scheme
	default:
		return ""
	}
}

// ValidateGenericGitSourceRepo is the shared syntactic admission boundary for source
// URLs that the worker will fetch without a provider-owned descriptor. It
// blocks local transports and obvious internal names without performing DNS
// at request time. DNS resolution here would be a time-of-check/time-of-use
// check, not a complete SSRF defense: a public-looking hostname can still
// resolve or rebind to a private address later. The worker/runtime network
// boundary must therefore also enforce private-address egress denial.
func ValidateGenericGitSourceRepo(raw string) error {
	repo := strings.TrimSpace(raw)
	if repo != raw {
		return errors.New("surrounding whitespace is not accepted")
	}
	parsed, err := url.Parse(repo)
	if err != nil || parsed.Opaque != "" ||
		!strings.EqualFold(parsed.Scheme, "https") ||
		parsed.Hostname() == "" {
		return errors.New("only an absolute HTTPS repository URL is accepted")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("credentials, query, and fragment are not accepted")
	}
	if parsed.Port() != "" || strings.HasSuffix(parsed.Host, ":") {
		return errors.New("an explicit network port is not accepted")
	}
	if parsed.EscapedPath() == "" || parsed.EscapedPath() == "/" {
		return errors.New("repository URL path is required")
	}

	rawHost := parsed.Hostname()
	if strings.HasSuffix(rawHost, ".") {
		return errors.New("hostname must use canonical form without a trailing dot")
	}
	host, err := idna.Lookup.ToASCII(rawHost)
	if err != nil {
		return errors.New("hostname is not a valid DNS name")
	}
	host = strings.ToLower(host)
	if net.ParseIP(host) != nil {
		return errors.New("IP-literal repository hosts are not accepted")
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return errors.New("repository hostname must be a public multi-label DNS name")
	}
	numericLabels := true
	for _, label := range labels {
		if !validDeploySourceDNSLabel(label) {
			return errors.New("hostname contains an invalid DNS label")
		}
		if !deploySourceNumericHostLabel(label) {
			numericLabels = false
		}
	}
	if numericLabels {
		return errors.New("numeric IP-like repository hosts are not accepted")
	}
	for _, suffix := range []string{
		"localhost",
		"local",
		"localdomain",
		"internal",
		"intranet",
		"private",
		"corp",
		"lan",
		"home",
		"home.arpa",
		"svc",
		"test",
		"invalid",
		"example",
		"onion",
	} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return errors.New("private, local, or special-use repository hosts are not accepted")
		}
	}
	return nil
}

func validDeploySourceDNSLabel(label string) bool {
	if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, value := range label {
		if (value < 'a' || value > 'z') &&
			(value < '0' || value > '9') &&
			value != '-' {
			return false
		}
	}
	return true
}

func deploySourceNumericHostLabel(label string) bool {
	base := 10
	value := label
	if strings.HasPrefix(value, "0x") {
		base = 16
		value = strings.TrimPrefix(value, "0x")
	}
	if value == "" {
		return false
	}
	for _, candidate := range value {
		if candidate >= '0' && candidate <= '9' {
			continue
		}
		if base == 16 && candidate >= 'a' && candidate <= 'f' {
			continue
		}
		return false
	}
	return true
}

func deploySourceRootDirectory(source *DeploySourceIntent) string {
	if source == nil {
		return ""
	}
	if root := strings.TrimSpace(source.RootDirectory); root != "" {
		return root
	}
	return strings.TrimSpace(source.Subpath)
}

func validateDeployRouteAndPass(request DeployIntakeRequest) (failurecodes.Code, string, map[string]any, bool) {
	if strings.TrimSpace(request.EnvironmentID) == "" {
		return failurecodes.ResourceValidationFailed, "deploy request requires environment_id", map[string]any{
			"field":  "environment_id",
			"reason": "deployments must be scoped to an environment before runtime side effects begin",
		}, false
	}
	if request.Route != nil {
		if !request.Route.Public {
			return failurecodes.ResourceValidationFailed, "route selectors require explicit public route opt-in", map[string]any{"field": "route.public"}, false
		}
		path := strings.TrimSpace(request.Route.Path)
		if path != "" && !strings.HasPrefix(path, "/") {
			return failurecodes.ResourceValidationFailed, "route path must start with /", map[string]any{"field": "route.path"}, false
		}
		if request.Route.TLS && !request.Route.Public {
			return failurecodes.ResourceValidationFailed, "TLS route requests require explicit public route opt-in", map[string]any{"field": "route.public"}, false
		}
	}
	return "", "", nil, true
}

func resolveManagedDeployRouteSelector(
	ctx context.Context,
	store router.DomainWorkflowStore,
	scope router.RouteScope,
	selector DeployRouteIntent,
) (DeployRouteIntent, error) {
	managed, ok := store.(router.ManagedServiceRouteStore)
	if !ok || managed == nil {
		return DeployRouteIntent{}, errors.New("managed route selector authority is unavailable")
	}
	value, err := managed.SelectManagedServiceRoute(ctx, scope, router.ManagedRouteSelector{
		Host: selector.Host, Name: selector.Name, Group: selector.Group,
		PathPrefix: selector.Path, TLSDesired: selector.TLS,
	})
	if err != nil {
		return DeployRouteIntent{}, err
	}
	group := strings.TrimSpace(selector.Group)
	host := ""
	if strings.TrimSpace(selector.Host) != "" {
		host = value.Domain.NormalizedHostname
	}
	return DeployRouteIntent{
		Name: value.Route.Name, Group: group,
		Host: host, Path: value.Route.PathPrefix,
		Public: true, TLS: value.Domain.TLSDesired,
	}, nil
}

func writeDeployIntakeTerminalError(w http.ResponseWriter, r *http.Request, idem idempotency.Store, ref idempotency.Reference, status int, code failurecodes.Code, message string, details map[string]any) {
	body, err := marshalErrorResponse(status, code, message, middleware.FromContext(r.Context()), details)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "error response encode failed", details)
		return
	}
	stored := idempotency.StoredResponse{
		StatusCode: responseStatus(body, status),
		Header:     http.Header{"Content-Type": []string{"application/problem+json"}},
		Body:       body,
	}
	if ref.Key != "" {
		if _, err := idem.CompleteFailure(r.Context(), ref, stored); err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency failure completion failed", details)
			return
		}
	}
	writeStoredResponse(w, stored)
}

func writeStoredResponse(w http.ResponseWriter, stored idempotency.StoredResponse) {
	for key, values := range stored.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(stored.StatusCode)
	_, _ = w.Write(stored.Body)
}

// -- "Deploy now" ------------------------------------------------------------
//
// A service that was just linked to a source, or that has run an image before,
// used to have no way to start a deployment from the console: the deployment
// actions (redeploy/rollback/restart) all need an existing deployment id, and
// linking a source deliberately starts nothing. `{"deploy_now": true}` closes
// that gap by deriving the intent from durable state and then re-entering this
// same intake, so RBAC, idempotency, the operation record, the canonical
// deployment row, and the queue payload are identical to an explicit request.

// ServiceSourceLink is the canonical source binding "Deploy now" builds from
// when a service has no usable image history.
type ServiceSourceLink struct {
	// Provider is the source integration ("git" for a plain public checkout).
	Provider string
	// Repo is the clone URL the build worker fetches.
	Repo string
	// Ref is the branch, tag, or commit to check out.
	Ref string
	// RootDirectory narrows a monorepo build context.
	RootDirectory string
	// ConfigPath is the optional repository-relative platform config file.
	ConfigPath string
}

// ServiceSourceLinkLookup exposes canonical source bindings that do not live
// in service.spec (the tenant `service_github_links` table). Server wiring may
// install one; when it is absent "Deploy now" falls back to the spec link and
// to deployment history, which is what an operator-linked service has.
type ServiceSourceLinkLookup interface {
	LookupServiceSourceLink(ctx context.Context, projectID, serviceID string) (ServiceSourceLink, bool, error)
}

var (
	serviceSourceLinkLookupMu    sync.RWMutex
	serviceSourceLinkLookupValue ServiceSourceLinkLookup
)

// SetServiceSourceLinkLookup installs (or clears with nil) the canonical
// source-link lookup used by "Deploy now". It returns a restore function.
func SetServiceSourceLinkLookup(lookup ServiceSourceLinkLookup) func() {
	serviceSourceLinkLookupMu.Lock()
	previous := serviceSourceLinkLookupValue
	serviceSourceLinkLookupValue = lookup
	serviceSourceLinkLookupMu.Unlock()
	return func() {
		serviceSourceLinkLookupMu.Lock()
		serviceSourceLinkLookupValue = previous
		serviceSourceLinkLookupMu.Unlock()
	}
}

func serviceSourceLinkLookup() ServiceSourceLinkLookup {
	serviceSourceLinkLookupMu.RLock()
	defer serviceSourceLinkLookupMu.RUnlock()
	return serviceSourceLinkLookupValue
}

// resolveDeployNowIntent rewrites a `deploy_now` request into a concrete
// source or image intent. Resolution order is deliberate:
//
//  1. the service's canonical source link — the service's declared truth,
//  2. the newest source-backed deployment for the environment,
//  3. the newest digest-pinned image the service has run in the environment.
//
// It never invents a route, an environment, or an image the service has not
// been told about.
func resolveDeployNowIntent(
	ctx context.Context,
	service Service,
	deployments DeploymentStore,
	request *DeployIntakeRequest,
) *deployIntakeError {
	if request == nil || !request.DeployNow {
		return nil
	}
	request.DeployNow = false
	if strings.TrimSpace(request.Image) != "" ||
		request.Source != nil ||
		strings.TrimSpace(request.SourceRef) != "" {
		return &deployIntakeError{
			Status:  http.StatusBadRequest,
			Code:    failurecodes.ResourceValidationFailed,
			Message: "deploy_now must not be combined with image or source",
			Details: map[string]any{
				"field":  "deploy_now",
				"reason": "deploy_now derives the intent from durable service state; pass image or source instead to state it explicitly",
			},
		}
	}
	if strings.TrimSpace(request.EnvironmentID) == "" {
		return &deployIntakeError{
			Status:  http.StatusBadRequest,
			Code:    failurecodes.ResourceValidationFailed,
			Message: "deploy_now requires environment_id",
			Details: map[string]any{"field": "environment_id"},
		}
	}

	if source, found, err := deployNowSource(ctx, service, deployments, request.EnvironmentID); err != nil {
		return &deployIntakeError{
			Status:  http.StatusInternalServerError,
			Code:    failurecodes.InternalError,
			Message: "deploy_now source lookup failed",
			Details: map[string]any{"service_id": string(service.ID)},
		}
	} else if found {
		request.Source = &source
		return nil
	}

	image, found, err := deployNowImage(ctx, deployments, service, request.EnvironmentID)
	if err != nil {
		return &deployIntakeError{
			Status:  http.StatusInternalServerError,
			Code:    failurecodes.InternalError,
			Message: "deploy_now deployment history lookup failed",
			Details: map[string]any{"service_id": string(service.ID)},
		}
	}
	if !found {
		return &deployIntakeError{
			Status:  http.StatusBadRequest,
			Code:    failurecodes.ResourceValidationFailed,
			Message: "service has nothing to deploy yet",
			Details: map[string]any{
				"field":           "deploy_now",
				"service_id":      string(service.ID),
				"environment_id":  strings.TrimSpace(request.EnvironmentID),
				"reason":          "the service has no linked source and has never run an image in this environment",
				"required_action": "link a source or deploy an image reference once; Deploy now can repeat it afterwards",
			},
		}
	}
	request.Image = image
	return nil
}

// deployNowSource returns the source intent "Deploy now" should build.
func deployNowSource(
	ctx context.Context,
	service Service,
	deployments DeploymentStore,
	environmentID string,
) (DeploySourceIntent, bool, error) {
	if lookup := serviceSourceLinkLookup(); lookup != nil {
		link, found, err := lookup.LookupServiceSourceLink(ctx, string(service.ProjectID), string(service.ID))
		if err != nil {
			return DeploySourceIntent{}, false, err
		}
		if found {
			if intent, ok := deploySourceIntentFromLink(link.Repo, link.Ref, link.RootDirectory, link.ConfigPath); ok {
				return intent, true, nil
			}
		}
	}
	if link, ok := readGitHubLink(service); ok {
		repo := strings.TrimSpace(link.CloneURL)
		if repo == "" && strings.TrimSpace(link.Repo) != "" {
			repo = "https://github.com/" + strings.TrimSpace(link.Repo) + ".git"
		}
		if intent, ok := deploySourceIntentFromLink(repo, link.Branch, link.RootDirectory, link.ConfigPath); ok {
			return intent, true, nil
		}
	}
	record, found, err := latestDeploymentRecord(ctx, deployments, service, environmentID, func(record DeploymentRecord) bool {
		return strings.TrimSpace(record.Repo) != "" && strings.TrimSpace(record.Ref) != ""
	})
	if err != nil || !found {
		return DeploySourceIntent{}, false, err
	}
	intent, ok := deploySourceIntentFromLink(record.Repo, record.Ref, record.RootDirectory, "")
	return intent, ok, nil
}

// deploySourceIntentFromLink builds a generic-git source intent. Provider
// identity is deliberately not forwarded: generic intake mints no GitHub App
// credential, so a link that only works with one is reported as "no source"
// rather than queued as a build that cannot authenticate.
func deploySourceIntentFromLink(repo, ref, rootDirectory, configPath string) (DeploySourceIntent, bool) {
	repo = strings.TrimSpace(repo)
	ref = strings.TrimSpace(ref)
	if repo == "" || ref == "" {
		return DeploySourceIntent{}, false
	}
	if err := ValidateGenericGitSourceRepo(repo); err != nil {
		return DeploySourceIntent{}, false
	}
	return DeploySourceIntent{
		Provider:      "git",
		Repo:          repo,
		Ref:           ref,
		RootDirectory: strings.TrimSpace(rootDirectory),
		ConfigPath:    strings.TrimSpace(configPath),
	}, true
}

// deployNowImage returns the newest immutable image the service has run in
// this environment.
func deployNowImage(
	ctx context.Context,
	deployments DeploymentStore,
	service Service,
	environmentID string,
) (string, bool, error) {
	record, found, err := latestDeploymentRecord(ctx, deployments, service, environmentID, func(record DeploymentRecord) bool {
		parsed, parseErr := registry.ParseImageReference(strings.TrimSpace(record.ImageRef))
		return parseErr == nil && parsed.Digest != ""
	})
	if err != nil || !found {
		return "", false, err
	}
	return strings.TrimSpace(record.ImageRef), true, nil
}

// latestDeploymentRecord returns the newest matching deployment for a service
// in one environment. The currently active record wins over a newer failed
// attempt so "Deploy now" repeats what the service actually ran.
func latestDeploymentRecord(
	ctx context.Context,
	deployments DeploymentStore,
	service Service,
	environmentID string,
	match func(DeploymentRecord) bool,
) (DeploymentRecord, bool, error) {
	if deployments == nil {
		return DeploymentRecord{}, false, nil
	}
	records, err := deployments.ListDeploymentsByService(ctx, string(service.ID))
	if err != nil {
		return DeploymentRecord{}, false, err
	}
	environmentID = strings.TrimSpace(environmentID)
	var best DeploymentRecord
	found := false
	for _, record := range records {
		if strings.TrimSpace(record.EnvironmentID) != environmentID || !match(record) {
			continue
		}
		if !found || betterDeployNowCandidate(record, best) {
			best, found = record, true
		}
	}
	return best, found, nil
}

// betterDeployNowCandidate prefers the active record, then the most recent one.
func betterDeployNowCandidate(candidate, current DeploymentRecord) bool {
	candidateActive := candidate.Active && candidate.State == deploy.DeploymentActive
	currentActive := current.Active && current.State == deploy.DeploymentActive
	if candidateActive != currentActive {
		return candidateActive
	}
	if !candidate.CreatedAt.Equal(current.CreatedAt) {
		return candidate.CreatedAt.After(current.CreatedAt)
	}
	return candidate.ID > current.ID
}
