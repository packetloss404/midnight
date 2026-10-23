// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/internal/volume"
)

const (
	environmentChangesetBodyLimit = 256 << 10
	environmentChangesetMaxItems  = 100
)

var (
	errEnvironmentChangesetNotFound = errors.New("environment changeset not found")
	errEnvironmentChangesetConflict = errors.New("environment changeset revision conflict")
	errEnvironmentChangesetInvalid  = errors.New("environment changeset invalid")
)

type EnvironmentChangesetCapability struct {
	Kind      string `json:"kind"`
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}

type EnvironmentChangesetItem struct {
	ID              string                     `json:"id"`
	Kind            string                     `json:"kind"`
	Operation       string                     `json:"operation"`
	ServiceID       string                     `json:"service_id"`
	TargetID        string                     `json:"target_id,omitempty"`
	Name            string                     `json:"name"`
	Scope           string                     `json:"scope,omitempty"`
	ExpectedVersion *int64                     `json:"expected_version,omitempty"`
	ValueChange     string                     `json:"value_change"`
	Runtime         *EnvironmentRuntimeSpec    `json:"runtime,omitempty"`
	Networking      *EnvironmentNetworkingSpec `json:"networking,omitempty"`
	Storage         *EnvironmentStorageSpec    `json:"storage,omitempty"`
}

type EnvironmentChangeset struct {
	ID              string                     `json:"id"`
	WorkspaceID     string                     `json:"workspace_id"`
	ProjectID       string                     `json:"project_id"`
	EnvironmentID   string                     `json:"environment_id"`
	CreatedBy       string                     `json:"created_by"`
	Note            string                     `json:"note,omitempty"`
	State           string                     `json:"state"`
	Revision        int64                      `json:"revision"`
	AppliedRevision *int64                     `json:"applied_revision,omitempty"`
	Items           []EnvironmentChangesetItem `json:"items"`
	CreatedAt       time.Time                  `json:"created_at"`
	UpdatedAt       time.Time                  `json:"updated_at"`
	AppliedAt       *time.Time                 `json:"applied_at,omitempty"`
	DiscardedAt     *time.Time                 `json:"discarded_at,omitempty"`
}

type EnvironmentAffectedResource struct {
	ServiceID        string   `json:"service_id"`
	ResourceKinds    []string `json:"resource_kinds"`
	RequiresDeploy   bool     `json:"requires_deploy"`
	DeploymentAction string   `json:"deployment_action"`
}

type EnvironmentChangesetValidation struct {
	Valid             bool                          `json:"valid"`
	AffectedServices  []string                      `json:"affected_services"`
	AffectedResources []EnvironmentAffectedResource `json:"affected_resources"`
	Errors            []string                      `json:"errors"`
	Warnings          []string                      `json:"warnings"`
}

type EnvironmentServiceConfigurationRevision struct {
	ServiceID            string `json:"service_id"`
	AppliedRevision      int64  `json:"applied_revision"`
	DeployedRevision     int64  `json:"deployed_revision"`
	DeploymentRequired   bool   `json:"deployment_required"`
	DeployedDeploymentID string `json:"deployed_deployment_id,omitempty"`
	PendingDeploymentID  string `json:"pending_deployment_id,omitempty"`
}

type EnvironmentConfigurationState struct {
	AppliedRevision int64                                     `json:"applied_revision"`
	Services        []EnvironmentServiceConfigurationRevision `json:"services"`
}

type EnvironmentOperationGroupItem struct {
	ServiceID        string   `json:"service_id"`
	ResourceKinds    []string `json:"resource_kinds"`
	RequiresDeploy   bool     `json:"requires_deploy"`
	DeploymentAction string   `json:"deployment_action"`
	IntakeState      string   `json:"intake_state"`
	DeploymentID     string   `json:"deployment_id,omitempty"`
	OperationID      string   `json:"operation_id,omitempty"`
	OperationState   string   `json:"operation_state,omitempty"`
	DeploymentState  string   `json:"deployment_state,omitempty"`
	FailureCode      string   `json:"failure_code,omitempty"`
	FailureReason    string   `json:"failure_reason,omitempty"`
}

type EnvironmentOperationGroup struct {
	ID                    string                          `json:"id"`
	EnvironmentID         string                          `json:"environment_id"`
	ChangesetID           string                          `json:"changeset_id"`
	ConfigurationRevision int64                           `json:"configuration_revision"`
	State                 string                          `json:"state"`
	Items                 []EnvironmentOperationGroupItem `json:"items"`
	CreatedAt             time.Time                       `json:"created_at"`
}

type EnvironmentApplyAndDeployResponse struct {
	AppliedRevision  int64                          `json:"applied_revision"`
	OperationGroupID string                         `json:"operation_group_id"`
	AffectedServices []string                       `json:"affected_services"`
	Changeset        *EnvironmentChangeset          `json:"changeset"`
	Validation       EnvironmentChangesetValidation `json:"validation"`
	OperationGroup   EnvironmentOperationGroup      `json:"operation_group"`
}

type EnvironmentChangesetView struct {
	Changeset          *EnvironmentChangeset            `json:"changeset"`
	Validation         EnvironmentChangesetValidation   `json:"validation"`
	Capabilities       []EnvironmentChangesetCapability `json:"capabilities"`
	ConfigurationState EnvironmentConfigurationState    `json:"configuration_state"`
}

type EnvironmentChangesetInput struct {
	Kind            string                     `json:"kind"`
	Operation       string                     `json:"operation"`
	ServiceID       string                     `json:"service_id"`
	TargetID        string                     `json:"target_id,omitempty"`
	Name            string                     `json:"name"`
	Scope           *secrets.Scope             `json:"scope,omitempty"`
	ExpectedVersion *int64                     `json:"expected_version,omitempty"`
	Value           *string                    `json:"value,omitempty"`
	Runtime         *EnvironmentRuntimeSpec    `json:"runtime,omitempty"`
	Networking      *EnvironmentNetworkingSpec `json:"networking,omitempty"`
	Storage         *EnvironmentStorageSpec    `json:"storage,omitempty"`
}

// EnvironmentRuntimeSpec is the complete redaction-safe desired runtime and
// build configuration for one service/environment. A changeset commits it
// through serviceconfig.Store.PutInTx; it never starts a deployment.
type EnvironmentRuntimeSpec struct {
	RootDirectory         string `json:"root_directory"`
	Builder               string `json:"builder"`
	DockerfilePath        string `json:"dockerfile_path"`
	BuildCommand          string `json:"build_command"`
	StartCommand          string `json:"start_command"`
	PredeployCommand      string `json:"predeploy_command"`
	TargetPort            int    `json:"target_port"`
	HealthKind            string `json:"health_kind"`
	HealthPath            string `json:"health_path"`
	HealthPort            int    `json:"health_port"`
	HealthTimeoutSeconds  int64  `json:"health_timeout_seconds"`
	HealthIntervalSeconds int64  `json:"health_interval_seconds"`
	RestartPolicy         string `json:"restart_policy"`
	RestartRetries        int    `json:"restart_retries"`
	Replicas              int    `json:"replicas"`
	OverlapEnabled        bool   `json:"overlap_enabled"`
	DrainGraceSeconds     int64  `json:"drain_grace_seconds"`
	RuntimeKind           string `json:"runtime_kind"`
	CronSchedule          string `json:"cron_schedule"`
	CronTimeoutSeconds    int64  `json:"cron_timeout_seconds"`
	CronMaxAttempts       int    `json:"cron_max_attempts"`
	CronRetryDelaySeconds int64  `json:"cron_retry_delay_seconds"`
}

// EnvironmentNetworkingSpec currently models the canonical generated public
// domain singleton. Custom hostnames retain their proof-of-control workflow.
type EnvironmentNetworkingSpec struct {
	Kind   string `json:"kind"`
	Public bool   `json:"public"`
}

// EnvironmentStorageSpec is durable volume intent. MountPath is immutable
// after creation; edit increases SizeBytes through the volume CAS seam.
type EnvironmentStorageSpec struct {
	SizeBytes int64  `json:"size_bytes"`
	MountPath string `json:"mount_path"`
}

type putEnvironmentChangesetBody struct {
	ExpectedRevision *int64                      `json:"expected_revision"`
	Note             string                      `json:"note,omitempty"`
	Changes          []EnvironmentChangesetInput `json:"changes"`
}

type transitionEnvironmentChangesetBody struct {
	ExpectedRevision *int64 `json:"expected_revision"`
}

type EnvironmentChangesetReadRequest struct {
	Principal     identity.Principal
	Session       identity.Session
	EnvironmentID string
}

type EnvironmentChangesetMutationRequest struct {
	EnvironmentChangesetReadRequest
	ExpectedRevision int64
	Note             string
	Changes          []EnvironmentChangesetInput
	Key              idempotency.Key
	RawBody          []byte
	Fingerprint      idempotency.RequestFingerprint
	Method           string
	Path             string
	RequestID        string
	SourceIP         string
	UserAgent        string
}

// EnvironmentChangesetCoordinator is intentionally an independently admitted
// human-session authority. Installing it does not widen legacy variable,
// networking, runtime, storage, deployment, or operator-recovery routes.
type EnvironmentChangesetCoordinator interface {
	GetEnvironmentChangeset(context.Context, EnvironmentChangesetReadRequest) (EnvironmentChangesetView, error)
	GetEnvironmentOperationGroup(context.Context, EnvironmentChangesetReadRequest, string) (EnvironmentOperationGroup, error)
	PutEnvironmentChangeset(context.Context, EnvironmentChangesetMutationRequest) (TenantMutationResult, error)
	ValidateEnvironmentChangeset(context.Context, EnvironmentChangesetReadRequest, int64) (EnvironmentChangesetView, error)
	ApplyEnvironmentChangeset(context.Context, EnvironmentChangesetMutationRequest) (TenantMutationResult, error)
	ApplyAndDeployEnvironmentChangeset(context.Context, EnvironmentChangesetMutationRequest) (TenantMutationResult, error)
	DiscardEnvironmentChangeset(context.Context, EnvironmentChangesetMutationRequest) (TenantMutationResult, error)
}

func environmentOperationGroupHandler(coordinator EnvironmentChangesetCoordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, classified := productPrincipalFromContext(r.Context())
		if !classified || principal.Kind != productPrincipalHuman {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "environment activity not found", nil)
			return
		}
		identityPrincipal, principalOK := identityPrincipalFromContext(r.Context())
		session, sessionOK := identitySessionFromContext(r.Context())
		if !principalOK || !sessionOK {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
			return
		}
		if coordinator == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "environment activity is unavailable", nil)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		group, err := coordinator.GetEnvironmentOperationGroup(r.Context(), EnvironmentChangesetReadRequest{Principal: identityPrincipal, Session: session, EnvironmentID: strings.TrimSpace(r.PathValue("environment_id"))}, strings.TrimSpace(r.PathValue("group_id")))
		if writeEnvironmentChangesetError(w, r, err) {
			return
		}
		WriteJSON(w, http.StatusOK, group)
	}
}

func environmentChangesetCapabilities() []EnvironmentChangesetCapability {
	return []EnvironmentChangesetCapability{
		{Kind: "variable", Supported: true},
		{Kind: "networking", Supported: true},
		{Kind: "runtime", Supported: true},
		{Kind: "storage", Supported: true},
	}
}

func environmentChangesetHandler(coordinator EnvironmentChangesetCoordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, classified := productPrincipalFromContext(r.Context())
		if !classified || principal.Kind != productPrincipalHuman {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "environment changeset not found", nil)
			return
		}
		identityPrincipal, principalOK := identityPrincipalFromContext(r.Context())
		session, sessionOK := identitySessionFromContext(r.Context())
		if !principalOK || !sessionOK {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
			return
		}
		if coordinator == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "durable environment changesets are unavailable", map[string]any{"capability": "environment-changesets"})
			return
		}
		environmentID := strings.TrimSpace(r.PathValue("environment_id"))
		read := EnvironmentChangesetReadRequest{Principal: identityPrincipal, Session: session, EnvironmentID: environmentID}
		action := strings.TrimSpace(r.PathValue("changeset_action"))
		if action == "" {
			for _, candidate := range []string{"validate", "apply-and-deploy", "apply", "discard"} {
				if strings.HasSuffix(r.URL.Path, "/changeset/"+candidate) {
					action = candidate
					break
				}
			}
		}

		if action == "" && r.Method == http.MethodGet {
			view, err := coordinator.GetEnvironmentChangeset(r.Context(), read)
			if writeEnvironmentChangesetError(w, r, err) {
				return
			}
			writeEnvironmentChangesetView(w, view)
			return
		}
		if action == "validate" && r.Method == http.MethodPost {
			var body transitionEnvironmentChangesetBody
			_, ok := readStrictEnvironmentChangesetBody(w, r, &body)
			if !ok {
				return
			}
			if body.ExpectedRevision == nil || *body.ExpectedRevision < 1 {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "expected_revision must be at least 1", map[string]any{"field": "expected_revision"})
				return
			}
			view, err := coordinator.ValidateEnvironmentChangeset(r.Context(), read, *body.ExpectedRevision)
			if writeEnvironmentChangesetError(w, r, err) {
				return
			}
			writeEnvironmentChangesetView(w, view)
			return
		}

		var expected int64
		var note string
		var changes []EnvironmentChangesetInput
		var raw []byte
		var ok bool
		if action == "" && r.Method == http.MethodPut {
			var body putEnvironmentChangesetBody
			raw, ok = readStrictEnvironmentChangesetBody(w, r, &body)
			if !ok {
				return
			}
			if body.ExpectedRevision == nil || *body.ExpectedRevision < 0 {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "expected_revision is required", map[string]any{"field": "expected_revision"})
				return
			}
			expected, note, changes = *body.ExpectedRevision, body.Note, body.Changes
		} else if (action == "apply" || action == "apply-and-deploy" || action == "discard") && r.Method == http.MethodPost {
			var body transitionEnvironmentChangesetBody
			raw, ok = readStrictEnvironmentChangesetBody(w, r, &body)
			if !ok {
				return
			}
			if body.ExpectedRevision == nil || *body.ExpectedRevision < 1 {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "expected_revision must be at least 1", map[string]any{"field": "expected_revision"})
				return
			}
			expected = *body.ExpectedRevision
		} else {
			allow := "GET, PUT"
			if action != "" {
				allow = "POST"
			}
			w.Header().Set("Allow", allow)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		defer clear(raw)
		if !requireMatchingExpectedVersion(w, r, expected) {
			return
		}
		key, keyOK := requireTenantVariableIdempotencyKey(w, r)
		if !keyOK {
			return
		}
		request := EnvironmentChangesetMutationRequest{
			EnvironmentChangesetReadRequest: read, ExpectedRevision: expected,
			Note: note, Changes: changes, Key: key, RawBody: raw,
			Method: r.Method, Path: r.URL.Path,
			RequestID: middleware.FromContext(r.Context()), SourceIP: r.RemoteAddr, UserAgent: r.UserAgent(),
		}
		var result TenantMutationResult
		var err error
		switch action {
		case "apply-and-deploy":
			result, err = coordinator.ApplyAndDeployEnvironmentChangeset(r.Context(), request)
		case "apply":
			result, err = coordinator.ApplyEnvironmentChangeset(r.Context(), request)
		case "discard":
			result, err = coordinator.DiscardEnvironmentChangeset(r.Context(), request)
		default:
			result, err = coordinator.PutEnvironmentChangeset(r.Context(), request)
		}
		if writeEnvironmentChangesetError(w, r, err) {
			return
		}
		if result.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		writeStoredResponse(w, result.Response)
	}
}

func readStrictEnvironmentChangesetBody(w http.ResponseWriter, r *http.Request, dst any) ([]byte, bool) {
	limited := http.MaxBytesReader(w, r.Body, environmentChangesetBodyLimit)
	raw, err := io.ReadAll(limited)
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "request body is invalid or too large", nil)
		return nil, false
	}
	if !decodeStrictJSONRequestBytes(w, r, raw, dst, "request body must be one strict JSON object") {
		clear(raw)
		return nil, false
	}
	return raw, true
}

func writeEnvironmentChangesetView(w http.ResponseWriter, view EnvironmentChangesetView) {
	version := int64(0)
	if view.Changeset != nil {
		version = view.Changeset.Revision
	}
	w.Header().Set("ETag", strongVersionETag(version))
	WriteJSON(w, http.StatusOK, view)
}

func writeEnvironmentChangesetError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
	case errors.Is(err, identity.ErrAccessNotFound), errors.Is(err, errEnvironmentChangesetNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "environment changeset not found", nil)
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to manage this environment changeset", nil)
	case errors.Is(err, errEnvironmentChangesetConflict), errors.Is(err, serviceconfig.ErrConflict), errors.Is(err, volume.ErrVolumeConflict), errors.Is(err, router.ErrDurableRouteRevisionMoved):
		writeIfMatchFailed(w, r, "environment changeset revision changed", nil)
	case errors.Is(err, errEnvironmentChangesetInvalid), errors.Is(err, secrets.ErrInvalidScope), errors.Is(err, secrets.ErrInvalidVariableName), errors.Is(err, serviceconfig.ErrInvalid), errors.Is(err, serviceconfig.ErrScope), errors.Is(err, volume.ErrVolumeInvalid), errors.Is(err, volume.ErrVolumeResizeDownUnsupported), errors.Is(err, volume.ErrVolumeAttached), errors.Is(err, router.ErrDurableRouteInvalid):
		writeRequestError(w, r, http.StatusUnprocessableEntity, failurecodes.ResourceValidationFailed, err.Error(), nil)
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
	default:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "environment changesets are unavailable", nil)
	}
	return true
}

func environmentChangesetStoredResponse(status int, view EnvironmentChangesetView) (idempotency.StoredResponse, error) {
	body, err := json.Marshal(view)
	if err != nil {
		return idempotency.StoredResponse{}, fmt.Errorf("environment changeset response encode: %w", err)
	}
	body = append(body, '\n')
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if view.Changeset != nil {
		headers.Set("ETag", strongVersionETag(view.Changeset.Revision))
	}
	return idempotency.StoredResponse{StatusCode: status, Header: headers, Body: body}, nil
}
