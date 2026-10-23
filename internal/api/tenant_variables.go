// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/secrets"
)

const tenantVariableBodyLimitBytes = 64 << 10
const tenantVariableNameLimitBytes = 255

var errTenantVariableSealedDowngrade = errors.New("sealed variables cannot be downgraded")
var tenantVariableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// TenantVariableReadRequest carries a currently verified human tuple and the
// exact service/environment route. Project and workspace authority are always
// derived inside the coordinator.
type TenantVariableReadRequest struct {
	Principal     identity.Principal
	Session       identity.Session
	ServiceID     string
	EnvironmentID string
	VariableID    secrets.VariableID
}

// TenantVariableMutationMeta is redaction-safe request evidence shared by the
// create, update, and delete coordinator paths.
type TenantVariableMutationMeta struct {
	Principal     identity.Principal
	Session       identity.Session
	ServiceID     string
	EnvironmentID string
	VariableID    secrets.VariableID
	Method        string
	Key           idempotency.Key
	RawBody       []byte
	RequestID     string
	SourceIP      string
	UserAgent     string
}

// TenantVariableCreateRequest contains the strict create-only human payload.
type TenantVariableCreateRequest struct {
	TenantVariableMutationMeta
	Name       string
	Scope      secrets.Scope
	Plaintext  []byte
	ValueKind  string
	References []secrets.VariableReference
}

// TenantVariableUpdateRequest rotates one exact variable with a positive CAS.
// A nil Scope retains the current privacy class.
type TenantVariableUpdateRequest struct {
	TenantVariableMutationMeta
	ExpectedVersion int
	Scope           *secrets.Scope
	Plaintext       []byte
	ValueKind       string
	References      []secrets.VariableReference
}

// TenantVariableDeleteRequest tombstones one exact active variable.
type TenantVariableDeleteRequest struct {
	TenantVariableMutationMeta
	ExpectedVersion int
}

// TenantVariableCoordinator independently admits only the exact human
// service/environment variable lifecycle. Installing it cannot authorize the
// legacy global operator routes or shared-variable mutation.
type TenantVariableCoordinator interface {
	ListVariables(context.Context, TenantVariableReadRequest) ([]secrets.VariableMeta, error)
	GetVariable(context.Context, TenantVariableReadRequest) (secrets.VariableMeta, error)
	CreateVariable(context.Context, TenantVariableCreateRequest) (TenantMutationResult, error)
	UpdateVariable(context.Context, TenantVariableUpdateRequest) (TenantMutationResult, error)
	DeleteVariable(context.Context, TenantVariableDeleteRequest) (TenantMutationResult, error)
}

type tenantVariableCreateBody struct {
	Name  string         `json:"name"`
	Scope *secrets.Scope `json:"scope"`
	Value *string        `json:"value"`
}

type tenantVariableUpdateBody struct {
	ExpectedVersion *int           `json:"expected_version"`
	Scope           *secrets.Scope `json:"scope,omitempty"`
	Value           *string        `json:"value"`
}

type tenantVariableDeleteBody struct {
	ExpectedVersion *int `json:"expected_version"`
}

func tenantVariablesHandler(coordinator TenantVariableCoordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, classified := productPrincipalFromContext(r.Context())
		if !classified || principal.Kind != productPrincipalHuman {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service variables not found", nil)
			return
		}
		serviceID := strings.TrimSpace(r.PathValue("service_id"))
		environmentID := strings.TrimSpace(r.PathValue("environment_id"))
		variableID := strings.TrimSpace(r.PathValue("variable_id"))
		if coordinator == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant variables are unavailable", nil)
			return
		}
		if serviceID == "" || environmentID == "" {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service or environment not found", nil)
			return
		}
		identityPrincipal, principalOK := identityPrincipalFromContext(r.Context())
		session, sessionOK := identitySessionFromContext(r.Context())
		if !principalOK || !sessionOK {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
			return
		}

		read := TenantVariableReadRequest{
			Principal:     identityPrincipal,
			Session:       session,
			ServiceID:     serviceID,
			EnvironmentID: environmentID,
			VariableID:    secrets.VariableID(variableID),
		}
		if variableID == "" {
			switch r.Method {
			case http.MethodGet:
				metas, err := coordinator.ListVariables(r.Context(), read)
				if writeTenantVariableError(w, r, err) {
					return
				}
				WriteJSON(w, http.StatusOK, metas)
			case http.MethodPost:
				createTenantVariable(w, r, coordinator, read)
			default:
				w.Header().Set("Allow", "GET, POST")
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			}
			return
		}

		switch r.Method {
		case http.MethodGet:
			meta, err := coordinator.GetVariable(r.Context(), read)
			if writeTenantVariableError(w, r, err) {
				return
			}
			w.Header().Set("ETag", variableETag(meta.Version))
			WriteJSON(w, http.StatusOK, meta)
		case http.MethodPatch:
			updateTenantVariable(w, r, coordinator, read)
		case http.MethodDelete:
			deleteTenantVariable(w, r, coordinator, read)
		default:
			w.Header().Set("Allow", "GET, PATCH, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func createTenantVariable(
	w http.ResponseWriter,
	r *http.Request,
	coordinator TenantVariableCoordinator,
	read TenantVariableReadRequest,
) {
	var body tenantVariableCreateBody
	raw, ok := readStrictTenantVariableBody(w, r, &body)
	if !ok {
		return
	}
	defer clear(raw)
	if !validTenantVariableName(body.Name) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable name must be a portable environment identifier no longer than 255 bytes", map[string]any{"field": "name"})
		return
	}
	if body.Scope == nil || !validVariableScope(*body.Scope) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable scope must be one of plain, secret, sealed", map[string]any{"field": "scope"})
		return
	}
	if body.Value == nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable value is required", map[string]any{"field": "value"})
		return
	}
	key, ok := requireTenantVariableIdempotencyKey(w, r)
	if !ok {
		return
	}
	valueKind, references, err := parseVariableValue(*body.Value)
	if err != nil {
		writeTenantVariableReferenceError(w, r, err)
		return
	}
	plaintext := []byte(*body.Value)
	defer clear(plaintext)
	result, err := coordinator.CreateVariable(r.Context(), TenantVariableCreateRequest{
		TenantVariableMutationMeta: tenantVariableMutationMeta(r, read, key, raw),
		Name:                       strings.TrimSpace(body.Name),
		Scope:                      *body.Scope,
		Plaintext:                  plaintext,
		ValueKind:                  valueKind,
		References:                 references,
	})
	if writeTenantVariableError(w, r, err) {
		return
	}
	writeTenantVariableMutationResult(w, result)
}

func updateTenantVariable(
	w http.ResponseWriter,
	r *http.Request,
	coordinator TenantVariableCoordinator,
	read TenantVariableReadRequest,
) {
	var body tenantVariableUpdateBody
	raw, ok := readStrictTenantVariableBody(w, r, &body)
	if !ok {
		return
	}
	defer clear(raw)
	if body.ExpectedVersion == nil || *body.ExpectedVersion < 1 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "expected_version is required and must be at least 1", map[string]any{"field": "expected_version"})
		return
	}
	if body.Scope != nil && !validVariableScope(*body.Scope) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable scope must be one of plain, secret, sealed", map[string]any{"field": "scope"})
		return
	}
	if body.Value == nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable value is required", map[string]any{"field": "value"})
		return
	}
	if !requireMatchingExpectedVersion(w, r, int64(*body.ExpectedVersion)) {
		return
	}
	key, ok := requireTenantVariableIdempotencyKey(w, r)
	if !ok {
		return
	}
	valueKind, references, err := parseVariableValue(*body.Value)
	if err != nil {
		writeTenantVariableReferenceError(w, r, err)
		return
	}
	plaintext := []byte(*body.Value)
	defer clear(plaintext)
	result, err := coordinator.UpdateVariable(r.Context(), TenantVariableUpdateRequest{
		TenantVariableMutationMeta: tenantVariableMutationMeta(r, read, key, raw),
		ExpectedVersion:            *body.ExpectedVersion,
		Scope:                      body.Scope,
		Plaintext:                  plaintext,
		ValueKind:                  valueKind,
		References:                 references,
	})
	if writeTenantVariableError(w, r, err) {
		return
	}
	writeTenantVariableMutationResult(w, result)
}

func deleteTenantVariable(
	w http.ResponseWriter,
	r *http.Request,
	coordinator TenantVariableCoordinator,
	read TenantVariableReadRequest,
) {
	var body tenantVariableDeleteBody
	raw, ok := readStrictTenantVariableBody(w, r, &body)
	if !ok {
		return
	}
	defer clear(raw)
	if body.ExpectedVersion == nil || *body.ExpectedVersion < 1 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "expected_version is required and must be at least 1", map[string]any{"field": "expected_version"})
		return
	}
	if !requireMatchingExpectedVersion(w, r, int64(*body.ExpectedVersion)) {
		return
	}
	key, ok := requireTenantVariableIdempotencyKey(w, r)
	if !ok {
		return
	}
	result, err := coordinator.DeleteVariable(r.Context(), TenantVariableDeleteRequest{
		TenantVariableMutationMeta: tenantVariableMutationMeta(r, read, key, raw),
		ExpectedVersion:            *body.ExpectedVersion,
	})
	if writeTenantVariableError(w, r, err) {
		return
	}
	writeTenantVariableMutationResult(w, result)
}

func tenantVariableMutationMeta(
	r *http.Request,
	read TenantVariableReadRequest,
	key idempotency.Key,
	raw []byte,
) TenantVariableMutationMeta {
	return TenantVariableMutationMeta{
		Principal:     read.Principal,
		Session:       read.Session,
		ServiceID:     read.ServiceID,
		EnvironmentID: read.EnvironmentID,
		VariableID:    read.VariableID,
		Method:        r.Method,
		Key:           key,
		RawBody:       append([]byte(nil), raw...),
		RequestID:     middleware.FromContext(r.Context()),
		SourceIP:      r.RemoteAddr,
		UserAgent:     r.UserAgent(),
	}
}

func requireTenantVariableIdempotencyKey(
	w http.ResponseWriter,
	r *http.Request,
) (idempotency.Key, bool) {
	key, failure := singletonIdempotencyKey(r.Header)
	switch failure {
	case "":
		return key, true
	case failurecodes.IdempotencyKeyRequired:
		writeRequestError(w, r, http.StatusBadRequest, failure, "exactly one Idempotency-Key header is required", nil)
	default:
		writeRequestError(w, r, http.StatusBadRequest, failure, "invalid idempotency key", nil)
	}
	return "", false
}

func readStrictTenantVariableBody(
	w http.ResponseWriter,
	r *http.Request,
	target any,
) ([]byte, bool) {
	limited := http.MaxBytesReader(w, r.Body, tenantVariableBodyLimitBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeRequestError(w, r, http.StatusRequestEntityTooLarge, failurecodes.ResourceValidationFailed, "variable request body exceeds the configured limit", map[string]any{"limit": tenantVariableBodyLimitBytes})
			return nil, false
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "request body read failed", nil)
		return nil, false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable request body must be one JSON object", nil)
		return nil, false
	}
	if err := rejectDuplicateTopLevelJSONFields(body); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid variable request body", nil)
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid variable request body", nil)
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable request body must contain exactly one JSON object", nil)
		return nil, false
	}
	return body, true
}

func rejectDuplicateTopLevelJSONFields(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return errors.New("JSON value is not an object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("JSON object key is not a string")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate JSON field %q", name)
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	token, err = decoder.Token()
	if err != nil {
		return err
	}
	delim, ok = token.(json.Delim)
	if !ok || delim != '}' {
		return errors.New("JSON object is not closed")
	}
	return nil
}

func writeTenantVariableReferenceError(w http.ResponseWriter, r *http.Request, err error) {
	writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable reference syntax is invalid", map[string]any{"field": "value", "reason": "variable.references.invalid"})
}

func validTenantVariableName(name string) bool {
	return len(name) > 0 &&
		len(name) <= tenantVariableNameLimitBytes &&
		tenantVariableNamePattern.MatchString(name)
}

func writeTenantVariableMutationResult(w http.ResponseWriter, result TenantMutationResult) {
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeStoredResponse(w, result.Response)
}

func writeTenantVariableError(w http.ResponseWriter, r *http.Request, err error) bool {
	var versionConflict *secrets.VariableVersionConflict
	switch {
	case err == nil:
		return false
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
	case errors.Is(err, identity.ErrAccessNotFound), errors.Is(err, secrets.ErrVariableNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service, environment, or variable not found", nil)
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to access variables", nil)
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with a different request", nil)
	case errors.Is(err, errTenantVariableSealedDowngrade):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, errTenantVariableSealedDowngrade.Error(), map[string]any{"field": "scope"})
	case errors.Is(err, idempotency.ErrInvalidKey),
		errors.Is(err, idempotency.ErrInvalidFingerprint),
		errors.Is(err, secrets.ErrInvalidScope),
		errors.Is(err, secrets.ErrInvalidVariableName):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
	case errors.As(err, &versionConflict):
		writeIfMatchFailed(w, r, "variable version changed", map[string]any{"expected_version": versionConflict.Expected, "current_version": versionConflict.Actual})
	default:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant variables are unavailable", nil)
	}
	return true
}
