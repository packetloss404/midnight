// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/varrefs"
)

const resourceCollectionVariables = "/variables"

// CreateVariableRequest is the JSON body accepted by POST /api/v1alpha/variables.
// The response is always metadata-only; no API route returns plaintext.
type CreateVariableRequest struct {
	Name          string        `json:"name"`
	WorkspaceID   string        `json:"workspace_id"`
	ProjectID     string        `json:"project_id"`
	EnvironmentID string        `json:"environment_id"`
	ServiceID     string        `json:"service_id"`
	Scope         secrets.Scope `json:"scope"`
	Value         string        `json:"value"`
	// ExpectedVersion is required when an existing canonical variable is
	// replaced. Omit it only when creating a new name.
	ExpectedVersion *int `json:"expected_version,omitempty"`
}

// UpdateVariableRequest is the write-only PATCH body. Identity is taken from
// the route; ownership is repeated and must match exactly so a stale UI cannot
// move a variable across scopes.
type UpdateVariableRequest struct {
	WorkspaceID     string        `json:"workspace_id"`
	ProjectID       string        `json:"project_id"`
	EnvironmentID   string        `json:"environment_id"`
	ServiceID       string        `json:"service_id"`
	Scope           secrets.Scope `json:"scope"`
	Value           string        `json:"value"`
	ExpectedVersion int           `json:"expected_version"`
}

// DeleteVariableRequest carries the exact ownership and version precondition.
// The value is never accepted or returned by delete.
type DeleteVariableRequest struct {
	WorkspaceID     string `json:"workspace_id"`
	ProjectID       string `json:"project_id"`
	EnvironmentID   string `json:"environment_id"`
	ServiceID       string `json:"service_id"`
	ExpectedVersion int    `json:"expected_version"`
}

func variablesHandler(store *secrets.Vault, idem idempotency.Store, emitter *auditEmitter, scopeResolvers ...MutationScopeResolver) http.HandlerFunc {
	scopeResolver := firstMutationScopeResolver(scopeResolvers)
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, v1alphaPrefix)
		if path == resourceCollectionVariables {
			switch r.Method {
			case http.MethodPost:
				createVariable(w, r, store, idem, emitter, scopeResolver)
				return
			case http.MethodGet:
				metas, err := store.List(r.Context())
				if err != nil {
					writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "variable list failed", nil)
					return
				}
				metas = filterVariableMetas(metas, r.URL.Query().Get("project_id"), r.URL.Query().Get("environment_id"), r.URL.Query().Get("service_id"))
				WriteJSON(w, http.StatusOK, metas)
				return
			default:
				w.Header().Set("Allow", "GET, POST")
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
				return
			}
		}
		id := strings.TrimPrefix(path, resourceCollectionVariables+"/")
		if id == "" || strings.Contains(id, "/") {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "variable not found", nil)
			return
		}
		switch r.Method {
		case http.MethodGet:
			variable, err := store.Get(r.Context(), secrets.VariableID(id))
			if errors.Is(err, secrets.ErrVariableNotFound) {
				writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "variable not found", map[string]any{"variable_id": id})
				return
			}
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "variable lookup failed", map[string]any{"variable_id": id})
				return
			}
			w.Header().Set("ETag", variableETag(variable.Meta().Version))
			WriteJSON(w, http.StatusOK, variable.Meta())
		case http.MethodPatch:
			updateVariable(w, r, store, idem, emitter, scopeResolver, secrets.VariableID(id))
		case http.MethodDelete:
			deleteVariable(w, r, store, idem, emitter, scopeResolver, secrets.VariableID(id))
		default:
			w.Header().Set("Allow", "GET, PATCH, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"variable_id": id})
		}
	}
}

func createVariable(w http.ResponseWriter, r *http.Request, store *secrets.Vault, idem idempotency.Store, emitter *auditEmitter, scopeResolvers ...MutationScopeResolver) {
	body, ok := readBoundedJSONRequestBody(w, r, "invalid variable request body")
	if !ok {
		return
	}
	reservation, ok := reserveIdempotencyOrAbort(w, r, idem, body)
	if !ok {
		return
	}
	idempotentResponse := newIdempotencyResponseCapture(w)
	w = idempotentResponse
	defer idempotentResponse.Complete(r, idem, reservation.Reference())
	ref := reservation.Reference()

	var request CreateVariableRequest
	if !decodeOptionalStrictJSONRequestBytes(w, r, body, &request, "invalid variable request body") {
		return
	}
	if err := validateCreateVariableRequest(request); err != nil {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
		return
	}
	if scopeResolver := firstMutationScopeResolver(scopeResolvers); scopeResolver != nil {
		resolved, err := scopeResolver.ResolveMutationScope(r.Context(), MutationScopeRequest{
			ProjectID:     request.ProjectID,
			ServiceID:     request.ServiceID,
			EnvironmentID: request.EnvironmentID,
		})
		if err != nil {
			status, code, message, details := mutationScopeHTTPError(err)
			writeDeployIntakeTerminalError(w, r, idem, ref, status, code, message, details)
			return
		}
		request.ProjectID = resolved.ProjectID
	}
	valueKind, refs, parseErr := parseVariableValue(request.Value)
	if parseErr != nil {
		writeVariableParseError(w, r, idem, ref, parseErr)
		return
	}
	input := secrets.VariableInput{
		Name:        strings.TrimSpace(request.Name),
		Workspace:   strings.TrimSpace(request.WorkspaceID),
		Project:     strings.TrimSpace(request.ProjectID),
		Environment: strings.TrimSpace(request.EnvironmentID),
		Service:     strings.TrimSpace(request.ServiceID),
		Scope:       request.Scope,
		Plaintext:   []byte(request.Value),
		ValueKind:   valueKind,
		References:  refs,
	}
	var variable *secrets.Variable
	var err error
	if request.ExpectedVersion == nil {
		variable, err = store.Create(r.Context(), input)
	} else {
		existing, findErr := store.Find(r.Context(), input.Project, input.Environment, input.Service, input.Name)
		if errors.Is(findErr, secrets.ErrVariableNotFound) {
			writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusNotFound, failurecodes.ResourceNotFound, "variable not found", map[string]any{
				"name":             input.Name,
				"project_id":       input.Project,
				"environment_id":   input.Environment,
				"service_id":       input.Service,
				"expected_version": *request.ExpectedVersion,
			})
			return
		}
		if findErr != nil {
			writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "variable lookup failed", nil)
			return
		}
		variable, err = store.Update(r.Context(), existing.Meta().ID, *request.ExpectedVersion, input)
	}
	if errors.Is(err, secrets.ErrInvalidScope) {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid variable scope", map[string]any{"scope": request.Scope})
		return
	}
	if errors.Is(err, secrets.ErrInvalidVariableName) {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid variable name", map[string]any{"field": "name"})
		return
	}
	if errors.Is(err, secrets.ErrVariableScopeIntegrity) {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable ownership scope is invalid", map[string]any{
			"project_id":     request.ProjectID,
			"environment_id": request.EnvironmentID,
			"service_id":     request.ServiceID,
		})
		return
	}
	if errors.Is(err, secrets.ErrVariableVersionConflict) {
		writeVariableConflict(w, r, idem, ref, err)
		return
	}
	if err != nil {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "variable create failed", nil)
		return
	}

	meta := variable.Meta()
	responseBody, err := json.Marshal(meta)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "variable response encode failed", map[string]any{"variable_id": meta.ID})
		return
	}
	responseBody = append(responseBody, '\n')
	location := v1alphaPrefix + resourceCollectionVariables + "/" + string(meta.ID)
	status := http.StatusCreated
	action := "variable.create"
	if request.ExpectedVersion != nil {
		status = http.StatusOK
		action = "variable.update"
	}
	if ref.Key != "" {
		if _, err := idem.CompleteSuccess(r.Context(), ref, idempotency.StoredResponse{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "Location": []string{location}, "ETag": []string{variableETag(meta.Version)}},
			Body:       responseBody,
		}); err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency completion failed", map[string]any{"variable_id": meta.ID})
			return
		}
	}
	emitter.emit(r.Context(), audit.Row{
		ResourceKind: "variable",
		ResourceID:   string(meta.ID),
		ProjectID:    meta.Project,
		Action:       action,
		After: map[string]any{
			"name":           meta.Name,
			"scope":          meta.Scope,
			"workspace_id":   meta.Workspace,
			"project_id":     meta.Project,
			"environment_id": meta.Environment,
			"service_id":     meta.Service,
			"length_bucket":  meta.LengthBucket,
		},
	})
	writeStoredResponse(w, idempotency.StoredResponse{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "Location": []string{location}, "ETag": []string{variableETag(meta.Version)}},
		Body:       responseBody,
	})
}

func validateCreateVariableRequest(request CreateVariableRequest) error {
	if !secrets.ValidVariableName(request.Name) {
		return errors.New("variable name is invalid")
	}
	if request.ExpectedVersion != nil && *request.ExpectedVersion < 1 {
		return errors.New("expected_version must be at least 1 when updating")
	}
	switch request.Scope {
	case secrets.ScopePlain, secrets.ScopeSecret, secrets.ScopeSealed:
		return nil
	default:
		return errors.New("variable scope must be one of plain, secret, sealed")
	}
}

func updateVariable(
	w http.ResponseWriter,
	r *http.Request,
	store *secrets.Vault,
	idem idempotency.Store,
	emitter *auditEmitter,
	scopeResolver MutationScopeResolver,
	id secrets.VariableID,
) {
	body, ok := readBoundedJSONRequestBody(w, r, "invalid variable update body")
	if !ok {
		return
	}
	var request UpdateVariableRequest
	if !decodeStrictJSONRequestBytes(w, r, body, &request, "invalid variable update body") {
		return
	}
	if request.ExpectedVersion < 1 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "expected_version must be at least 1", map[string]any{"field": "expected_version"})
		return
	}
	if !validVariableScope(request.Scope) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid variable scope", map[string]any{"scope": request.Scope})
		return
	}
	ifMatch, ok := requireIfMatchVersion(w, r)
	if !ok {
		return
	}
	if !expectedVersionMatchesIfMatch(w, r, int64(request.ExpectedVersion), ifMatch) {
		return
	}
	reservation, ok := reserveIdempotencyOrAbort(w, r, idem, body)
	if !ok {
		return
	}
	idempotentResponse := newIdempotencyResponseCapture(w)
	w = idempotentResponse
	defer idempotentResponse.Complete(r, idem, reservation.Reference())
	ref := reservation.Reference()
	current, err := store.Get(r.Context(), id)
	if errors.Is(err, secrets.ErrVariableNotFound) {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusNotFound, failurecodes.ResourceNotFound, "variable not found", map[string]any{"variable_id": id})
		return
	}
	if err != nil {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "variable lookup failed", map[string]any{"variable_id": id})
		return
	}
	currentMeta := current.Meta()
	if !variableOwnershipMatches(currentMeta, request.WorkspaceID, request.ProjectID, request.EnvironmentID, request.ServiceID) {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable ownership scope is invalid", variableOwnershipDetails(request.ProjectID, request.EnvironmentID, request.ServiceID))
		return
	}
	if scopeResolver != nil {
		resolved, resolveErr := scopeResolver.ResolveMutationScope(r.Context(), MutationScopeRequest{
			ProjectID:     request.ProjectID,
			ServiceID:     request.ServiceID,
			EnvironmentID: request.EnvironmentID,
		})
		if resolveErr != nil {
			status, code, message, details := mutationScopeHTTPError(resolveErr)
			writeDeployIntakeTerminalError(w, r, idem, ref, status, code, message, details)
			return
		}
		if resolved.ProjectID != currentMeta.Project {
			writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable ownership scope is invalid", variableOwnershipDetails(request.ProjectID, request.EnvironmentID, request.ServiceID))
			return
		}
	}
	valueKind, refs, parseErr := parseVariableValue(request.Value)
	if parseErr != nil {
		writeVariableParseError(w, r, idem, ref, parseErr)
		return
	}
	updated, err := store.Update(r.Context(), id, request.ExpectedVersion, secrets.VariableInput{
		Name:        currentMeta.Name,
		Workspace:   strings.TrimSpace(request.WorkspaceID),
		Project:     strings.TrimSpace(request.ProjectID),
		Environment: strings.TrimSpace(request.EnvironmentID),
		Service:     strings.TrimSpace(request.ServiceID),
		Scope:       request.Scope,
		Plaintext:   []byte(request.Value),
		ValueKind:   valueKind,
		References:  refs,
	})
	if errors.Is(err, secrets.ErrVariableVersionConflict) {
		writeVariableConflict(w, r, idem, ref, err)
		return
	}
	if errors.Is(err, secrets.ErrVariableScopeIntegrity) {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable ownership scope is invalid", variableOwnershipDetails(request.ProjectID, request.EnvironmentID, request.ServiceID))
		return
	}
	if err != nil {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "variable update failed", map[string]any{"variable_id": id})
		return
	}
	meta := updated.Meta()
	responseBody, err := json.Marshal(meta)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "variable response encode failed", map[string]any{"variable_id": id})
		return
	}
	responseBody = append(responseBody, '\n')
	response := idempotency.StoredResponse{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "ETag": []string{variableETag(meta.Version)}},
		Body:       responseBody,
	}
	if ref.Key != "" {
		if _, err := idem.CompleteSuccess(r.Context(), ref, response); err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency completion failed", map[string]any{"variable_id": id})
			return
		}
	}
	emitter.emit(r.Context(), audit.Row{
		ResourceKind: "variable",
		ResourceID:   string(id),
		ProjectID:    meta.Project,
		Action:       "variable.update",
		Before:       variableAuditMeta(currentMeta),
		After:        variableAuditMeta(meta),
	})
	writeStoredResponse(w, response)
}

func deleteVariable(
	w http.ResponseWriter,
	r *http.Request,
	store *secrets.Vault,
	idem idempotency.Store,
	emitter *auditEmitter,
	scopeResolver MutationScopeResolver,
	id secrets.VariableID,
) {
	body, ok := readBoundedJSONRequestBody(w, r, "invalid variable delete body")
	if !ok {
		return
	}
	var request DeleteVariableRequest
	if !decodeStrictJSONRequestBytes(w, r, body, &request, "invalid variable delete body") {
		return
	}
	if request.ExpectedVersion < 1 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "expected_version must be at least 1", map[string]any{"field": "expected_version"})
		return
	}
	ifMatch, ok := requireIfMatchVersion(w, r)
	if !ok {
		return
	}
	if !expectedVersionMatchesIfMatch(w, r, int64(request.ExpectedVersion), ifMatch) {
		return
	}
	reservation, ok := reserveIdempotencyOrAbort(w, r, idem, body)
	if !ok {
		return
	}
	idempotentResponse := newIdempotencyResponseCapture(w)
	w = idempotentResponse
	defer idempotentResponse.Complete(r, idem, reservation.Reference())
	ref := reservation.Reference()
	current, err := store.Get(r.Context(), id)
	if errors.Is(err, secrets.ErrVariableNotFound) {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusNotFound, failurecodes.ResourceNotFound, "variable not found", map[string]any{"variable_id": id})
		return
	}
	if err != nil {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "variable lookup failed", map[string]any{"variable_id": id})
		return
	}
	meta := current.Meta()
	if !variableOwnershipMatches(meta, request.WorkspaceID, request.ProjectID, request.EnvironmentID, request.ServiceID) {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable ownership scope is invalid", variableOwnershipDetails(request.ProjectID, request.EnvironmentID, request.ServiceID))
		return
	}
	if scopeResolver != nil {
		resolved, resolveErr := scopeResolver.ResolveMutationScope(r.Context(), MutationScopeRequest{
			ProjectID:     request.ProjectID,
			ServiceID:     request.ServiceID,
			EnvironmentID: request.EnvironmentID,
		})
		if resolveErr != nil {
			status, code, message, details := mutationScopeHTTPError(resolveErr)
			writeDeployIntakeTerminalError(w, r, idem, ref, status, code, message, details)
			return
		}
		if resolved.ProjectID != meta.Project {
			writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable ownership scope is invalid", variableOwnershipDetails(request.ProjectID, request.EnvironmentID, request.ServiceID))
			return
		}
	}
	if err := store.Delete(r.Context(), id, request.ExpectedVersion); errors.Is(err, secrets.ErrVariableVersionConflict) {
		writeVariableConflict(w, r, idem, ref, err)
		return
	} else if errors.Is(err, secrets.ErrVariableNotFound) {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusNotFound, failurecodes.ResourceNotFound, "variable not found", map[string]any{"variable_id": id})
		return
	} else if err != nil {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusInternalServerError, failurecodes.InternalError, "variable delete failed", map[string]any{"variable_id": id})
		return
	}
	response := idempotency.StoredResponse{StatusCode: http.StatusNoContent, Header: http.Header{}}
	if ref.Key != "" {
		if _, err := idem.CompleteSuccess(r.Context(), ref, response); err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency completion failed", map[string]any{"variable_id": id})
			return
		}
	}
	emitter.emit(r.Context(), audit.Row{
		ResourceKind: "variable",
		ResourceID:   string(id),
		ProjectID:    meta.Project,
		Action:       "variable.delete",
		Before:       variableAuditMeta(meta),
		After: map[string]any{
			"deleted": true,
			"version": meta.Version + 1,
		},
	})
	writeStoredResponse(w, response)
}

func parseVariableValue(value string) (string, []secrets.VariableReference, error) {
	parsedTemplate, err := varrefs.Parse(value)
	if err != nil {
		return "", nil, err
	}
	if len(parsedTemplate.Refs) == 0 {
		return "literal", nil, nil
	}
	refs := make([]secrets.VariableReference, 0, len(parsedTemplate.Refs))
	for _, ref := range parsedTemplate.Refs {
		refs = append(refs, secrets.VariableReference{
			Kind:    string(ref.Kind),
			Service: ref.Service,
			Name:    ref.Name,
		})
	}
	return "reference", refs, nil
}

func writeVariableParseError(w http.ResponseWriter, r *http.Request, idem idempotency.Store, ref idempotency.Reference, err error) {
	var syn varrefs.ErrInvalidSyntax
	if errors.As(err, &syn) {
		writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable reference syntax is invalid", map[string]any{
			"field":  "value",
			"reason": "variable.references.invalid",
			"pos":    syn.Pos,
		})
		return
	}
	writeDeployIntakeTerminalError(w, r, idem, ref, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "variable reference syntax is invalid", map[string]any{"field": "value", "reason": "variable.references.invalid"})
}

func writeVariableConflict(w http.ResponseWriter, r *http.Request, idem idempotency.Store, ref idempotency.Reference, err error) {
	details := map[string]any{}
	var conflict *secrets.VariableVersionConflict
	if errors.As(err, &conflict) {
		details["expected_version"] = conflict.Expected
		details["actual_version"] = conflict.Actual
	}
	status, code := http.StatusConflict, failurecodes.ResourceConflict
	if r.Method == http.MethodPatch || r.Method == http.MethodDelete {
		status, code = http.StatusPreconditionFailed, failurecodes.PreconditionFailed
	}
	writeDeployIntakeTerminalError(w, r, idem, ref, status, code, "variable version conflict", details)
}

func validVariableScope(scope secrets.Scope) bool {
	switch scope {
	case secrets.ScopePlain, secrets.ScopeSecret, secrets.ScopeSealed:
		return true
	default:
		return false
	}
}

func variableOwnershipMatches(meta secrets.VariableMeta, workspaceID, projectID, environmentID, serviceID string) bool {
	return meta.Workspace == strings.TrimSpace(workspaceID) &&
		meta.Project == strings.TrimSpace(projectID) &&
		meta.Environment == strings.TrimSpace(environmentID) &&
		meta.Service == strings.TrimSpace(serviceID)
}

func variableOwnershipDetails(projectID, environmentID, serviceID string) map[string]any {
	return map[string]any{
		"project_id":     strings.TrimSpace(projectID),
		"environment_id": strings.TrimSpace(environmentID),
		"service_id":     strings.TrimSpace(serviceID),
	}
}

func variableAuditMeta(meta secrets.VariableMeta) map[string]any {
	return map[string]any{
		"name":            meta.Name,
		"scope":           meta.Scope,
		"workspace_id":    meta.Workspace,
		"project_id":      meta.Project,
		"environment_id":  meta.Environment,
		"service_id":      meta.Service,
		"length_bucket":   meta.LengthBucket,
		"value_kind":      meta.ValueKind,
		"version":         meta.Version,
		"last_rotated_at": meta.LastRotatedAt,
	}
}

func variableETag(version int) string {
	return strongVersionETag(int64(version))
}

func filterVariableMetas(metas []secrets.VariableMeta, projectID, environmentID, serviceID string) []secrets.VariableMeta {
	projectID = strings.TrimSpace(projectID)
	environmentID = strings.TrimSpace(environmentID)
	serviceID = strings.TrimSpace(serviceID)
	if projectID == "" && environmentID == "" && serviceID == "" {
		return metas
	}
	out := make([]secrets.VariableMeta, 0, len(metas))
	for _, meta := range metas {
		if projectID != "" && meta.Project != projectID {
			continue
		}
		if environmentID != "" && meta.Environment != environmentID {
			continue
		}
		if serviceID != "" && meta.Service != serviceID {
			// Shared variables are meaningful only inside an explicitly
			// selected environment. A bare service_id filter must not collect
			// shared metadata from every environment.
			if meta.Service != "" || environmentID == "" {
				continue
			}
		}
		out = append(out, meta)
	}
	return out
}
