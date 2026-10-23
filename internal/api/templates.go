// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/templates"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const resourceCollectionTemplates = "/templates"

// TemplateDeployRequest is the JSON body accepted by
// POST /api/v1alpha/templates/{slug}/instances. The slug comes from
// the URL path; the body identifies the destination project/environment
// and (optionally) the desired display name for the materialized
// services.
type TemplateDeployRequest struct {
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id,omitempty"`
	Name          string `json:"name,omitempty"`
}

// TemplateDeployResponse is what the materialize endpoint returns.
type TemplateDeployResponse struct {
	OperationID      string                      `json:"operation_id"`
	ProvisionRunID   string                      `json:"provision_run_id"`
	TemplateSlug     string                      `json:"template_slug"`
	State            string                      `json:"state"`
	EnvironmentID    string                      `json:"environment_id"`
	DeploymentID     string                      `json:"deployment_id"`
	CreatedServices  []TemplateCreatedService    `json:"created_services"`
	CreatedVariables []templates.CreatedResource `json:"created_variables,omitempty"`
	CreatedVolumes   []templates.CreatedResource `json:"created_volumes,omitempty"`
	StatusURL        string                      `json:"status_url"`
	Note             string                      `json:"note,omitempty"`
}

// TemplateCreatedService is the subset of the created-resource list
// returned to surface the service ids.
type TemplateCreatedService struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

// templatesHandler dispatches the /templates collection plus the
// /{slug} and /{slug}/instances sub-routes.
func templatesHandler(deps templateDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, v1alphaPrefix)
		if path == resourceCollectionTemplates {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
				return
			}
			listTemplates(w, r, deps)
			return
		}
		rest := strings.TrimPrefix(path, resourceCollectionTemplates+"/")
		if rest == "" || rest == path {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "template not found", nil)
			return
		}
		if strings.HasPrefix(rest, "community/") {
			communityTemplatesHandler(w, r, rest, deps)
			return
		}
		slug, suffix, hasSuffix := strings.Cut(rest, "/")
		if slug == "" {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "template not found", nil)
			return
		}
		if !hasSuffix {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"slug": slug})
				return
			}
			getTemplate(w, r, slug)
			return
		}
		if strings.HasPrefix(suffix, "instances/") {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"slug": slug})
				return
			}
			getTemplateProvisionRun(w, r, slug, strings.TrimPrefix(suffix, "instances/"), deps)
			return
		}
		if suffix != "instances" {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "template route not found", map[string]any{"slug": slug, "suffix": suffix})
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", map[string]any{"slug": slug})
			return
		}
		deployTemplate(w, r, slug, deps)
	}
}

type templateSummaryResponse struct {
	templates.Summary
	Provisionable    bool   `json:"provisionable"`
	CapabilityReason string `json:"capability_reason,omitempty"`
}

func listTemplates(w http.ResponseWriter, _ *http.Request, deps templateDeps) {
	summaries := templates.Summaries()
	response := make([]templateSummaryResponse, 0, len(summaries))
	for _, summary := range summaries {
		item := templateSummaryResponse{Summary: summary}
		switch {
		case !isOfficialRuntimeTemplate(summary.Slug):
			item.CapabilityReason = "catalog-only until a database-specific runtime and persistence verifier is implemented"
		case deps.provisioner == nil:
			item.CapabilityReason = "requires the Postgres-backed API and Docker worker"
		default:
			item.Provisionable = true
		}
		response = append(response, item)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"templates": response})
}

func getTemplate(w http.ResponseWriter, r *http.Request, slug string) {
	tmpl, err := templates.Lookup(slug)
	if errors.Is(err, templates.ErrTemplateNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "template not found", map[string]any{"slug": slug})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "template lookup failed", map[string]any{"slug": slug})
		return
	}
	WriteJSON(w, http.StatusOK, tmpl)
}

func deployTemplate(w http.ResponseWriter, r *http.Request, slug string, deps templateDeps) {
	if deps.provisioner == nil {
		writeRequestError(
			w,
			r,
			http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable,
			"one-click template deployment requires the durable Postgres-backed official-database saga",
			map[string]any{
				"capability": "one-click-template-deploy",
				"state":      "unavailable",
				"recovery":   "run the Postgres-backed API and Docker worker; memory-only mode cannot promise durable database provisioning",
			},
		)
		return
	}
	tmpl, err := templates.Lookup(slug)
	if errors.Is(err, templates.ErrTemplateNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "template not found", map[string]any{"slug": slug})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "template lookup failed", map[string]any{"slug": slug})
		return
	}
	if !isOfficialRuntimeTemplate(tmpl.Slug) {
		writeRequestError(w, r, http.StatusUnprocessableEntity, failurecodes.ResourceValidationFailed,
			"this template is catalog-only until its database-specific runtime and persistence checks are implemented",
			map[string]any{"slug": slug, "runtime_supported": officialRuntimeTemplateSlugs()})
		return
	}

	body, ok := readBoundedJSONRequestBody(w, r, "invalid template deploy body")
	if !ok {
		return
	}
	reservation, ok := reserveIdempotencyOrAbort(w, r, deps.idem, body)
	if !ok {
		return
	}
	idempotentResponse := newIdempotencyResponseCapture(w)
	w = idempotentResponse
	defer idempotentResponse.Complete(r, deps.idem, reservation.Reference())
	key := idempotency.Key(r.Header.Get("Idempotency-Key"))

	var request TemplateDeployRequest
	if !decodeOptionalStrictJSONRequestBytes(w, r, body, &request, "invalid template deploy body") {
		return
	}
	if strings.TrimSpace(request.ProjectID) == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id is required", map[string]any{"field": "project_id"})
		return
	}
	if deps.scopeResolver != nil {
		resolved, err := deps.scopeResolver.ResolveMutationScope(r.Context(), MutationScopeRequest{
			ProjectID:          request.ProjectID,
			EnvironmentID:      request.EnvironmentID,
			RequireProject:     true,
			RequireEnvironment: request.EnvironmentID != "",
		})
		if err != nil {
			writeMutationScopeError(w, r, err)
			return
		}
		request.ProjectID = resolved.ProjectID
		if resolved.Environment != nil {
			request.EnvironmentID = string(resolved.Environment.ID)
		}
	}
	result, err := deps.provisioner.ProvisionTemplate(r.Context(), TemplateProvisionRequest{
		TemplateSlug: tmpl.Slug, ProjectID: request.ProjectID,
		EnvironmentID: request.EnvironmentID, Name: request.Name,
		IdempotencyKey: key, IdempotencyRef: reservation.Reference(),
	})
	if err != nil {
		if errors.Is(err, ErrResourceNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "template destination project was not found", map[string]any{"project_id": request.ProjectID})
			return
		}
		var scopeErr *MutationScopeError
		if errors.As(err, &scopeErr) {
			writeMutationScopeError(w, r, err)
			return
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "template provisioning intake failed", map[string]any{"slug": slug})
		return
	}

	deps.emitter.emit(r.Context(), audit.Row{
		ResourceKind: "template",
		ResourceID:   slug,
		ProjectID:    request.ProjectID,
		Action:       "template.deploy",
		After: map[string]any{
			"slug":                    slug,
			"operation_id":            result.Run.OperationID,
			"provision_run_id":        result.Run.ID,
			"environment_id":          result.Run.EnvironmentID,
			"runtime_success_pending": true,
		},
	})
	response := templateProvisionResponse(result)
	responseBody, err := json.Marshal(response)
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "template response encode failed", map[string]any{"operation_id": result.Run.OperationID})
		return
	}
	responseBody = append(responseBody, '\n')
	location := response.StatusURL
	writeStoredResponse(w, idempotency.StoredResponse{
		StatusCode: http.StatusAccepted,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "Location": []string{location}},
		Body:       responseBody,
	})
}

func templateProvisionResponse(result TemplateProvisionResult) TemplateDeployResponse {
	spec, _ := officialTemplateRuntimeSpecForSlug(result.Run.TemplateSlug)
	return TemplateDeployResponse{
		OperationID: result.Run.OperationID, ProvisionRunID: result.Run.ID,
		TemplateSlug: result.Run.TemplateSlug, State: result.Run.State,
		EnvironmentID: result.Run.EnvironmentID, DeploymentID: result.Run.DeploymentID,
		CreatedServices: []TemplateCreatedService{result.Service},
		CreatedVolumes:  []templates.CreatedResource{{Kind: "volume", ID: result.VolumeID, Name: "data"}},
		StatusURL:       v1alphaPrefix + "/templates/" + result.Run.TemplateSlug + "/instances/" + result.Run.ID,
		Note:            templateVerificationNote(spec),
	}
}

func isOfficialRuntimeTemplate(slug string) bool {
	_, ok := officialTemplateRuntimeSpecForSlug(slug)
	return ok
}

func getTemplateProvisionRun(w http.ResponseWriter, r *http.Request, slug, id string, deps templateDeps) {
	if deps.runReader == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "template provision status is unavailable", nil)
		return
	}
	run, err := deps.runReader.GetProvisionRun(r.Context(), slug, id)
	if errors.Is(err, templates.ErrProvisionRunNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "template provision run not found", map[string]any{"id": id})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "template provision status failed", nil)
		return
	}
	WriteJSON(w, http.StatusOK, run)
}

func buildTemplateDeployResponse(operation orailtypes.Operation, result templates.Result) TemplateDeployResponse {
	response := TemplateDeployResponse{
		OperationID:  string(operation.ID),
		TemplateSlug: result.TemplateSlug,
	}
	for _, resource := range result.CreatedResources {
		switch resource.Kind {
		case "service":
			response.CreatedServices = append(response.CreatedServices, TemplateCreatedService{ID: resource.ID, Slug: resource.Name})
		case "variable":
			response.CreatedVariables = append(response.CreatedVariables, resource)
		case "volume":
			response.CreatedVolumes = append(response.CreatedVolumes, resource)
		}
	}
	if len(response.CreatedVolumes) > 0 {
		response.Note = "Volume rows are declared by this materializer; the volume worker integration honors the attachment at deploy time. Until the worker integration lands, container restarts may not preserve data."
	}
	return response
}

// templateDeps bundles the seams the templates handler needs. The
// router builds one of these from the DataStack at startup.
type templateDeps struct {
	serviceStore          ServiceStore
	variableStore         *secrets.Vault
	operationStore        OperationStore
	idem                  idempotency.Store
	emitter               *auditEmitter
	scopeResolver         MutationScopeResolver
	provisioner           TemplateProvisioner
	runReader             templates.RunReader
	communityDiscoverer   templates.CommunityDiscoverer
	communityForks        templates.CommunityForkStore
	identityAccess        identity.AccessStore
	serviceConfigs        ServiceConfigStore
	environmentChangesets EnvironmentChangesetCoordinator
}

// serviceCreatorAdapter satisfies templates.ServiceCreator by calling
// into ServiceStore.CreateService. It carries the project id so the
// materializer does not need to know about the ServiceStore shape.
type serviceCreatorAdapter struct {
	store     ServiceStore
	projectID string
}

func newServiceCreatorAdapter(store ServiceStore, projectID string) *serviceCreatorAdapter {
	return &serviceCreatorAdapter{store: store, projectID: projectID}
}

func (a *serviceCreatorAdapter) CreateServiceService(ctx context.Context, in templates.CreateServiceInput) (templates.CreatedService, error) {
	display := strings.TrimSpace(in.DisplayName)
	if display == "" {
		display = in.Slug
	}
	service, err := a.store.CreateService(ctx, Service{
		ProjectID:   orailtypes.ID(a.projectID),
		Slug:        in.Slug,
		DisplayName: display,
	})
	if err != nil {
		return templates.CreatedService{}, err
	}
	return templates.CreatedService{ID: string(service.ID), Slug: service.Slug}, nil
}

// variableUpserterAdapter satisfies templates.VariableUpserter by
// calling into the secrets vault. Secret values land in ScopeSealed;
// non-secret literals land in ScopePlain.
type variableUpserterAdapter struct {
	vault         *secrets.Vault
	projectID     string
	environmentID string
}

func newVariableUpserterAdapter(vault *secrets.Vault, projectID, environmentID string) *variableUpserterAdapter {
	return &variableUpserterAdapter{vault: vault, projectID: projectID, environmentID: environmentID}
}

func (a *variableUpserterAdapter) UpsertVariableService(ctx context.Context, in templates.UpsertVariableInput) (templates.UpsertedVariable, error) {
	scope := secrets.ScopePlain
	if in.Secret {
		scope = secrets.ScopeSealed
	}
	variable, err := a.vault.Set(ctx, secrets.VariableInput{
		Name:        in.Name,
		Project:     a.projectID,
		Environment: a.environmentID,
		Service:     in.ServiceID,
		Scope:       scope,
		Plaintext:   []byte(in.Value),
	})
	if err != nil {
		return templates.UpsertedVariable{}, err
	}
	meta := variable.Meta()
	return templates.UpsertedVariable{ID: string(meta.ID), Name: meta.Name}, nil
}

// noopVolumeAttacher is the placeholder hand-off for the volume worker
// integration. It returns synthetic IDs so the materializer's result
// surfaces a stub row the caller can audit; the volume worker on the
// concurrent track replaces this with the real adapter when it lands.
type noopVolumeAttacher struct{}

func (noopVolumeAttacher) AttachTemplateVolume(_ context.Context, in templates.AttachVolumeInput) (templates.AttachedVolume, error) {
	// Stubbed: the volume worker integration replaces this. Return
	// a deterministic synthetic id so the response audit trail
	// shows the declared volume requirement.
	id := "vol_pending_" + in.ServiceID + "_" + in.Name
	return templates.AttachedVolume{VolumeID: id, AttachmentID: id + "_attach"}, nil
}
